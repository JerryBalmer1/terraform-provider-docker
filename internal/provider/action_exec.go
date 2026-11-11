package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*execAction)(nil)
	_ action.ActionWithValidateConfig = (*execAction)(nil)
)

// actExecDefaultTimeout bounds a docker_exec run when timeout is unset.
const actExecDefaultTimeout = 5 * time.Minute

// actExecMaxBytes caps each stream docker_exec keeps for its result.
const actExecMaxBytes = 4 << 20

// NewExecAction returns the docker_exec action.
func NewExecAction() action.Action { return &execAction{} }

type execAction struct{ actBase }

type execActionModel struct {
	ContainerID types.String `tfsdk:"container_id"`
	ServiceID   types.String `tfsdk:"service_id"`
	Command     types.List   `tfsdk:"command"`
	Env         types.Map    `tfsdk:"env"`
	EnvFile     types.List   `tfsdk:"env_file"`
	User        types.String `tfsdk:"user"`
	Workdir     types.String `tfsdk:"workdir"`
	Privileged  types.Bool   `tfsdk:"privileged"`
	TTY         types.Bool   `tfsdk:"tty"`
	ConsoleSize types.List   `tfsdk:"console_size"`
	Detach      types.Bool   `tfsdk:"detach"`
	Stdin       types.String `tfsdk:"stdin"`
	Timeout     types.String `tfsdk:"timeout"`
	ResultPath  types.String `tfsdk:"result_path"`
}

func (a *execAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exec"
}

