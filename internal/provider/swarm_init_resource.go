package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// swarmInitErrLocked marks a swarm manager that is locked by autolock.
var swarmInitErrLocked = errors.New("swarm manager is locked")

var (
	_ resource.Resource                = (*swarmInitResource)(nil)
	_ resource.ResourceWithConfigure   = (*swarmInitResource)(nil)
	_ resource.ResourceWithImportState = (*swarmInitResource)(nil)
)

// NewSwarmInitResource returns the docker_swarm_init resource.
func NewSwarmInitResource() resource.Resource { return &swarmInitResource{} }

type swarmInitResource struct {
	pd *providerData
}

type swarmInitModel struct {
	ID               types.String `tfsdk:"id"`
	AdvertiseAddr    types.String `tfsdk:"advertise_addr"`
	ListenAddr       types.String `tfsdk:"listen_addr"`
	DefaultAddrPool  types.List   `tfsdk:"default_addr_pool"`
	SubnetSize       types.Int64  `tfsdk:"subnet_size"`
	DataPathPort     types.Int64  `tfsdk:"data_path_port"`
	ForceNewCluster  types.Bool   `tfsdk:"force_new_cluster"`
	Autolock         types.Bool   `tfsdk:"autolock"`
	ClusterID        types.String `tfsdk:"cluster_id"`
	NodeID           types.String `tfsdk:"node_id"`
	ManagerJoinToken types.String `tfsdk:"manager_join_token"`
	WorkerJoinToken  types.String `tfsdk:"worker_join_token"`
	UnlockKey        types.String `tfsdk:"unlock_key"`
}

func (r *swarmInitResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_init"
}

func (r *swarmInitResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keepStr := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Initializes a Docker Swarm on the provider's Docker host (`docker swarm init` through the Engine API), " +
			"making that daemon the first manager. Exposes the cluster ID and the worker/manager join tokens for `docker_swarm_node`. " +
			"Destroying the resource makes the host leave the swarm with force, which dissolves a single-manager swarm.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: keepStr,
				MarkdownDescription: "Swarm cluster ID (same as `cluster_id`). This is the import ID.",
			},
			"advertise_addr": schema.StringAttribute{
				Optional: true, PlanModifiers: replaceStr,
				MarkdownDescription: "Address advertised to other nodes, as `<ip|interface>[:port]`, e.g. `10.0.0.5` or `eth0:2377`. " +
					"Needed when the host has several addresses. The daemon does not report it back, so the configured value is kept. " +
					"Changing this forces a new resource.",
			},
			"listen_addr": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(swarmDefaultListenAddr), PlanModifiers: replaceStr,
				MarkdownDescription: "Address for inter-manager communication, `<ip|interface>[:port]`. Default `" + swarmDefaultListenAddr + "`. " +
					"Changing this forces a new resource.",
			},
			"default_addr_pool": schema.ListAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown(), listplanmodifier.RequiresReplace()},
				MarkdownDescription: "CIDR pools that overlay network subnets are allocated from, e.g. `[\"10.20.0.0/16\"]`. " +
					"When unset the daemon default (`10.0.0.0/8`) is used and reported. Changing this forces a new resource.",
			},
			"subnet_size": schema.Int64Attribute{
				Optional: true, Computed: true, Validators: []validator.Int64{int64validator.Between(1, 32)},
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
				MarkdownDescription: "Prefix length of each subnet carved from `default_addr_pool`. Daemon default 24. Changing this forces a new resource.",
			},
			"data_path_port": schema.Int64Attribute{
				Optional: true, Computed: true, Validators: []validator.Int64{int64validator.Between(1024, 49151)},
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
				MarkdownDescription: "UDP port for VXLAN data-path traffic. Daemon default 4789. Changing this forces a new resource.",
			},
			"force_new_cluster": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
				MarkdownDescription: "Force a new single-manager cluster from the current state of this node (disaster recovery). " +
					"Only read at creation. Changing this forces a new resource.",
			},
			"autolock": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Enable manager autolock: managers need `unlock_key` after a daemon restart. Updated in place through SwarmUpdate.",
			},
			"cluster_id": schema.StringAttribute{
				Computed: true, PlanModifiers: keepStr,
				MarkdownDescription: "Swarm cluster ID reported by SwarmInspect.",
			},
			"node_id": schema.StringAttribute{
				Computed: true, PlanModifiers: keepStr,
				MarkdownDescription: "Node ID of the initial manager (the provider's host).",
			},
			"manager_join_token": schema.StringAttribute{
				Computed: true, Sensitive: true, PlanModifiers: keepStr,
				MarkdownDescription: "Token that joins a node as a manager. Pass it to `docker_swarm_node.join_token`.",
			},
			"worker_join_token": schema.StringAttribute{
				Computed: true, Sensitive: true, PlanModifiers: keepStr,
				MarkdownDescription: "Token that joins a node as a worker. Pass it to `docker_swarm_node.join_token`.",
			},
			"unlock_key": schema.StringAttribute{
				Computed: true, Sensitive: true,
				MarkdownDescription: "Swarm unlock key when `autolock` is true, otherwise null.",
			},
		},
	}
}

