package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*serviceResource)(nil)
	_ resource.ResourceWithConfigure   = (*serviceResource)(nil)
	_ resource.ResourceWithImportState = (*serviceResource)(nil)
	_ resource.ResourceWithIdentity    = (*serviceResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*serviceResource)(nil)
)

// NewServiceResource returns the docker_service resource.
func NewServiceResource() resource.Resource { return &serviceResource{} }

type serviceResource struct {
	pd *providerData
}

type serviceResourceModel struct {
	ID             types.String `tfsdk:"id"`
	VersionIndex   types.Int64  `tfsdk:"version_index"`
	ImageDigestRef types.String `tfsdk:"image_digest_ref"`
	Adopt          types.Bool   `tfsdk:"adopt"`
	svcSpecModel
	contractSwarmService

	// Outputs of inventory item compose:service-deploy (the compose
	// `deploy` view of the spec), see svcDeployOutputs.
	Deploy             types.Object `tfsdk:"deploy"`
	ComposeSpecVersion types.String `tfsdk:"compose_spec_version"`
	ComposeGoVersion   types.String `tfsdk:"compose_go_version"`
}

// svcDeployAssign sets the compose:service-deploy outputs from vals.
func (m *serviceResourceModel) svcDeployAssign(vals map[string]attr.Value) {
	m.Deploy = vals["deploy"].(types.Object)
	m.ComposeSpecVersion = vals["compose_spec_version"].(types.String)
	m.ComposeGoVersion = vals["compose_go_version"].(types.String)
}

// svcResourceComputedNull sets the computed outputs to null (an
// intermediate state before the first read).
func (m *serviceResourceModel) svcResourceComputedNull() {
	m.VersionIndex, m.ImageDigestRef = types.Int64Null(), types.StringNull()
	m.contractSetNull()
	m.svcDeployAssign(contractNullValues("compose:service-deploy"))
}

// svcRemoveTimeout bounds the wait for a removed service's tasks to go.
const svcRemoveTimeout = 2 * time.Minute

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (r *serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs, blocks := svcSchema(true)
	attrs["version_index"] = schema.Int64Attribute{Computed: true,
		MarkdownDescription: "The service object's `Version.Index`; it changes on every update of the service."}
	attrs["image_digest_ref"] = schema.StringAttribute{Computed: true,
		MarkdownDescription: "Image the tasks run, pinned by digest (`repo@sha256:...`), from the daemon's digest pin; null when the daemon did not pin one."}
	attrs["adopt"] = ownerAdoptAttribute()
	contractAddResourceAttributes(attrs, "swarm:service")
	deployAttrs := contractResourceAttributes("compose:service-deploy")
	for _, n := range svcDeployOutputs {
		attrs[n] = deployAttrs[n]
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Swarm service: a desired number of tasks (containers) of one image scheduled across the Swarm. " +
			"Requires the provider to point at a Swarm manager. Almost every change is a rolling update in place " +
			"(`ServiceUpdate`, governed by `update_config`); only `name` and changing `mode` (replicated, global, replicated_job, " +
			"global_job) replace the service. Destroy removes the service and waits until its tasks are gone, so the networks, secrets " +
			"and configs it used can be removed next. Add `converge_config {}` to make apply wait for the tasks to run.",
		Attributes: attrs,
		Blocks:     blocks,
	}
}

func (r *serviceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *serviceResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

// ModifyPlan checks the Engine API version of the configured attributes and
// the owner label of an existing service.
func (r *serviceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	r.pd.gateAPISince(ctx, "docker_service", req.Config, &resp.Diagnostics)
	if req.State.Raw.IsNull() {
		return
	}
	var name types.String
	var adopt types.Bool
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("adopt"), &adopt)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(ownerPlanCheck(ctx, r.pd, req.Private, "service", name.ValueString(), adopt)...)
}

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lk, err := svcLoadLookup(ctx, r.pd.Client, "")
	if err != nil {
		resp.Diagnostics.AddError("Cannot resolve service references", err.Error())
		return
	}
	spec, d := svcExpand(ctx, &plan.svcSpecModel, lk, svcExpandOpts{FullName: plan.Name.ValueString()})
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := swarmOwnerCreateCheck(ctx, r.pd, "service", spec.Name); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Cannot create service", err.Error())
		return
	}
	spec.Labels = r.pd.ownerInject(spec.Labels)
	auth, err := r.pd.registryAuthFor(spec.TaskTemplate.ContainerSpec.Image)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("image"), "Cannot resolve registry auth", err.Error())
		return
	}
	out, err := r.pd.Client.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: spec, EncodedRegistryAuth: auth, QueryRegistry: true})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create service", err.Error())
		return
	}
	for _, w := range out.Warnings {
		resp.Diagnostics.AddWarning("Service create warning", w)
	}
	plan.ID = types.StringValue(out.ID)
	plan.svcResourceComputedNull()
	// Save the ID first so a failed convergence still leaves a tracked
	// (tainted) resource rather than an orphan.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, out.ID)...)
	if err := svcWaitConverged(ctx, r.pd.Client, out.ID, plan.ConvergeConfig); err != nil {
		resp.Diagnostics.AddError("Service did not converge", err.Error())
		return
	}
	r.refresh(ctx, &plan, lk, &resp.Diagnostics, swarmPriv(resp.Private))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

