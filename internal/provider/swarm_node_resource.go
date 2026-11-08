package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*swarmNodeResource)(nil)
	_ resource.ResourceWithConfigure   = (*swarmNodeResource)(nil)
	_ resource.ResourceWithImportState = (*swarmNodeResource)(nil)
	_ resource.ResourceWithIdentity    = (*swarmNodeResource)(nil)
)

// swarmNodeImportedKey is the private-state key set by ImportState. While it
// is set, filling in host/join_token/... in config updates state in place
// instead of forcing replacement.
const swarmNodeImportedKey = "swarm_node_imported"

// NewSwarmNodeResource returns the docker_swarm_node resource.
func NewSwarmNodeResource() resource.Resource { return &swarmNodeResource{} }

type swarmNodeResource struct {
	pd *providerData
}

type swarmNodeModel struct {
	ID             types.String `tfsdk:"id"`
	NodeID         types.String `tfsdk:"node_id"`
	Hostname       types.String `tfsdk:"hostname"`
	Host           types.String `tfsdk:"host"`
	TLS            *tlsModel    `tfsdk:"tls"`
	SSH            *sshModel    `tfsdk:"ssh"`
	JoinToken      types.String `tfsdk:"join_token"`
	ManagerAddress types.String `tfsdk:"manager_address"`
	AdvertiseAddr  types.String `tfsdk:"advertise_addr"`
	ListenAddr     types.String `tfsdk:"listen_addr"`
	Availability   types.String `tfsdk:"availability"`
	Role           types.String `tfsdk:"role"`
	Labels         types.Map    `tfsdk:"labels"`
}

func (r *swarmNodeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_node"
}

// swarmNodeRequiresReplace forces replacement on change, except right after
// import (see swarmNodeImportedKey).
func swarmNodeRequiresReplace() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(
		func(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !swarmNodeIsImported(ctx, req.Private)
		},
		"Changing this forces a new resource (except to fill it in after import).",
		"Changing this forces a new resource (except to fill it in after import).",
	)
}

type swarmPrivateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

func swarmNodeIsImported(ctx context.Context, p swarmPrivateGetter) bool {
	if p == nil {
		return false
	}
	b, _ := p.GetKey(ctx, swarmNodeImportedKey)
	return string(b) == "true"
}

// swarmNodeTLSBlockSchema mirrors the provider's tls block with resource
// schema types.
func swarmNodeTLSBlockSchema() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: "TLS client material for the joining node's `tcp://` `host`. Each value is PEM content or a path to a PEM file. " +
			"Only used to reach that daemon; changing it does not replace the node.",
		Attributes: map[string]schema.Attribute{
			"ca":   schema.StringAttribute{Optional: true, MarkdownDescription: "CA certificate (PEM or path)."},
			"cert": schema.StringAttribute{Optional: true, MarkdownDescription: "Client certificate (PEM or path)."},
			"key":  schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Client private key (PEM or path)."},
		},
	}
}

