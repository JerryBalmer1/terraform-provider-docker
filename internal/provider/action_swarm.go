package provider

// docker_swarm_rotate_tokens, docker_node_drain and docker_node_activate.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ action.ActionWithConfigure      = (*swarmRotateTokensAction)(nil)
	_ action.ActionWithValidateConfig = (*swarmRotateTokensAction)(nil)
	_ action.ActionWithConfigure      = (*nodeDrainAction)(nil)
	_ action.ActionWithValidateConfig = (*nodeDrainAction)(nil)
	_ action.ActionWithConfigure      = (*nodeActivateAction)(nil)
)

// actNodeDrainDefaultTimeout bounds how long docker_node_drain waits.
const actNodeDrainDefaultTimeout = 5 * time.Minute

// ---------------------------------------------------------------------------
// docker_swarm_rotate_tokens

// NewSwarmRotateTokensAction returns the docker_swarm_rotate_tokens action.
func NewSwarmRotateTokensAction() action.Action { return &swarmRotateTokensAction{} }

type swarmRotateTokensAction struct{ actBase }

type swarmRotateTokensActionModel struct {
	Worker  types.Bool `tfsdk:"worker"`
	Manager types.Bool `tfsdk:"manager"`
}

func (a *swarmRotateTokensAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_rotate_tokens"
}

func (a *swarmRotateTokensAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Rotates the swarm join tokens, like `docker swarm join-token --rotate`. " +
			"Nodes already in the swarm are not affected; the old token stops working for new joins. " +
			"The provider's host must be a swarm manager. Read the new tokens with the `docker_swarm_tokens` data source; they are never printed.",
		Attributes: map[string]schema.Attribute{
			"worker":  schema.BoolAttribute{Optional: true, MarkdownDescription: "Rotate the worker join token. Default `true`."},
			"manager": schema.BoolAttribute{Optional: true, MarkdownDescription: "Rotate the manager join token. Default `false`."},
		},
	}
}

func (a *swarmRotateTokensAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m swarmRotateTokensActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Worker.IsUnknown() || m.Manager.IsUnknown() {
		return
	}
	if !actBool(m.Worker, true) && !actBool(m.Manager, false) {
		resp.Diagnostics.AddError("Nothing to rotate", "Set worker or manager (or both) to true.")
	}
}

func (a *swarmRotateTokensAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m swarmRotateTokensActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	flags := swarm.UpdateFlags{RotateWorkerToken: actBool(m.Worker, true), RotateManagerToken: actBool(m.Manager, false)}
	var lastErr error
	for attempt := 0; attempt < swarmNodeUpdateAttempts; attempt++ {
		sw, err := c.SwarmInspect(ctx)
		if err != nil {
			resp.Diagnostics.AddError("docker_swarm_rotate_tokens failed", fmt.Sprintf("inspecting swarm (the provider's host must be a swarm manager): %v", err))
			return
		}
		err = c.SwarmUpdate(ctx, sw.Version, sw.Spec, flags)
		if err == nil {
			var which []string
			if flags.RotateWorkerToken {
				which = append(which, "worker")
			}
			if flags.RotateManagerToken {
				which = append(which, "manager")
			}
			actProgress(ctx, resp, "rotated swarm %s join token(s)", strings.Join(which, " and "))
			return
		}
		if !swarmIsVersionConflict(err) {
			resp.Diagnostics.AddError("docker_swarm_rotate_tokens failed", err.Error())
			return
		}
		lastErr = err
	}
	resp.Diagnostics.AddError("docker_swarm_rotate_tokens failed", fmt.Sprintf("gave up after %d version conflicts: %v", swarmNodeUpdateAttempts, lastErr))
}

// ---------------------------------------------------------------------------
// docker_node_drain

// NewNodeDrainAction returns the docker_node_drain action.
func NewNodeDrainAction() action.Action { return &nodeDrainAction{} }

type nodeDrainAction struct{ actBase }

type nodeDrainActionModel struct {
	Node    types.String `tfsdk:"node"`
	Wait    types.Bool   `tfsdk:"wait"`
	Timeout types.String `tfsdk:"timeout"`
}

func (a *nodeDrainAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node_drain"
}

