package provider

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
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*execAction)(nil)
	_ action.ActionWithValidateConfig = (*execAction)(nil)
)

// actExecDefaultTimeout bounds a docker_exec run when timeout is unset.
const actExecDefaultTimeout = 5 * time.Minute

// NewExecAction returns the docker_exec action.
func NewExecAction() action.Action { return &execAction{} }

type execAction struct{ actBase }

type execActionModel struct {
	Container  types.String `tfsdk:"container"`
	Service    types.String `tfsdk:"service"`
	Command    types.List   `tfsdk:"command"`
	Env        types.Map    `tfsdk:"env"`
	User       types.String `tfsdk:"user"`
	Workdir    types.String `tfsdk:"workdir"`
	Privileged types.Bool   `tfsdk:"privileged"`
	Timeout    types.String `tfsdk:"timeout"`
}

func (a *execAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exec"
}

func (a *execAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Runs a command inside a running container, like `docker exec`, and fails when it exits non-zero. " +
			"stdout and stderr are streamed line by line as action progress and to the provider log; on failure the error carries the exit code " +
			"and the last 20 lines of output. Runs through the Engine exec API; no docker CLI is involved.",
		Attributes: map[string]schema.Attribute{
			"container": schema.StringAttribute{Optional: true, MarkdownDescription: "Name or ID of a running container, e.g. `docker_container.app.name`. Exactly one of `container` and `service`."},
			"service": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of a Swarm service: the command runs in one running task container of it on the provider's daemon (the first by name). " +
					"Pin the service to that node with a placement constraint. Exactly one of `container` and `service`.",
			},
			"command": schema.ListAttribute{
				Required: true, ElementType: types.StringType,
				MarkdownDescription: "Command and arguments, exec form, e.g. `[\"sh\", \"-c\", \"echo hello\"]`. No shell is added.",
			},
			"env":        schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Extra environment variables for the command."},
			"user":       schema.StringAttribute{Optional: true, MarkdownDescription: "User (and optional group) to run as, `name|uid[:group|gid]`. Default: the container's user."},
			"workdir":    schema.StringAttribute{Optional: true, MarkdownDescription: "Working directory inside the container. Default: the container's working directory."},
			"privileged": schema.BoolAttribute{Optional: true, MarkdownDescription: "Give the command extended privileges. Default `false`."},
			"timeout": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "How long the command may run, as a Go duration (`30s`, `10m`). Default `5m`. On timeout the action fails; the process keeps running in the container.",
			},
		},
	}
}

func (a *execAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m execActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actDuration(m.Timeout, "timeout", &resp.Diagnostics)
	if !m.Container.IsUnknown() && !m.Service.IsUnknown() && m.Container.IsNull() == m.Service.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("container"), "Exactly one of container and service", "Set container (a container name or ID) or service (a Swarm service name), not both and not neither.")
	}
	if !m.Command.IsUnknown() && !m.Command.IsNull() && len(m.Command.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("command"), "Empty command", "command needs at least one element.")
	}
}

func (a *execAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m execActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	cmd := stringListFrom(ctx, m.Command, &resp.Diagnostics)
	envMap := stringMapFrom(ctx, m.Env, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	ctr := m.Container.ValueString()
	if svc := m.Service.ValueString(); svc != "" {
		ctr = actExecServiceContainer(ctx, c, svc, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	timeout := actTimeout(m.Timeout, actExecDefaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	created, err := c.ExecCreate(ctx, ctr, client.ExecCreateOptions{
		User:         m.User.ValueString(),
		Privileged:   actBool(m.Privileged, false),
		AttachStdout: true,
		AttachStderr: true,
		Env:          env,
		WorkingDir:   m.Workdir.ValueString(),
		Cmd:          cmd,
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_exec: cannot create exec", fmt.Sprintf("container %q: %v", ctr, err))
		return
	}
	actProgress(ctx, resp, "exec %s in %s: %s", svcShortID(created.ID), ctr, strings.Join(cmd, " "))
	hj, err := c.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_exec: cannot start exec", fmt.Sprintf("container %q: %v", ctr, err))
		return
	}
	// Closing the hijacked connection unblocks StdCopy when ctx ends.
	stop := context.AfterFunc(ctx, hj.Close)
	defer stop()
	defer hj.Close()

	var tail []string
	emit := func(s string) {
		tflog.Info(ctx, s, map[string]any{"exec": created.ID})
		if resp.SendProgress != nil {
			resp.SendProgress(action.InvokeProgressEvent{Message: s})
		}
	}
	stdout := &actLineWriter{emit: emit, tail: &tail}
	stderr := &actLineWriter{prefix: "stderr: ", emit: emit, tail: &tail}
	_, cerr := stdcopy.StdCopy(stdout, stderr, hj.Reader)
	stdout.Flush()
	stderr.Flush()
	if ctx.Err() != nil {
		resp.Diagnostics.AddError("docker_exec: timed out",
			fmt.Sprintf("command %q in container %q did not finish within %s.%s", strings.Join(cmd, " "), ctr, timeout, actExecTail(tail)))
		return
	}
	if cerr != nil {
		resp.Diagnostics.AddError("docker_exec: reading output failed", fmt.Sprintf("%v%s", cerr, actExecTail(tail)))
		return
	}

	// The stream ends when the process exits; the exit code can lag a
	// moment behind, so poll until Running is false.
	var insp client.ExecInspectResult
	werr := waitFor(ctx, timeout, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
		var err error
		insp, err = c.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return false, err
		}
		return !insp.Running, nil
	})
	if werr != nil {
		if errors.Is(werr, context.DeadlineExceeded) {
			werr = fmt.Errorf("exit code not available within %s", timeout)
		}
		resp.Diagnostics.AddError("docker_exec: cannot read exit code", werr.Error())
		return
	}
	if insp.ExitCode != 0 {
		resp.Diagnostics.AddError("docker_exec: command failed",
			fmt.Sprintf("command %q in container %q exited with code %d.%s", strings.Join(cmd, " "), ctr, insp.ExitCode, actExecTail(tail)))
		return
	}
	actProgress(ctx, resp, "exec %s exited 0", svcShortID(created.ID))
}

func actExecTail(tail []string) string {
	if len(tail) == 0 {
		return ""
	}
	return "\n\nLast output:\n" + strings.Join(tail, "\n")
}

// actExecServiceContainer picks the running task container of a Swarm service
// on the provider's daemon (lowest name first, so repeated runs hit the same
// task) and returns its ID.
func actExecServiceContainer(ctx context.Context, c *client.Client, svc string, diags *diag.Diagnostics) string {
	listRes, err := c.ContainerList(ctx, client.ContainerListOptions{Filters: client.Filters{}.Add("label", "com.docker.swarm.service.name="+svc).Add("status", "running")})
	if err != nil {
		diags.AddError("docker_exec: cannot list service tasks", fmt.Sprintf("service %q: %v", svc, err))
		return ""
	}
	list := listRes.Items
	if len(list) == 0 {
		diags.AddError("docker_exec: no running task", fmt.Sprintf("service %q has no running task container on this daemon. "+
			"Pin it to the provider's node with a placement constraint, or use container.", svc))
		return ""
	}
	sort.Slice(list, func(i, j int) bool { return strings.Join(list[i].Names, ",") < strings.Join(list[j].Names, ",") })
	return list[0].ID
}
