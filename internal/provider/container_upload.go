package provider

// Pass 3: docker_container `files` blocks (`upload` before Pass 5, Q9) and `mounts.selinux_relabel`.
//
// Uploads are written with CopyToContainer (PUT /containers/{id}/archive)
// from a single-file tar built in memory, after ContainerCreate and before
// ContainerStart. The daemon cannot report them back, so Read keeps the
// prior value. A changed upload replaces the container, except when the
// container is stopped and must_run is false: then Update copies the files
// into the stopped container in place.
//
// selinux_relabel cannot be expressed through HostConfig.Mounts (mount.Mount
// has no relabel field), so a mounts entry that sets it is sent as a
// HostConfig.Binds entry "<source>:<target>:<opts>" whose options carry `z`
// or `Z`, exactly like `docker run -v src:dst:z`.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	fwpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// containerUploadModel is one files block (Q9; formerly `upload`).
type containerUploadModel struct {
	Path          types.String `tfsdk:"path"`
	Content       types.String `tfsdk:"content"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	Source        types.String `tfsdk:"source"`
	SourceHash    types.String `tfsdk:"source_hash"`
	Mode          types.String `tfsdk:"mode"`
	UID           types.Int64  `tfsdk:"uid"`
	GID           types.Int64  `tfsdk:"gid"`
}

const containerUploadModeDefault = 0o644

var containerSELinuxModes = []string{"z", "Z"}

// containerUploadBlockSchema is the docker_container files block.
func containerUploadBlockSchema() schema.ListNestedBlock {
	sib := func(name string) fwpath.Expression { return fwpath.MatchRelative().AtParent().AtName(name) }
	return schema.ListNestedBlock{
		MarkdownDescription: "Files written into the container with `PutContainerArchive` after create and before start (like `docker cp`). " +
			"Each block sets exactly one of `content`, `content_base64` or `source`. The parent directory of `path` must already exist " +
			"in the image. Files are not read back from the daemon. Changing a file forces a new container, unless the container " +
			"is stopped (`must_run = false` and not running), in which case the files are copied in place. " +
			"To copy files into a running container at any time use the `docker_container_cp` action.",
		PlanModifiers: []planmodifier.List{containerUploadReplaceIf()},
		NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
			"path": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Absolute path of the file inside the container, e.g. `/etc/app/config.json`.",
				Validators: []validator.String{stringvalidator.RegexMatches(containerUploadAbsRe,
					"must be an absolute path that does not end in /")},
			},
			"content": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Literal file content (UTF-8 text).",
				Validators:          []validator.String{stringvalidator.ExactlyOneOf(sib("content_base64"), sib("source"))},
			},
			"content_base64": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "File content, base64-encoded (use `filebase64()` for binary files).",
			},
			"source": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Path of a local file (on the machine running Terraform) whose content is uploaded. It is read at apply; " +
					"set `source_hash` (e.g. `filesha256(...)`) so content changes are planned.",
			},
			"source_hash": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Any string that changes when the `source` file changes, e.g. `filesha256(\"app.conf\")`. Only used to trigger a re-upload.",
			},
			"mode": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "File mode as an octal string, e.g. `\"0640\"` or `\"0755\"` for an executable. Unset uploads with `0644`.",
				Validators:          []validator.String{stringvalidator.RegexMatches(containerUploadPermRe, "must be an octal mode such as 0644")},
			},
			"uid": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Numeric user ID that owns the file. Default `0`.",
			},
			"gid": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Numeric group ID that owns the file. Default `0`.",
			},
		}},
	}
}

// containerUploadReplaceIf forces replacement on an upload change unless the
// prior container is stopped and the plan does not require it to run.
func containerUploadReplaceIf() planmodifier.List {
	desc := "Changing files forces a new container unless the container is stopped and must_run is false."
	return listplanmodifier.RequiresReplaceIf(func(ctx context.Context, req planmodifier.ListRequest, resp *listplanmodifier.RequiresReplaceIfFuncResponse) {
		var mustRun, running types.Bool
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, fwpath.Root("must_run"), &mustRun)...)
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, fwpath.Root("running"), &running)...)
		resp.RequiresReplace = containerUploadNeedsReplace(mustRun, running)
	}, desc, desc)
}

// containerUploadNeedsReplace is the decision behind containerUploadReplaceIf.
func containerUploadNeedsReplace(mustRun, running types.Bool) bool {
	if mustRun.IsUnknown() || mustRun.IsNull() || mustRun.ValueBool() {
		return true // null must_run means the default, true
	}
	return running.IsUnknown() || running.ValueBool()
}

var (
	containerUploadAbsRe  = regexp.MustCompile(`^/.*[^/]$`)
	containerUploadPermRe = regexp.MustCompile(`^0?[0-7]{3,4}$`)
)

// containerUploadItem is one prepared upload: the directory to extract into
// and the tar holding the single file.
type containerUploadItem struct {
	File string
	Dir  string
	Tar  []byte
}

// containerUploadContent resolves the bytes of one upload.
func containerUploadContent(u containerUploadModel) ([]byte, error) {
	set := 0
	var data []byte
	if v, ok := knownString(u.Content); ok {
		set++
		data = []byte(v)
	}
	if v, ok := knownString(u.ContentBase64); ok {
		set++
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("content_base64 is not valid base64: %w", err)
		}
		data = b
	}
	if v, ok := knownString(u.Source); ok {
		set++
		b, err := os.ReadFile(expandHome(v))
		if err != nil {
			return nil, fmt.Errorf("reading source: %w", err)
		}
		data = b
	}
	if set != 1 {
		return nil, errors.New("exactly one of content, content_base64 or source must be set")
	}
	return data, nil
}