func (r *swarmNodeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{swarmNodeRequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Joins a Docker host to an existing swarm. The join is sent to the joining node's OWN daemon (`host`, " +
			"optionally with a `tls` block, or over SSH with an `ssh://` host and an `ssh` block); availability, role and labels are then set through the provider's Docker host, which must be a swarm manager. " +
			"Destroying the resource makes the node leave the swarm (force), waits until the manager reports it down, and removes it from the node list " +
			"(managers are demoted first).\n\nImport with the node ID; `host`, `join_token` and `manager_address` must then be set in configuration " +
			"(the next apply records them without replacing the node).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Swarm node ID (same as `node_id`).",
			},
			"node_id": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Swarm node ID, read from the joining daemon after the join.",
			},
			"hostname": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "Hostname the node reports to the swarm.",
			},
			"host": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "Docker endpoint of the JOINING node, e.g. `tcp://10.0.0.6:2376` or `ssh://admin@10.0.0.6` (with an optional `ssh` block for credentials). " +
					"Changing this forces a new resource.",
			},
			"join_token": schema.StringAttribute{
				Required: true, Sensitive: true, PlanModifiers: replace,
				MarkdownDescription: "Worker or manager join token, e.g. `docker_swarm_init.this.worker_join_token`. Changing this forces a new resource.",
			},
			"manager_address": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "Address of an existing manager as seen from the joining node, e.g. `10.0.0.5:2377`. Changing this forces a new resource.",
			},
			"advertise_addr": schema.StringAttribute{
				Optional: true, PlanModifiers: replace,
				MarkdownDescription: "Address the node advertises, `<ip|interface>[:port]`. Changing this forces a new resource.",
			},
			"listen_addr": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(swarmDefaultListenAddr), PlanModifiers: replace,
				MarkdownDescription: "Listen address for swarm traffic on the node. Default `" + swarmDefaultListenAddr + "`. Changing this forces a new resource.",
			},
			"availability": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(string(swarm.NodeAvailabilityActive)),
				Validators:          []validator.String{stringvalidator.OneOf(swarmValidAvailabilities...)},
				MarkdownDescription: "Scheduling availability: `active`, `pause` or `drain`. Default `active`. Updated in place through the manager.",
			},
			"role": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(string(swarm.NodeRoleWorker)),
				Validators: []validator.String{stringvalidator.OneOf(swarmValidRoles...)},
				MarkdownDescription: "Node role: `worker` or `manager`. Default `worker`. Updated in place (promote/demote) through the manager, " +
					"whichever token was used to join.",
			},
			"labels": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Node labels (`node.labels.*` in placement constraints). Updated in place; labels set outside Terraform are removed.",
			},
		},
		Blocks: map[string]schema.Block{
			"tls": swarmNodeTLSBlockSchema(),
			"ssh": sshResourceBlockSchema(swarmNodeSSHBlockDoc),
		},
	}
}

func (r *swarmNodeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// swarmNodeEndpoint builds the joining node's endpoint from the model.
func swarmNodeEndpoint(m swarmNodeModel, userAgent string) endpointConfig {
	ep := endpointConfig{Host: m.Host.ValueString(), UserAgent: userAgent}
	if m.TLS != nil {
		ep.CA = m.TLS.CA.ValueString()
		ep.Cert = m.TLS.Cert.ValueString()
		ep.Key = m.TLS.Key.ValueString()
	}
	return ep
}

// swarmNodeChange turns the plan into the node spec change applied through
// the manager.
func swarmNodeChange(ctx context.Context, m swarmNodeModel, diags *diag.Diagnostics) swarmNodeSpecChange {
	var ch swarmNodeSpecChange
	if v, ok := knownString(m.Availability); ok && v != "" {
		a := swarm.NodeAvailability(v)
		ch.Availability = &a
	}
	if v, ok := knownString(m.Role); ok && v != "" {
		ro := swarm.NodeRole(v)
		ch.Role = &ro
	}
	if !m.Labels.IsUnknown() {
		l := stringMapFrom(ctx, m.Labels, diags)
		if l == nil {
			l = map[string]string{}
		}
		ch.Labels = &l
	}
	return ch
}

// swarmNodeFlatten copies the manager's view of the node into m.
func swarmNodeFlatten(m *swarmNodeModel, n swarm.Node) {
	m.ID = types.StringValue(n.ID)
	m.NodeID = types.StringValue(n.ID)
	m.Hostname = types.StringValue(n.Description.Hostname)
	m.Availability = types.StringValue(string(n.Spec.Availability))
	m.Role = types.StringValue(string(n.Spec.Role))
	m.Labels = swarmLabelsValue(m.Labels, n.Spec.Labels)
	if m.ListenAddr.IsNull() || m.ListenAddr.IsUnknown() {
		m.ListenAddr = types.StringValue(swarmDefaultListenAddr)
	}
}

// swarmNodeSSHBlockDoc documents docker_swarm_node's ssh block.
const swarmNodeSSHBlockDoc = "Reach the JOINING node's daemon over SSH in pure Go (same shape as the provider's `ssh` block). " +
	"Set `host` to `ssh://user@node[:port]`; this block adds credentials and may override user, host or port. " +
	"Only used to reach that daemon; changing it does not replace the node."

// swarmNodeSSH resolves the node's SSH endpoint, or nil when host is not
// ssh:// and no ssh block is set. passwordWO is ssh.password_wo from config
// (write-only, so absent from plan and state).
func swarmNodeSSH(m swarmNodeModel, passwordWO string) (*sshConfig, error) {
	host := m.Host.ValueString()
	if m.SSH == nil && !isSSHHost(host) {
		return nil, nil
	}
	if !isSSHHost(host) {
		return nil, fmt.Errorf("the ssh block needs host = \"ssh://user@node[:port]\", got %q", host)
	}
	sm := m.SSH
	if sm != nil && passwordWO != "" {
		cp := *sm
		cp.PasswordWO = types.StringValue(passwordWO)
		sm = &cp
	}
	return sshConfigFrom(sm, host, os.Getenv)
}

func (r *swarmNodeResource) nodeClient(m swarmNodeModel, passwordWO string) (*client.Client, error) {
	ua := ""
	if r.pd != nil {
		ua = r.pd.Endpoint.UserAgent
	}
	ep := swarmNodeEndpoint(m, ua)
	sc, err := swarmNodeSSH(m, passwordWO)
	if err != nil {
		return nil, err
	}
	if sc != nil {
		ep.SSH = sc
		ep.Host = sc.dockerHost()
	}
	return newDockerClient(ep)
}

func (r *swarmNodeResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

func (r *swarmNodeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan swarmNodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	// ssh.password_wo is write-only: it is only in the config.
	var cfg swarmNodeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pwWO := ""
	if cfg.SSH != nil {
		pwWO = knownStr(cfg.SSH.PasswordWO)
	}
	if plan.SSH != nil {
		plan.SSH.PasswordWO = types.StringNull()
	}
	nc, err := r.nodeClient(plan, pwWO)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("host"), "Cannot build Docker client for the joining node", err.Error())
		return
	}
	defer nc.Close()
	joinReq := swarmBuildJoinRequest(plan.ManagerAddress.ValueString(), plan.JoinToken.ValueString(),
		plan.AdvertiseAddr.ValueString(), plan.ListenAddr.ValueString())
	nodeID, err := swarmJoinNode(ctx, nc, r.pd.Client, joinReq, swarmJoinTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Cannot join node to swarm", err.Error())
		return
	}
	// Record the node right away so a failure below leaves it tracked.
	plan.ID = types.StringValue(nodeID)
	plan.NodeID = types.StringValue(nodeID)
	change := swarmNodeChange(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := swarmUpdateNode(ctx, r.pd.Client, nodeID, change); err != nil {
		resp.Diagnostics.AddError("Cannot set node availability/role/labels", err.Error())
		plan.Hostname = types.StringNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, nodeID)...)
		return
	}
	nRes, err := r.pd.Client.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect joined node", err.Error())
		return
	}
	n := nRes.Node
	swarmNodeFlatten(&plan, n)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

