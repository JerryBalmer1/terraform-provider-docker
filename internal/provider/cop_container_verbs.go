package provider

// Pass 5 Lane A (WP cop): the container verb actions (DECISIONS Q15)
// docker_container_start / _stop / _restart / _kill / _pause / _unpause.
// They share one implementation: a verb table entry names the type, the
// inventory item, the extra arguments and the Engine call. Lane C's
// docker_compose_* verbs are a separate implementation.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// copVerbArgs are the optional arguments a verb may take.
type copVerbArgs struct {
	Signal  string
	Timeout *int
}

// copVerb describes one container verb action.
type copVerb struct {
	suffix   string // type name after "docker_"
	itemID   string
	doc      string
	signal   string // signal argument doc; "" = no signal argument
	timeout  bool   // has a timeout (seconds) argument
	progress string // past tense for messages
	run      func(ctx context.Context, c *client.Client, id string, a copVerbArgs) error
	// already reports that the container is in the verb's target state, so
	// the Engine call (which would fail with 409) is skipped. nil = always call.
	already func(st *container.State) bool
}

var copVerbs = map[string]copVerb{
	"start": {
		suffix: "container_start", itemID: "engine:container-start", progress: "started",
		doc: "Starts a stopped or created container, like `docker container start`. " +
			"docker_container never starts a container with `state = \"created\"`; use this action to start one later, for example after `docker_container_stop`. " +
			"Starting a running container is a no-op.",
		run: func(ctx context.Context, c *client.Client, id string, _ copVerbArgs) error {
			_, err := c.ContainerStart(ctx, id, client.ContainerStartOptions{})
			return err
		},
	},
	"stop": {
		suffix: "container_stop", itemID: "engine:container-stop", progress: "stopped",
		doc: "Stops a running container, like `docker container stop`: it sends the stop signal, waits for the grace period and then sends SIGKILL. " +
			"Stopping a stopped container is a no-op.",
		signal:  "Signal sent first (Engine `signal`), e.g. `SIGINT`. Default: the container's `StopSignal`, else `SIGTERM`.",
		timeout: true,
		run: func(ctx context.Context, c *client.Client, id string, a copVerbArgs) error {
			_, err := c.ContainerStop(ctx, id, client.ContainerStopOptions{Signal: a.Signal, Timeout: a.Timeout})
			return err
		},
	},
	"restart": {
		suffix: "container_restart", itemID: "engine:container-restart", progress: "restarted",
		doc: "Restarts a container, like `docker container restart`: a stop (signal, grace period, SIGKILL) followed by a start. " +
			"A stopped container is just started.",
		signal:  "Signal sent to stop the container (Engine `signal`). Default: the container's `StopSignal`, else `SIGTERM`.",
		timeout: true,
		run: func(ctx context.Context, c *client.Client, id string, a copVerbArgs) error {
			_, err := c.ContainerRestart(ctx, id, client.ContainerRestartOptions{Signal: a.Signal, Timeout: a.Timeout})
			return err
		},
	},
	"kill": {
		suffix: "container_kill", itemID: "engine:container-kill", progress: "signalled",
		doc: "Sends a signal to a running container, like `docker container kill`. " +
			"The default SIGKILL stops it at once; other signals (for example `SIGHUP` to reload a configuration) leave it running if it handles them.",
		signal: "Signal to send (Engine `signal`), a name such as `SIGHUP` or a number. Default `SIGKILL`.",
		run: func(ctx context.Context, c *client.Client, id string, a copVerbArgs) error {
			_, err := c.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: a.Signal})
			return err
		},
	},
	"pause": {
		suffix: "container_pause", itemID: "engine:container-pause", progress: "paused",
		doc: "Freezes all processes of a running container, like `docker container pause` (the cgroup freezer). " +
			"Resume it with `docker_container_unpause`. Pausing a paused container is a no-op (like `docker_compose_pause`).",
		run: func(ctx context.Context, c *client.Client, id string, _ copVerbArgs) error {
			_, err := c.ContainerPause(ctx, id, client.ContainerPauseOptions{})
			return err
		},
		already: func(st *container.State) bool { return st != nil && st.Paused },
	},
	"unpause": {
		suffix: "container_unpause", itemID: "engine:container-unpause", progress: "unpaused",
		doc: "Resumes the processes of a paused container, like `docker container unpause`. " +
			"Unpausing a container that is not paused is a no-op (like `docker_compose_unpause`).",
		run: func(ctx context.Context, c *client.Client, id string, _ copVerbArgs) error {
			_, err := c.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
			return err
		},
		already: func(st *container.State) bool { return st != nil && !st.Paused },
	},
}

