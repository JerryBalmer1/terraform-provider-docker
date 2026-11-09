package provider

// Compose per-container verbs (Q15): docker_compose_start / _stop /
// _restart / _kill / _pause / _unpause. One implementation, parameterized
// by verb; containers are selected by the com.docker.compose.project and
// com.docker.compose.service labels like the CLI does. These are separate
// from the docker_container_* actions (Lane A).

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	aschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// composeVerb describes one verb.
type composeVerb struct {
	Suffix  string // type name suffix after docker_compose_
	Item    string // inventory item id
	CLI     string
	Summary string
	// Pick says whether a container in state s is acted on.
	Pick func(s container.ContainerState) bool
	// Timeout / Signal say which optional attributes the verb has.
	Timeout, Signal bool
	Do              func(ctx context.Context, c *client.Client, id string, m *composeVerbOpts) error
}

func composeVerbTimeout(m *composeVerbOpts) *int {
	if m.Timeout.IsNull() || m.Timeout.IsUnknown() {
		return nil
	}
	t := int(m.Timeout.ValueInt64())
	return &t
}

var composeVerbs = map[string]composeVerb{
	"start": {Suffix: "start", Item: "compose:cli-start", CLI: "docker compose start",
		Summary: "Starts the existing stopped containers of a compose project",
		Pick: func(s container.ContainerState) bool {
			return s == container.StateCreated || s == container.StateExited
		},
		Do: func(ctx context.Context, c *client.Client, id string, _ *composeVerbOpts) error {
			_, err := c.ContainerStart(ctx, id, client.ContainerStartOptions{})
			return err
		}},
	"stop": {Suffix: "stop", Item: "compose:cli-stop", CLI: "docker compose stop", Timeout: true,
		Summary: "Stops the running containers of a compose project without removing them",
		Pick: func(s container.ContainerState) bool {
			return s == container.StateRunning || s == container.StateRestarting || s == container.StatePaused
		},
		Do: func(ctx context.Context, c *client.Client, id string, m *composeVerbOpts) error {
			_, err := c.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: composeVerbTimeout(m)})
			return err
		}},
	"restart": {Suffix: "restart", Item: "compose:cli-restart", CLI: "docker compose restart", Timeout: true,
		Summary: "Restarts the containers of a compose project (stopped ones included)",
		Pick:    func(container.ContainerState) bool { return true },
		Do: func(ctx context.Context, c *client.Client, id string, m *composeVerbOpts) error {
			_, err := c.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: composeVerbTimeout(m)})
			return err
		}},
	"kill": {Suffix: "kill", Item: "compose:cli-kill", CLI: "docker compose kill", Signal: true,
		Summary: "Sends a signal (default `SIGKILL`) to the running containers of a compose project",
		Pick:    func(s container.ContainerState) bool { return s == container.StateRunning },
		Do: func(ctx context.Context, c *client.Client, id string, m *composeVerbOpts) error {
			_, err := c.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: m.Signal.ValueString()})
			return err
		}},
	"pause": {Suffix: "pause", Item: "compose:cli-pause", CLI: "docker compose pause",
		Summary: "Pauses the running containers of a compose project",
		Pick:    func(s container.ContainerState) bool { return s == container.StateRunning },
		Do: func(ctx context.Context, c *client.Client, id string, _ *composeVerbOpts) error {
			_, err := c.ContainerPause(ctx, id, client.ContainerPauseOptions{})
			return err
		}},
	"unpause": {Suffix: "unpause", Item: "compose:cli-unpause", CLI: "docker compose unpause",
		Summary: "Unpauses the paused containers of a compose project",
		Pick:    func(s container.ContainerState) bool { return s == container.StatePaused },
		Do: func(ctx context.Context, c *client.Client, id string, _ *composeVerbOpts) error {
			_, err := c.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
			return err
		}},
}

// NewComposeStartAction returns docker_compose_start.
func NewComposeStartAction() action.Action { return &composeVerbAction{verb: composeVerbs["start"]} }

// NewComposeStopAction returns docker_compose_stop.
func NewComposeStopAction() action.Action { return &composeVerbAction{verb: composeVerbs["stop"]} }

// NewComposeRestartAction returns docker_compose_restart.
func NewComposeRestartAction() action.Action {
	return &composeVerbAction{verb: composeVerbs["restart"]}
}

// NewComposeKillAction returns docker_compose_kill.
func NewComposeKillAction() action.Action { return &composeVerbAction{verb: composeVerbs["kill"]} }

