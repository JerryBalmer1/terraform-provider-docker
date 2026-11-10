package provider

// Pass 5 (Q1): docker_container `networks`, a map keyed by network name whose
// values mirror NetworkingConfig.EndpointsConfig (EndpointSettings). The
// settings are inputs; the read-only EndpointSettings fields (IP address,
// gateway, ...) are computed attributes of the same element. Adding or
// removing a key, or changing an element's inputs, is applied in place with
// NetworkConnect / NetworkDisconnect; it never replaces the container.

import (
	"context"
	"fmt"
	"net"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// ctrNetworkModel is one networks element.
type ctrNetworkModel struct {
	Aliases             types.Set    `tfsdk:"aliases"`
	IPv4Address         types.String `tfsdk:"ipv4_address"`
	IPv6Address         types.String `tfsdk:"ipv6_address"`
	LinkLocalIPs        types.Set    `tfsdk:"link_local_ips"`
	MacAddress          types.String `tfsdk:"mac_address"`
	DriverOpts          types.Map    `tfsdk:"driver_opts"`
	GwPriority          types.Int64  `tfsdk:"gw_priority"`
	Links               types.List   `tfsdk:"links"`
	NetworkID           types.String `tfsdk:"network_id"`
	EndpointID          types.String `tfsdk:"endpoint_id"`
	Gateway             types.String `tfsdk:"gateway"`
	IPAddress           types.String `tfsdk:"ip_address"`
	IPPrefixLen         types.Int64  `tfsdk:"ip_prefix_len"`
	IPv6Gateway         types.String `tfsdk:"ipv6_gateway"`
	GlobalIPv6Address   types.String `tfsdk:"global_ipv6_address"`
	GlobalIPv6PrefixLen types.Int64  `tfsdk:"global_ipv6_prefix_len"`
}

// ctrNetworkAttrTypes is the object type of one networks element.
var ctrNetworkAttrTypes = map[string]attr.Type{
	"aliases":                types.SetType{ElemType: types.StringType},
	"ipv4_address":           types.StringType,
	"ipv6_address":           types.StringType,
	"link_local_ips":         types.SetType{ElemType: types.StringType},
	"mac_address":            types.StringType,
	"driver_opts":            types.MapType{ElemType: types.StringType},
	"gw_priority":            types.Int64Type,
	"links":                  types.ListType{ElemType: types.StringType},
	"network_id":             types.StringType,
	"endpoint_id":            types.StringType,
	"gateway":                types.StringType,
	"ip_address":             types.StringType,
	"ip_prefix_len":          types.Int64Type,
	"ipv6_gateway":           types.StringType,
	"global_ipv6_address":    types.StringType,
	"global_ipv6_prefix_len": types.Int64Type,
}

// ctrNetworkInputs are the element attributes the user sets; a change in any
// of them reconnects the container to that network.
var ctrNetworkInputs = []string{"aliases", "ipv4_address", "ipv6_address", "link_local_ips", "driver_opts", "gw_priority", "links"}

func ctrNetworksSchema() schema.MapNestedAttribute {
	keep := []planmodifier.String{ctrNetKeepState{}}
	keepInt := []planmodifier.Int64{ctrNetKeepState{}}
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc, PlanModifiers: keep}
	}
	return schema.MapNestedAttribute{
		Optional: true,
		MarkdownDescription: "Networks the container is attached to, keyed by network name (e.g. a `docker_network`'s `name`); each value " +
			"mirrors `NetworkingConfig.EndpointsConfig` (`EndpointSettings`). Use `{}` for an endpoint with no settings. " +
			"All endpoints are configured at create; the container's `NetworkMode` is `network_mode` when set, else the alphabetically first key. " +
			"Adding or removing a key, or changing an endpoint's settings, connects or disconnects the container in place " +
			"(`NetworkConnect` / `NetworkDisconnect`) without replacing it. Unset, the container joins the daemon's default `bridge`, " +
			"which is not reported.",
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"aliases": schema.SetAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Extra DNS names of the container on this network (`Aliases`)."},
			"ipv4_address": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Static IPv4 address (`IPAMConfig.IPv4Address`); needs a user-defined subnet."},
			"ipv6_address": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Static IPv6 address (`IPAMConfig.IPv6Address`); needs an IPv6-enabled network with a user-defined IPv6 subnet."},
			"link_local_ips": schema.SetAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Link-local addresses (`IPAMConfig.LinkLocalIPs`)."},
			"mac_address": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
				MarkdownDescription: "MAC address of the endpoint (`MacAddress`). Unset, the daemon generates one, which is reported."},
			"driver_opts": schema.MapAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Driver-specific endpoint options (`DriverOpts`)."},
			"gw_priority": schema.Int64Attribute{Optional: true,
				MarkdownDescription: "Gateway priority (`GwPriority`): the endpoint with the highest priority provides the container's default gateway." + apiSince("1.48")},
			"links": schema.ListAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Legacy links on this network, each `<name|id>[:<alias>]` (`Links`, `docker network connect --link`): " +
					"the alias resolves to that container on this network only."},
			"network_id":          str("ID of the network (`NetworkID`)."),
			"endpoint_id":         str("ID of the endpoint (`EndpointID`)."),
			"gateway":             str("IPv4 gateway of the network (`Gateway`)."),
			"ip_address":          str("IPv4 address of the container on this network (`IPAddress`)."),
			"ip_prefix_len":       schema.Int64Attribute{Computed: true, PlanModifiers: keepInt, MarkdownDescription: "IPv4 prefix length (`IPPrefixLen`)."},
			"ipv6_gateway":        str("IPv6 gateway of the network (`IPv6Gateway`)."),
			"global_ipv6_address": str("Global IPv6 address of the container on this network (`GlobalIPv6Address`)."),
			"global_ipv6_prefix_len": schema.Int64Attribute{Computed: true, PlanModifiers: keepInt,
				MarkdownDescription: "Global IPv6 prefix length (`GlobalIPv6PrefixLen`)."},
		}},
	}
}

