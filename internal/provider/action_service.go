package provider

// docker_service_scale, docker_service_force_update and docker_service_rollback.

import (
	"context"
	"errors"
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"time"
)

var (
	_ action.ActionWithConfigure      = (*serviceScaleAction)(nil)
	_ action.ActionWithValidateConfig = (*serviceScaleAction)(nil)
	_ action.ActionWithConfigure      = (*serviceForceUpdateAction)(nil)
	_ action.ActionWithValidateConfig = (*serviceForceUpdateAction)(nil)
	_ action.ActionWithConfigure      = (*serviceRollbackAction)(nil)
	_ action.ActionWithValidateConfig = (*serviceRollbackAction)(nil)
)

const (
	// actServiceDefaultTimeout bounds the converge wait of the service actions.
	actServiceDefaultTimeout = 5 * time.Minute
	// actRolloutGracePolls is how many polls a waiting action gives the
	// daemon to report the new rollout before it trusts the task list alone.
	actRolloutGracePolls = 5
)

var (
	actServiceWaitDoc    = "Wait until the service converges (every wanted task running). Default `true`."
	actServiceTimeoutDoc = "How long to wait for convergence, as a Go duration. Default `5m`."
)

// actServiceUpdate reads service ref, lets mutate change it, and sends the
// update, retrying on version conflicts. It returns the service ID and the
// daemon's warnings.
func actServiceUpdate(ctx context.Context, pd *providerData, ref string, rollback bool, mutate func(*swarm.Service) error, diags *diag.Diagnostics) (string, []string) {
	c := pd.Client
	id, err := resolveServiceID(ctx, c, ref)
	if err != nil {
		diags.AddError("Cannot find service", err.Error())
		return "", nil
	}
	var lastErr error
	for attempt := 0; attempt < swarmNodeUpdateAttempts; attempt++ {
		svcRes, err := c.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
		if err != nil {
			diags.AddError("Cannot inspect service", err.Error())
			return "", nil
		}
		svc := svcRes.Service
		if err := mutate(&svc); err != nil {
			diags.AddError("Cannot update service", err.Error())
			return "", nil
		}
		img := svc.Spec.TaskTemplate.ContainerSpec
		opts := client.ServiceUpdateOptions{}
		if img != nil && img.Image != "" {
			ref := img.Image
			if rollback && svc.PreviousSpec != nil && svc.PreviousSpec.TaskTemplate.ContainerSpec != nil {
				ref = svc.PreviousSpec.TaskTemplate.ContainerSpec.Image
			}
			if auth, err := pd.registryAuthFor(ref); err == nil {
				opts.EncodedRegistryAuth = auth
			}
		}
		if rollback {
			opts.Rollback = "previous"
		}
		if opts.EncodedRegistryAuth == "" {
			// No credentials for the image: let the daemon reuse the ones
			// stored with the spec the tasks will run, the previous spec on
			// a rollback (registryAuthFrom=previous-spec), else the current
			// one (spec), as `docker service update` does.
			opts.RegistryAuthFrom = swarm.RegistryAuthFromSpec
			if rollback && svc.PreviousSpec != nil {
				opts.RegistryAuthFrom = swarm.RegistryAuthFromPreviousSpec
			}
		}
		opts.Version, opts.Spec = svc.Version, svc.Spec
		out, err := c.ServiceUpdate(ctx, id, opts)
		if err == nil {
			for _, w := range out.Warnings {
				diags.AddWarning("Docker daemon warning", w)
			}
			return id, out.Warnings
		}
		if !swarmIsVersionConflict(err) {
			diags.AddError("Service update failed", err.Error())
			return "", nil
		}
		lastErr = err
	}
	diags.AddError("Service update failed", fmt.Sprintf("gave up after %d version conflicts: %v", swarmNodeUpdateAttempts, lastErr))
	return "", nil
}

