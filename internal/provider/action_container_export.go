package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var _ action.ActionWithConfigure = (*containerExportAction)(nil)

// NewContainerExportAction returns the docker_container_export action.
func NewContainerExportAction() action.Action { return &containerExportAction{} }

type containerExportAction struct{ actBase }

type containerExportActionModel struct {
	ContainerID types.String `tfsdk:"container_id"`
	Output      types.String `tfsdk:"output"`
	ResultPath  types.String `tfsdk:"result_path"`
}

func (a *containerExportAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_export"
}

func (a *containerExportAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Writes a container's filesystem to a local tar file, like `docker container export --output`. " +
			"The file is written next to its final name and renamed into place, so a failed export never leaves a partial tar at `output`. " +
			"The result object holds the archive size; `archive_sha256` is the file's sha256.",
		Attributes: map[string]schema.Attribute{
			"container_id": schema.StringAttribute{Required: true, MarkdownDescription: "The container to export (running or stopped), by ID or name."},
			"output": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Local file to write, on the machine running Terraform (CLI `--output`). Parent directories are created; an existing file is replaced. `~` expands to the home directory.",
			},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *containerExportAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m containerExportActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ref, p := m.ContainerID.ValueString(), m.Output.ValueString()
	insp, _, err := copInspect(ctx, c, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "docker_container_export", ref, err)
		return
	}
	rc, err := c.ContainerExport(ctx, insp.ID, client.ContainerExportOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_container_export failed", fmt.Sprintf("container %q: %v", ref, err))
		return
	}
	defer rc.Close()
	actProgress(ctx, resp, "exporting container %s to %s", ref, p)
	h := sha256.New()
	n, err := actWriteFile(p, io.TeeReader(rc, h), func(w int64) { actProgress(ctx, resp, "exported %s", actHumanSize(uint64(w))) })
	if err != nil {
		resp.Diagnostics.AddError("docker_container_export failed", err.Error())
		return
	}
	actProgress(ctx, resp, "exported container %s: %s written to %s", ref, actHumanSize(uint64(n)), p)
	actFinishResult(ctx, resp, a.pd, "engine:container-export", map[string]any{
		"container_id":   insp.ID,
		"path":           p,
		"archive_sha256": hex.EncodeToString(h.Sum(nil)),
		"result":         map[string]any{"archive_size": n},
	}, m.ResultPath)
}
