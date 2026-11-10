package provider

// Compose actions on one service container, found by the
// com.docker.compose.* labels:
//   - docker_compose_commit (compose:cli-commit)
//   - docker_compose_cp     (compose:cli-cp)
//   - docker_compose_export (compose:cli-export)
//   - docker_compose_wait   (compose:cli-wait; every selected container)

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	aschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func composeOneContainerAttrs(extra map[string]aschema.Attribute) map[string]aschema.Attribute {
	attrs := map[string]aschema.Attribute{
		"name":        composeActionProjectAttr(),
		"service":     aschema.StringAttribute{Required: true, MarkdownDescription: "The service whose container is used."},
		"index":       composeIndexAttr(),
		"result_path": actResultPathAttribute(),
	}
	for k, v := range extra {
		attrs[k] = v
	}
	return attrs
}

// ---------------------------------------------------------------------------
// docker_compose_commit

// NewComposeCommitAction returns docker_compose_commit.
func NewComposeCommitAction() action.Action { return &composeCommitAction{} }

type composeCommitAction struct{ actBase }

type composeCommitModel struct {
	Name       types.String `tfsdk:"name"`
	Service    types.String `tfsdk:"service"`
	Index      types.Int64  `tfsdk:"index"`
	Repository types.String `tfsdk:"repository"`
	Comment    types.String `tfsdk:"comment"`
	Author     types.String `tfsdk:"author"`
	Changes    types.List   `tfsdk:"changes"`
	Pause      types.Bool   `tfsdk:"pause"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *composeCommitAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_commit"
}

func (a *composeCommitAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Creates an image from a compose service container's changes, like `docker compose commit`. " +
			"The result lists the filesystem changes that went into the image (`result.changes`, from the container diff).",
		Attributes: composeOneContainerAttrs(map[string]aschema.Attribute{
			"repository": aschema.StringAttribute{Optional: true,
				MarkdownDescription: "Image reference to tag the new image with, `repo:tag`. Default: untagged."},
			"comment": aschema.StringAttribute{Optional: true, MarkdownDescription: "Commit message (CLI `--message`)."},
			"author":  aschema.StringAttribute{Optional: true, MarkdownDescription: "Author (CLI `--author`)."},
			"changes": aschema.ListAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Dockerfile instructions applied to the image (CLI `--change`), e.g. `[\"ENV DEBUG=1\"]`."},
			"pause": aschema.BoolAttribute{Optional: true, MarkdownDescription: "Pause the container while committing (CLI `--pause`). Default `true`."},
		}),
	}
}

