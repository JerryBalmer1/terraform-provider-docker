package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*networkDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*networkDataSource)(nil)
)

// NewNetworkDataSource is the data "docker_network" constructor.
func NewNetworkDataSource() datasource.DataSource { return &networkDataSource{} }

type networkDataSource struct {
	pd *providerData
}

type networkDataSourceModel struct {
	Name       types.String `tfsdk:"name"`
	ID         types.String `tfsdk:"id"`
	Driver     types.String `tfsdk:"driver"`
	Scope      types.String `tfsdk:"scope"`
	Internal   types.Bool   `tfsdk:"internal"`
	Attachable types.Bool   `tfsdk:"attachable"`
	Ingress    types.Bool   `tfsdk:"ingress"`
	EnableIPv6 types.Bool   `tfsdk:"enable_ipv6"`
	IPAMDriver types.String `tfsdk:"ipam_driver"`
	IPAMConfig types.List   `tfsdk:"ipam_config"`
	Options    types.Map    `tfsdk:"options"`
	Labels     types.Map    `tfsdk:"labels"`
	Containers types.Map    `tfsdk:"containers"`
	contractEngineNetworkData
}

var networkDataContainerAttrTypes = map[string]attr.Type{
	"name":         types.StringType,
	"endpoint_id":  types.StringType,
	"mac_address":  types.StringType,
	"ipv4_address": types.StringType,
	"ipv6_address": types.StringType,
}

var networkDataIPAMAttrTypes = map[string]attr.Type{
	"subnet":      types.StringType,
	"ip_range":    types.StringType,
	"gateway":     types.StringType,
	"aux_address": types.MapType{ElemType: types.StringType},
}

func (d *networkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an existing Docker network (e.g. `bridge`, `host`, or one created outside Terraform) by name or ID (`GET /networks/{id}`). " +
			"`containers` lists the containers attached on this daemon; `inspect` holds the whole response.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Network name, ID or unique ID prefix."},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Network ID."},
			"driver":      schema.StringAttribute{Computed: true, MarkdownDescription: "Network driver."},
			"scope":       schema.StringAttribute{Computed: true, MarkdownDescription: "`local` or `swarm`."},
			"internal":    schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether external access is restricted."},
			"attachable":  schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether standalone containers can attach (overlay)."},
			"ingress":     schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this is the Swarm ingress network."},
			"enable_ipv6": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether IPv6 is enabled (`EnableIPv6`)."},
			"ipam_driver": schema.StringAttribute{Computed: true, MarkdownDescription: "IPAM driver."},
			"ipam_config": schema.ListNestedAttribute{
				Computed: true, MarkdownDescription: "IPAM pools, including daemon-assigned ones.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"subnet":      schema.StringAttribute{Computed: true, MarkdownDescription: "Subnet (CIDR)."},
					"ip_range":    schema.StringAttribute{Computed: true, MarkdownDescription: "Allocation range."},
					"gateway":     schema.StringAttribute{Computed: true, MarkdownDescription: "Gateway address."},
					"aux_address": schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Auxiliary addresses."},
				}},
			},
			"options": schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Driver options."},
			"labels":  schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Network labels."},
			"containers": schema.MapNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Containers attached to the network on this daemon, keyed by container ID (`Containers` in `GET /networks/{id}`).",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "Container name."},
					"endpoint_id":  schema.StringAttribute{Computed: true, MarkdownDescription: "Endpoint ID of the attachment."},
					"mac_address":  schema.StringAttribute{Computed: true, MarkdownDescription: "MAC address of the endpoint."},
					"ipv4_address": schema.StringAttribute{Computed: true, MarkdownDescription: "IPv4 address with prefix length, e.g. `172.19.0.2/16`."},
					"ipv6_address": schema.StringAttribute{Computed: true, MarkdownDescription: "IPv6 address with prefix length, empty without IPv6."},
				}},
			},
		}, "engine:network-data"),
	}
}

func (d *networkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// networkDataFromInspect fills the data source model from inspect output.
func networkDataFromInspect(n network.Inspect, name types.String) networkDataSourceModel {
	pools := make([]attr.Value, 0, len(n.IPAM.Config))
	for _, c := range n.IPAM.Config {
		pools = append(pools, types.ObjectValueMust(networkDataIPAMAttrTypes, map[string]attr.Value{
			"subnet":      types.StringValue(prefixString(c.Subnet)),
			"ip_range":    types.StringValue(prefixString(c.IPRange)),
			"gateway":     types.StringValue(addrString(c.Gateway)),
			"aux_address": stringMapValue(auxAddressStrings(c.AuxAddress), false),
		}))
	}
	return networkDataSourceModel{
		Name:       name,
		ID:         types.StringValue(n.ID),
		Driver:     types.StringValue(n.Driver),
		Scope:      types.StringValue(n.Scope),
		Internal:   types.BoolValue(n.Internal),
		Attachable: types.BoolValue(n.Attachable),
		Ingress:    types.BoolValue(n.Ingress),
		EnableIPv6: types.BoolValue(n.EnableIPv6),
		IPAMDriver: types.StringValue(n.IPAM.Driver),
		IPAMConfig: types.ListValueMust(types.ObjectType{AttrTypes: networkDataIPAMAttrTypes}, pools),
		Options:    stringMapValue(n.Options, false),
		Labels:     stringMapValue(n.Labels, false),
		Containers: networkDataContainers(n.Containers),
	}
}

// networkDataContainers maps the inspect Containers map.
func networkDataContainers(in map[string]network.EndpointResource) types.Map {
	vals := map[string]attr.Value{}
	for id, e := range in {
		vals[id] = types.ObjectValueMust(networkDataContainerAttrTypes, map[string]attr.Value{
			"name":         types.StringValue(e.Name),
			"endpoint_id":  types.StringValue(e.EndpointID),
			"mac_address":  types.StringValue(e.MacAddress.String()),
			"ipv4_address": types.StringValue(prefixString(e.IPv4Address)),
			"ipv6_address": types.StringValue(prefixString(e.IPv6Address)),
		})
	}
	return types.MapValueMust(types.ObjectType{AttrTypes: networkDataContainerAttrTypes}, vals)
}

func (d *networkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg networkDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || d.pd == nil {
		return
	}
	id, err := resolveNetworkID(ctx, d.pd.Client, cfg.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot find network", err.Error())
		return
	}
	nRes, err := d.pd.Client.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect network", err.Error())
		return
	}
	state := networkDataFromInspect(nRes.Network, cfg.Name)
	resp.Diagnostics.Append(state.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": nRes.Raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
