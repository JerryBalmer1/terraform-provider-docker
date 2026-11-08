package provider

import (
	"context"
	"fmt"

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
	Container types.String `tfsdk:"container"`
	Path      types.String `tfsdk:"path"`
}

func (a *containerExportAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_export"
}

func (a *containerExportAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Writes a container's filesystem to a local tar file, like `docker export`. " +
			"The file is written next to its final name and renamed into place, so a failed export never leaves a partial tar at `path`.",
		Attributes: map[string]schema.Attribute{
			"container": schema.StringAttribute{Required: true, MarkdownDescription: "Name or ID of the container (running or stopped)."},
			"path": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Local file to write, on the machine running Terraform. Parent directories are created; an existing file is replaced. `~` expands to the home directory.",
			},
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
	ctr, p := m.Container.ValueString(), m.Path.ValueString()
	rc, err := c.ContainerExport(ctx, ctr, client.ContainerExportOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_container_export failed", fmt.Sprintf("container %q: %v", ctr, err))
		return
	}
	defer rc.Close()
	actProgress(ctx, resp, "exporting container %s to %s", ctr, p)
	n, err := actWriteFile(p, rc, func(w int64) { actProgress(ctx, resp, "exported %s", actHumanSize(uint64(w))) })
	if err != nil {
		resp.Diagnostics.AddError("docker_container_export failed", err.Error())
		return
	}
	actProgress(ctx, resp, "exported container %s: %s written to %s", ctr, actHumanSize(uint64(n)), p)
}