func (a *composeCommitAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composeCommitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctr, err := composeServiceContainer(ctx, a.pd, m.Name.ValueString(), m.Service.ValueString(), m.Index, false)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_commit: no container", err.Error())
		return
	}
	diff, err := c.ContainerDiff(ctx, ctr.ID, client.ContainerDiffOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_commit: cannot read changes", err.Error())
		return
	}
	res, err := c.ContainerCommit(ctx, ctr.ID, client.ContainerCommitOptions{
		Reference: m.Repository.ValueString(), Comment: m.Comment.ValueString(), Author: m.Author.ValueString(),
		// Owner label, inject-only (Pass 5.3 Lane Z): a LABEL change, as
		// docker_image_commit does without `config`.
		Changes: rOwnerLabelChanges(a.pd, stringListFrom(ctx, m.Changes, &resp.Diagnostics)), NoPause: !actBool(m.Pause, true),
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_commit failed", fmt.Sprintf("%s: %v", composeSummaryName(ctr), err))
		return
	}
	changes := diff.Changes
	if changes == nil {
		changes = []container.FilesystemChange{}
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-commit", map[string]any{
		"name": m.Name.ValueString(), "container_id": ctr.ID, "id": res.ID, "image_ref": m.Repository.ValueString(),
		"result": map[string]any{"changes": changes},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_compose_cp

// NewComposeCpAction returns docker_compose_cp.
func NewComposeCpAction() action.Action { return &composeCpAction{} }

type composeCpAction struct{ actBase }

type composeCpModel struct {
	Name          types.String `tfsdk:"name"`
	Service       types.String `tfsdk:"service"`
	Index         types.Int64  `tfsdk:"index"`
	Direction     types.String `tfsdk:"direction"`
	ContainerPath types.String `tfsdk:"container_path"`
	LocalPath     types.String `tfsdk:"local_path"`
	ResultPath    types.String `tfsdk:"result_path"`
}

const (
	composeCpFrom = "from_container"
	composeCpTo   = "to_container"
)

func (a *composeCpAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_cp"
}

func (a *composeCpAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Copies files between a compose service container and the machine running Terraform, like `docker compose cp`. " +
			"`from_container` extracts the container path (file or directory) into `local_path`; `to_container` packs `local_path` and " +
			"extracts it into the container directory `container_path`. The result is the container path's stat " +
			"(`result.path_stat`).",
		Attributes: composeOneContainerAttrs(map[string]aschema.Attribute{
			"direction": aschema.StringAttribute{Required: true,
				MarkdownDescription: "`from_container` or `to_container`.",
				Validators:          []validator.String{stringvalidator.OneOf(composeCpFrom, composeCpTo)}},
			"container_path": aschema.StringAttribute{Required: true,
				MarkdownDescription: "Absolute path in the container: the source (`from_container`) or the destination directory (`to_container`)."},
			"local_path": aschema.StringAttribute{Required: true,
				MarkdownDescription: "Local path: the destination directory (`from_container`, created when missing) or the file or directory to copy (`to_container`)."},
		}),
	}
}

// composeExtractTar extracts r into dir, refusing entries that escape it.
// Symbolic and hard links are skipped.
func composeExtractTar(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(path.Clean("/"+h.Name)))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q escapes the destination", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o777|0o200)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}

// composePackTar packs a local file or directory into a tar whose top entry
// is its base name.
func composePackTar(src string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	base := filepath.Dir(src)
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// composePathStat is the result.path_stat value.
func composePathStat(s container.PathStat) map[string]any {
	return map[string]any{"name": s.Name, "size": s.Size, "mode": uint32(s.Mode), "mtime": s.Mtime.UTC().Format(time.RFC3339Nano), "linkTarget": s.LinkTarget}
}

func (a *composeCpAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composeCpModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctr, err := composeServiceContainer(ctx, a.pd, m.Name.ValueString(), m.Service.ValueString(), m.Index, false)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_cp: no container", err.Error())
		return
	}
	cpath := m.ContainerPath.ValueString()
	local := expandHome(m.LocalPath.ValueString())
	var stat container.PathStat
	if m.Direction.ValueString() == composeCpFrom {
		res, err := c.CopyFromContainer(ctx, ctr.ID, client.CopyFromContainerOptions{SourcePath: cpath})
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_cp failed", fmt.Sprintf("%s:%s: %v", composeSummaryName(ctr), cpath, err))
			return
		}
		err = composeExtractTar(res.Content, local)
		res.Content.Close()
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_cp: cannot extract", err.Error())
			return
		}
		stat = res.Stat
	} else {
		tr, err := composePackTar(local)
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_cp: cannot read local path", err.Error())
			return
		}
		if _, err := c.CopyToContainer(ctx, ctr.ID, client.CopyToContainerOptions{DestinationPath: cpath, Content: tr}); err != nil {
			resp.Diagnostics.AddError("docker_compose_cp failed", fmt.Sprintf("%s:%s: %v", composeSummaryName(ctr), cpath, err))
			return
		}
		st, err := c.ContainerStatPath(ctx, ctr.ID, client.ContainerStatPathOptions{Path: cpath})
		if err == nil {
			stat = st.Stat
		}
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-cp", map[string]any{
		"name": m.Name.ValueString(), "container_id": ctr.ID, "path": cpath,
		"result": map[string]any{"path_stat": composePathStat(stat)},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_compose_export

// NewComposeExportAction returns docker_compose_export.
func NewComposeExportAction() action.Action { return &composeExportAction{} }

type composeExportAction struct{ actBase }

