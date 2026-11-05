package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	Container types.String `tfsdk:"container"`
	Command   types.List   `tfsdk:"command"`
	User      types.String `tfsdk:"user"`
	Workdir   types.String `tfsdk:"workdir"`
	Env       types.Map    `tfsdk:"env"`
	Timeout   types.String `tfsdk:"timeout"`
	Stdout    types.String `tfsdk:"stdout"`
	Stderr    types.String `tfsdk:"stderr"`
	ExitCode  types.Int64  `tfsdk:"exit_code"`
}

func (e *execOutputEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exec_output"
}

func (e *execOutputEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Runs a command in a running container (`docker exec`) and returns its standard output as an ephemeral value that is never written to plan or state. " +
			"A non-zero exit code is an error that carries the command's stderr. Use it to read a generated credential or setting out of a container during a run; " +
			"the command runs on every plan and apply, so it must be safe to repeat.",
		Attributes: map[string]schema.Attribute{
			"container": schema.StringAttribute{Required: true, MarkdownDescription: "Container name or ID; it must be running."},
			"command": schema.ListAttribute{
				ElementType:         types.StringType,
				Required:            true,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				MarkdownDescription: "Command and arguments, exec form, e.g. `[\"cat\", \"/run/secrets/token\"]`. Use `[\"sh\", \"-c\", \"...\"]` for shell syntax.",
			},
			"user":    schema.StringAttribute{Optional: true, MarkdownDescription: "User to run as (`name`, `uid` or `uid:gid`). Default: the container's user."},
			"workdir": schema.StringAttribute{Optional: true, MarkdownDescription: "Working directory inside the container."},
			"env":     schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Extra environment variables for the command."},
			"timeout": schema.StringAttribute{Optional: true, MarkdownDescription: "Maximum run time as a Go duration, e.g. `30s`. Default `60s`."},
			"stdout":  schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Standard output of the command, exactly as written (up to 4 MiB)."},
			"stderr":  schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Standard error of the command (up to 4 MiB)."},
			"exit_code": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Exit code of the command; always 0, because any other code fails the read.",
			},
		},
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
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)

	stdout, stderr, code, err := execOutputRun(ctx, e.pd, m.Container.ValueString(), container.ExecOptions{
		Cmd: cmd, User: m.User.ValueString(), WorkingDir: m.Workdir.ValueString(), Env: env,
		AttachStdout: true, AttachStderr: true,
	}, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Cannot run command in container", fmt.Sprintf("%s: %v", m.Container.ValueString(), err))
		return
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "(no stderr)"
		}
		resp.Diagnostics.AddError("Command failed in container",
			fmt.Sprintf("%q in %s exited with code %d: %s", strings.Join(cmd, " "), m.Container.ValueString(), code, msg))
		return
	}
	m.Stdout = types.StringValue(stdout)
	m.Stderr = types.StringValue(stderr)
	m.ExitCode = types.Int64Value(int64(code))
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
}

// execOutputRun creates, attaches and inspects one exec, returning captured
// stdout, stderr and the exit code.
func execOutputRun(ctx context.Context, pd *providerData, ctr string, opts container.ExecOptions, timeout time.Duration) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	created, err := pd.Client.ContainerExecCreate(ctx, ctr, opts)
	if err != nil {
		return "", "", 0, err
	}
	att, err := pd.Client.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", 0, fmt.Errorf("attaching to exec %s: %w", created.ID, err)
	}
	defer att.Close()
	outBuf := &cappedBuffer{max: execOutputMaxBytes}
	errBuf := &cappedBuffer{max: execOutputMaxBytes}
	done := make(chan error, 1)
	go func() {
		// Without a TTY the stream is multiplexed: demux it.
		_, err := stdcopy.StdCopy(outBuf, errBuf, att.Reader)
		done <- err
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		att.Close()
		return "", "", 0, fmt.Errorf("command did not finish within %s", timeout)
	}
	if err != nil && err != io.EOF {
		return "", "", 0, fmt.Errorf("reading exec output: %w", err)
	}
	// The exit code can lag the stream end by a moment.
	var code int
	err = waitFor(ctx, timeout, 200*time.Millisecond, func(ctx context.Context) (bool, error) {
		insp, err := pd.Client.ContainerExecInspect(ctx, created.ID)
		if err != nil {
			return false, err
		}
		code = insp.ExitCode
		return !insp.Running, nil
	})
	if err != nil {
		return "", "", 0, fmt.Errorf("inspecting exec %s: %w", created.ID, err)
	}
	return outBuf.String(), errBuf.String(), code, nil
}
