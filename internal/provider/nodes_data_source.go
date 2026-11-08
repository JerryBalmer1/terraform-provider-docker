package provider

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*nodesDSDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*nodesDSDataSource)(nil)
)

// NewNodesDataSource returns the docker_nodes data source.
func NewNodesDataSource() datasource.DataSource { return &nodesDSDataSource{} }

type nodesDSDataSource struct {
	pd *providerData
}

type nodesDSModel struct {
	Role  types.String `tfsdk:"role"`
	Nodes types.List   `tfsdk:"nodes"`
}

func (d *nodesDSDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nodes"
}

func (d *nodesDSDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the swarm's nodes through the provider's Docker host (which must be a manager), " +
			"sorted by hostname, optionally filtered by `role`.",
		Attributes: map[string]schema.Attribute{
			"role": schema.StringAttribute{
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf(swarmValidRoles...)},
				MarkdownDescription: "Only list nodes with this role: `worker` or `manager`.",
			},
			"nodes": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The nodes, sorted by hostname then ID.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":             schema.StringAttribute{Computed: true, MarkdownDescription: "Node ID."},
						"hostname":       schema.StringAttribute{Computed: true, MarkdownDescription: "Node hostname."},
						"role":           schema.StringAttribute{Computed: true, MarkdownDescription: "`worker` or `manager`."},
						"availability":   schema.StringAttribute{Computed: true, MarkdownDescription: "`active`, `pause` or `drain`."},
						"state":          schema.StringAttribute{Computed: true, MarkdownDescription: "`ready`, `down`, `disconnected` or `unknown`."},
						"addr":           schema.StringAttribute{Computed: true, MarkdownDescription: "IP address the node is reached at."},
						"labels":         schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Node labels."},
						"engine_version": schema.StringAttribute{Computed: true, MarkdownDescription: "Docker Engine version."},
						"leader":         schema.BoolAttribute{Computed: true, MarkdownDescription: "True for the raft leader."},
					},
				},
			},
		},
	}
}

func (d *nodesDSDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// nodesDSListOptions builds the NodeList filter for an optional role.
func nodesDSListOptions(role string) client.NodeListOptions {
	args := client.Filters{}
	if role != "" {
		args.Add("role", role)
	}
	return client.NodeListOptions{Filters: args}
}

// nodesDSListValue converts nodes to the `nodes` list value.
func nodesDSListValue(nodes []swarm.Node) types.List {
	swarmSortNodes(nodes)
	elems := make([]attr.Value, 0, len(nodes))
	for _, n := range nodes {
		elems = append(elems, swarmNodeObjectValue(swarmFlattenNode(n)))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: swarmNodeObjectAttrTypes}, elems)
}

func (d *nodesDSDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg nodesDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || d.pd == nil {
		return
	}
	nodesRes, err := d.pd.Client.NodeList(ctx, nodesDSListOptions(cfg.Role.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError("Cannot list swarm nodes", "The provider's Docker host must be a swarm manager: "+err.Error())
		return
	}
	nodes := nodesRes.Items
	cfg.Nodes = nodesDSListValue(nodes)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
