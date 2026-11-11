package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*networkResource)(nil)
	_ resource.ResourceWithConfigure   = (*networkResource)(nil)
	_ resource.ResourceWithImportState = (*networkResource)(nil)
	_ resource.ResourceWithIdentity    = (*networkResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*networkResource)(nil)
)

// NewNetworkResource is the docker_network constructor.
func NewNetworkResource() resource.Resource { return &networkResource{} }

type networkResource struct {
	pd *providerData
}

type networkIPAMConfigModel struct {
	Subnet     types.String `tfsdk:"subnet"`
	IPRange    types.String `tfsdk:"ip_range"`
	Gateway    types.String `tfsdk:"gateway"`
	AuxAddress types.Map    `tfsdk:"aux_address"`
}

type networkResourceModel struct {
	ID          types.String             `tfsdk:"id"`
	Name        types.String             `tfsdk:"name"`
	Driver      types.String             `tfsdk:"driver"`
	Attachable  types.Bool               `tfsdk:"attachable"`
	Internal    types.Bool               `tfsdk:"internal"`
	EnableIPv6  types.Bool               `tfsdk:"enable_ipv6"`
	Ingress     types.Bool               `tfsdk:"ingress"`
	IPAMDriver  types.String             `tfsdk:"ipam_driver"`
	IPAMConfig  []networkIPAMConfigModel `tfsdk:"ipam_config"`
	IPAMOptions types.Map                `tfsdk:"ipam_options"`
	Options     types.Map                `tfsdk:"options"`
	Labels      types.Map                `tfsdk:"labels"`
	Scope       types.String             `tfsdk:"scope"`
	ConfigOnly  types.Bool               `tfsdk:"config_only"`
	ConfigFrom  types.String             `tfsdk:"config_from"`
	EnableIPv4  types.Bool               `tfsdk:"enable_ipv4"`
	Adopt       types.Bool               `tfsdk:"adopt"`
	contractEngineNetwork
}

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rrBool := []planmodifier.Bool{boolplanmodifier.RequiresReplace()}
	rrMap := []planmodifier.Map{mapplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Docker network (`NetworkCreate`): a local `bridge`/`macvlan`/`ipvlan` network, or a Swarm-scoped `overlay` " +
			"network when the daemon is a Swarm manager. Docker networks cannot be changed after creation, so every argument forces " +
			"a new network. Import by network name or ID.",
		Attributes: contractAddResourceAttributes(map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Network ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true, MarkdownDescription: "Network name. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"driver": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("bridge"),
				MarkdownDescription: "Network driver: `bridge` (default), `overlay` (Swarm manager only), `macvlan`, `ipvlan`, or a plugin. " +
					"Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"attachable": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Let standalone containers attach to an `overlay` network. Changing this forces a new resource.",
				PlanModifiers:       rrBool,
			},
			"internal": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Restrict external access to the network. Changing this forces a new resource.",
				PlanModifiers:       rrBool,
			},
			"enable_ipv6": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Enable IPv6 on the network (`EnableIPv6`). Changing this forces a new resource.",
				PlanModifiers:       rrBool,
			},
			"ingress": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Create the Swarm routing-mesh (ingress) network. Changing this forces a new resource.",
				PlanModifiers:       rrBool,
			},
			"ipam_driver": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("default"),
				MarkdownDescription: "IPAM driver. Default `default`. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ipam_options": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "IPAM driver options (`IPAM.Options`), e.g. for a third-party IPAM plugin. Changing this forces a new resource.",
				PlanModifiers:       rrMap,
			},
			"options": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Driver options, e.g. `{ \"com.docker.network.bridge.name\" = \"br-app\" }`. Options the driver adds " +
					"itself are not reported. Changing this forces a new resource.",
				PlanModifiers: rrMap,
			},
			"labels": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Network labels. Docker has no network update, so changing this forces a new resource.",
				PlanModifiers:       rrMap,
			},
			"scope": schema.StringAttribute{
				Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.OneOf("local", "swarm", "global")},
				MarkdownDescription: "Level at which the network exists (`Scope`): `local` (this host) or `swarm` (cluster-wide; `global` is " +
					"also accepted by plugins). Unset lets the driver decide, and the daemon's value is reported. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"config_only": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Create a config-only network (`ConfigOnly`): a placeholder holding IPAM and driver options for other networks' " +
					"`config_from`; containers cannot attach to it. Changing this forces a new resource.",
				PlanModifiers: rrBool,
			},
			"config_from": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of the config-only network that provides this network's configuration (`ConfigFrom.Network`), e.g. " +
					"for a swarm-scoped `macvlan` network. The network takes its IPAM, IPv4/IPv6 settings and labels from it: `labels`, " +
					"`ipam_config`, `ipam_options`, `enable_ipv4 = false` and `enable_ipv6 = true` are rejected with it (the Engine refuses " +
					"them), and no owner or managed-by label is added. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enable_ipv4": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Enable IPv4 on the network (`EnableIPv4`); `false` disables IPv4 IPAM (only for networks that support it). " +
					"Unset uses the daemon default (enabled), which is not reported. Changing this forces a new resource." + apiSince("1.48"),
				PlanModifiers: rrBool,
			},
			"adopt": ownerAdoptAttribute(),
		}, "engine:network"),
		Blocks: map[string]schema.Block{
			"ipam_config": schema.ListNestedBlock{
				MarkdownDescription: "Static IPAM pools. Without this block the daemon assigns a subnet itself (not reported in state). " +
					"Forces replacement when a configured value differs from the network's: a missing block, or an argument left unset in a " +
					"pool, accepts what the network already has (e.g. the daemon-assigned pool a kreuzwerker/docker state records) and only " +
					"drops it from state.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplaceIf(networkIPAMRequiresReplace,
					"Forces replacement when a configured IPAM value differs from the network's.",
					"Forces replacement when a configured IPAM value differs from the network's.")},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"subnet":   schema.StringAttribute{Optional: true, MarkdownDescription: "Subnet in CIDR form, e.g. `10.20.0.0/24`."},
					"ip_range": schema.StringAttribute{Optional: true, MarkdownDescription: "Sub-range to allocate container IPs from."},
					"gateway":  schema.StringAttribute{Optional: true, MarkdownDescription: "Gateway address. Unset lets the daemon pick (not reported)."},
					"aux_address": schema.MapAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Auxiliary addresses the IPAM driver must not hand out, name to IP.",
					},
				}},
			},
		},
	}
}

