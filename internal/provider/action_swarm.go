package provider

// Swarm topology actions: docker_swarm_rotate_tokens,
// docker_swarm_rotate_unlock_key, docker_swarm_rotate_ca (Q10),
// docker_swarm_unlock, and the node actions docker_node_drain,
// docker_node_activate, docker_node_promote and docker_node_demote (Q5).
// Every action reports one result object (Q35, action_result.go) and takes
// result_path; node targets are named node_id (Q40).

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*swarmRotateTokensAction)(nil)
	_ action.ActionWithValidateConfig = (*swarmRotateTokensAction)(nil)
	_ action.ActionWithConfigure      = (*swarmRotateUnlockKeyAction)(nil)
	_ action.ActionWithConfigure      = (*swarmRotateCAAction)(nil)
	_ action.ActionWithValidateConfig = (*swarmRotateCAAction)(nil)
	_ action.ActionWithConfigure      = (*swarmUnlockAction)(nil)
	_ action.ActionWithConfigure      = (*nodeDrainAction)(nil)
	_ action.ActionWithValidateConfig = (*nodeDrainAction)(nil)
	_ action.ActionWithConfigure      = (*nodeActivateAction)(nil)
	_ action.ActionWithConfigure      = (*nodeRoleAction)(nil)
)

// actNodeDrainDefaultTimeout bounds how long docker_node_drain waits.
const actNodeDrainDefaultTimeout = 5 * time.Minute

// actCARotateDefaultTimeout bounds how long docker_swarm_rotate_ca waits
// for the root rotation to finish.
const actCARotateDefaultTimeout = 10 * time.Minute

const actNodeIDDoc = "Target node: ID, unique ID prefix, hostname or node name (resolved to the node ID)."