// NewContainerStartAction returns the docker_container_start action.
func NewContainerStartAction() action.Action { return &copVerbAction{verb: copVerbs["start"]} }

// NewContainerStopAction returns the docker_container_stop action.
func NewContainerStopAction() action.Action { return &copVerbAction{verb: copVerbs["stop"]} }

// NewContainerRestartAction returns the docker_container_restart action.
func NewContainerRestartAction() action.Action { return &copVerbAction{verb: copVerbs["restart"]} }

// NewContainerKillAction returns the docker_container_kill action.
func NewContainerKillAction() action.Action { return &copVerbAction{verb: copVerbs["kill"]} }

// NewContainerPauseAction returns the docker_container_pause action.
func NewContainerPauseAction() action.Action { return &copVerbAction{verb: copVerbs["pause"]} }

// NewContainerUnpauseAction returns the docker_container_unpause action.
func NewContainerUnpauseAction() action.Action { return &copVerbAction{verb: copVerbs["unpause"]} }

var _ action.ActionWithConfigure = (*copVerbAction)(nil)

// copVerbAction is the one implementation behind the six verb actions.
type copVerbAction struct {
	actBase
	verb copVerb
}

func (a *copVerbAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + a.verb.suffix
}

// copContainerIDActionAttr is the container_id target argument (Q40).
func copContainerIDActionAttr() schema.StringAttribute {
	return schema.StringAttribute{Required: true,
		MarkdownDescription: "The container the action targets, by ID or name, e.g. `docker_container.app.id`."}
}

func (a *copVerbAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"container_id": copContainerIDActionAttr(),
		"result_path":  actResultPathAttribute(),
	}
	if a.verb.signal != "" {
		attrs["signal"] = schema.StringAttribute{Optional: true, MarkdownDescription: a.verb.signal}
	}
	if a.verb.timeout {
		attrs["timeout"] = schema.Int64Attribute{Optional: true,
			Validators: []validator.Int64{int64validator.AtLeast(-1)},
			MarkdownDescription: "Seconds to wait for the container to exit before SIGKILL (Engine `t`). `-1` waits forever, `0` kills at once. " +
				"Default: the container's `StopTimeout`, else 10."}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: a.verb.doc + " The result object holds the container's `state` after the call.",
		Attributes:          attrs,
	}
}

func (a *copVerbAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	c := a.client(&resp.Diagnostics)
	var ref, resultPath types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("container_id"), &ref)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("result_path"), &resultPath)...)
	var args copVerbArgs
	if a.verb.signal != "" {
		var s types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("signal"), &s)...)
		args.Signal = s.ValueString()
	}
	if a.verb.timeout {
		var t types.Int64
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("timeout"), &t)...)
		if !t.IsNull() && !t.IsUnknown() {
			v := int(t.ValueInt64())
			args.Timeout = &v
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	typ := "docker_" + a.verb.suffix
	insp, _, err := copInspect(ctx, c, ref.ValueString())
	if err != nil {
		copInspectErr(&resp.Diagnostics, typ, ref.ValueString(), err)
		return
	}
	if a.verb.already != nil && a.verb.already(insp.State) {
		// Pass 6b Lane Z: already in the target state; the Engine would
		// answer 409 ("already paused" / "is not paused"). No-op, as the
		// docker_compose_pause / _unpause actions.
		actProgress(ctx, resp, "container %s already %s; nothing to do", copName(insp.Name), a.verb.progress)
	} else if err := a.verb.run(ctx, c, insp.ID, args); err != nil {
		resp.Diagnostics.AddError(typ+" failed", fmt.Sprintf("container %q: %v", ref.ValueString(), err))
		return
	}
	after, _, err := copInspect(ctx, c, insp.ID)
	if err != nil {
		resp.Diagnostics.AddError(typ+": cannot read container state", fmt.Sprintf("container %q: %v", ref.ValueString(), err))
		return
	}
	status := ""
	if after.State != nil {
		status = string(after.State.Status)
	}
	actProgress(ctx, resp, "container %s %s (status %s)", copName(after.Name), a.verb.progress, strconv.Quote(status))
	actFinishResult(ctx, resp, a.pd, a.verb.itemID, map[string]any{
		"container_id": insp.ID,
		"result":       map[string]any{"state": after.State},
	}, resultPath)
}
