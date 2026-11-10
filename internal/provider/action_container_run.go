package provider

// Pass 5.3 Lane A (D2): docker_container_run, the one-shot container run
// that `docker run --rm` performs: create, start, wait for the exit, capture
// the exit code and the output, then remove the container (remove = true by
// default). A one-shot container that should be kept is docker_container
// with state = "exited" (D1). Nothing attaches to the container: the output
// is read after the exit with ContainerLogs.

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*ctrRunAction)(nil)
	_ action.ActionWithValidateConfig = (*ctrRunAction)(nil)
)

// ctrRunDefaultTimeout bounds a run when timeout is unset.
const ctrRunDefaultTimeout = 5 * time.Minute

// NewContainerRunAction returns the docker_container_run action.
func NewContainerRunAction() action.Action { return &ctrRunAction{} }

type ctrRunAction struct{ actBase }

type ctrRunModel struct {
	Image             types.String `tfsdk:"image"`
	Name              types.String `tfsdk:"name"`
	Command           types.List   `tfsdk:"command"`
	Entrypoint        types.List   `tfsdk:"entrypoint"`
	Env               types.Map    `tfsdk:"env"`
	User              types.String `tfsdk:"user"`
	WorkingDir        types.String `tfsdk:"working_dir"`
	Labels            types.Map    `tfsdk:"labels"`
	NetworkMode       types.String `tfsdk:"network_mode"`
	Binds             types.List   `tfsdk:"binds"`
	Timeout           types.String `tfsdk:"timeout"`
	Tail              types.Int64  `tfsdk:"tail"`
	ExpectedExitCodes types.List   `tfsdk:"expected_exit_codes"`
	Remove            types.Bool   `tfsdk:"remove"`
	ResultPath        types.String `tfsdk:"result_path"`
}

func (a *ctrRunAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_run"
}

func (a *ctrRunAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	strList := func(desc string) schema.ListAttribute {
		return schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Runs a one-shot container to completion, like `docker run --rm`: creates it (pulling the image when missing), " +
			"starts it, waits until it exits within `timeout`, reads its exit code and output, then removes it (`remove`, default `true`). " +
			"The result object holds `exit_code`, `stdout` and `stderr` (the last `tail` lines; both sensitive). An exit code outside " +
			"`expected_exit_codes` fails the action. Nothing attaches to the container; the output is read after the exit with " +
			"`ContainerLogs`. To keep the exited container in state instead, use `docker_container` with `state = \"exited\"`.",
		Attributes: map[string]schema.Attribute{
			"image": schema.StringAttribute{Required: true,
				MarkdownDescription: "Image to run, e.g. `busybox:1.36` (`Config.Image`). Pulled with the provider's registry auth when not present locally."},
			"name": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Container name. Unset lets the daemon generate one, so repeated runs never collide."},
			"command":    strList("Command (`Config.Cmd`), exec form, e.g. `[\"sh\", \"-c\", \"echo hi\"]`. Unset uses the image's `CMD`."),
			"entrypoint": strList("Entrypoint override (`Config.Entrypoint`). Unset uses the image's."),
			"env": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Environment variables (`Config.Env`), merged over the image's."},
			"user":        schema.StringAttribute{Optional: true, MarkdownDescription: "User (and optional group) to run as (`Config.User`)."},
			"working_dir": schema.StringAttribute{Optional: true, MarkdownDescription: "Working directory (`Config.WorkingDir`)."},
			"labels": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Container labels (`Config.Labels`). With the provider's `owner` set, the owner label is added."},
			"network_mode": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Network mode (`HostConfig.NetworkMode`): `bridge` (daemon default), `host`, `none`, `container:<name|id>` or a network name."},
			"binds": strList("Volume bindings (`HostConfig.Binds`), `<host path or volume>:<container path>[:ro]`, like `docker run -v`."),
			"timeout": schema.StringAttribute{Optional: true,
				MarkdownDescription: "How long the run may take, as a Go duration (`30s`, `10m`). Default `5m`. On timeout the action fails and " +
					"the container is removed (killed) when `remove` is true."},
			"tail": schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.AtLeast(0)},
				MarkdownDescription: "Lines kept from the end of the output in `stdout` / `stderr` (the Engine's `tail`). Default `1000`."},
			"expected_exit_codes": schema.ListAttribute{Optional: true, ElementType: types.Int64Type,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				MarkdownDescription: "Exit codes that count as success. Default `[0]`."},
			"remove": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Remove the container after the run, also when it fails (`docker run --rm`). Default `true`."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *ctrRunAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m ctrRunModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actDuration(m.Timeout, "timeout", &resp.Diagnostics)
}

// ctrRunConfig builds the create arguments of a run.
func ctrRunConfig(ctx context.Context, pd *providerData, m ctrRunModel, diags *diag.Diagnostics) (*container.Config, *container.HostConfig) {
	cfg := &container.Config{
		Image:      m.Image.ValueString(),
		Cmd:        stringListFrom(ctx, m.Command, diags),
		Entrypoint: stringListFrom(ctx, m.Entrypoint, diags),
		Env:        containerEnvToSlice(stringMapFrom(ctx, m.Env, diags)),
		User:       m.User.ValueString(),
		WorkingDir: m.WorkingDir.ValueString(),
		Labels:     pd.ownerInject(mergeLabels(stringMapFrom(ctx, m.Labels, diags), map[string]string{labelManagedBy: labelManagedByValue})),
	}
	hc := &container.HostConfig{
		NetworkMode: container.NetworkMode(m.NetworkMode.ValueString()),
		Binds:       stringListFrom(ctx, m.Binds, diags),
	}
	return cfg, hc
}

func (a *ctrRunAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m ctrRunModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg, hc := ctrRunConfig(ctx, a.pd, m, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := containerEnsureImage(ctx, a.pd, cfg.Image); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("image"), "docker_container_run: cannot pull image", fmt.Sprintf("%s: %v", cfg.Image, err))
		return
	}
	created, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hc, Name: m.Name.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("docker_container_run: cannot create container", err.Error())
		return
	}
	if actBool(m.Remove, true) {
		defer func() {
			// A fresh context: the run's may have timed out.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if _, err := c.ContainerRemove(rctx, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
				resp.Diagnostics.AddWarning("docker_container_run: cannot remove container", fmt.Sprintf("%s: %v", copShortID(created.ID), err))
			}
		}()
	}
	timeout := actTimeout(m.Timeout, ctrRunDefaultTimeout)
	actProgress(ctx, resp, "running %s in container %s", cfg.Image, copShortID(created.ID))
	run, err := ctrStartAndWaitExit(ctx, a.pd, created.ID, timeout, logTailValue(m.Tail))
	if err != nil {
		resp.Diagnostics.AddError("docker_container_run failed", fmt.Sprintf("image %s: %v", cfg.Image, err))
		return
	}
	actFinishResult(ctx, resp, a.pd, "engine:container-run", map[string]any{
		"container_id": created.ID,
		"result":       map[string]any{"exit_code": run.ExitCode, "stdout": run.Stdout, "stderr": run.Stderr},
	}, m.ResultPath)
	if ok, want := ctrExitCodeExpected(ctx, m.ExpectedExitCodes, run.ExitCode); !ok {
		resp.Diagnostics.AddError("docker_container_run: unexpected exit code",
			fmt.Sprintf("%s exited with code %d; expected_exit_codes is %v.%s", cfg.Image, run.ExitCode, want, ctrLogTailText(run.Entries, 20)))
	}
}
