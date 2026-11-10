package provider

// Pass 5 Lane A (WP cop): docker_container_wait (POST /containers/{id}/wait)
// and docker_container_cp (PUT /containers/{id}/archive, DECISIONS Q9).

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*copWaitAction)(nil)
	_ action.ActionWithValidateConfig = (*copWaitAction)(nil)
	_ action.ActionWithConfigure      = (*copCpAction)(nil)
	_ action.ActionWithValidateConfig = (*copCpAction)(nil)
)

// copWaitDefaultTimeout bounds docker_container_wait when timeout is unset.
const copWaitDefaultTimeout = 10 * time.Minute

var (
	copAbsPathRe = regexp.MustCompile(`^/(.*[^/])?$`)
	copModeRe    = regexp.MustCompile(`^0?[0-7]{3,4}$`)
)

// copCpMaxBytes caps the archive docker_container_cp builds in memory.
const copCpMaxBytes = 256 << 20

// ---------------------------------------------------------------------------
// docker_container_wait

// NewContainerWaitAction returns the docker_container_wait action.
func NewContainerWaitAction() action.Action { return &copWaitAction{} }

type copWaitAction struct{ actBase }

type copWaitModel struct {
	ContainerID    types.String `tfsdk:"container_id"`
	Condition      types.String `tfsdk:"condition"`
	Timeout        types.String `tfsdk:"timeout"`
	ExpectExitCode types.Int64  `tfsdk:"expect_exit_code"`
	ResultPath     types.String `tfsdk:"result_path"`
}

func (a *copWaitAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_wait"
}

func (a *copWaitAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Blocks until a container reaches a condition and reports its exit code, like `docker container wait`. " +
			"The result object holds the Engine's `status_code` and `error`. With `expect_exit_code` set, a different exit code fails the action, " +
			"which makes it a gate for one-shot containers such as migrations.",
		Attributes: map[string]schema.Attribute{
			"container_id": copContainerIDActionAttr(),
			"condition": schema.StringAttribute{Optional: true,
				Validators: []validator.String{stringvalidator.OneOf("not-running", "next-exit", "removed")},
				MarkdownDescription: "What to wait for (Engine `condition`): `not-running` (default; returns at once for a stopped container), " +
					"`next-exit` (the next time it exits) or `removed`."},
			"timeout": schema.StringAttribute{Optional: true,
				MarkdownDescription: "How long to wait, as a Go duration (`30s`, `1h`). Default `10m`. On timeout the action fails; the container is left alone."},
			"expect_exit_code": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Fail the action unless the container exits with this code, e.g. `0`. Default: any exit code succeeds."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *copWaitAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m copWaitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actDuration(m.Timeout, "timeout", &resp.Diagnostics)
}

func (a *copWaitAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m copWaitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.ContainerID.ValueString()
	insp, _, err := copInspect(ctx, c, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "docker_container_wait", ref, err)
		return
	}
	cond := container.WaitConditionNotRunning
	if s := m.Condition.ValueString(); s != "" {
		cond = container.WaitCondition(s)
	}
	timeout := actTimeout(m.Timeout, copWaitDefaultTimeout)
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	actProgress(ctx, resp, "waiting for container %s (%s)", copName(insp.Name), cond)
	w := c.ContainerWait(wctx, insp.ID, client.ContainerWaitOptions{Condition: cond})
	var res container.WaitResponse
	select {
	case res = <-w.Result:
	case err := <-w.Error:
		if wctx.Err() != nil {
			err = fmt.Errorf("container did not reach %q within %s", cond, timeout)
		}
		resp.Diagnostics.AddError("docker_container_wait failed", fmt.Sprintf("container %q: %v", ref, err))
		return
	}
	result := map[string]any{"StatusCode": res.StatusCode}
	if res.Error != nil {
		result["Error"] = map[string]any{"Message": res.Error.Message}
	}
	actFinishResult(ctx, resp, a.pd, "engine:container-wait", map[string]any{
		"container_id": insp.ID,
		"result":       result,
	}, m.ResultPath)
	if res.Error != nil && res.Error.Message != "" {
		resp.Diagnostics.AddError("docker_container_wait: wait error", fmt.Sprintf("container %q: %s", ref, res.Error.Message))
		return
	}
	if !m.ExpectExitCode.IsNull() && !m.ExpectExitCode.IsUnknown() && res.StatusCode != m.ExpectExitCode.ValueInt64() {
		resp.Diagnostics.AddError("docker_container_wait: unexpected exit code",
			fmt.Sprintf("container %q exited with code %d, expected %d.", ref, res.StatusCode, m.ExpectExitCode.ValueInt64()))
	}
}

