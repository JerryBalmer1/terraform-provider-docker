package provider

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/network"
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
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*networkResource)(nil)
	_ resource.ResourceWithConfigure   = (*networkResource)(nil)
	_ resource.ResourceWithImportState = (*networkResource)(nil)
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
	ID         types.String             `tfsdk:"id"`
	Name       types.String             `tfsdk:"name"`
	Driver     types.String             `tfsdk:"driver"`
	Attachable types.Bool               `tfsdk:"attachable"`
	Internal   types.Bool               `tfsdk:"internal"`
	IPv6       types.Bool               `tfsdk:"ipv6"`
	Ingress    types.Bool               `tfsdk:"ingress"`
	IPAMDriver types.String             `tfsdk:"ipam_driver"`
	IPAMConfig []networkIPAMConfigModel `tfsdk:"ipam_config"`
	Options    types.Map                `tfsdk:"options"`
	Labels     types.Map                `tfsdk:"labels"`
	Scope      types.String             `tfsdk:"scope"`
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
		Attributes: map[string]schema.Attribute{
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
			"ipv6": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Enable IPv6. Changing this forces a new resource.",
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
				Computed: true, MarkdownDescription: "`local` or `swarm`, as reported by the daemon.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
		Blocks: map[string]schema.Block{
			"ipam_config": schema.ListNestedBlock{
				MarkdownDescription: "Static IPAM pools. Without this block the daemon assigns a subnet itself (not reported in state). " +
					"Changing this forces a new resource.",
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
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

func (r *networkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
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
		ipam.Config = append(ipam.Config, network.IPAMConfig{
			Subnet: c.Subnet.ValueString(), IPRange: c.IPRange.ValueString(), Gateway: c.Gateway.ValueString(), AuxAddress: aux,
		})
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
			Subnet:     stringOrNull(g.Subnet),
			IPRange:    stringOrNull(g.IPRange),
			Gateway:    stringOrNull(g.Gateway),
			AuxAddress: stringMapValue(g.AuxAddress, true),
		}
		if p != nil {
			if p.Gateway.IsNull() {
				m.Gateway = types.StringNull()
			}
			if p.IPRange.IsNull() {
				m.IPRange = types.StringNull()
			}
			m.AuxAddress = containerMapValue(p.AuxAddress, g.AuxAddress)
		}
		return m
	}
	for i := range prior {
		p := &prior[i]
		for j, g := range got {
			if !used[j] && g.Subnet == p.Subnet.ValueString() {
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
	out.IPv6 = types.BoolValue(n.EnableIPv6)
	out.Ingress = types.BoolValue(n.Ingress)
	ipamDriver := n.IPAM.Driver
	if ipamDriver == "" {
		ipamDriver = "default"
	}
	out.IPAMDriver = types.StringValue(ipamDriver)
	out.IPAMConfig = networkFlattenIPAM(n.IPAM.Config, prior.IPAMConfig, importing)
	opts := n.Options
	if !importing {
		opts = containerSubtractDefaults(n.Options, nil, containerMapKeys(prior.Options), true)
	}
	out.Options = containerMapValue(prior.Options, opts)
	out.Labels = containerMapValue(prior.Labels, userLabels(n.Labels, false))
	out.Scope = types.StringValue(n.Scope)
	return out
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
	ipv6 := plan.IPv6.ValueBool()
	opts := network.CreateOptions{
		Driver:     plan.Driver.ValueString(),
		Attachable: plan.Attachable.ValueBool(),
		Internal:   plan.Internal.ValueBool(),
		Ingress:    plan.Ingress.ValueBool(),
		EnableIPv6: &ipv6,
		IPAM:       ipam,
		Options:    stringMapFrom(ctx, plan.Options, &resp.Diagnostics),
		Labels:     mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue}),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.pd.Client.NetworkCreate(ctx, plan.Name.ValueString(), opts)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create network", err.Error())
		return
	}
	plan.ID = types.StringValue(created.ID)
	n, err := r.pd.Client.NetworkInspect(ctx, created.ID, network.InspectOptions{})
	if err != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
		resp.Diagnostics.AddError("Cannot inspect network", err.Error())
		return
	}
	state := networkFromInspect(n, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	n, err := r.pd.Client.NetworkInspect(ctx, prior.ID.ValueString(), network.InspectOptions{})
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect network", err.Error())
		return
	}
	state := networkFromInspect(n, prior)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is only reached for no-op plans: every argument forces replacement.
func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan networkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if err := r.pd.Client.NetworkRemove(ctx, state.ID.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove network", fmt.Sprintf("%s: %v (are containers or services still attached?)", state.Name.ValueString(), err))
	}
}

func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "Cannot import before the provider is configured.")
		return
	}
	id, err := resolveNetworkID(ctx, r.pd.Client, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot find network", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