// refresh replaces m with the daemon's view, keeping m's spellings. It
// returns false when the service no longer exists. priv (when not nil)
// records the live owner label.
func (r *serviceResource) refresh(ctx context.Context, m *serviceResourceModel, lk *svcLookup, diags *diag.Diagnostics, priv privateSetter) bool {
	svcRes, err := r.pd.Client.ServiceInspect(ctx, m.ID.ValueString(), client.ServiceInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false
		}
		diags.AddError("Cannot read service", err.Error())
		return true
	}
	svc := svcRes.Service
	if lk == nil {
		if lk, err = svcLoadLookup(ctx, r.pd.Client, ""); err != nil {
			diags.AddError("Cannot resolve service references", err.Error())
			return true
		}
	}
	m.ID = types.StringValue(svc.ID)
	m.svcSpecModel = svcFlatten(svc.Spec, m.svcSpecModel, lk, svcFlattenOpts{})
	if priv != nil {
		diags.Append(ownerRecord(ctx, priv, svc.Spec.Labels)...)
	}
	m.VersionIndex = swarmVersionIndex(svc.Version)
	m.ImageDigestRef = types.StringNull()
	if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
		if ref := svcImageDigestRef(cs.Image); ref != "" {
			m.ImageDigestRef = types.StringValue(ref)
		}
	}
	diags.Append(m.contractFill(ctx, r.pd, svcContractInput(svcRes.Raw, svc))...)
	dvals, d := contractValues(ctx, r.pd, "compose:service-deploy", contractFillInput{Structured: map[string]any{"deploy": svcComposeDeploy(svc.Spec)}})
	diags.Append(d...)
	if d.HasError() {
		dvals = contractNullValues("compose:service-deploy")
	}
	m.svcDeployAssign(dvals)
	return true
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.refresh(ctx, &state, nil, &resp.Diagnostics, swarmPriv(resp.Private)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serviceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	lk, err := svcLoadLookup(ctx, r.pd.Client, "")
	if err != nil {
		resp.Diagnostics.AddError("Cannot resolve service references", err.Error())
		return
	}
	spec, d := svcExpand(ctx, &plan.svcSpecModel, lk, svcExpandOpts{FullName: plan.Name.ValueString()})
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec.Labels = r.pd.ownerInject(spec.Labels)
	if err := svcUpdateKeepForce(ctx, r.pd, state.ID.ValueString(), spec, !containerKnown(plan.ForceUpdate)); err != nil {
		resp.Diagnostics.AddError("Cannot update service", err.Error())
		return
	}
	if err := svcWaitConverged(ctx, r.pd.Client, state.ID.ValueString(), plan.ConvergeConfig); err != nil {
		resp.Diagnostics.AddError("Service did not converge", err.Error())
		return
	}
	r.refresh(ctx, &plan, lk, &resp.Diagnostics, swarmPriv(resp.Private))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

// svcUpdate applies spec to an existing service at its current version. A
// zero TaskTemplate.ForceUpdate keeps the daemon's counter.
func svcUpdate(ctx context.Context, pd *providerData, id string, spec swarm.ServiceSpec) error {
	return svcUpdateKeepForce(ctx, pd, id, spec, spec.TaskTemplate.ForceUpdate == 0)
}

// svcUpdateKeepForce is svcUpdate; keepForce copies the daemon's ForceUpdate
// counter into spec (force_update unset) so no forced rollout is replayed.
func svcUpdateKeepForce(ctx context.Context, pd *providerData, id string, spec swarm.ServiceSpec, keepForce bool) error {
	curRes, err := pd.Client.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
	if err != nil {
		return err
	}
	cur := curRes.Service
	auth, err := pd.registryAuthFor(spec.TaskTemplate.ContainerSpec.Image)
	if err != nil {
		return err
	}
	if keepForce {
		spec.TaskTemplate.ForceUpdate = cur.Spec.TaskTemplate.ForceUpdate
	}
	opts := client.ServiceUpdateOptions{Version: cur.Version, Spec: spec, EncodedRegistryAuth: auth, QueryRegistry: true}
	if auth == "" {
		// No credentials for this image: keep the ones stored with the
		// current spec, as `docker service update` does without
		// --with-registry-auth (registryAuthFrom=spec).
		opts.RegistryAuthFrom = swarm.RegistryAuthFromSpec
	}
	_, err = pd.Client.ServiceUpdate(ctx, id, opts)
	return err
}

func (r *serviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := svcRemoveAndWait(ctx, r.pd.Client, state.ID.ValueString(), svcRemoveTimeout); err != nil {
		resp.Diagnostics.AddError("Cannot remove service", err.Error())
	}
}

func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := resolveServiceID(ctx, r.pd.Client, ref)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, id)...)
}