func (a *nodeDrainAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a swarm node's availability to `drain` and waits until its tasks have left, like `docker node update --availability drain`. " +
			"The provider's host must be a swarm manager. If a `docker_swarm_node` resource manages the node with `availability = \"active\"`, " +
			"its next plan shows the change; use `docker_node_activate` to undo.",
		Attributes: map[string]schema.Attribute{
			"node":    schema.StringAttribute{Required: true, MarkdownDescription: "Node ID, hostname or node name."},
			"wait":    schema.BoolAttribute{Optional: true, MarkdownDescription: "Wait until no task on the node is still running. Default `true`."},
			"timeout": schema.StringAttribute{Optional: true, MarkdownDescription: "How long to wait for the tasks to leave, as a Go duration. Default `5m`."},
		},
	}
}

func (a *nodeDrainAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m nodeDrainActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actDuration(m.Timeout, "timeout", &resp.Diagnostics)
}

// actTaskActive reports whether a task still occupies its node.
func actTaskActive(t swarm.Task) bool {
	switch t.Status.State {
	case swarm.TaskStateComplete, swarm.TaskStateShutdown, swarm.TaskStateFailed,
		swarm.TaskStateRejected, swarm.TaskStateRemove, swarm.TaskStateOrphaned:
		return false
	}
	return true
}

// actNodeActiveTasks returns the IDs of tasks still active on node id.
func actNodeActiveTasks(ctx context.Context, c *client.Client, id string) ([]string, error) {
	tasks, err := c.TaskList(ctx, swarm.TaskListOptions{Filters: filters.NewArgs(filters.Arg("node", id))})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tasks {
		if t.NodeID == id && actTaskActive(t) {
			out = append(out, svcShortID(t.ID))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (a *nodeDrainAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m nodeDrainActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := resolveNodeID(ctx, c, m.Node.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("docker_node_drain failed", err.Error())
		return
	}
	drain := swarm.NodeAvailabilityDrain
	if err := swarmUpdateNode(ctx, c, id, swarmNodeSpecChange{Availability: &drain}); err != nil {
		resp.Diagnostics.AddError("docker_node_drain failed", err.Error())
		return
	}
	actProgress(ctx, resp, "node %s set to drain", svcShortID(id))
	if !actBool(m.Wait, true) {
		return
	}
	timeout := actTimeout(m.Timeout, actNodeDrainDefaultTimeout)
	last := -1
	var remaining []string
	werr := waitFor(ctx, timeout, actPollInterval, func(ctx context.Context) (bool, error) {
		got, err := actNodeActiveTasks(ctx, c, id)
		if err != nil {
			return false, err
		}
		remaining = got
		if len(remaining) != last {
			last = len(remaining)
			actProgress(ctx, resp, "node %s: %d task(s) still running", svcShortID(id), last)
		}
		return len(remaining) == 0, nil
	})
	if werr != nil {
		msg := werr.Error()
		if errors.Is(werr, context.DeadlineExceeded) {
			msg = fmt.Sprintf("node %s is drained but %d task(s) were still running after %s: %s", id, len(remaining), timeout, strings.Join(remaining, ", "))
		}
		resp.Diagnostics.AddError("docker_node_drain: tasks did not leave", msg)
		return
	}
	actProgress(ctx, resp, "node %s drained", svcShortID(id))
}

// ---------------------------------------------------------------------------
// docker_node_activate

// NewNodeActivateAction returns the docker_node_activate action.
func NewNodeActivateAction() action.Action { return &nodeActivateAction{} }

type nodeActivateAction struct{ actBase }

type nodeActivateActionModel struct {
	Node types.String `tfsdk:"node"`
}

func (a *nodeActivateAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node_activate"
}

func (a *nodeActivateAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a swarm node's availability back to `active`, like `docker node update --availability active`. " +
			"The scheduler can then place new tasks on it; running services are not rebalanced. The provider's host must be a swarm manager.",
		Attributes: map[string]schema.Attribute{
			"node": schema.StringAttribute{Required: true, MarkdownDescription: "Node ID, hostname or node name."},
		},
	}
}

func (a *nodeActivateAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m nodeActivateActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := resolveNodeID(ctx, c, m.Node.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("docker_node_activate failed", err.Error())
		return
	}
	active := swarm.NodeAvailabilityActive
	if err := swarmUpdateNode(ctx, c, id, swarmNodeSpecChange{Availability: &active}); err != nil {
		resp.Diagnostics.AddError("docker_node_activate failed", err.Error())
		return
	}
	actProgress(ctx, resp, "node %s set to active", svcShortID(id))
}