// ---------------------------------------------------------------------------
// docker_container_cp

// NewContainerCpAction returns the docker_container_cp action.
func NewContainerCpAction() action.Action { return &copCpAction{} }

type copCpAction struct{ actBase }

type copCpModel struct {
	ContainerID               types.String `tfsdk:"container_id"`
	Path                      types.String `tfsdk:"path"`
	Content                   types.String `tfsdk:"content"`
	ContentBase64             types.String `tfsdk:"content_base64"`
	Source                    types.String `tfsdk:"source"`
	Mode                      types.String `tfsdk:"mode"`
	UID                       types.Int64  `tfsdk:"uid"`
	GID                       types.Int64  `tfsdk:"gid"`
	AllowOverwriteDirWithFile types.Bool   `tfsdk:"allow_overwrite_dir_with_file"`
	CopyUIDGID                types.Bool   `tfsdk:"copy_uidgid"`
	ResultPath                types.String `tfsdk:"result_path"`
}

func (a *copCpAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_cp"
}

func (a *copCpAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Copies a file or directory into an existing container at any time, like `docker container cp SRC CONTAINER:PATH`. " +
			"The source is inline `content`, `content_base64` or a local `source` path (a file or a directory, copied recursively); " +
			"it is packed into a tar archive and extracted by the daemon (`PUT /containers/{id}/archive`), so the container may be running or stopped. " +
			"docker_container's `files` block covers files that must exist before the first start.",
		Attributes: map[string]schema.Attribute{
			"container_id": copContainerIDActionAttr(),
			"path": schema.StringAttribute{Required: true,
				Validators: []validator.String{stringvalidator.RegexMatches(copAbsPathRe, "must be an absolute container path that does not end in /")},
				MarkdownDescription: "Absolute destination path inside the container: the file to write, or the directory to create for a directory `source`. " +
					"Its parent directory must exist."},
			"content":        schema.StringAttribute{Optional: true, MarkdownDescription: "Inline UTF-8 file content. Exactly one of `content`, `content_base64` and `source`."},
			"content_base64": schema.StringAttribute{Optional: true, MarkdownDescription: "Inline binary file content, base64-encoded. Exactly one of `content`, `content_base64` and `source`."},
			"source": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Local file or directory on the machine running Terraform; `~` expands to the home directory. Exactly one of `content`, `content_base64` and `source`."},
			"mode": schema.StringAttribute{Optional: true,
				Validators:          []validator.String{stringvalidator.RegexMatches(copModeRe, "must be an octal mode such as 0644")},
				MarkdownDescription: "Octal permission bits for the copied files, e.g. `0644` or `0755`. Default `0644` for inline content, the local file's mode for `source`."},
			"uid": schema.Int64Attribute{Optional: true, MarkdownDescription: "Numeric owner of the copied files. Default 0 (root)."},
			"gid": schema.Int64Attribute{Optional: true, MarkdownDescription: "Numeric group of the copied files. Default 0 (root)."},
			"allow_overwrite_dir_with_file": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Let a file replace an existing directory of the same name (Engine `noOverwriteDirNonDir=false`). Default `false`."},
			"copy_uidgid": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Map the archive's `uid` / `gid` through the container's user-namespace mapping so the files belong to the same " +
					"user inside the container as they would without remapping (Engine `copyUIDGID`, `docker cp --archive`). Default `false`."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *copCpAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m copCpModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	set, unknown := 0, false
	for _, v := range []types.String{m.Content, m.ContentBase64, m.Source} {
		if v.IsUnknown() {
			unknown = true
		} else if !v.IsNull() {
			set++
		}
	}
	if !unknown && set != 1 {
		resp.Diagnostics.AddAttributeError(tfpath.Root("content"), "Exactly one source",
			"Set exactly one of content, content_base64 and source.")
	}
	if s, ok := knownString(m.ContentBase64); ok && s != "" {
		if _, err := base64.StdEncoding.DecodeString(s); err != nil {
			resp.Diagnostics.AddAttributeError(tfpath.Root("content_base64"), "Invalid base64", err.Error())
		}
	}
}

func (a *copCpAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m copCpModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !a.pd.gateItemAPI(ctx, "engine:container-upload", &resp.Diagnostics) {
		return
	}
	ref, dst := m.ContainerID.ValueString(), path.Clean(m.Path.ValueString())
	opts := copTarOptions{UID: int(m.UID.ValueInt64()), GID: int(m.GID.ValueInt64())}
	if s := m.Mode.ValueString(); s != "" {
		v, _ := strconv.ParseUint(s, 8, 32)
		mode := fs.FileMode(v)
		opts.Mode = &mode
	}
	var archive []byte
	var err error
	switch {
	case !m.Source.IsNull():
		archive, err = copTarSource(expandHome(m.Source.ValueString()), path.Base(dst), opts)
	case !m.ContentBase64.IsNull():
		var b []byte
		b, err = base64.StdEncoding.DecodeString(m.ContentBase64.ValueString())
		if err == nil {
			archive, err = copTarBytes(path.Base(dst), b, opts)
		}
	default:
		archive, err = copTarBytes(path.Base(dst), []byte(m.Content.ValueString()), opts)
	}
	if err != nil {
		resp.Diagnostics.AddError("docker_container_cp: cannot build archive", err.Error())
		return
	}
	insp, _, err := copInspect(ctx, c, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "docker_container_cp", ref, err)
		return
	}
	sum := sha256.Sum256(archive)
	_, err = c.CopyToContainer(ctx, insp.ID, client.CopyToContainerOptions{
		DestinationPath:           path.Dir(dst),
		Content:                   bytes.NewReader(archive),
		AllowOverwriteDirWithFile: actBool(m.AllowOverwriteDirWithFile, false),
		CopyUIDGID:                actBool(m.CopyUIDGID, false),
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_container_cp failed", fmt.Sprintf("copying to %s in container %q: %v", dst, ref, err))
		return
	}
	actProgress(ctx, resp, "copied %s to %s:%s", actHumanSize(uint64(len(archive))), copName(insp.Name), dst)
	result := map[string]any{}
	if st, err := c.ContainerStatPath(ctx, insp.ID, client.ContainerStatPathOptions{Path: dst}); err == nil {
		result["path_stat"] = st.Stat
	} else {
		resp.Diagnostics.AddWarning("docker_container_cp: cannot stat the copied path", err.Error())
	}
	actFinishResult(ctx, resp, a.pd, "engine:container-upload", map[string]any{
		"container_id":   insp.ID,
		"path":           dst,
		"archive_sha256": hex.EncodeToString(sum[:]),
		"result":         result,
	}, m.ResultPath)
}

// copTarOptions sets ownership and (optionally) the mode of every entry.
type copTarOptions struct {
	UID, GID int
	Mode     *fs.FileMode
}

// copTarBytes packs one regular file named name into a tar archive.
func copTarBytes(name string, content []byte, o copTarOptions) ([]byte, error) {
	if len(content) > copCpMaxBytes {
		return nil, fmt.Errorf("content is %d bytes; docker_container_cp copies at most %d", len(content), copCpMaxBytes)
	}
	mode := fs.FileMode(0o644)
	if o.Mode != nil {
		mode = *o.Mode
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	h := &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(content)), Mode: int64(mode.Perm()),
		Uid: o.UID, Gid: o.GID, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX}
	if err := tw.WriteHeader(h); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// copTarSource packs a local file or directory tree under the name name.
// Symlinks are stored as links; other special files are an error.
func copTarSource(src, name string, o copTarOptions) ([]byte, error) {
	root, err := os.Lstat(src)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(p, rel string, fi fs.FileInfo) error {
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !fi.Mode().IsRegular() && !fi.IsDir() {
			return fmt.Errorf("%s: only regular files, directories and symlinks can be copied", p)
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			h.Name += "/"
		}
		h.Uid, h.Gid, h.Uname, h.Gname = o.UID, o.GID, "", ""
		h.Format = tar.FormatPAX
		if o.Mode != nil && fi.Mode().IsRegular() {
			h.Mode = int64(o.Mode.Perm())
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		if buf.Len() > copCpMaxBytes {
			return fmt.Errorf("archive exceeds %d bytes; docker_container_cp copies at most that much", copCpMaxBytes)
		}
		return nil
	}
	if !root.IsDir() {
		err = add(src, name, root)
	} else {
		err = filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			if rel == "." {
				rel = name
			} else {
				rel = name + "/" + filepath.ToSlash(rel)
			}
			return add(p, rel, fi)
		})
	}
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
