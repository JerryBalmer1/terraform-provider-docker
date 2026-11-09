package provider

// docker_image_import, docker_image_load and docker_image_save.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"

	"github.com/distribution/reference"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*imageImportAction)(nil)
	_ action.ActionWithValidateConfig = (*imageImportAction)(nil)
	_ action.ActionWithConfigure      = (*imageLoadAction)(nil)
	_ action.ActionWithConfigure      = (*imageSaveAction)(nil)
	_ action.ActionWithValidateConfig = (*imageSaveAction)(nil)
)

// actIsURL reports whether an import source is a URL the daemon fetches.
func actIsURL(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// actStreamProgress forwards each human line of a JSON message stream.
func actStreamProgress(ctx context.Context, resp *action.InvokeResponse) func(jsonstream.Message) {
	return func(m jsonstream.Message) {
		line := strings.TrimRight(m.Stream, "\r\n")
		if line == "" && m.Status != "" && m.Progress == nil {
			line = strings.TrimSpace(m.ID + " " + m.Status)
		}
		if line != "" {
			actProgress(ctx, resp, "%s", line)
		}
	}
}

// imgPlatformAttr is the optional platform argument of load / save.
func imgPlatformAttr(cli string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true,
		MarkdownDescription: "Only this platform of multi-platform images, `os[/arch[/variant]]` (`" + cli + " --platform`; " +
			"API 1.48+ with the containerd image store). Default: all platforms.",
	}
}

// ---------------------------------------------------------------------------
// docker_image_import

// NewImageImportAction returns the docker_image_import action.
func NewImageImportAction() action.Action { return &imageImportAction{} }

type imageImportAction struct{ actBase }

type imageImportActionModel struct {
	Source     types.String `tfsdk:"source"`
	Repository types.String `tfsdk:"repository"`
	Tag        types.String `tfsdk:"tag"`
	Changes    types.List   `tfsdk:"changes"`
	Message    types.String `tfsdk:"message"`
	Platform   types.String `tfsdk:"platform"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *imageImportAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_import"
}

func (a *imageImportAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates an image from a filesystem tarball, like `docker import`. " +
			"The source is a local tar file (streamed by the provider) or an `http(s)://` URL (fetched by the daemon). " +
			"The image is not tracked in state; manage its lifetime yourself. The result object holds `id`, `image_ref` and " +
			"`result.image` (size, created, architecture, os and variant of the new image).",
		Attributes: map[string]schema.Attribute{
			"source": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Local path of a tar archive (optionally gzip/bzip2/xz compressed) or an `http://` / `https://` URL the daemon downloads.",
			},
			"repository": schema.StringAttribute{Optional: true, MarkdownDescription: "Repository to tag the image with, e.g. `example/rootfs`. Without it the image is untagged."},
			"tag":        schema.StringAttribute{Optional: true, MarkdownDescription: "Tag for `repository`. Default `latest`. Requires `repository`."},
			"changes": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Dockerfile instructions applied to the image config, e.g. `[\"CMD [\\\"/bin/sh\\\"]\", \"ENV A=b\"]` (`docker import --change`).",
			},
			"message":     schema.StringAttribute{Optional: true, MarkdownDescription: "Commit message recorded in the image history."},
			"platform":    schema.StringAttribute{Optional: true, MarkdownDescription: "Platform of the imported image, `os[/arch[/variant]]`, e.g. `linux/amd64`. Default: the daemon's."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *imageImportAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m imageImportActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Tag.IsNull() && m.Repository.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("tag"), "tag needs repository", "Set repository when setting tag.")
	}
	if r, ok := knownString(m.Repository); ok && r != "" {
		if _, err := reference.ParseNormalizedNamed(r); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		}
	}
}