// containerUploadMode returns the file mode for one files block.
func containerUploadMode(u containerUploadModel) (int64, error) {
	if p, ok := knownString(u.Mode); ok && p != "" {
		m, err := strconv.ParseUint(p, 8, 32)
		if err != nil || m > 0o7777 {
			return 0, fmt.Errorf("mode %q: want an octal mode such as 0644", p)
		}
		return int64(m), nil
	}
	return containerUploadModeDefault, nil
}

// containerUploadTar builds a one-file tar for u.
func containerUploadTar(u containerUploadModel, now time.Time) (containerUploadItem, error) {
	file := u.Path.ValueString()
	if !strings.HasPrefix(file, "/") || strings.HasSuffix(file, "/") {
		return containerUploadItem{}, fmt.Errorf("path %q must be an absolute path to a file", file)
	}
	file = path.Clean(file)
	data, err := containerUploadContent(u)
	if err != nil {
		return containerUploadItem{}, err
	}
	mode, err := containerUploadMode(u)
	if err != nil {
		return containerUploadItem{}, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     path.Base(file),
		Mode:     mode,
		Size:     int64(len(data)),
		Uid:      int(u.UID.ValueInt64()),
		Gid:      int(u.GID.ValueInt64()),
		ModTime:  now,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return containerUploadItem{}, err
	}
	if _, err := tw.Write(data); err != nil {
		return containerUploadItem{}, err
	}
	if err := tw.Close(); err != nil {
		return containerUploadItem{}, err
	}
	return containerUploadItem{File: file, Dir: path.Dir(file), Tar: buf.Bytes()}, nil
}

// containerPrepareUploads builds every upload's tar before anything is sent,
// so a missing source file fails before the container is created.
func containerPrepareUploads(in []containerUploadModel) ([]containerUploadItem, error) {
	out := make([]containerUploadItem, 0, len(in))
	now := time.Now()
	for i, u := range in {
		it, err := containerUploadTar(u, now)
		if err != nil {
			return nil, fmt.Errorf("files[%d]: %w", i, err)
		}
		out = append(out, it)
	}
	return out, nil
}

// containerApplyUploads copies each prepared file into the container.
func containerApplyUploads(ctx context.Context, pd *providerData, id string, items []containerUploadItem) error {
	for _, it := range items {
		if _, err := pd.Client.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: it.Dir, Content: bytes.NewReader(it.Tar)}); err != nil {
			return fmt.Errorf("%s: %w", it.File, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// selinux_relabel

// containerBindSpec renders a mounts entry with selinux_relabel as a
// HostConfig.Binds string.
func containerBindSpec(i int, m containerMountModel, relabel string) (string, error) {
	t := m.Type.ValueString()
	src, dst := m.Source.ValueString(), m.Target.ValueString()
	switch mount.Type(t) {
	case mount.TypeBind:
		if !strings.HasPrefix(src, "/") {
			return "", fmt.Errorf("mounts[%d]: selinux_relabel on a bind mount needs an absolute host source path", i)
		}
	case mount.TypeVolume:
		if src == "" || strings.HasPrefix(src, "/") {
			return "", fmt.Errorf("mounts[%d]: selinux_relabel on a volume mount needs a named volume source", i)
		}
	default:
		return "", fmt.Errorf("mounts[%d]: selinux_relabel only applies to bind and volume mounts", i)
	}
	if relabel != "z" && relabel != "Z" {
		return "", fmt.Errorf("mounts[%d].selinux_relabel %q: must be z or Z", i, relabel)
	}
	opts := []string{"rw"}
	if m.ReadOnly.ValueBool() {
		opts[0] = "ro"
	}
	opts = append(opts, relabel)
	return src + ":" + dst + ":" + strings.Join(opts, ","), nil
}

// containerFlattenBinds maps HostConfig.Binds entries to mounts blocks. An
// entry is "src:dst[:opts]"; a source without a leading / is a named volume.
func containerFlattenBinds(binds []string) []containerMountModel {
	out := make([]containerMountModel, 0, len(binds))
	for _, b := range binds {
		parts := strings.Split(b, ":")
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		src, dst := parts[0], parts[1]
		m := containerMountModel{
			Type:           types.StringValue(string(mount.TypeBind)),
			Source:         types.StringValue(src),
			Target:         types.StringValue(dst),
			ReadOnly:       types.BoolValue(false),
			SelinuxRelabel: types.StringNull(),
		}
		if !strings.HasPrefix(src, "/") {
			m.Type = types.StringValue(string(mount.TypeVolume))
		}
		if len(parts) == 3 {
			for _, o := range strings.Split(parts[2], ",") {
				switch o {
				case "ro":
					m.ReadOnly = types.BoolValue(true)
				case "z", "Z":
					m.SelinuxRelabel = types.StringValue(o)
				}
			}
		}
		out = append(out, m)
	}
	return out
}

// containerUploadsEqual reports whether two upload lists are identical.
func containerUploadsEqual(a, b []containerUploadModel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if !x.Path.Equal(y.Path) || !x.Content.Equal(y.Content) || !x.ContentBase64.Equal(y.ContentBase64) ||
			!x.Source.Equal(y.Source) || !x.SourceHash.Equal(y.SourceHash) || !x.Mode.Equal(y.Mode) ||
			!x.UID.Equal(y.UID) || !x.GID.Equal(y.GID) {
			return false
		}
	}
	return true
}