// actServiceFinish reports the service as the result object of a service
// action: the identity values, plus result = {update_response,
// update_status} and the extra result fields (a func(swarm.Service) any
// value is called with the service as read after the update).
func actServiceFinish(ctx context.Context, resp *action.InvokeResponse, pd *providerData, itemID, id string, warnings []string, extra map[string]any, resultPath types.String) {
	svcRes, err := pd.Client.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect service", "reading the service after the update: "+err.Error())
		return
	}
	svc := svcRes.Service
	if warnings == nil {
		warnings = []string{}
	}
	result := map[string]any{"update_response": map[string]any{"Warnings": warnings}, "update_status": svc.UpdateStatus}
	for k, v := range extra {
		if f, ok := v.(func(swarm.Service) any); ok {
			v = f(svc)
		}
		result[k] = v
	}
	values := map[string]any{
		"service_id":    svc.ID,
		"name":          svc.Spec.Name,
		"version_index": svc.Version.Index,
		"result":        result,
	}
	if itemID == "swarm:service-rollback" {
		if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
			if ref := svcImageDigestRef(cs.Image); ref != "" {
				values["image_digest_ref"] = ref
			}
		}
	}
	actFinishResult(ctx, resp, pd, itemID, values, resultPath)
}

// actServiceStatus returns the service's running/desired task counts
// (ServiceList with status=true), or nil when the daemon does not say.
func actServiceStatus(ctx context.Context, c *client.Client, id string) *swarm.ServiceStatus {
	res, err := c.ServiceList(ctx, client.ServiceListOptions{Filters: client.Filters{}.Add("id", id), Status: true})
	if err != nil {
		return nil
	}
	for _, s := range res.Items {
		if s.ID == id {
			return s.ServiceStatus
		}
	}
	return nil
}

const actServiceIDDoc = "Target service: ID, unique ID prefix or name (resolved to the service ID)."

// actServiceReplicas is the wanted task count for svcConvergeCheck: the
// replica count for replicated services, -1 for global ones.
func actServiceReplicas(svc swarm.Service) int {
	if r := svc.Spec.Mode.Replicated; r != nil {
		if r.Replicas == nil {
			return 1
		}
		return int(*r.Replicas)
	}
	return -1
}

// actRolloutStatus returns the service's UpdateStatus when it belongs to
// the current spec (not an earlier rollout), else nil.
func actRolloutStatus(svc swarm.Service) *swarm.UpdateStatus {
	us := svc.UpdateStatus
	if us == nil || us.StartedAt == nil {
		return nil
	}
	if !svc.UpdatedAt.IsZero() && us.StartedAt.Before(svc.UpdatedAt.Add(-time.Second)) {
		return nil
	}
	return us
}

// actWaitRolloutStarted gives the daemon a few polls to report the rollout
// that the last ServiceUpdate started, so the converge wait does not pass on
// the old tasks before the update begins.
func actWaitRolloutStarted(ctx context.Context, c *client.Client, id string) error {
	for i := 0; i < actRolloutGracePolls; i++ {
		svcRes, err := c.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
		if err != nil {
			return err
		}
		svc := svcRes.Service
		if actRolloutStatus(svc) != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(actPollInterval):
		}
	}
	return nil
}

// actServiceWait waits for convergence with the shared service helper.
func actServiceWait(ctx context.Context, c *client.Client, id string, timeout time.Duration) error {
	return svcWaitConverged(ctx, c, id, &svcConvergeModel{
		Timeout: types.StringValue(timeout.String()),
		Delay:   types.StringValue(actPollInterval.String()),
	})
}

// ---------------------------------------------------------------------------
// docker_service_scale

// NewServiceScaleAction returns the docker_service_scale action.
func NewServiceScaleAction() action.Action { return &serviceScaleAction{} }

type serviceScaleAction struct{ actBase }

