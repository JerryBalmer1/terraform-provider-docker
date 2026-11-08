package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/image"
)

var (
	_ datasource.DataSource              = (*imageDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*imageDataSource)(nil)
)

// NewImageDataSource returns the docker_image data source.
func NewImageDataSource() datasource.DataSource { return &imageDataSource{} }

type imageDataSource struct {
	pd *providerData
}

type imageDataSourceModel struct {
	Name       types.String `tfsdk:"name"`
	ID         types.String `tfsdk:"id"`
	ImageID    types.String `tfsdk:"image_id"`
	RepoDigest types.String `tfsdk:"repo_digest"`
	RepoTags   types.List   `tfsdk:"repo_tags"`
	Labels     types.Map    `tfsdk:"labels"`
}

func (d *imageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (d *imageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an image already present on the provider's daemon (no pull). Fails if the image does not exist locally.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Image name, tag or ID to inspect, e.g. `nginx:1.27` or `sha256:...`.",
			},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Image ID (`sha256:...`)."},
			"image_id":    schema.StringAttribute{Computed: true, MarkdownDescription: "Image ID (`sha256:...`), same as `id`."},
			"repo_digest": schema.StringAttribute{Computed: true, MarkdownDescription: "Repository digest (`repo@sha256:...`) for the repository of `name`, if known to the daemon."},
			"repo_tags": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Every tag referencing this image on the daemon.",
			},
			"labels": schema.MapAttribute{
				Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Labels from the image config.",
			},
		},
	}
}

func (d *imageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *imageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var m imageDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	insp, err := d.pd.Client.ImageInspect(ctx, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect image", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
		return
	}
	imgDataSourceApply(&m, insp.InspectResponse)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// imgDataSourceApply fills the computed attributes from an inspect result.
func imgDataSourceApply(m *imageDataSourceModel, insp image.InspectResponse) {
	m.ID = types.StringValue(insp.ID)
	m.ImageID = types.StringValue(insp.ID)
	m.RepoDigest = stringOrNull(imgPickRepoDigest(m.Name.ValueString(), insp.RepoDigests))
	m.RepoTags = stringListValue(insp.RepoTags, false)
	var labels map[string]string
	if insp.Config != nil {
		labels = insp.Config.Labels
	}
	m.Labels = stringMapValue(labels, false)
}