func (r *swarmNodeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state swarmNodeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	nRes, err := r.pd.Client.NodeInspect(ctx, state.ID.ValueString(), client.NodeInspectOptions{})
	n := nRes.Node
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect swarm node", err.Error())
		return
	}
	swarmNodeFlatten(&state, n)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

func (r *swarmNodeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state swarmNodeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	plan.ID = state.ID
	plan.NodeID = state.ID
	change := swarmNodeChange(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := swarmUpdateNode(ctx, r.pd.Client, state.ID.ValueString(), change); err != nil {
		resp.Diagnostics.AddError("Cannot update swarm node", err.Error())
		return
	}
	nRes, err := r.pd.Client.NodeInspect(ctx, state.ID.ValueString(), client.NodeInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect swarm node", err.Error())
		return
	}
	n := nRes.Node
	swarmNodeFlatten(&plan, n)
	// Config is now complete; later changes to host etc. replace the node.
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, swarmNodeImportedKey, nil)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

func (r *swarmNodeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state swarmNodeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	var nc *client.Client
	if _, ok := knownString(state.Host); ok {
		c, err := r.nodeClient(state, "")
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("host"), "Cannot build Docker client for the node", err.Error())
			return
		}
		defer c.Close()
		nc = c
	} else {
		resp.Diagnostics.AddWarning("Node host unknown",
			fmt.Sprintf("host is not set for node %s, so it cannot be told to leave; it is removed from the manager by force.", state.ID.ValueString()))
	}
	if err := swarmLeaveAndRemove(ctx, nc, r.pd.Client, state.ID.ValueString(), swarmLeaveTimeout); err != nil {
		resp.Diagnostics.AddError("Cannot remove swarm node", err.Error())
	}
}

func (r *swarmNodeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("node_id"), id)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, swarmNodeImportedKey, []byte("true"))...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, id)...)
}
