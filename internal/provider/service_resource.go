package provider

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*serviceResource)(nil)
	_ resource.ResourceWithConfigure   = (*serviceResource)(nil)
	_ resource.ResourceWithImportState = (*serviceResource)(nil)
)

// NewServiceResource returns the docker_service resource.
func NewServiceResource() resource.Resource { return &serviceResource{} }

type serviceResource struct {
	pd *providerData
}

type serviceResourceModel struct {
	ID types.String `tfsdk:"id"`
	svcSpecModel
}

// svcRemoveTimeout bounds the wait for a removed service's tasks to go.
const svcRemoveTimeout = 2 * time.Minute

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (r *serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs, blocks := svcSchema(true)
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
	auth, err := r.pd.registryAuthFor(spec.TaskTemplate.ContainerSpec.Image)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("image"), "Cannot resolve registry auth", err.Error())
		return
	}
	out, err := r.pd.Client.ServiceCreate(ctx, spec, swarm.ServiceCreateOptions{EncodedRegistryAuth: auth, QueryRegistry: true})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create service", err.Error())
		return
	}
	for _, w := range out.Warnings {
		resp.Diagnostics.AddWarning("Service create warning", w)
	}
	plan.ID = types.StringValue(out.ID)
	// Save the ID first so a failed convergence still leaves a tracked
	// (tainted) resource rather than an orphan.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err := svcWaitConverged(ctx, r.pd.Client, out.ID, plan.ConvergeConfig); err != nil {
		resp.Diagnostics.AddError("Service did not converge", err.Error())
		return
	}
	r.refresh(ctx, &plan, lk, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// refresh replaces m with the daemon's view, keeping m's spellings. It
// returns false when the service no longer exists.
func (r *serviceResource) refresh(ctx context.Context, m *serviceResourceModel, lk *svcLookup, diags *diag.Diagnostics) bool {
	svc, _, err := r.pd.Client.ServiceInspectWithRaw(ctx, m.ID.ValueString(), swarm.ServiceInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false
		}
		diags.AddError("Cannot read service", err.Error())
		return true
	}
	if lk == nil {
		if lk, err = svcLoadLookup(ctx, r.pd.Client, ""); err != nil {
			diags.AddError("Cannot resolve service references", err.Error())
			return true
		}
	}
	m.ID = types.StringValue(svc.ID)
	m.svcSpecModel = svcFlatten(svc.Spec, m.svcSpecModel, lk, svcFlattenOpts{})
	return true
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.refresh(ctx, &state, nil, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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
	if err := svcUpdateKeepForce(ctx, r.pd, state.ID.ValueString(), spec, !containerKnown(plan.ForceUpdate)); err != nil {
		resp.Diagnostics.AddError("Cannot update service", err.Error())
		return
	}
	if err := svcWaitConverged(ctx, r.pd.Client, state.ID.ValueString(), plan.ConvergeConfig); err != nil {
		resp.Diagnostics.AddError("Service did not converge", err.Error())
		return
	}
	r.refresh(ctx, &plan, lk, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// svcUpdate applies spec to an existing service at its current version. A
// zero TaskTemplate.ForceUpdate keeps the daemon's counter.
func svcUpdate(ctx context.Context, pd *providerData, id string, spec swarm.ServiceSpec) error {
	return svcUpdateKeepForce(ctx, pd, id, spec, spec.TaskTemplate.ForceUpdate == 0)
}

// svcUpdateKeepForce is svcUpdate; keepForce copies the daemon's ForceUpdate
// counter into spec (force_update unset) so no forced rollout is replayed.
func svcUpdateKeepForce(ctx context.Context, pd *providerData, id string, spec swarm.ServiceSpec, keepForce bool) error {
	cur, _, err := pd.Client.ServiceInspectWithRaw(ctx, id, swarm.ServiceInspectOptions{})
	if err != nil {
		return err
	}
	auth, err := pd.registryAuthFor(spec.TaskTemplate.ContainerSpec.Image)
	if err != nil {
		return err
	}
	if keepForce {
		spec.TaskTemplate.ForceUpdate = cur.Spec.TaskTemplate.ForceUpdate
	}
	_, err = pd.Client.ServiceUpdate(ctx, id, cur.Version, spec, swarm.ServiceUpdateOptions{EncodedRegistryAuth: auth, QueryRegistry: true})
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
	id, err := resolveServiceID(ctx, r.pd.Client, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