func (a *execAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Runs a command inside a running container, like `docker exec`, and fails when it exits non-zero. " +
			"stdout and stderr are streamed line by line as action progress and to the provider log; on failure the error carries the exit code " +
			"and the last 20 lines of output. The result object (`GET /exec/{id}/json`: `exit_code`, `pid`, `process_config`, ...) and the " +
			"captured `stdout` / `stderr` (up to 4 MiB each, with their `*_sha256`) go to the final progress message and to `result_path`. " +
			"Runs through the Engine exec API; no docker CLI is involved.",
		Attributes: map[string]schema.Attribute{
			"container_id": schema.StringAttribute{Optional: true, MarkdownDescription: "The running container to run the command in, by ID or name, e.g. `docker_container.app.id`. Exactly one of `container_id` and `service_id`."},
			"service_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "A Swarm service, by name: the command runs in one running task container of it on the provider's daemon (the first by name). " +
					"Pin the service to that node with a placement constraint. Exactly one of `container_id` and `service_id`.",
			},
			"command": schema.ListAttribute{
				Required: true, ElementType: types.StringType,
				MarkdownDescription: "Command and arguments, exec form, e.g. `[\"sh\", \"-c\", \"echo hello\"]`. No shell is added.",
			},
			"env": schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Extra environment variables for the command. They override `env_file` entries."},
			"env_file": schema.ListAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Local files of `KEY=VALUE` lines read on the machine running Terraform, like `docker exec --env-file`; `#` comments and blank lines are skipped and a bare `KEY` takes the value from Terraform's environment."},
			"user":       schema.StringAttribute{Optional: true, MarkdownDescription: "User (and optional group) to run as, `name|uid[:group|gid]`. Default: the container's user."},
			"workdir":    schema.StringAttribute{Optional: true, MarkdownDescription: "Working directory inside the container. Default: the container's working directory."},
			"privileged": schema.BoolAttribute{Optional: true, MarkdownDescription: "Give the command extended privileges. Default `false`."},
			"tty": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Allocate a pseudo-TTY (`docker exec --tty`). Default `false`. With a TTY, stdout and stderr arrive merged in `stdout`."},
			"console_size": schema.ListAttribute{Optional: true, ElementType: types.Int64Type,
				Validators: []validator.List{listvalidator.SizeBetween(2, 2)},
				MarkdownDescription: "Initial terminal size as `[height, width]` (Engine `ConsoleSize`); only with `tty = true`, e.g. for programs " +
					"that format output to the terminal width."},
			"detach": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Start the command and return at once without waiting for it (`docker exec --detach`). Default `false`. Output is not captured and the exit code is not checked."},
			"stdin": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Data written to the command's standard input, which is then closed, like `echo ... | docker exec -i`. " +
					"Unset (the default), the command gets no stdin. It is never logged or written to `result_path`, but action arguments cannot be marked sensitive or write-only (terraform-plugin-framework v1.19), so pass secrets through `docker_exec_output`, whose `stdin` is sensitive. Not with `tty` or `detach`.",
			},
			"timeout": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "How long the command may run, as a Go duration (`30s`, `10m`). Default `5m`. On timeout the action fails; the process keeps running in the container.",
			},
			"result_path": actResultPathAttribute(),
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
	if !m.ContainerID.IsUnknown() && !m.ServiceID.IsUnknown() && m.ContainerID.IsNull() == m.ServiceID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("container_id"), "Exactly one of container_id and service_id",
			"Set container_id (a container ID or name) or service_id (a Swarm service name), not both and not neither.")
	}
	if containerKnown(m.ConsoleSize) && !actBool(m.TTY, false) {
		resp.Diagnostics.AddAttributeError(path.Root("console_size"), "console_size needs tty", "The Engine only applies console_size to a TTY; set tty = true.")
	}
	if !m.Stdin.IsNull() && !m.Stdin.IsUnknown() {
		if actBool(m.TTY, false) {
			resp.Diagnostics.AddAttributeError(path.Root("stdin"), "stdin needs tty = false",
				"With a TTY the daemon cannot signal end of file on stdin (it closes the output streams instead), so the command would never see the end of the input. Unset tty.")
		}
		if actBool(m.Detach, false) {
			resp.Diagnostics.AddAttributeError(path.Root("stdin"), "stdin cannot be used with detach",
				"A detached command is not attached to the provider, so there is no stream to write stdin to. Unset detach.")
		}
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
	envFiles := stringListFrom(ctx, m.EnvFile, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	fileEnv, err := copReadEnvFiles(envFiles)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("env_file"), "Cannot read env_file", err.Error())
		return
	}
	ctr := m.ContainerID.ValueString()
	if svc := m.ServiceID.ValueString(); svc != "" {
		ctr = actExecServiceContainer(ctx, c, svc, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	insp, _, err := copInspect(ctx, c, ctr)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "docker_exec", ctr, err)
		return
	}
	timeout := actTimeout(m.Timeout, actExecDefaultTimeout)
	var tail []string
	emit := func(s string) {
		tflog.Info(ctx, s, map[string]any{"container": insp.ID})
		if resp.SendProgress != nil {
			resp.SendProgress(action.InvokeProgressEvent{Message: s})
		}
	}
	stdout := &actLineWriter{emit: emit, tail: &tail}
	stderr := &actLineWriter{prefix: "stderr: ", emit: emit, tail: &tail}
	detach := actBool(m.Detach, false)
	actProgress(ctx, resp, "exec in %s: %s", ctr, strings.Join(cmd, " "))
	out, err := copExecRun(ctx, c, copExecSpec{
		Container: insp.ID,
		Opts: client.ExecCreateOptions{
			User:        m.User.ValueString(),
			Privileged:  actBool(m.Privileged, false),
			TTY:         actBool(m.TTY, false),
			ConsoleSize: actExecConsoleSize(ctx, m.ConsoleSize, &resp.Diagnostics),
			Env:         copMergeEnv(fileEnv, envMap),
			WorkingDir:  m.Workdir.ValueString(),
			Cmd:         cmd,
		},
		Detach:   detach,
		Stdin:    actExecStdin(m.Stdin),
		Timeout:  timeout,
		MaxBytes: actExecMaxBytes,
		Stdout:   stdout,
		Stderr:   stderr,
	})
	stdout.Flush()
	stderr.Flush()
	if err != nil {
		if errors.Is(err, errCopExecTimeout) {
			resp.Diagnostics.AddError("docker_exec: timed out",
				fmt.Sprintf("command %q in container %q did not finish within %s.%s", strings.Join(cmd, " "), ctr, timeout, actExecTail(tail)))
			return
		}
		resp.Diagnostics.AddError("docker_exec failed", fmt.Sprintf("container %q: %v%s", ctr, err, actExecTail(tail)))
		return
	}
	if !detach && out.ExitCode != 0 {
		resp.Diagnostics.AddError("docker_exec: command failed",
			fmt.Sprintf("command %q in container %q exited with code %d.%s", strings.Join(cmd, " "), ctr, out.ExitCode, actExecTail(tail)))
		return
	}
	if detach {
		actProgress(ctx, resp, "exec %s started detached", copShortID(out.ExecID))
	} else {
		actProgress(ctx, resp, "exec %s exited 0", copShortID(out.ExecID))
	}
	actFinishResult(ctx, resp, a.pd, "engine:exec", map[string]any{
		"exec_id":      out.ExecID,
		"container_id": insp.ID,
		"stdout":       out.Stdout,
		"stderr":       out.Stderr,
		"result":       out.Inspect,
	}, m.ResultPath)
}

// actExecStdin returns the stdin data, or nil when stdin is unset.
func actExecStdin(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
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
			"Pin it to the provider's node with a placement constraint, or use container_id.", svc))
		return ""
	}
	sort.Slice(list, func(i, j int) bool { return strings.Join(list[i].Names, ",") < strings.Join(list[j].Names, ",") })
	return list[0].ID
}

// actExecConsoleSize converts console_size ([height, width]) to the
// client's ConsoleSize (zero when unset).
func actExecConsoleSize(ctx context.Context, l types.List, diags *diag.Diagnostics) client.ConsoleSize {
	if !containerKnown(l) {
		return client.ConsoleSize{}
	}
	var hw []int64
	diags.Append(l.ElementsAs(ctx, &hw, false)...)
	if len(hw) != 2 || hw[0] < 0 || hw[1] < 0 {
		return client.ConsoleSize{}
	}
	return client.ConsoleSize{Height: uint(hw[0]), Width: uint(hw[1])}
}
