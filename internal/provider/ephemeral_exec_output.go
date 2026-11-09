package provider

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ ephemeral.EphemeralResource              = (*execOutputEphemeral)(nil)
	_ ephemeral.EphemeralResourceWithConfigure = (*execOutputEphemeral)(nil)
)

// execOutputMaxBytes caps each captured stream.
const execOutputMaxBytes = 4 << 20

// NewExecOutputEphemeralResource returns the docker_exec_output ephemeral resource.
func NewExecOutputEphemeralResource() ephemeral.EphemeralResource { return &execOutputEphemeral{} }

type execOutputEphemeral struct {
	pd *providerData
}

type execOutputModel struct {
	ContainerID types.String `tfsdk:"container_id"`
	Command     types.List   `tfsdk:"command"`
	User        types.String `tfsdk:"user"`
	Workdir     types.String `tfsdk:"workdir"`
	Env         types.Map    `tfsdk:"env"`
	Privileged  types.Bool   `tfsdk:"privileged"`
	Timeout     types.String `tfsdk:"timeout"`
	ExecID      types.String `tfsdk:"exec_id"`
	ExitCode    types.Int64  `tfsdk:"exit_code"`
	contractEngineExecOutput
}

func (e *execOutputEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exec_output"
}

func (e *execOutputEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Runs a command in a running container (`docker exec`) and returns its standard output and error as ephemeral values that are never written to plan or state. " +
			"A non-zero exit code is an error that carries the command's stderr. Use it to read a generated credential or setting out of a container during a run; " +
			"the command runs on every plan and apply, so it must be safe to repeat. `result` holds the Engine's exec inspect (`GET /exec/{id}/json`).",
		Attributes: contractAddEphemeralAttributes(map[string]schema.Attribute{
			"container_id": schema.StringAttribute{Required: true, MarkdownDescription: "The container to run the command in, by ID or name; it must be running."},
			"command": schema.ListAttribute{
				ElementType:         types.StringType,
				Required:            true,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				MarkdownDescription: "Command and arguments, exec form, e.g. `[\"cat\", \"/run/secrets/token\"]`. Use `[\"sh\", \"-c\", \"...\"]` for shell syntax.",
			},
			"user":       schema.StringAttribute{Optional: true, MarkdownDescription: "User to run as (`name`, `uid` or `uid:gid`). Default: the container's user."},
			"workdir":    schema.StringAttribute{Optional: true, MarkdownDescription: "Working directory inside the container."},
			"env":        schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Extra environment variables for the command."},
			"privileged": schema.BoolAttribute{Optional: true, MarkdownDescription: "Give the command extended privileges. Default `false`."},
			"timeout":    schema.StringAttribute{Optional: true, MarkdownDescription: "Maximum run time as a Go duration, e.g. `30s`. Default `60s`."},
			"exec_id":    schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the exec instance (`POST /containers/{id}/exec`)."},
			"exit_code": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Exit code of the command; always 0, because any other code fails the read.",
			},
		}, "engine:exec-output"),
	}
}

func (e *execOutputEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	e.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// cappedBuffer keeps the first max bytes written and drops the rest.
type cappedBuffer struct {
	bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		b.truncated = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func (e *execOutputEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	if e.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var m execOutputModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, err := parseDurationOr(m.Timeout.ValueString(), 60*time.Second)
	if err != nil || timeout <= 0 {
		resp.Diagnostics.AddAttributeError(path.Root("timeout"), "Invalid timeout", fmt.Sprintf("%q is not a positive Go duration.", m.Timeout.ValueString()))
		return
	}
	var cmd []string
	resp.Diagnostics.Append(m.Command.ElementsAs(ctx, &cmd, false)...)
	envMap := stringMapFrom(ctx, m.Env, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.ContainerID.ValueString()
	insp, _, err := copInspect(ctx, e.pd.Client, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "docker_exec_output", ref, err)
		return
	}
	out, err := copExecRun(ctx, e.pd.Client, copExecSpec{
		Container: insp.ID,
		Opts: client.ExecCreateOptions{
			Cmd: cmd, User: m.User.ValueString(), WorkingDir: m.Workdir.ValueString(), Env: copMergeEnv(nil, envMap),
			Privileged: m.Privileged.ValueBool(),
		},
		Timeout:  timeout,
		MaxBytes: execOutputMaxBytes,
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot run command in container", fmt.Sprintf("%s: %v", ref, err))
		return
	}
	if out.ExitCode != 0 {
		msg := strings.TrimSpace(out.Stderr)
		if msg == "" {
			msg = "(no stderr)"
		}
		resp.Diagnostics.AddError("Command failed in container",
			fmt.Sprintf("%q in %s exited with code %d: %s", strings.Join(cmd, " "), ref, out.ExitCode, msg))
		return
	}
	m.ExecID = types.StringValue(out.ExecID)
	m.ExitCode = types.Int64Value(int64(out.ExitCode))
	resp.Diagnostics.Append(m.contractFill(ctx, e.pd, contractFillInput{
		Raw:    map[string][]byte{"stdout": []byte(out.Stdout), "stderr": []byte(out.Stderr)},
		Result: map[string]any{"result": out.Inspect},
	})...)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
}