// NewComposePauseAction returns docker_compose_pause.
func NewComposePauseAction() action.Action { return &composeVerbAction{verb: composeVerbs["pause"]} }

// NewComposeUnpauseAction returns docker_compose_unpause.
func NewComposeUnpauseAction() action.Action {
	return &composeVerbAction{verb: composeVerbs["unpause"]}
}

type composeVerbAction struct {
	actBase
	verb composeVerb
}

type composeVerbModel struct {
	Name       types.String `tfsdk:"name"`
	Services   types.List   `tfsdk:"services"`
	ResultPath types.String `tfsdk:"result_path"`
}

// composeVerbOpts are the verb-specific attributes, read with GetAttribute
// only for the verbs that have them.
type composeVerbOpts struct {
	Timeout types.Int64
	Signal  types.String
}

func (a *composeVerbAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_" + a.verb.Suffix
}

// composeActionProjectAttr / composeActionServicesAttr are the label
// selector attributes of the compose actions.
func composeActionProjectAttr() aschema.StringAttribute {
	return aschema.StringAttribute{Required: true,
		MarkdownDescription: "The compose project name (`com.docker.compose.project` label, CLI `--project-name`)."}
}

func composeActionServicesAttr() aschema.ListAttribute {
	return aschema.ListAttribute{ElementType: types.StringType, Optional: true,
		MarkdownDescription: "Only these services (default: every service of the project)."}
}

func (a *composeVerbAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	attrs := map[string]aschema.Attribute{
		"name":        composeActionProjectAttr(),
		"services":    composeActionServicesAttr(),
		"result_path": actResultPathAttribute(),
	}
	if a.verb.Timeout {
		attrs["timeout"] = aschema.Int64Attribute{Optional: true,
			MarkdownDescription: "Seconds to wait for each container to stop before it is killed (CLI `--timeout`). Default: the container's stop timeout.",
			Validators:          []validator.Int64{int64validator.AtLeast(0)}}
	}
	if a.verb.Signal {
		attrs["signal"] = aschema.StringAttribute{Optional: true,
			MarkdownDescription: "Signal to send, e.g. `SIGHUP` or `9` (CLI `--signal`). Default `SIGKILL`."}
	}
	resp.Schema = aschema.Schema{
		MarkdownDescription: a.verb.Summary + ", like `" + a.verb.CLI + "`. Containers are selected by the `com.docker.compose.project` " +
			"and `com.docker.compose.service` labels (one-off `run` containers excluded), so projects made by `docker_compose` and by the CLI " +
			"both work. The result lists each container's state afterwards (`result.container_states`).",
		Attributes: attrs,
	}
}

func (a *composeVerbAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	// The schema differs per verb (timeout / signal), so attributes are
	// read one by one.
	var m composeVerbModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &m.Name)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("services"), &m.Services)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(actResultPathAttr), &m.ResultPath)...)
	var opts composeVerbOpts
	if a.verb.Timeout {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("timeout"), &opts.Timeout)...)
	}
	if a.verb.Signal {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("signal"), &opts.Signal)...)
	}
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	sel := composeSelector{Project: m.Name.ValueString(), Services: stringListFrom(ctx, m.Services, &resp.Diagnostics), All: true}
	cs, err := composeContainers(ctx, a.pd, sel)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_"+a.verb.Suffix+": cannot list containers", err.Error())
		return
	}
	if len(cs) == 0 {
		resp.Diagnostics.AddError("docker_compose_"+a.verb.Suffix+": no containers",
			fmt.Sprintf("project %q has no containers for the selected services", sel.Project))
		return
	}
	acted := 0
	for _, s := range cs {
		if !a.verb.Pick(s.State) {
			continue
		}
		if err := a.verb.Do(ctx, c, s.ID, &opts); err != nil {
			resp.Diagnostics.AddError("docker_compose_"+a.verb.Suffix+" failed", fmt.Sprintf("container %s: %v", composeSummaryName(s), err))
			return
		}
		acted++
		actProgress(ctx, resp, "%s %s", a.verb.Suffix, composeSummaryName(s))
	}
	after, err := composeContainers(ctx, a.pd, sel)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_"+a.verb.Suffix+": cannot list containers", err.Error())
		return
	}
	states := make([]any, 0, len(after))
	for _, s := range after {
		states = append(states, composeContainerState(s))
	}
	actFinishResult(ctx, resp, a.pd, a.verb.Item, map[string]any{
		"name":          sel.Project,
		"service_names": composeServiceNames(after),
		"container_ids": composeContainerIDs(after),
		"result":        map[string]any{"container_states": states},
	}, m.ResultPath)
}
