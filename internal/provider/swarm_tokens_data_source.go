package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*swarmTokensDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*swarmTokensDataSource)(nil)
)

// NewSwarmTokensDataSource returns the docker_swarm_tokens data source.
func NewSwarmTokensDataSource() datasource.DataSource { return &swarmTokensDataSource{} }

type swarmTokensDataSource struct {
	pd *providerData
}

type swarmTokensModel struct {
	ID      types.String `tfsdk:"id"`
	Worker  types.String `tfsdk:"worker"`
	Manager types.String `tfsdk:"manager"`
}

func (d *swarmTokensDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_tokens"
}

func (d *swarmTokensDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the join tokens of the swarm the provider's Docker host manages (SwarmInspect). " +
			"Use it to join nodes to a swarm that was not created by `docker_swarm_init` in this configuration. The host must be a manager.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, MarkdownDescription: "Swarm cluster ID."},
			"worker":  schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Token that joins a node as a worker."},
			"manager": schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Token that joins a node as a manager."},
		},
	}
}

func (d *swarmTokensDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *swarmTokensDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		return
	}
	swRes, err := d.pd.Client.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect swarm", "The provider's Docker host must be a swarm manager: "+err.Error())
		return
	}
	sw := swRes.Swarm
	m := swarmTokensModel{
		ID:      types.StringValue(sw.ID),
		Worker:  types.StringValue(sw.JoinTokens.Worker),
		Manager: types.StringValue(sw.JoinTokens.Manager),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