// ctrNetworksFrom decodes a known networks map (nil when null or unknown).
func ctrNetworksFrom(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]ctrNetworkModel {
	if !containerKnown(m) {
		return nil
	}
	out := map[string]ctrNetworkModel{}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out
}

// ctrSortedKeys returns the keys of m sorted.
func ctrSortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ctrEndpointFromModel builds the EndpointSettings for one element.
func ctrEndpointFromModel(ctx context.Context, n ctrNetworkModel, diags *diag.Diagnostics) (*network.EndpointSettings, error) {
	ep, err := containerEndpointSettings(stringSetFrom(ctx, n.Aliases, diags), n.IPv4Address.ValueString(), n.IPv6Address.ValueString())
	if err != nil {
		return nil, err
	}
	if ll := stringSetFrom(ctx, n.LinkLocalIPs, diags); len(ll) > 0 {
		addrs, err := parseAddrs("link_local_ips", ll)
		if err != nil {
			return nil, err
		}
		if ep.IPAMConfig == nil {
			ep.IPAMConfig = &network.EndpointIPAMConfig{}
		}
		ep.IPAMConfig.LinkLocalIPs = addrs
	}
	if mac, ok := knownString(n.MacAddress); ok && mac != "" {
		hw, err := net.ParseMAC(mac)
		if err != nil {
			return nil, fmt.Errorf("mac_address: %q is not a MAC address: %w", mac, err)
		}
		ep.MacAddress = network.HardwareAddr(hw)
	}
	ep.DriverOpts = ctrStringMap(n.DriverOpts)
	if v, ok := knownInt64(n.GwPriority); ok {
		ep.GwPriority = int(v)
	}
	ep.Links = stringListFrom(ctx, n.Links, diags)
	return ep, nil
}

// ctrNetworkingConfig builds the create-time NetworkingConfig and the
// NetworkMode from networks and network_mode.
func ctrNetworkingConfig(ctx context.Context, nets map[string]ctrNetworkModel, networkMode string, diags *diag.Diagnostics) (*network.NetworkingConfig, string) {
	if len(nets) == 0 {
		return nil, networkMode
	}
	cfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	keys := ctrSortedKeys(nets)
	for _, k := range keys {
		ep, err := ctrEndpointFromModel(ctx, nets[k], diags)
		if err != nil {
			diags.AddAttributeError(path.Root("networks").AtMapKey(k), "Invalid networks entry", err.Error())
			continue
		}
		cfg.EndpointsConfig[k] = ep
	}
	if networkMode == "" {
		networkMode = keys[0]
	}
	return cfg, networkMode
}

// ctrNetInputsEqual reports whether two elements have the same inputs.
// mac_address counts only when a is known and set (an unset mac_address
// is computed).
func ctrNetInputsEqual(a, b ctrNetworkModel) bool {
	if !ctrSetEqual(a.Aliases, b.Aliases) || !ctrSetEqual(a.LinkLocalIPs, b.LinkLocalIPs) ||
		!a.IPv4Address.Equal(b.IPv4Address) || !a.IPv6Address.Equal(b.IPv6Address) ||
		!ctrMapEqual(a.DriverOpts, b.DriverOpts) || !ctrInt64Equal(a.GwPriority, b.GwPriority) || !ctrListEqual(a.Links, b.Links) {
		return false
	}
	if mac, ok := knownString(a.MacAddress); ok && mac != "" && !a.MacAddress.Equal(b.MacAddress) {
		return false
	}
	return true
}