func (a *imageImportAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m imageImportActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	changes := stringListFrom(ctx, m.Changes, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	src := m.Source.ValueString()
	is := client.ImageImportSource{SourceName: src}
	if !actIsURL(src) {
		f, err := os.Open(expandHome(src))
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("source"), "docker_image_import: cannot open source", err.Error())
			return
		}
		defer f.Close()
		is = client.ImageImportSource{Source: f, SourceName: "-"}
	}
	ref := m.Repository.ValueString()
	tag := m.Tag.ValueString()
	actProgress(ctx, resp, "importing %s", src)
	rc, err := c.ImageImport(ctx, is, ref, client.ImageImportOptions{
		Tag:      tag,
		Message:  m.Message.ValueString(),
		Changes:  changes,
		Platform: platformFromString(m.Platform.ValueString()),
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_image_import failed", err.Error())
		return
	}
	defer rc.Close()
	id := ""
	_, err = imgDecodeStream(rc, func(msg jsonstream.Message) {
		if strings.HasPrefix(msg.Status, "sha256:") {
			id = strings.TrimSpace(msg.Status)
			return
		}
		actStreamProgress(ctx, resp)(msg)
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_image_import failed", err.Error())
		return
	}
	name := id
	values := map[string]any{"id": id}
	if ref != "" {
		if tag == "" {
			tag = "latest"
		}
		values["image_ref"] = imgNormalizedRef(ref + ":" + tag)
		name = ref + ":" + tag + " (" + id + ")"
	}
	actProgress(ctx, resp, "imported image %s", name)
	if id != "" {
		if insp, err := c.ImageInspect(ctx, id); err == nil {
			values["result"] = map[string]any{"image": map[string]any{
				"size": insp.Size, "created": insp.Created, "architecture": insp.Architecture, "os": insp.Os, "variant": insp.Variant,
			}}
		} else {
			resp.Diagnostics.AddWarning("docker_image_import: cannot inspect the new image", err.Error())
		}
	}
	actFinishResult(ctx, resp, a.pd, "engine:image-import", values, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_image_load

// NewImageLoadAction returns the docker_image_load action.
func NewImageLoadAction() action.Action { return &imageLoadAction{} }

type imageLoadAction struct{ actBase }

type imageLoadActionModel struct {
	Input      types.String `tfsdk:"input"`
	Platform   types.String `tfsdk:"platform"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *imageLoadAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_load"
}

func (a *imageLoadAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Loads images from a local tar archive into the daemon, like `docker load`. " +
			"The archive is what `docker save` or the `docker_image_save` action writes; the loaded image names are reported as progress " +
			"and in the result object (`loaded_images`, `result.archive_size`). The images are not tracked in state.",
		Attributes: map[string]schema.Attribute{
			"input": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Local path of the image archive (optionally gzip/bzip2/xz compressed), like `docker load --input`. `~` expands to the home directory.",
			},
			"platform":    imgPlatformAttr("docker load"),
			"result_path": actResultPathAttribute(),
		},
	}
}

// imgLoadedImage returns the reference or ID of a "Loaded image: ..." line.
func imgLoadedImage(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, pfx := range []string{"Loaded image ID: ", "Loaded image: "} {
		if strings.HasPrefix(line, pfx) {
			return strings.TrimSpace(strings.TrimPrefix(line, pfx)), true
		}
	}
	return "", false
}

func (a *imageLoadAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m imageLoadActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	p := m.Input.ValueString()
	f, err := os.Open(expandHome(p))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("input"), "docker_image_load: cannot open archive", err.Error())
		return
	}
	defer f.Close()
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	opts := []client.ImageLoadOption{client.ImageLoadWithQuiet(true)}
	if pl := m.Platform.ValueString(); pl != "" {
		opts = append(opts, client.ImageLoadWithPlatforms(platformFromString(pl)))
	}
	actProgress(ctx, resp, "loading images from %s", p)
	lr, err := c.ImageLoad(ctx, f, opts...)
	if err != nil {
		resp.Diagnostics.AddError("docker_image_load failed", err.Error())
		return
	}
	defer lr.Close()
	loaded := []string{}
	note := func(line string) {
		if ref, ok := imgLoadedImage(line); ok {
			loaded = append(loaded, ref)
		}
	}
	// The v0 client no longer reports the response Content-Type; current
	// daemons always answer with a JSON message stream, older ones with
	// plain text. Peek at the first byte to tell them apart.
	br := bufio.NewReader(lr)
	if first, _ := br.Peek(1); len(first) == 0 || first[0] != '{' {
		b, _ := io.ReadAll(br)
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" {
				note(l)
				actProgress(ctx, resp, "%s", l)
			}
		}
	} else {
		progress := actStreamProgress(ctx, resp)
		if _, err := imgDecodeStream(br, func(msg jsonstream.Message) {
			note(msg.Stream)
			progress(msg)
		}); err != nil {
			resp.Diagnostics.AddError("docker_image_load failed", err.Error())
			return
		}
	}
	actFinishResult(ctx, resp, a.pd, "engine:image-load", map[string]any{
		"loaded_images": loaded,
		"result":        map[string]any{"archive_size": size},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_image_save

// NewImageSaveAction returns the docker_image_save action.
func NewImageSaveAction() action.Action { return &imageSaveAction{} }

type imageSaveAction struct{ actBase }

type imageSaveActionModel struct {
	Images     types.List   `tfsdk:"images"`
	Output     types.String `tfsdk:"output"`
	Platform   types.String `tfsdk:"platform"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *imageSaveAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_save"
}

func (a *imageSaveAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Writes one or more images to a local tar archive, like `docker save`. " +
			"The archive keeps every tag and can be loaded with `docker load` or the `docker_image_load` action; it is written to a temporary name and renamed into place. " +
			"The result object holds `images`, `path`, `archive_sha256` and `result.archive_size`.",
		Attributes: map[string]schema.Attribute{
			"images": schema.ListAttribute{
				Required: true, ElementType: types.StringType,
				MarkdownDescription: "Image names, tags or IDs to save, e.g. `[docker_image.app.name]`. All must exist on the daemon.",
			},
			"output": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Local tar file to write, like `docker save --output`. Parent directories are created; an existing file is replaced.",
			},
			"platform":    imgPlatformAttr("docker save"),
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *imageSaveAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m imageSaveActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Images.IsUnknown() && !m.Images.IsNull() && len(m.Images.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("images"), "No images", "images needs at least one image.")
	}
}

func (a *imageSaveAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m imageSaveActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	imgs := stringListFrom(ctx, m.Images, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	p := m.Output.ValueString()
	var opts []client.ImageSaveOption
	if pl := m.Platform.ValueString(); pl != "" {
		opts = append(opts, client.ImageSaveWithPlatforms(platformFromString(pl)))
	}
	actProgress(ctx, resp, "saving %s to %s", strings.Join(imgs, ", "), p)
	rc, err := c.ImageSave(ctx, imgs, opts...)
	if err != nil {
		resp.Diagnostics.AddError("docker_image_save failed", err.Error())
		return
	}
	defer rc.Close()
	hash := sha256.New()
	n, err := actWriteFile(p, io.TeeReader(rc, hash), func(w int64) { actProgress(ctx, resp, "saved %s", actHumanSize(uint64(w))) })
	if err != nil {
		resp.Diagnostics.AddError("docker_image_save failed", err.Error())
		return
	}
	actProgress(ctx, resp, "saved %d image(s): %s written to %s", len(imgs), actHumanSize(uint64(n)), p)
	actFinishResult(ctx, resp, a.pd, "engine:image-save", map[string]any{
		"images":         imgs,
		"path":           p,
		"archive_sha256": hex.EncodeToString(hash.Sum(nil)),
		"result":         map[string]any{"archive_size": n},
	}, m.ResultPath)
}
