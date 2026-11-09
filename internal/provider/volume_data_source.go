package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*volumeDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*volumeDataSource)(nil)
)

// NewVolumeDataSource is the data "docker_volume" constructor.
func NewVolumeDataSource() datasource.DataSource { return &volumeDataSource{} }

type volumeDataSource struct {
	pd *providerData
}

type volumeDataSourceModel struct {
	Name       types.String `tfsdk:"name"`
	ID         types.String `tfsdk:"id"`
	Driver     types.String `tfsdk:"driver"`
	DriverOpts types.Map    `tfsdk:"driver_opts"`
	Labels     types.Map    `tfsdk:"labels"`
	Mountpoint types.String `tfsdk:"mountpoint"`
	Scope      types.String `tfsdk:"scope"`
	contractEngineVolumeData
}

func (d *volumeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (d *volumeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an existing Docker volume by name (`GET /volumes/{name}`). " +
			"The raw `inspect_json` output is sensitive because driver options can hold credentials.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Volume name."},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Volume name (volumes have no separate ID)."},
			"driver":      schema.StringAttribute{Computed: true, MarkdownDescription: "Volume driver."},
			"driver_opts": schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Driver options."},
			"labels":      schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Volume labels."},
			"mountpoint":  schema.StringAttribute{Computed: true, MarkdownDescription: "Host path of the volume data."},
			"scope":       schema.StringAttribute{Computed: true, MarkdownDescription: "`local` or `global`."},
		}, "engine:volume-data"),
	}
}

func (d *volumeDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func volumeDataFromInspect(v volume.Volume) volumeDataSourceModel {
	return volumeDataSourceModel{
		Name:       types.StringValue(v.Name),
		ID:         types.StringValue(v.Name),
		Driver:     types.StringValue(v.Driver),
		DriverOpts: stringMapValue(v.Options, false),
		Labels:     stringMapValue(v.Labels, false),
		Mountpoint: types.StringValue(v.Mountpoint),
		Scope:      types.StringValue(v.Scope),
	}
}

func (d *volumeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg volumeDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || d.pd == nil {
		return
	}
	vRes, err := d.pd.Client.VolumeInspect(ctx, cfg.Name.ValueString(), client.VolumeInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot find volume", err.Error())
		return
	}
	state := volumeDataFromInspect(vRes.Volume)
	resp.Diagnostics.Append(state.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(vRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