// ctrSetEqual treats null and empty as equal.
func ctrSetEqual(a, b types.Set) bool {
	if a.IsUnknown() || b.IsUnknown() {
		return a.Equal(b)
	}
	if len(a.Elements()) == 0 && len(b.Elements()) == 0 {
		return true
	}
	return a.Equal(b)
}

// ctrListEqual treats null and empty as equal.
func ctrListEqual(a, b types.List) bool {
	if a.IsUnknown() || b.IsUnknown() {
		return a.Equal(b)
	}
	if len(a.Elements()) == 0 && len(b.Elements()) == 0 {
		return true
	}
	return a.Equal(b)
}

func ctrMapEqual(a, b types.Map) bool {
	if a.IsUnknown() || b.IsUnknown() {
		return a.Equal(b)
	}
	if len(a.Elements()) == 0 && len(b.Elements()) == 0 {
		return true
	}
	return a.Equal(b)
}

// ctrInt64Equal treats null and 0 as equal.
func ctrInt64Equal(a, b types.Int64) bool {
	if a.IsUnknown() || b.IsUnknown() {
		return a.Equal(b)
	}
	return a.ValueInt64() == b.ValueInt64()
}

// ctrFlattenNetworks maps NetworkSettings.Networks onto the networks map.
// Keys of prior are kept (a key may be a network ID or ID prefix). Endpoints
// prior does not list are reported as drift, except the network the daemon
// attached because of NetworkMode (bridge/host/none, or the network_mode the
// user set). Aliases the daemon adds (container name, ID, short ID) are
// dropped.
func ctrFlattenNetworks(eps map[string]*network.EndpointSettings, networkMode string, userMode bool, containerID, containerName string, prior types.Map) types.Map {
	priorNets := map[string]ctrNetworkModel{}
	if containerKnown(prior) {
		_ = prior.ElementsAs(context.Background(), &priorNets, false)
	}
	keys := ctrSortedKeys(eps)
	used := map[string]bool{}
	vals := map[string]attr.Value{}
	for _, ref := range ctrSortedKeys(priorNets) {
		for _, k := range keys {
			ep := eps[k]
			if used[k] || ep == nil || !containerNetworkMatches(ref, k, ep.NetworkID) {
				continue
			}
			used[k] = true
			vals[ref] = ctrNetworkValue(ep, priorNets[ref], containerID, containerName)
			break
		}
	}
	for _, k := range keys {
		ep := eps[k]
		if used[k] || ep == nil {
			continue
		}
		if containerIsDefaultNetwork(k, networkMode) || (userMode && k == networkMode) {
			continue
		}
		vals[k] = ctrNetworkValue(ep, ctrNullNetwork(), containerID, containerName)
	}
	objType := types.ObjectType{AttrTypes: ctrNetworkAttrTypes}
	if len(vals) == 0 && !containerKnown(prior) {
		return types.MapNull(objType)
	}
	return types.MapValueMust(objType, vals)
}

// ctrNullNetwork is an element with every attribute null.
func ctrNullNetwork() ctrNetworkModel {
	return ctrNetworkModel{
		Aliases: types.SetNull(types.StringType), LinkLocalIPs: types.SetNull(types.StringType),
		IPv4Address: types.StringNull(), IPv6Address: types.StringNull(), MacAddress: types.StringNull(),
		DriverOpts: types.MapNull(types.StringType), GwPriority: types.Int64Null(), Links: types.ListNull(types.StringType),
	}
}

func ctrNetworkValue(ep *network.EndpointSettings, p ctrNetworkModel, containerID, containerName string) attr.Value {
	short := containerID
	if len(short) > 12 {
		short = short[:12]
	}
	var aliases []string
	for _, a := range ep.Aliases {
		if a == containerName || a == containerID || a == short {
			continue
		}
		aliases = append(aliases, a)
	}
	ipv4, ipv6 := "", ""
	var ll []string
	if ep.IPAMConfig != nil {
		ipv4, ipv6 = addrString(ep.IPAMConfig.IPv4Address), addrString(ep.IPAMConfig.IPv6Address)
		ll = addrStrings(ep.IPAMConfig.LinkLocalIPs)
	}
	mac := ""
	if len(ep.MacAddress) > 0 {
		mac = ep.MacAddress.String()
	}
	return types.ObjectValueMust(ctrNetworkAttrTypes, map[string]attr.Value{
		"aliases":                containerSetValue(p.Aliases, aliases),
		"ipv4_address":           stringOrNull(ipv4),
		"ipv6_address":           stringOrNull(ipv6),
		"link_local_ips":         containerSetValue(p.LinkLocalIPs, ll),
		"mac_address":            stringOrNull(mac),
		"driver_opts":            containerMapValue(p.DriverOpts, ep.DriverOpts),
		"gw_priority":            containerInt64Read(p.GwPriority, int64(ep.GwPriority)),
		"links":                  ctrLinksRead(ctrKnownList(p.Links), ep.Links),
		"network_id":             stringOrNull(ep.NetworkID),
		"endpoint_id":            stringOrNull(ep.EndpointID),
		"gateway":                stringOrNull(addrString(ep.Gateway)),
		"ip_address":             stringOrNull(addrString(ep.IPAddress)),
		"ip_prefix_len":          types.Int64Value(int64(ep.IPPrefixLen)),
		"ipv6_gateway":           stringOrNull(addrString(ep.IPv6Gateway)),
		"global_ipv6_address":    stringOrNull(addrString(ep.GlobalIPv6Address)),
		"global_ipv6_prefix_len": types.Int64Value(int64(ep.GlobalIPv6PrefixLen)),
	})
}