type serviceScaleActionModel struct {
	ServiceID  types.String `tfsdk:"service_id"`
	Replicas   types.Int64  `tfsdk:"replicas"`
	Wait       types.Bool   `tfsdk:"wait"`
	Timeout    types.String `tfsdk:"timeout"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *serviceScaleAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_scale"
}

func (a *serviceScaleAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the replica count of a replicated swarm service, like `docker service scale`, and waits for the tasks. " +
			"If a `docker_service` resource manages the service with `replicas` set, its next plan reverts the change; " +
			"use this action for services whose replica count Terraform does not own, or for one-off scaling. " +
			"The result object has the update warnings, the rollout status, the service mode and its running/desired task counts.",
		Attributes: map[string]schema.Attribute{
			"service_id":  schema.StringAttribute{Required: true, MarkdownDescription: actServiceIDDoc},
			"replicas":    schema.Int64Attribute{Required: true, MarkdownDescription: "Wanted number of tasks, `0` or more."},
			"wait":        schema.BoolAttribute{Optional: true, MarkdownDescription: actServiceWaitDoc},
			"timeout":     schema.StringAttribute{Optional: true, MarkdownDescription: actServiceTimeoutDoc},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *serviceScaleAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m serviceScaleActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actDuration(m.Timeout, "timeout", &resp.Diagnostics)
	if n, ok := knownInt64(m.Replicas); ok && n < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("replicas"), "Invalid replicas", "replicas must be 0 or more.")
	}
}

func (a *serviceScaleAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m serviceScaleActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	n := uint64(m.Replicas.ValueInt64())
	ref := m.ServiceID.ValueString()
	id, warnings := actServiceUpdate(ctx, a.pd, ref, false, func(svc *swarm.Service) error {
		if svc.Spec.Mode.Replicated == nil {
			return fmt.Errorf("service %s is not in replicated mode; only replicated services can be scaled", ref)
		}
		svc.Spec.Mode.Replicated.Replicas = &n
		return nil
	}, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	actProgress(ctx, resp, "service %s scaled to %d", ref, n)
	if actBool(m.Wait, true) {
		if err := actServiceWait(ctx, c, id, actTimeout(m.Timeout, actServiceDefaultTimeout)); err != nil {
			resp.Diagnostics.AddError("docker_service_scale: service did not converge", err.Error())
			return
		}
		actProgress(ctx, resp, "service %s converged with %d replicas", ref, n)
	}
	actServiceFinish(ctx, resp, a.pd, "swarm:service-scale", id, warnings, map[string]any{
		"mode":           func(s swarm.Service) any { return s.Spec.Mode },
		"service_status": actServiceStatus(ctx, c, id),
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_service_force_update

// NewServiceForceUpdateAction returns the docker_service_force_update action.
func NewServiceForceUpdateAction() action.Action { return &serviceForceUpdateAction{} }

type serviceForceUpdateAction struct{ actBase }

type serviceWaitActionModel struct {
	ServiceID  types.String `tfsdk:"service_id"`
	Wait       types.Bool   `tfsdk:"wait"`
	Timeout    types.String `tfsdk:"timeout"`
	ResultPath types.String `tfsdk:"result_path"`
}

func serviceWaitActionAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"service_id":  schema.StringAttribute{Required: true, MarkdownDescription: actServiceIDDoc},
		"wait":        schema.BoolAttribute{Optional: true, MarkdownDescription: actServiceWaitDoc},
		"timeout":     schema.StringAttribute{Optional: true, MarkdownDescription: actServiceTimeoutDoc},
		"result_path": actResultPathAttribute(),
	}
}

func validateServiceWaitAction(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m serviceWaitActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actDuration(m.Timeout, "timeout", &resp.Diagnostics)
}

func (a *serviceForceUpdateAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_force_update"
}

func (a *serviceForceUpdateAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Redeploys every task of a swarm service without changing its spec, like `docker service update --force`. " +
			"The rollout follows the service's update_config. It bumps TaskTemplate.ForceUpdate; a `docker_service` that leaves `force_update` unset ignores the new value. " +
			"The result object has the update warnings, the rollout status and the new `force_update` counter.",
		Attributes: serviceWaitActionAttributes(),
	}
}

func (a *serviceForceUpdateAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	validateServiceWaitAction(ctx, req, resp)
}

func (a *serviceForceUpdateAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m serviceWaitActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.ServiceID.ValueString()
	var counter uint64
	id, warnings := actServiceUpdate(ctx, a.pd, ref, false, func(svc *swarm.Service) error {
		svc.Spec.TaskTemplate.ForceUpdate++
		counter = svc.Spec.TaskTemplate.ForceUpdate
		return nil
	}, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	actProgress(ctx, resp, "service %s force update %d started", ref, counter)
	if actBool(m.Wait, true) {
		timeout := actTimeout(m.Timeout, actServiceDefaultTimeout)
		wctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := actWaitRolloutStarted(wctx, c, id); err != nil {
			resp.Diagnostics.AddError("docker_service_force_update: service did not converge", err.Error())
			return
		}
		if err := actServiceWait(wctx, c, id, timeout); err != nil {
			resp.Diagnostics.AddError("docker_service_force_update: service did not converge", err.Error())
			return
		}
		actProgress(ctx, resp, "service %s redeployed and converged", ref)
	}
	actServiceFinish(ctx, resp, a.pd, "swarm:service-force-update", id, warnings, map[string]any{"force_update": counter}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_service_rollback

// NewServiceRollbackAction returns the docker_service_rollback action.
func NewServiceRollbackAction() action.Action { return &serviceRollbackAction{} }

type serviceRollbackAction struct{ actBase }

func (a *serviceRollbackAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_rollback"
}

func (a *serviceRollbackAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Rolls a swarm service back to its previous spec, like `docker service rollback`, and waits for the rollback to complete. " +
			"The service must have been updated at least once. A `docker_service` resource managing the service will plan to re-apply its configured spec afterwards. " +
			"The result object has the update warnings and the rollout status, beside the image the service runs now (`image_digest_ref`).",
		Attributes: serviceWaitActionAttributes(),
	}
}

func (a *serviceRollbackAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	validateServiceWaitAction(ctx, req, resp)
}

// actRollbackCheck decides whether a rollback finished. graceOver is set
// once the daemon had its chance to report the rollback status.
func actRollbackCheck(svc swarm.Service, tasks []swarm.Task, graceOver bool) (bool, error) {
	if us := actRolloutStatus(svc); us != nil {
		switch us.State {
		case swarm.UpdateStatePaused, swarm.UpdateStateRollbackPaused:
			return false, fmt.Errorf("rollback paused (%s): %s", us.State, us.Message)
		case swarm.UpdateStateRollbackStarted, swarm.UpdateStateUpdating:
			return false, nil
		}
	} else if !graceOver {
		return false, nil
	}
	return svcConvergeCheck(tasks, actServiceReplicas(svc), nil, time.Time{}).Done, nil
}

func (a *serviceRollbackAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m serviceWaitActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.ServiceID.ValueString()
	id, warnings := actServiceUpdate(ctx, a.pd, ref, true, func(svc *swarm.Service) error {
		if svc.PreviousSpec == nil {
			return fmt.Errorf("service %s has no previous spec to roll back to", ref)
		}
		if svc.Spec.Mode.ReplicatedJob != nil || svc.Spec.Mode.GlobalJob != nil {
			return fmt.Errorf("service %s is a job; jobs do not support rollback", ref)
		}
		return nil
	}, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	actProgress(ctx, resp, "service %s rollback started", ref)
	if !actBool(m.Wait, true) {
		actServiceFinish(ctx, resp, a.pd, "swarm:service-rollback", id, warnings, nil, m.ResultPath)
		return
	}
	timeout := actTimeout(m.Timeout, actServiceDefaultTimeout)
	polls := 0
	var running, want int
	werr := waitFor(ctx, timeout, actPollInterval, func(ctx context.Context) (bool, error) {
		polls++
		svcRes, err := c.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
		if err != nil {
			return false, err
		}
		svc := svcRes.Service
		tasksRes, err := c.TaskList(ctx, client.TaskListOptions{Filters: client.Filters{}.Add("service", id)})
		if err != nil {
			return false, err
		}
		tasks := tasksRes.Items
		st := svcConvergeCheck(tasks, actServiceReplicas(svc), nil, time.Time{})
		running, want = st.Running, st.Want
		return actRollbackCheck(svc, tasks, polls > actRolloutGracePolls)
	})
	if werr != nil {
		msg := werr.Error()
		if errors.Is(werr, context.DeadlineExceeded) {
			msg = fmt.Sprintf("service %s did not finish rolling back within %s: %d/%d tasks running", id, timeout, running, want)
		}
		resp.Diagnostics.AddError("docker_service_rollback failed", msg)
		return
	}
	actProgress(ctx, resp, "service %s rolled back and converged", ref)
	actServiceFinish(ctx, resp, a.pd, "swarm:service-rollback", id, warnings, nil, m.ResultPath)
}