// networkIPAMRequiresReplace: the pools in state are the network's (after
// a create, the configured ones; after an import or a move from
// kreuzwerker/docker, every pool the daemon reports, gateway included). A
// config without ipam_config, or a pool argument left unset, means "the
// daemon's choice", which the existing network satisfies; only a configured
// value that differs, or a different number of configured pools, needs a
// new network.
func networkIPAMRequiresReplace(ctx context.Context, req planmodifier.ListRequest, resp *listplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() || req.ConfigValue.IsUnknown() || len(req.ConfigValue.Elements()) == 0 {
		return
	}
	var cfg, state []networkIPAMConfigModel
	resp.Diagnostics.Append(req.ConfigValue.ElementsAs(ctx, &cfg, false)...)
	resp.Diagnostics.Append(req.StateValue.ElementsAs(ctx, &state, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(cfg) != len(state) {
		resp.RequiresReplace = true
		return
	}
	differs := func(c, s types.String) bool { return !c.IsNull() && (c.IsUnknown() || !c.Equal(s)) }
	for i := range cfg {
		c, st := cfg[i], state[i]
		if differs(c.Subnet, st.Subnet) || differs(c.IPRange, st.IPRange) || differs(c.Gateway, st.Gateway) ||
			(!c.AuxAddress.IsNull() && (c.AuxAddress.IsUnknown() || !c.AuxAddress.Equal(st.AuxAddress))) {
			resp.RequiresReplace = true
			return
		}
	}
}

func (r *networkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan checks Engine API minimums (enable_ipv4) and the owner label.
func (r *networkResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.Config.Raw.IsNull() {
		r.pd.gateAPISince(ctx, "docker_network", req.Config, &resp.Diagnostics)
	}
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var name types.String
	var adopt types.Bool
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("adopt"), &adopt)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(ownerPlanCheck(ctx, r.pd, req.Private, "network", name.ValueString(), adopt)...)
}

// networkExpandIPAM builds the IPAM config from the plan.
func networkExpandIPAM(ctx context.Context, driver string, cfgs []networkIPAMConfigModel) (*network.IPAM, error) {
	ipam := &network.IPAM{Driver: driver}
	for i, c := range cfgs {
		var diags diag.Diagnostics
		aux := stringMapFrom(ctx, c.AuxAddress, &diags)
		if diags.HasError() {
			return nil, fmt.Errorf("ipam_config[%d].aux_address: %v", i, diags)
		}
		pool, err := ipamConfigFrom(c.Subnet.ValueString(), c.IPRange.ValueString(), c.Gateway.ValueString(), aux)
		if err != nil {
			return nil, fmt.Errorf("ipam_config[%d].%w", i, err)
		}
		ipam.Config = append(ipam.Config, pool)
	}
	return ipam, nil
}

// networkFlattenIPAM maps inspect IPAM pools to blocks. Unless importing,
// only pools whose subnet the prior state lists are reported (daemon-assigned
// pools are not), and gateway/ip_range are only reported when the prior set
// them (the daemon fills in a gateway itself).
func networkFlattenIPAM(got []network.IPAMConfig, prior []networkIPAMConfigModel, importing bool) []networkIPAMConfigModel {
	out := []networkIPAMConfigModel{}
	used := make([]bool, len(got))
	pick := func(p *networkIPAMConfigModel, g network.IPAMConfig) networkIPAMConfigModel {
		m := networkIPAMConfigModel{
			Subnet:     stringOrNull(prefixString(g.Subnet)),
			IPRange:    stringOrNull(prefixString(g.IPRange)),
			Gateway:    stringOrNull(addrString(g.Gateway)),
			AuxAddress: stringMapValue(auxAddressStrings(g.AuxAddress), true),
		}
		if p != nil {
			if p.Gateway.IsNull() {
				m.Gateway = types.StringNull()
			}
			if p.IPRange.IsNull() {
				m.IPRange = types.StringNull()
			}
			m.AuxAddress = containerMapValue(p.AuxAddress, auxAddressStrings(g.AuxAddress))
		}
		return m
	}
	for i := range prior {
		p := &prior[i]
		for j, g := range got {
			if !used[j] && prefixString(g.Subnet) == p.Subnet.ValueString() {
				used[j] = true
				out = append(out, pick(p, g))
				break
			}
		}
	}
	if importing {
		for j, g := range got {
			if !used[j] {
				out = append(out, pick(nil, g))
			}
		}
	}
	return out
}

// networkFromInspect maps NetworkInspect output onto prior. An import is
// detected by a null prior name.
func networkFromInspect(n network.Inspect, prior networkResourceModel) networkResourceModel {
	importing := prior.Name.IsNull()
	out := prior
	out.ID = types.StringValue(n.ID)
	out.Name = types.StringValue(n.Name)
	out.Driver = types.StringValue(n.Driver)
	// A swarm network inspected right after create reports Driver "" until the
	// allocator fills DriverState (daemon/cluster/convert/network.go): keep prior.
	if n.Driver == "" && !prior.Driver.IsNull() && !prior.Driver.IsUnknown() && prior.Driver.ValueString() != "" {
		out.Driver = prior.Driver
	}
	out.Attachable = types.BoolValue(n.Attachable)
	out.Internal = types.BoolValue(n.Internal)
	out.EnableIPv6 = types.BoolValue(n.EnableIPv6)
	out.Ingress = types.BoolValue(n.Ingress)
	ipamDriver := n.IPAM.Driver
	if ipamDriver == "" {
		ipamDriver = "default"
	}
	out.IPAMDriver = types.StringValue(ipamDriver)
	out.IPAMConfig = networkFlattenIPAM(n.IPAM.Config, prior.IPAMConfig, importing)
	// A config-only network is stored, never plumbed: libnetwork resets
	// its type to "null" and skips IPAM allocation (Engine 29.9.3
	// daemon/libnetwork/controller.go:560-568), and inspect reports only
	// allocated pools (daemon/network.go:813-881, buildIPAMResources reads
	// IpamInfo, empty here). So Driver "null" and no IPAM pools say nothing
	// about the configuration: keep the configured driver and pools (an
	// import has no prior and reads "null").
	if n.ConfigOnly && !importing {
		if d, ok := knownString(prior.Driver); ok && n.Driver == "null" {
			out.Driver = types.StringValue(d)
		}
		if len(n.IPAM.Config) == 0 && len(prior.IPAMConfig) > 0 {
			out.IPAMConfig = prior.IPAMConfig
		}
	}
	out.IPAMOptions = containerMapValue(prior.IPAMOptions, n.IPAM.Options)
	opts := n.Options
	if !importing {
		opts = containerSubtractDefaults(n.Options, nil, containerMapKeys(prior.Options), true)
	}
	out.Options = containerMapValue(prior.Options, opts)
	out.Labels = containerMapValue(prior.Labels, ownerStrip(userLabels(n.Labels, false)))
	// A config_from network reports the config-only network's labels
	// (applyConfigurationTo copies them) and cannot have its own, so its
	// configured labels (none, see ValidateConfig) stay.
	if n.ConfigFrom.Network != "" && !importing {
		out.Labels = prior.Labels
	}
	out.Scope = types.StringValue(n.Scope)
	out.ConfigOnly = types.BoolValue(n.ConfigOnly)
	out.ConfigFrom = containerStringRead(prior.ConfigFrom, n.ConfigFrom.Network, "")
	out.EnableIPv4 = types.BoolNull()
	if containerKnown(prior.EnableIPv4) {
		out.EnableIPv4 = types.BoolValue(n.EnableIPv4)
	}
	return out
}

func (r *networkResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan networkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	ipam, err := networkExpandIPAM(ctx, plan.IPAMDriver.ValueString(), plan.IPAMConfig)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ipam_config"), "Invalid ipam_config", err.Error())
		return
	}
	ipam.Options = stringMapFrom(ctx, plan.IPAMOptions, &resp.Diagnostics)
	ipv6 := plan.EnableIPv6.ValueBool()
	opts := client.NetworkCreateOptions{
		Driver:     plan.Driver.ValueString(),
		Attachable: plan.Attachable.ValueBool(),
		Internal:   plan.Internal.ValueBool(),
		Ingress:    plan.Ingress.ValueBool(),
		EnableIPv6: &ipv6,
		IPAM:       ipam,
		Options:    stringMapFrom(ctx, plan.Options, &resp.Diagnostics),
		Labels:     r.pd.ownerInject(mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue})),
		ConfigOnly: plan.ConfigOnly.ValueBool(),
		ConfigFrom: plan.ConfigFrom.ValueString(),
	}
	// A network built from a config-only one takes its labels (and IPAM,
	// IPv4/IPv6 settings) from it, and libnetwork refuses any label of its
	// own: "user-specified configurations are not supported if the network
	// depends on a configuration network" (Engine 29.9.3
	// daemon/libnetwork/network.go:410-419; copied by applyConfigurationTo,
	// :447-457). So it gets no managed-by or owner label; it carries the
	// config-only network's, which this provider labelled.
	if cf, ok := knownString(plan.ConfigFrom); ok && cf != "" {
		opts.Labels = nil
	}
	if sc, ok := knownString(plan.Scope); ok {
		opts.Scope = sc
	}
	if v, ok := knownBool(plan.EnableIPv4); ok {
		opts.EnableIPv4 = &v
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if r.pd.Owner != "" {
		if ex, err := r.pd.Client.NetworkInspect(ctx, plan.Name.ValueString(), client.NetworkInspectOptions{}); err == nil && ex.Network.Name == plan.Name.ValueString() {
			if err := ownerCreateCheck(r.pd, "network", plan.Name.ValueString(), ex.Network.Labels); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("name"), "Network owned by another configuration", err.Error())
				return
			}
		}
	}
	created, err := r.pd.Client.NetworkCreate(ctx, plan.Name.ValueString(), opts)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create network", err.Error())
		return
	}
	plan.ID = types.StringValue(created.ID)
	nRes, err := r.pd.Client.NetworkInspect(ctx, created.ID, client.NetworkInspectOptions{})
	if err != nil {
		plan.contractSetNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, created.ID)...)
		resp.Diagnostics.AddError("Cannot inspect network", err.Error())
		return
	}
	state := networkFromInspect(nRes.Network, plan)
	resp.Diagnostics.Append(state.contractFill(ctx, r.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(nRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	nRes, err := r.pd.Client.NetworkInspect(ctx, prior.ID.ValueString(), client.NetworkInspectOptions{})
	n := nRes.Network
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect network", err.Error())
		return
	}
	state := networkFromInspect(n, prior)
	if !ctrNilPrivate(resp.Private) {
		resp.Diagnostics.Append(ownerRecord(ctx, resp.Private, n.Labels)...)
	}
	resp.Diagnostics.Append(state.contractFill(ctx, r.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(nRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

// Update is only reached for no-op plans (every argument forces
// replacement); it re-reads the network so the contract outputs are known.
func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan networkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	nRes, err := r.pd.Client.NetworkInspect(ctx, plan.ID.ValueString(), client.NetworkInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect network", err.Error())
		return
	}
	plan = networkFromInspect(nRes.Network, plan)
	resp.Diagnostics.Append(plan.contractFill(ctx, r.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(nRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if _, err := r.pd.Client.NetworkRemove(ctx, state.ID.ValueString(), client.NetworkRemoveOptions{}); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove network", fmt.Sprintf("%s: %v (are containers or services still attached?)", state.Name.ValueString(), err))
	}
}

func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "Cannot import before the provider is configured.")
		return
	}
	ref := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := resolveNetworkID(ctx, r.pd.Client, ref)
	if err != nil {
		resp.Diagnostics.AddError("Cannot find network", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, id)...)
}