func (r *swarmInitResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// swarmInitBuildRequest turns the plan into an InitRequest.
func swarmInitBuildRequest(ctx context.Context, m swarmInitModel, diags *diag.Diagnostics) swarm.InitRequest {
	req := swarm.InitRequest{
		ListenAddr:    m.ListenAddr.ValueString(),
		AdvertiseAddr: m.AdvertiseAddr.ValueString(),
	}
	if req.ListenAddr == "" {
		req.ListenAddr = swarmDefaultListenAddr
	}
	req.DefaultAddrPool = stringListFrom(ctx, m.DefaultAddrPool, diags)
	if v, ok := knownInt64(m.SubnetSize); ok {
		req.SubnetSize = uint32(v)
	}
	if v, ok := knownInt64(m.DataPathPort); ok {
		req.DataPathPort = uint32(v)
	}
	if v, ok := knownBool(m.ForceNewCluster); ok {
		req.ForceNewCluster = v
	}
	if v, ok := knownBool(m.Autolock); ok {
		req.AutoLockManagers = v
	}
	return req
}

// swarmInitFlatten copies daemon-reported values into m.
func swarmInitFlatten(m *swarmInitModel, nodeID string, sw swarm.Swarm, unlockKey string) {
	m.ID = types.StringValue(sw.ID)
	m.ClusterID = types.StringValue(sw.ID)
	m.NodeID = types.StringValue(nodeID)
	m.ManagerJoinToken = types.StringValue(sw.JoinTokens.Manager)
	m.WorkerJoinToken = types.StringValue(sw.JoinTokens.Worker)
	m.DefaultAddrPool = stringListValue(sw.DefaultAddrPool, false)
	m.SubnetSize = types.Int64Value(int64(sw.SubnetSize))
	m.DataPathPort = types.Int64Value(int64(sw.DataPathPort))
	m.Autolock = types.BoolValue(sw.Spec.EncryptionConfig.AutoLockManagers)
	if sw.Spec.EncryptionConfig.AutoLockManagers && unlockKey != "" {
		m.UnlockKey = types.StringValue(unlockKey)
	} else {
		m.UnlockKey = types.StringNull()
	}
	if m.ListenAddr.IsNull() || m.ListenAddr.IsUnknown() {
		m.ListenAddr = types.StringValue(swarmDefaultListenAddr)
	}
	if m.ForceNewCluster.IsNull() || m.ForceNewCluster.IsUnknown() {
		m.ForceNewCluster = types.BoolValue(false)
	}
}

// swarmInitRefresh reads the daemon into m. It returns false when the host is
// no longer an active member of the swarm recorded in m.ID. A locked manager
// (autolock after restart) is reported present with m left unchanged and
// swarmInitErrLocked.
func swarmInitRefresh(ctx context.Context, c *client.Client, m *swarmInitModel) (bool, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return false, fmt.Errorf("reading daemon info: %w", err)
	}
	if info.Swarm.LocalNodeState == swarm.LocalNodeStateLocked {
		return true, swarmInitErrLocked
	}
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateActive {
		return false, nil
	}
	sw, err := c.SwarmInspect(ctx)
	if err != nil {
		return false, fmt.Errorf("inspecting swarm: %w", err)
	}
	if id, ok := knownString(m.ID); ok && id != "" && id != sw.ID {
		return false, nil
	}
	unlock := ""
	if sw.Spec.EncryptionConfig.AutoLockManagers {
		k, err := c.SwarmGetUnlockKey(ctx)
		if err != nil {
			return false, fmt.Errorf("reading unlock key: %w", err)
		}
		unlock = k.UnlockKey
	}
	swarmInitFlatten(m, info.Swarm.NodeID, sw, unlock)
	return true, nil
}

func (r *swarmInitResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan swarmInitModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	initReq := swarmInitBuildRequest(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.pd.Client.SwarmInit(ctx, initReq); err != nil {
		resp.Diagnostics.AddError("Cannot initialize swarm", err.Error())
		return
	}
	plan.ID = types.StringUnknown()
	ok, err := swarmInitRefresh(ctx, r.pd.Client, &plan)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("daemon is not an active swarm member after init")
		}
		resp.Diagnostics.AddError("Cannot read swarm after init", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *swarmInitResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state swarmInitModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	ok, err := swarmInitRefresh(ctx, r.pd.Client, &state)
	if errors.Is(err, swarmInitErrLocked) {
		resp.Diagnostics.AddWarning("Swarm is locked",
			"The provider's Docker host is a locked swarm manager (autolock). Unlock it with the unlock_key; state was left unchanged.")
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read swarm", err.Error())
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *swarmInitResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state swarmInitModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	want, _ := knownBool(plan.Autolock)
	sw, err := r.pd.Client.SwarmInspect(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect swarm", err.Error())
		return
	}
	if sw.Spec.EncryptionConfig.AutoLockManagers != want {
		spec := sw.Spec
		spec.EncryptionConfig.AutoLockManagers = want
		if err := r.pd.Client.SwarmUpdate(ctx, sw.Version, spec, swarm.UpdateFlags{}); err != nil {
			resp.Diagnostics.AddError("Cannot update swarm autolock", err.Error())
			return
		}
	}
	plan.ID = state.ID
	ok, err := swarmInitRefresh(ctx, r.pd.Client, &plan)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("swarm %s is gone", state.ID.ValueString())
		}
		resp.Diagnostics.AddError("Cannot read swarm after update", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *swarmInitResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.pd == nil {
		return
	}
	if err := swarmLeave(ctx, r.pd.Client); err != nil {
		resp.Diagnostics.AddError("Cannot leave swarm", err.Error())
	}
}

func (r *swarmInitResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "docker_swarm_init import needs a configured provider.")
		return
	}
	sw, err := r.pd.Client.SwarmInspect(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect swarm", fmt.Sprintf("the provider's host must be a swarm manager to import docker_swarm_init: %v", err))
		return
	}
	if sw.ID != req.ID {
		resp.Diagnostics.AddError("Swarm ID mismatch",
			fmt.Sprintf("import ID %q does not match the cluster ID %q of the provider's Docker host.", req.ID, sw.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), sw.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("listen_addr"), swarmDefaultListenAddr)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_new_cluster"), false)...)
}
