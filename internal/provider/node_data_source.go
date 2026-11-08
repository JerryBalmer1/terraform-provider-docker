package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*nodeDSDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*nodeDSDataSource)(nil)
)

// NewNodeDataSource returns the docker_node data source.
func NewNodeDataSource() datasource.DataSource { return &nodeDSDataSource{} }

type nodeDSDataSource struct {
	pd *providerData
}

type nodeDSModel struct {
	ID            types.String `tfsdk:"id"`
	Hostname      types.String `tfsdk:"hostname"`
	Role          types.String `tfsdk:"role"`
	Availability  types.String `tfsdk:"availability"`
	State         types.String `tfsdk:"state"`
	Addr          types.String `tfsdk:"addr"`
	Labels        types.Map    `tfsdk:"labels"`
	EngineVersion types.String `tfsdk:"engine_version"`
	Leader        types.Bool   `tfsdk:"leader"`
}

// nodeDSFromInfo fills a docker_node model from a flattened node.
func nodeDSFromInfo(i swarmNodeInfo) nodeDSModel {
	return nodeDSModel{
		ID:            types.StringValue(i.ID),
		Hostname:      types.StringValue(i.Hostname),
		Role:          types.StringValue(i.Role),
		Availability:  types.StringValue(i.Availability),
		State:         types.StringValue(i.State),
		Addr:          types.StringValue(i.Addr),
		Labels:        stringMapValue(i.Labels, false),
		EngineVersion: types.StringValue(i.EngineVersion),
		Leader:        types.BoolValue(i.Leader),
	}
}

// nodeDSRef picks the lookup reference and the attribute it came from.
func nodeDSRef(m nodeDSModel) (ref, attr string) {
	if v, ok := knownString(m.ID); ok && v != "" {
		return v, "id"
	}
	return m.Hostname.ValueString(), "hostname"
}

func (d *nodeDSDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node"
}

func (d *nodeDSDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one swarm node through the provider's Docker host (which must be a manager), " +
			"by `id` (full ID or unique prefix) or `hostname`. Set exactly one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional: true, Computed: true,
				Validators:          []validator.String{stringvalidator.ExactlyOneOf(path.MatchRoot("hostname"))},
				MarkdownDescription: "Node ID or unique ID prefix; resolved to the full ID. Conflicts with `hostname`.",
			},
			"hostname": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Node hostname (or node name). Conflicts with `id`.",
			},
			"role":           schema.StringAttribute{Computed: true, MarkdownDescription: "`worker` or `manager`."},
			"availability":   schema.StringAttribute{Computed: true, MarkdownDescription: "`active`, `pause` or `drain`."},
			"state":          schema.StringAttribute{Computed: true, MarkdownDescription: "Node status: `ready`, `down`, `disconnected` or `unknown`."},
			"addr":           schema.StringAttribute{Computed: true, MarkdownDescription: "IP address the node is reached at."},
			"labels":         schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Node labels (`node.labels.*`)."},
			"engine_version": schema.StringAttribute{Computed: true, MarkdownDescription: "Docker Engine version on the node."},
			"leader":         schema.BoolAttribute{Computed: true, MarkdownDescription: "True when the node is the current raft leader."},
		},
	}
}

func (d *nodeDSDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *nodeDSDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg nodeDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || d.pd == nil {
		return
	}
	ref, attrName := nodeDSRef(cfg)
	id, err := resolveNodeID(ctx, d.pd.Client, ref)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root(attrName), "Cannot find swarm node", err.Error())
		return
	}
	nRes, err := d.pd.Client.NodeInspect(ctx, id, client.NodeInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect swarm node", err.Error())
		return
	}
	n := nRes.Node
	m := nodeDSFromInfo(swarmFlattenNode(n))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
