package provider

// docker_node (DECISIONS Q4): labels, availability and role of a swarm
// node that already joined, through NodeUpdate on the provider's Docker host
// (a manager). It never joins or leaves; docker_swarm_node keeps the meaning
// "join". Destroy only forgets the node.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*nodeResource)(nil)
	_ resource.ResourceWithConfigure   = (*nodeResource)(nil)
	_ resource.ResourceWithImportState = (*nodeResource)(nil)
)

// NewNodeResource returns the docker_node resource.
func NewNodeResource() resource.Resource { return &nodeResource{} }

type nodeResource struct {
	pd *providerData
}

type nodeResourceModel struct {
	NodeID       types.String `tfsdk:"node_id"`
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Hostname     types.String `tfsdk:"hostname"`
	Addr         types.String `tfsdk:"addr"`
	VersionIndex types.Int64  `tfsdk:"version_index"`
	Availability types.String `tfsdk:"availability"`
	Role         types.String `tfsdk:"role"`
	Labels       types.Map    `tfsdk:"labels"`
	contractSwarmNode
}

func (r *nodeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node"
}

func (r *nodeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs := map[string]schema.Attribute{
		"node_id": schema.StringAttribute{Required: true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			MarkdownDescription: "The node to manage: ID, unique ID prefix, hostname or node name (resolved to the node ID, see `id`). " +
				"Forces replacement."},
		"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Full node ID. This is the import ID (a hostname works too)."},
		"name": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
			MarkdownDescription: "Node name (`Spec.Name`), else the hostname. Set it to name the node (`NodeSpec.Name`); unset leaves " +
				"the node's name as it is. Updated in place (NodeUpdate)."},
		"hostname": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "Hostname the node reports (`Description.Hostname`)."},
		"addr":     schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "IP address the node is reached at (`Status.Addr`)."},
		"version_index": schema.Int64Attribute{Computed: true,
			MarkdownDescription: "The node object's `Version.Index`; it changes on every update of the node."},
		"availability": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
			Validators: []validator.String{stringvalidator.OneOf(swarmValidAvailabilities...)},
			MarkdownDescription: "Scheduling availability: `active`, `pause` or `drain` (`Spec.Availability`). Unset leaves it as it is. " +
				"Updated in place (NodeUpdate); draining does not wait for tasks to leave (use `docker_node_drain` for that)."},
		"role": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
			Validators: []validator.String{stringvalidator.OneOf(swarmValidRoles...)},
			MarkdownDescription: "Node role: `worker` or `manager` (`Spec.Role`). Unset leaves it as it is. Updated in place (promote / demote); " +
				"the Engine refuses to demote the last manager."},
		"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true, Computed: true,
			PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "Node labels (`Spec.Labels`, `node.labels.*` in placement constraints). When set, the node gets exactly these " +
				"labels (others are removed); unset leaves them as they are. Updated in place."},
	}
	contractAddResourceAttributes(attrs, "swarm:node")
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the labels, availability and role of a swarm node that has already joined, like `docker node update`, " +
			"through the provider's Docker host (which must be a manager). It never joins or leaves: use `docker_swarm_node` to join a host. " +
			"Destroying the resource only removes it from Terraform state; the node stays in the swarm with its current labels, availability and role.",
		Attributes: attrs,
	}
}

func (r *nodeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// nodeResourceChange is the NodeUpdate change of the plan: only known,
// non-null values.
func nodeResourceChange(ctx context.Context, m nodeResourceModel, diags *diag.Diagnostics) swarmNodeSpecChange {
	var ch swarmNodeSpecChange
	if v, ok := knownString(m.Availability); ok && v != "" {
		a := swarm.NodeAvailability(v)
		ch.Availability = &a
	}
	if v, ok := knownString(m.Role); ok && v != "" {
		ro := swarm.NodeRole(v)
		ch.Role = &ro
	}
	if v, ok := knownString(m.Name); ok && v != "" {
		ch.Name = &v
	}
	if !m.Labels.IsUnknown() && !m.Labels.IsNull() {
		l := stringMapFrom(ctx, m.Labels, diags)
		if l == nil {
			l = map[string]string{}
		}
		ch.Labels = &l
	}
	return ch
}

// refresh reads the node into m; false when it is gone.
func (r *nodeResource) refresh(ctx context.Context, m *nodeResourceModel, diags *diag.Diagnostics) bool {
	id := m.ID.ValueString()
	if id == "" {
		var err error
		if id, err = resolveNodeID(ctx, r.pd.Client, m.NodeID.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("node_id"), "Cannot find swarm node", err.Error())
			return true
		}
	}
	res, err := r.pd.Client.NodeInspect(ctx, id, client.NodeInspectOptions{})
	if isNotFound(err) {
		return false
	}
	if err != nil {
		diags.AddError("Cannot inspect swarm node", err.Error())
		return true
	}
	n := res.Node
	m.ID = types.StringValue(n.ID)
	m.Name = types.StringValue(swarmNodeName(n))
	m.Hostname = types.StringValue(n.Description.Hostname)
	m.Addr = types.StringValue(n.Status.Addr)
	m.VersionIndex = swarmVersionIndex(n.Version)
	m.Availability = types.StringValue(string(n.Spec.Availability))
	m.Role = types.StringValue(string(n.Spec.Role))
	m.Labels = swarmLabelsValue(m.Labels, ownerStrip(n.Spec.Labels))
	if m.NodeID.IsNull() || m.NodeID.IsUnknown() {
		m.NodeID = types.StringValue(n.ID)
	}
	diags.Append(m.contractFill(ctx, r.pd, swarmInspectInput(res.Raw, n))...)
	return true
}

// apply resolves the node and sends the plan's change.
// cfgName is `name` from the configuration: a name only computed from the
// hostname is never sent as NodeSpec.Name.
func (r *nodeResource) apply(ctx context.Context, m *nodeResourceModel, id string, cfgName types.String, diags *diag.Diagnostics) {
	sent := *m
	sent.Name = cfgName
	change := nodeResourceChange(ctx, sent, diags)
	if diags.HasError() {
		return
	}
	if err := swarmUpdateNode(ctx, r.pd.Client, id, change); err != nil {
		diags.AddError("Cannot update swarm node", err.Error())
		return
	}
	m.ID = types.StringValue(id)
	if !r.refresh(ctx, m, diags) {
		diags.AddError("Swarm node is gone", "node "+id+" disappeared during the update")
	}
}

func (r *nodeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan nodeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	id, err := resolveNodeID(ctx, r.pd.Client, plan.NodeID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("node_id"), "Cannot find swarm node",
			"The node must already be part of the swarm (join it with docker_swarm_node): "+err.Error())
		return
	}
	var cfgName types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &cfgName)...)
	r.apply(ctx, &plan, id, cfgName, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *nodeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state nodeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if !r.refresh(ctx, &state, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *nodeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state nodeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	var cfgName types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &cfgName)...)
	r.apply(ctx, &plan, state.ID.ValueString(), cfgName, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete only forgets the node (see the type description).
func (r *nodeResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

func (r *nodeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "docker_node import needs a configured provider.")
		return
	}
	id, err := resolveNodeID(ctx, r.pd.Client, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import swarm node", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("node_id"), req.ID)...)
}