type composeExportModel struct {
	Name       types.String `tfsdk:"name"`
	Service    types.String `tfsdk:"service"`
	Index      types.Int64  `tfsdk:"index"`
	Output     types.String `tfsdk:"output"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *composeExportAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_export"
}

func (a *composeExportAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Exports a compose service container's filesystem as a tar archive to a local file, like " +
			"`docker compose export -o`. The file is written atomically; the result carries its size (`result.archive_size`).",
		Attributes: composeOneContainerAttrs(map[string]aschema.Attribute{
			"output": aschema.StringAttribute{Required: true, MarkdownDescription: "Local file the tar archive is written to (CLI `--output`)."},
		}),
	}
}

func (a *composeExportAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composeExportModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctr, err := composeServiceContainer(ctx, a.pd, m.Name.ValueString(), m.Service.ValueString(), m.Index, false)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_export: no container", err.Error())
		return
	}
	rc, err := c.ContainerExport(ctx, ctr.ID, client.ContainerExportOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_export failed", err.Error())
		return
	}
	defer rc.Close()
	n, err := actWriteFile(m.Output.ValueString(), rc, nil)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_export: cannot write output", err.Error())
		return
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-export", map[string]any{
		"name": m.Name.ValueString(), "container_id": ctr.ID, "path": m.Output.ValueString(),
		"result": map[string]any{"archive_size": n},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_compose_wait

// NewComposeWaitAction returns docker_compose_wait.
func NewComposeWaitAction() action.Action { return &composeWaitAction{} }

type composeWaitAction struct{ actBase }

type composeWaitModel struct {
	Name        types.String `tfsdk:"name"`
	Services    types.List   `tfsdk:"services"`
	Timeout     types.String `tfsdk:"timeout"`
	FailOnError types.Bool   `tfsdk:"fail_on_error"`
	ResultPath  types.String `tfsdk:"result_path"`
}

func (a *composeWaitAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_wait"
}

func (a *composeWaitAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Blocks until the containers of compose services stop, like `docker compose wait`. Each container's exit code is " +
			"reported in `result.results`, keyed by container name.",
		Attributes: map[string]aschema.Attribute{
			"name":     composeActionProjectAttr(),
			"services": composeActionServicesAttr(),
			"timeout":  composeTimeoutAttr("the wait"),
			"fail_on_error": aschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Fail the action when a container exits non-zero. Default `false` (the codes are only reported)."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *composeWaitAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composeWaitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	cs, err := composeContainers(ctx, a.pd, composeSelector{Project: m.Name.ValueString(), Services: stringListFrom(ctx, m.Services, &resp.Diagnostics), All: true})
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_wait: cannot list containers", err.Error())
		return
	}
	if len(cs) == 0 {
		resp.Diagnostics.AddError("docker_compose_wait: no containers", fmt.Sprintf("project %q has no containers for the selected services", m.Name.ValueString()))
		return
	}
	timeout := actTimeout(m.Timeout, composeActionDefaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := map[string]any{}
	var failed []string
	for _, s := range cs {
		wr := c.ContainerWait(ctx, s.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case r := <-wr.Result:
			results[composeSummaryName(s)] = map[string]any{"exit_code": r.StatusCode, "error": composeWaitErr(r)}
			if r.StatusCode != 0 {
				failed = append(failed, fmt.Sprintf("%s exited %d", composeSummaryName(s), r.StatusCode))
			}
			actProgress(ctx, resp, "%s exited with code %d", composeSummaryName(s), r.StatusCode)
		case err := <-wr.Error:
			if ctx.Err() != nil {
				err = fmt.Errorf("did not stop within %s", timeout)
			}
			resp.Diagnostics.AddError("docker_compose_wait failed", fmt.Sprintf("%s: %v", composeSummaryName(s), err))
			return
		}
	}
	if len(failed) > 0 && m.FailOnError.ValueBool() {
		resp.Diagnostics.AddError("docker_compose_wait: containers failed", strings.Join(failed, "; "))
		return
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-wait", map[string]any{
		"name": m.Name.ValueString(), "service_names": composeServiceNames(cs),
		"result": map[string]any{"results": results},
	}, m.ResultPath)
}

func composeWaitErr(r container.WaitResponse) any {
	if r.Error == nil || r.Error.Message == "" {
		return nil
	}
	return r.Error.Message
}