// ctrUpdateNetworks applies a networks change in place: disconnect removed
// and changed endpoints, then connect added and changed ones.
func ctrUpdateNetworks(ctx context.Context, pd *providerData, id string, plan, state map[string]ctrNetworkModel, diags *diag.Diagnostics) {
	var disconnect, connect []string
	for _, k := range ctrSortedKeys(state) {
		p, ok := plan[k]
		if !ok || !ctrNetInputsEqual(p, state[k]) {
			disconnect = append(disconnect, k)
		}
	}
	for _, k := range ctrSortedKeys(plan) {
		s, ok := state[k]
		if !ok || !ctrNetInputsEqual(plan[k], s) {
			connect = append(connect, k)
		}
	}
	for _, k := range disconnect {
		ref := k
		if nid, ok := knownString(state[k].NetworkID); ok && nid != "" {
			ref = nid
		}
		if _, err := pd.Client.NetworkDisconnect(ctx, ref, client.NetworkDisconnectOptions{Container: id, Force: true}); err != nil && !isNotFound(err) {
			diags.AddAttributeError(path.Root("networks").AtMapKey(k), "Cannot disconnect container from network", fmt.Sprintf("%s: %v", k, err))
			return
		}
	}
	for _, k := range connect {
		ep, err := ctrEndpointFromModel(ctx, plan[k], diags)
		if err != nil {
			diags.AddAttributeError(path.Root("networks").AtMapKey(k), "Invalid networks entry", err.Error())
			return
		}
		if _, err := pd.Client.NetworkConnect(ctx, k, client.NetworkConnectOptions{Container: id, EndpointConfig: ep}); err != nil {
			diags.AddAttributeError(path.Root("networks").AtMapKey(k), "Cannot connect container to network", fmt.Sprintf("%s: %v", k, err))
			return
		}
	}
}

// ctrNetKeepState keeps the prior value of a computed networks element
// attribute while the element's inputs are unchanged (like
// UseStateForUnknown, but a reconnected endpoint gets new values).
type ctrNetKeepState struct{}

func (ctrNetKeepState) Description(context.Context) string {
	return "Keeps the prior value while the endpoint's settings are unchanged."
}

func (m ctrNetKeepState) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (ctrNetKeepState) same(ctx context.Context, plan, state ctrAttrGetter, p path.Path) bool {
	var pe, se types.Object
	if d := plan.GetAttribute(ctx, p, &pe); d.HasError() {
		return false
	}
	if d := state.GetAttribute(ctx, p, &se); d.HasError() {
		return false
	}
	if !containerKnown(pe) || !containerKnown(se) {
		return false
	}
	var a, b ctrNetworkModel
	if d := pe.As(ctx, &a, basetypes.ObjectAsOptions{}); d.HasError() {
		return false
	}
	if d := se.As(ctx, &b, basetypes.ObjectAsOptions{}); d.HasError() {
		return false
	}
	return ctrNetInputsEqual(a, b)
}

func (m ctrNetKeepState) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if m.same(ctx, req.Plan, req.State, req.Path.ParentPath()) {
		resp.PlanValue = req.StateValue
	}
}

func (m ctrNetKeepState) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if m.same(ctx, req.Plan, req.State, req.Path.ParentPath()) {
		resp.PlanValue = req.StateValue
	}
}

// ctrAttrGetter is tfsdk.Plan / tfsdk.State.
type ctrAttrGetter interface {
	GetAttribute(ctx context.Context, p path.Path, target interface{}) diag.Diagnostics
}

// ctrKnownList returns l, or a typed null when l is the zero value.
func ctrKnownList(l types.List) types.List {
	if l.IsNull() {
		return types.ListNull(types.StringType)
	}
	return l
}
