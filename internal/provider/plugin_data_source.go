package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*pluginDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*pluginDataSource)(nil)
)

// NewPluginDataSource is the docker_plugin data source constructor.
func NewPluginDataSource() datasource.DataSource { return &pluginDataSource{} }

type pluginDataSource struct {
	pd *providerData
}

type pluginDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	Alias           types.String `tfsdk:"alias"`
	Name            types.String `tfsdk:"name"`
	PluginReference types.String `tfsdk:"plugin_reference"`
	Enabled         types.Bool   `tfsdk:"enabled"`
	Env             types.Set    `tfsdk:"env"`
	contractEnginePluginData
}

func (d *pluginDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin"
}

func (d *pluginDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	one := []validator.String{stringvalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("alias"))}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a managed plugin installed on the provider's daemon by `id` or `alias` (`GET /plugins/{name}/json`). " +
			"Fails if no such plugin exists. The raw `inspect_json` output is sensitive because plugin settings can hold credentials.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional: true, Computed: true, Validators: one,
				MarkdownDescription: "Plugin ID (or unique ID prefix). Exactly one of `id` and `alias` is required.",
			},
			"alias": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Plugin name on the daemon, e.g. `vieux/sshfs:latest` or an install alias such as `sshfs`.",
			},
			"name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Familiar reference of the plugin, e.g. `vieux/sshfs:latest` (same spelling as `docker_plugin.name`).",
			},
			"plugin_reference": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Fully qualified reference the plugin was installed from, e.g. `docker.io/vieux/sshfs:latest`.",
			},
			"enabled": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the plugin is enabled."},
			"env": schema.SetAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Current plugin settings as `KEY=VALUE`.",
			},
		}, "engine:plugin-data"),
	}
}

func (d *pluginDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *pluginDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m pluginDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || d.pd == nil || !d.pd.gateItemAPI(ctx, "engine:plugin-data", &resp.Diagnostics) {
		return
	}
	ref := m.ID.ValueString()
	if ref == "" {
		ref = m.Alias.ValueString()
	}
	pRes, err := d.pd.Client.PluginInspect(ctx, ref, client.PluginInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot find plugin", fmt.Sprintf("%q: %v", ref, err))
		return
	}
	p := pRes.Plugin
	var rm pluginResourceModel
	rm.Alias = m.Alias
	pluginApply(&rm, &p)
	m.ID, m.Alias, m.Name, m.PluginReference, m.Enabled, m.Env = rm.ID, rm.Alias, rm.Name, rm.PluginReference, rm.Enabled, rm.Env
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(pRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