// actSwarmUpdate applies mutate to the current swarm spec and sends
// SwarmUpdate with flags, retrying on version conflicts.
func actSwarmUpdate(ctx context.Context, c *client.Client, flags client.SwarmUpdateOptions, mutate func(*swarm.Spec) error) error {
	var lastErr error
	for attempt := 0; attempt < swarmNodeUpdateAttempts; attempt++ {
		swRes, err := c.SwarmInspect(ctx, client.SwarmInspectOptions{})
		if err != nil {
			return fmt.Errorf("inspecting swarm (the provider's host must be a swarm manager): %w", err)
		}
		sw := swRes.Swarm
		spec := sw.Spec
		if mutate != nil {
			if err := mutate(&spec); err != nil {
				return err
			}
		}
		flags.Version, flags.Spec = sw.Version, spec
		if _, err = c.SwarmUpdate(ctx, flags); err == nil {
			return nil
		}
		if !swarmIsVersionConflict(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("gave up after %d version conflicts: %w", swarmNodeUpdateAttempts, lastErr)
}

// actSwarmInspect reads the swarm after an update (for the result object).
func actSwarmInspect(ctx context.Context, c *client.Client, diags *diag.Diagnostics, typ string) (swarm.Swarm, bool) {
	swRes, err := c.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		diags.AddError(typ+" failed", "reading the swarm after the update: "+err.Error())
		return swarm.Swarm{}, false
	}
	return swRes.Swarm, true
}

// ---------------------------------------------------------------------------
// docker_swarm_rotate_tokens

// NewSwarmRotateTokensAction returns the docker_swarm_rotate_tokens action.
func NewSwarmRotateTokensAction() action.Action { return &swarmRotateTokensAction{} }

type swarmRotateTokensAction struct{ actBase }

type swarmRotateTokensActionModel struct {
	Worker     types.Bool   `tfsdk:"worker"`
	Manager    types.Bool   `tfsdk:"manager"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *swarmRotateTokensAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_rotate_tokens"
}

func (a *swarmRotateTokensAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Rotates the swarm join tokens, like `docker swarm join-token --rotate`. " +
			"Nodes already in the swarm are not affected; the old token stops working for new joins. " +
			"The provider's host must be a swarm manager. The result object carries the new tokens (`result.join_tokens`, " +
			"`worker_join_token`, `manager_join_token`): they show as `(sensitive)` in the progress message and are written in full only " +
			"to `result_path`; the `docker_swarm_tokens` data source or ephemeral resource reads them too.",
		Attributes: map[string]schema.Attribute{
			"worker":      schema.BoolAttribute{Optional: true, MarkdownDescription: "Rotate the worker join token. Default `true`."},
			"manager":     schema.BoolAttribute{Optional: true, MarkdownDescription: "Rotate the manager join token. Default `false`."},
			"result_path": actResultPathAttribute(),
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
	flags := client.SwarmUpdateOptions{RotateWorkerToken: actBool(m.Worker, true), RotateManagerToken: actBool(m.Manager, false)}
	if err := actSwarmUpdate(ctx, c, flags, nil); err != nil {
		resp.Diagnostics.AddError("docker_swarm_rotate_tokens failed", err.Error())
		return
	}
	var which []string
	if flags.RotateWorkerToken {
		which = append(which, "worker")
	}
	if flags.RotateManagerToken {
		which = append(which, "manager")
	}
	actProgress(ctx, resp, "rotated swarm %s join token(s)", strings.Join(which, " and "))
	sw, ok := actSwarmInspect(ctx, c, &resp.Diagnostics, "docker_swarm_rotate_tokens")
	if !ok {
		return
	}
	actFinishResult(ctx, resp, a.pd, "swarm:swarm-rotate-tokens", map[string]any{
		"id":                 sw.ID,
		"version_index":      sw.Version.Index,
		"worker_join_token":  sw.JoinTokens.Worker,
		"manager_join_token": sw.JoinTokens.Manager,
		"result":             map[string]any{"join_tokens": sw.JoinTokens},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_swarm_rotate_unlock_key

// NewSwarmRotateUnlockKeyAction returns the docker_swarm_rotate_unlock_key action.
func NewSwarmRotateUnlockKeyAction() action.Action { return &swarmRotateUnlockKeyAction{} }

type swarmRotateUnlockKeyAction struct{ actBase }

type swarmResultOnlyActionModel struct {
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *swarmRotateUnlockKeyAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_rotate_unlock_key"
}

func (a *swarmRotateUnlockKeyAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Rotates the manager unlock key of an autolocked swarm, like `docker swarm unlock-key --rotate`. " +
			"The provider's host must be a manager and autolock must be on (`docker_swarm_init.autolock`). The new key is the " +
			"`unlock_key` value of the result object: `(sensitive)` in the progress message, in full only in `result_path`. Store it " +
			"before restarting a manager; `docker_swarm_init.unlock_key` shows it after the next refresh.",
		Attributes: map[string]schema.Attribute{"result_path": actResultPathAttribute()},
	}
}

func (a *swarmRotateUnlockKeyAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m swarmResultOnlyActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	err := actSwarmUpdate(ctx, c, client.SwarmUpdateOptions{RotateManagerUnlockKey: true}, func(s *swarm.Spec) error {
		if !s.EncryptionConfig.AutoLockManagers {
			return errors.New("autolock is off, so the swarm has no unlock key to rotate; enable it first (docker_swarm_init.autolock = true)")
		}
		return nil
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_swarm_rotate_unlock_key failed", err.Error())
		return
	}
	key, err := c.SwarmGetUnlockKey(ctx)
	if err != nil {
		resp.Diagnostics.AddError("docker_swarm_rotate_unlock_key failed", "the key was rotated but reading it back failed: "+err.Error())
		return
	}
	actProgress(ctx, resp, "rotated the swarm unlock key")
	sw, ok := actSwarmInspect(ctx, c, &resp.Diagnostics, "docker_swarm_rotate_unlock_key")
	if !ok {
		return
	}
	actFinishResult(ctx, resp, a.pd, "swarm:swarm-rotate-unlock-key", map[string]any{
		"id":            sw.ID,
		"version_index": sw.Version.Index,
		"unlock_key":    key.Key,
		"result":        map[string]any{"autolock_managers": sw.Spec.EncryptionConfig.AutoLockManagers},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_swarm_rotate_ca

// NewSwarmRotateCaAction returns the docker_swarm_rotate_ca action.
func NewSwarmRotateCaAction() action.Action { return &swarmRotateCAAction{} }

type swarmRotateCAAction struct{ actBase }

type swarmRotateCAActionModel struct {
	SigningCACert types.String `tfsdk:"signing_ca_cert"`
	SigningCAKey  types.String `tfsdk:"signing_ca_key"`
	Wait          types.Bool   `tfsdk:"wait"`
	Timeout       types.String `tfsdk:"timeout"`
	ResultPath    types.String `tfsdk:"result_path"`
}

func (a *swarmRotateCAAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_rotate_ca"
}

func (a *swarmRotateCAAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Rotates the swarm root CA, like `docker swarm ca --rotate`: a new self-signed root, or the given signing " +
			"certificate and key (`Spec.CAConfig.SigningCACert` / `SigningCAKey`), with `CAConfig.ForceRotate` incremented. Every node " +
			"gets a certificate from the new root. The provider's host must be a manager. The result object has the new `tls_info` " +
			"and whether the root rotation is still in progress.",
		Attributes: map[string]schema.Attribute{
			"signing_ca_cert": schema.StringAttribute{Optional: true,
				MarkdownDescription: "PEM root CA certificate to rotate to (`SigningCACert`). Requires `signing_ca_key`. Unset generates a new root."},
			"signing_ca_key": schema.StringAttribute{Optional: true,
				MarkdownDescription: "PEM private key of `signing_ca_cert` (`SigningCAKey`). Actions keep no state, so the key is only sent to the " +
					"Engine; it is never returned by the Engine or written to the progress messages or the result."},
			"wait":        schema.BoolAttribute{Optional: true, MarkdownDescription: "Wait until the root rotation has finished on every node. Default `true`."},
			"timeout":     schema.StringAttribute{Optional: true, MarkdownDescription: "How long to wait, as a Go duration. Default `10m`."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *swarmRotateCAAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m swarmRotateCAActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actDuration(m.Timeout, "timeout", &resp.Diagnostics)
	if m.SigningCACert.IsUnknown() || m.SigningCAKey.IsUnknown() {
		return
	}
	if m.SigningCACert.IsNull() != m.SigningCAKey.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("signing_ca_cert"), "Incomplete signing CA", "Set both signing_ca_cert and signing_ca_key, or neither.")
	}
}

func (a *swarmRotateCAAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m swarmRotateCAActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var counter uint64
	err := actSwarmUpdate(ctx, c, client.SwarmUpdateOptions{}, func(s *swarm.Spec) error {
		s.CAConfig.ForceRotate++
		counter = s.CAConfig.ForceRotate
		if v, ok := knownString(m.SigningCACert); ok && v != "" {
			s.CAConfig.SigningCACert = v
			s.CAConfig.SigningCAKey = m.SigningCAKey.ValueString()
		}
		return nil
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_swarm_rotate_ca failed", err.Error())
		return
	}
	actProgress(ctx, resp, "swarm root CA rotation %d started", counter)
	sw, ok := actSwarmInspect(ctx, c, &resp.Diagnostics, "docker_swarm_rotate_ca")
	if !ok {
		return
	}
	if actBool(m.Wait, true) && sw.RootRotationInProgress {
		timeout := actTimeout(m.Timeout, actCARotateDefaultTimeout)
		werr := waitFor(ctx, timeout, actPollInterval, func(ctx context.Context) (bool, error) {
			swRes, err := c.SwarmInspect(ctx, client.SwarmInspectOptions{})
			if err != nil {
				return false, err
			}
			sw = swRes.Swarm
			return !sw.RootRotationInProgress, nil
		})
		if werr != nil {
			msg := werr.Error()
			if errors.Is(werr, context.DeadlineExceeded) {
				msg = fmt.Sprintf("the root CA rotation was still in progress after %s (some nodes have not renewed their certificates yet)", timeout)
			}
			resp.Diagnostics.AddError("docker_swarm_rotate_ca: rotation did not finish", msg)
			return
		}
		actProgress(ctx, resp, "swarm root CA rotation finished")
	}
	actFinishResult(ctx, resp, a.pd, "swarm:swarm-ca-rotate", map[string]any{
		"id":            sw.ID,
		"version_index": sw.Version.Index,
		"result":        map[string]any{"tls_info": sw.TLSInfo, "root_rotation_in_progress": sw.RootRotationInProgress},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_swarm_unlock

// NewSwarmUnlockAction returns the docker_swarm_unlock action.
func NewSwarmUnlockAction() action.Action { return &swarmUnlockAction{} }

type swarmUnlockAction struct{ actBase }

type swarmUnlockActionModel struct {
	UnlockKey  types.String `tfsdk:"unlock_key"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *swarmUnlockAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_unlock"
}

func (a *swarmUnlockAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Unlocks a locked swarm manager, like `docker swarm unlock`: after a daemon restart, a manager of an " +
			"autolocked swarm stays locked until it gets the unlock key. It acts on the provider's Docker host (`POST /swarm/unlock`). " +
			"An already unlocked manager is left alone. The result object is the node's swarm status (`GET /info` `Swarm`) afterwards.",
		Attributes: map[string]schema.Attribute{
			"unlock_key": schema.StringAttribute{Required: true,
				MarkdownDescription: "The swarm unlock key (`SWMKEY-1-...`), e.g. from `docker_swarm_init.unlock_key` or " +
					"`docker_swarm_rotate_unlock_key`. Actions keep no state, so the key is only sent to the Engine; it never appears in " +
					"the progress messages or the result."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *swarmUnlockAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m swarmUnlockActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	infoRes, err := c.Info(ctx, client.InfoOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_swarm_unlock failed", "reading daemon info: "+err.Error())
		return
	}
	switch infoRes.Info.Swarm.LocalNodeState {
	case swarm.LocalNodeStateLocked:
		if _, err := c.SwarmUnlock(ctx, client.SwarmUnlockOptions{Key: m.UnlockKey.ValueString()}); err != nil {
			resp.Diagnostics.AddError("docker_swarm_unlock failed", err.Error())
			return
		}
		actProgress(ctx, resp, "swarm manager unlocked")
		if infoRes, err = c.Info(ctx, client.InfoOptions{}); err != nil {
			resp.Diagnostics.AddError("docker_swarm_unlock failed", "reading daemon info after unlock: "+err.Error())
			return
		}
	case swarm.LocalNodeStateActive:
		actProgress(ctx, resp, "swarm manager is not locked; nothing to do")
	default:
		resp.Diagnostics.AddError("docker_swarm_unlock failed",
			fmt.Sprintf("the provider's Docker host is not a locked swarm manager (local node state %q)", infoRes.Info.Swarm.LocalNodeState))
		return
	}
	si := infoRes.Info.Swarm
	actFinishResult(ctx, resp, a.pd, "swarm:swarm-unlock", map[string]any{
		"node_id": si.NodeID,
		"result": map[string]any{"swarm_info": map[string]any{
			"NodeID": si.NodeID, "NodeAddr": si.NodeAddr, "LocalNodeState": si.LocalNodeState,
			"ControlAvailable": si.ControlAvailable, "Error": si.Error,
		}},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// Node actions

// actNodeFinish reports the node's state as the result object of a node
// action (result = {spec}).
func actNodeFinish(ctx context.Context, resp *action.InvokeResponse, pd *providerData, c *client.Client, itemID, id string, resultPath types.String) {
	nRes, err := c.NodeInspect(ctx, id, client.NodeInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect node", "reading the node after the update: "+err.Error())
		return
	}
	n := nRes.Node
	actFinishResult(ctx, resp, pd, itemID, map[string]any{
		"node_id":       n.ID,
		"hostname":      n.Description.Hostname,
		"version_index": n.Version.Index,
		"result":        map[string]any{"spec": n.Spec},
	}, resultPath)
}

type nodeActionModel struct {
	NodeID     types.String `tfsdk:"node_id"`
	ResultPath types.String `tfsdk:"result_path"`
}

// NewNodeDrainAction returns the docker_node_drain action.
func NewNodeDrainAction() action.Action { return &nodeDrainAction{} }

type nodeDrainAction struct{ actBase }

type nodeDrainActionModel struct {
	NodeID     types.String `tfsdk:"node_id"`
	Wait       types.Bool   `tfsdk:"wait"`
	Timeout    types.String `tfsdk:"timeout"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *nodeDrainAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node_drain"
}

func (a *nodeDrainAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a swarm node's availability to `drain` and waits until its tasks have left, like `docker node update --availability drain`. " +
			"The provider's host must be a swarm manager. If a `docker_node` or `docker_swarm_node` resource manages the node with " +
			"`availability = \"active\"`, its next plan shows the change; use `docker_node_activate` to undo. The result object has the node's spec afterwards.",
		Attributes: map[string]schema.Attribute{
			"node_id":     schema.StringAttribute{Required: true, MarkdownDescription: actNodeIDDoc},
			"wait":        schema.BoolAttribute{Optional: true, MarkdownDescription: "Wait until no task on the node is still running. Default `true`."},
			"timeout":     schema.StringAttribute{Optional: true, MarkdownDescription: "How long to wait for the tasks to leave, as a Go duration. Default `5m`."},
			"result_path": actResultPathAttribute(),
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
	tasksRes, err := c.TaskList(ctx, client.TaskListOptions{Filters: client.Filters{}.Add("node", id)})
	if err != nil {
		return nil, err
	}
	tasks := tasksRes.Items
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
	id, err := resolveNodeID(ctx, c, m.NodeID.ValueString())
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
	if actBool(m.Wait, true) {
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
	actNodeFinish(ctx, resp, a.pd, c, "swarm:node-drain", id, m.ResultPath)
}

// NewNodeActivateAction returns the docker_node_activate action.
func NewNodeActivateAction() action.Action { return &nodeActivateAction{} }

type nodeActivateAction struct{ actBase }

func (a *nodeActivateAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node_activate"
}

func (a *nodeActivateAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets a swarm node's availability back to `active`, like `docker node update --availability active`. " +
			"The scheduler can then place new tasks on it; running services are not rebalanced. The provider's host must be a swarm manager. " +
			"The result object has the node's spec afterwards.",
		Attributes: map[string]schema.Attribute{
			"node_id":     schema.StringAttribute{Required: true, MarkdownDescription: actNodeIDDoc},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *nodeActivateAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m nodeActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := resolveNodeID(ctx, c, m.NodeID.ValueString())
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
	actNodeFinish(ctx, resp, a.pd, c, "swarm:node-activate", id, m.ResultPath)
}

// nodeRoleAction is docker_node_promote (role manager) and
// docker_node_demote (role worker).
type nodeRoleAction struct {
	actBase
	role swarm.NodeRole
}

// NewNodePromoteAction returns the docker_node_promote action.
func NewNodePromoteAction() action.Action { return &nodeRoleAction{role: swarm.NodeRoleManager} }

// NewNodeDemoteAction returns the docker_node_demote action.
func NewNodeDemoteAction() action.Action { return &nodeRoleAction{role: swarm.NodeRoleWorker} }

func (a *nodeRoleAction) verb() string {
	if a.role == swarm.NodeRoleManager {
		return "promote"
	}
	return "demote"
}

func (a *nodeRoleAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_node_" + a.verb()
}

func (a *nodeRoleAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	desc := "Promotes a swarm worker to manager, like `docker node promote` (NodeUpdate with `Role = manager`). " +
		"Keep an odd number of managers for raft quorum. "
	if a.role == swarm.NodeRoleWorker {
		desc = "Demotes a swarm manager to worker, like `docker node demote` (NodeUpdate with `Role = worker`). " +
			"The Engine refuses to demote the last manager; demoting a manager can cost raft quorum. "
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: desc + "The provider's host must be a manager. A node already in that role is left alone. If a `docker_node` or " +
			"`docker_swarm_node` resource manages the node's `role`, its next plan reverts the change. The result object has the node's spec afterwards.",
		Attributes: map[string]schema.Attribute{
			"node_id":     schema.StringAttribute{Required: true, MarkdownDescription: actNodeIDDoc},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *nodeRoleAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m nodeActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	typ := "docker_node_" + a.verb()
	id, err := resolveNodeID(ctx, c, m.NodeID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(typ+" failed", err.Error())
		return
	}
	role := a.role
	if err := swarmUpdateNode(ctx, c, id, swarmNodeSpecChange{Role: &role}); err != nil {
		resp.Diagnostics.AddError(typ+" failed", err.Error())
		return
	}
	actProgress(ctx, resp, "node %s role set to %s", svcShortID(id), role)
	actNodeFinish(ctx, resp, a.pd, c, "swarm:node-"+a.verb(), id, m.ResultPath)
}
