package provider

// Pass 5 Lane A (WP img): action docker_image_commit (POST /commit).

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*imgCommitAction)(nil)
	_ action.ActionWithValidateConfig = (*imgCommitAction)(nil)
)

// NewImageCommitAction returns the docker_image_commit action.
func NewImageCommitAction() action.Action { return &imgCommitAction{} }

type imgCommitAction struct{ actBase }

type imgCommitModel struct {
	ContainerID types.String          `tfsdk:"container_id"`
	Reference   types.String          `tfsdk:"reference"`
	Author      types.String          `tfsdk:"author"`
	Message     types.String          `tfsdk:"message"`
	Changes     types.List            `tfsdk:"changes"`
	Pause       types.Bool            `tfsdk:"pause"`
	Config      *imgCommitConfigModel `tfsdk:"config"`
	ResultPath  types.String          `tfsdk:"result_path"`
}

// imgCommitConfigModel is the ImageCommit body (ContainerConfig), named
// after its upstream fields in snake_case.
type imgCommitConfigModel struct {
	Hostname        types.String               `tfsdk:"hostname"`
	Domainname      types.String               `tfsdk:"domainname"`
	User            types.String               `tfsdk:"user"`
	AttachStdin     types.Bool                 `tfsdk:"attach_stdin"`
	AttachStdout    types.Bool                 `tfsdk:"attach_stdout"`
	AttachStderr    types.Bool                 `tfsdk:"attach_stderr"`
	ExposedPorts    types.Set                  `tfsdk:"exposed_ports"`
	Tty             types.Bool                 `tfsdk:"tty"`
	OpenStdin       types.Bool                 `tfsdk:"open_stdin"`
	StdinOnce       types.Bool                 `tfsdk:"stdin_once"`
	Env             types.List                 `tfsdk:"env"`
	Cmd             types.List                 `tfsdk:"cmd"`
	Healthcheck     *containerHealthcheckModel `tfsdk:"healthcheck"`
	ArgsEscaped     types.Bool                 `tfsdk:"args_escaped"`
	Image           types.String               `tfsdk:"image"`
	Volumes         types.Set                  `tfsdk:"volumes"`
	WorkingDir      types.String               `tfsdk:"working_dir"`
	Entrypoint      types.List                 `tfsdk:"entrypoint"`
	NetworkDisabled types.Bool                 `tfsdk:"network_disabled"`
	OnBuild         types.List                 `tfsdk:"on_build"`
	Labels          types.Map                  `tfsdk:"labels"`
	StopSignal      types.String               `tfsdk:"stop_signal"`
	StopTimeout     types.Int64                `tfsdk:"stop_timeout"`
	Shell           types.List                 `tfsdk:"shell"`
}

// imgCommitConfigSchema is the `config` attribute of docker_image_commit.
func imgCommitConfigSchema() schema.SingleNestedAttribute {
	str := func(d string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: d}
	}
	boo := func(d string) schema.BoolAttribute {
		return schema.BoolAttribute{Optional: true, MarkdownDescription: d}
	}
	lst := func(d string) schema.ListAttribute {
		return schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: d}
	}
	return schema.SingleNestedAttribute{
		Optional: true,
		MarkdownDescription: "Image configuration sent as the commit body (`ContainerConfig`), the API-level counterpart of `changes`. " +
			"When set, the daemon starts from this object instead of the container's configuration and fills `user`, `exposed_ports`, " +
			"`env` (merged by key), `labels` (merged), `cmd`/`entrypoint`, `healthcheck`, `working_dir`, `volumes` (merged) and " +
			"`stop_signal` from the container where they are unset; `on_build` and `shell` are not inherited. `changes` are applied on " +
			"top. Without it the container's configuration is used, like `docker container commit`.",
		Attributes: map[string]schema.Attribute{
			"hostname":      str("Hostname recorded in the image configuration (`Hostname`)."),
			"domainname":    str("Domain name recorded in the image configuration (`Domainname`)."),
			"user":          str("User (`user[:group]`, names or IDs) that commands run as (`User`)."),
			"attach_stdin":  boo("Attach to `stdin` by default (`AttachStdin`)."),
			"attach_stdout": boo("Attach to `stdout` by default (`AttachStdout`)."),
			"attach_stderr": boo("Attach to `stderr` by default (`AttachStderr`)."),
			"exposed_ports": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Exposed ports as `<port>/<tcp|udp|sctp>`, e.g. `80/tcp` (`ExposedPorts`)."},
			"tty":        boo("Allocate a TTY for containers of the image (`Tty`)."),
			"open_stdin": boo("Keep `stdin` open (`OpenStdin`)."),
			"stdin_once": boo("Close `stdin` after the first attached client disconnects (`StdinOnce`)."),
			"env":        lst("Environment as `KEY=value` strings (`Env`)."),
			"cmd":        lst("Default command (`Cmd`)."),
			"healthcheck": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Healthcheck of the image (`Healthcheck`). Durations are Go durations such as `30s`.",
				Attributes: map[string]schema.Attribute{
					"test":           lst("Test command: `[\"CMD\", ...]`, `[\"CMD-SHELL\", \"...\"]` or `[\"NONE\"]` (`Test`)."),
					"interval":       str("Time between checks (`Interval`)."),
					"timeout":        str("Time before a check is considered hung (`Timeout`)."),
					"start_period":   str("Start period before failures count (`StartPeriod`)."),
					"start_interval": str("Time between checks during the start period (`StartInterval`)."),
					"retries":        schema.Int64Attribute{Optional: true, MarkdownDescription: "Consecutive failures before `unhealthy` (`Retries`)."},
				},
			},
			"args_escaped": boo("The command is already escaped (`ArgsEscaped`, Windows images only)."),
			"image":        str("Image name recorded in the configuration (`Image`)."),
			"volumes": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Mount point paths declared as volumes (`Volumes`)."},
			"working_dir":      str("Working directory (`WorkingDir`)."),
			"entrypoint":       lst("Entrypoint (`Entrypoint`)."),
			"network_disabled": boo("Disable networking for containers of the image (`NetworkDisabled`)."),
			"on_build":         lst("`ONBUILD` instructions (`OnBuild`)."),
			"labels": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Labels of the image (`Labels`)."},
			"stop_signal":  str("Signal that stops containers of the image, e.g. `SIGTERM` (`StopSignal`)."),
			"stop_timeout": schema.Int64Attribute{Optional: true, MarkdownDescription: "Seconds to wait before killing on stop (`StopTimeout`)."},
			"shell":        lst("Shell for the shell form of `RUN`, `CMD` and `ENTRYPOINT` (`Shell`)."),
		},
	}
}

// imgCommitExpandConfig builds the commit body; nil when m is nil.
func imgCommitExpandConfig(ctx context.Context, m *imgCommitConfigModel, diags *diag.Diagnostics) *container.Config {
	if m == nil {
		return nil
	}
	at := path.Root("config")
	cfg := &container.Config{
		Hostname:        m.Hostname.ValueString(),
		Domainname:      m.Domainname.ValueString(),
		User:            m.User.ValueString(),
		AttachStdin:     m.AttachStdin.ValueBool(),
		AttachStdout:    m.AttachStdout.ValueBool(),
		AttachStderr:    m.AttachStderr.ValueBool(),
		Tty:             m.Tty.ValueBool(),
		OpenStdin:       m.OpenStdin.ValueBool(),
		StdinOnce:       m.StdinOnce.ValueBool(),
		Env:             stringListFrom(ctx, m.Env, diags),
		Cmd:             stringListFrom(ctx, m.Cmd, diags),
		ArgsEscaped:     m.ArgsEscaped.ValueBool(),
		Image:           m.Image.ValueString(),
		WorkingDir:      m.WorkingDir.ValueString(),
		Entrypoint:      stringListFrom(ctx, m.Entrypoint, diags),
		NetworkDisabled: m.NetworkDisabled.ValueBool(),
		OnBuild:         stringListFrom(ctx, m.OnBuild, diags),
		Labels:          stringMapFrom(ctx, m.Labels, diags),
		StopSignal:      m.StopSignal.ValueString(),
		Shell:           stringListFrom(ctx, m.Shell, diags),
	}
	if ports := stringSetFrom(ctx, m.ExposedPorts, diags); len(ports) > 0 {
		cfg.ExposedPorts = network.PortSet{}
		for _, s := range ports {
			p, err := network.ParsePort(s)
			if err != nil {
				diags.AddAttributeError(at.AtName("exposed_ports"), "Invalid exposed port", fmt.Sprintf("%q: %v", s, err))
				continue
			}
			cfg.ExposedPorts[p] = struct{}{}
		}
	}
	if vols := stringSetFrom(ctx, m.Volumes, diags); len(vols) > 0 {
		cfg.Volumes = map[string]struct{}{}
		for _, v := range vols {
			cfg.Volumes[v] = struct{}{}
		}
	}
	if v, ok := knownInt64(m.StopTimeout); ok {
		n := int(v)
		cfg.StopTimeout = &n
	}
	if m.Healthcheck != nil {
		test := stringListFrom(ctx, m.Healthcheck.Test, diags)
		hc, err := containerExpandHealthcheck(test, m.Healthcheck)
		if err != nil {
			diags.AddAttributeError(at.AtName("healthcheck"), "Invalid healthcheck", err.Error())
		}
		cfg.Healthcheck = hc
	}
	return cfg
}

// imgCommitConfigKnown reports whether the values ValidateConfig checks
// (exposed ports, healthcheck durations) are all known.
func imgCommitConfigKnown(m *imgCommitConfigModel) bool {
	if m.ExposedPorts.IsUnknown() {
		return false
	}
	if h := m.Healthcheck; h != nil {
		for _, v := range []types.String{h.Interval, h.Timeout, h.StartPeriod, h.StartInterval} {
			if v.IsUnknown() {
				return false
			}
		}
	}
	return true
}

func (a *imgCommitAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_commit"
}

func (a *imgCommitAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates an image from a container's filesystem changes, like `docker container commit` (`POST /commit`). " +
			"The new image is not tracked in state. The result object holds `id`, `image_ref`, `container_id` and `result.changes` " +
			"(the filesystem changes that went into the image, from `GET /containers/{id}/changes` just before the commit). " +
			"With the provider's `owner` set, the new image's labels include the owner label.",
		Attributes: map[string]schema.Attribute{
			"container_id": schema.StringAttribute{Required: true, MarkdownDescription: "The container (ID or name) to commit."},
			"reference": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Reference to tag the new image with, `repository[:tag]` (no tag means `latest`). Without it the image is untagged.",
			},
			"author":  schema.StringAttribute{Optional: true, MarkdownDescription: "Author recorded in the image (`--author`)."},
			"message": schema.StringAttribute{Optional: true, MarkdownDescription: "Commit message recorded in the image history (`--message`)."},
			"changes": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Dockerfile instructions applied to the image config, e.g. `[\"ENV A=b\", \"EXPOSE 80\"]` (`--change`).",
			},
			"pause":       schema.BoolAttribute{Optional: true, MarkdownDescription: "Pause the container during the commit (`--pause`). Default `true`."},
			"config":      imgCommitConfigSchema(),
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *imgCommitAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m imgCommitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r, ok := knownString(m.Reference); ok && r != "" {
		if _, err := tagNormalizeTarget(r); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("reference"), "Invalid reference", err.Error())
		}
	}
	if m.Config != nil && imgCommitConfigKnown(m.Config) {
		var d diag.Diagnostics
		imgCommitExpandConfig(ctx, m.Config, &d)
		resp.Diagnostics.Append(d.Errors()...)
	}
}

func (a *imgCommitAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m imgCommitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	changes := stringListFrom(ctx, m.Changes, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctr := m.ContainerID.ValueString()
	insp, err := c.ContainerInspect(ctx, ctr, client.ContainerInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("container_id"), "docker_image_commit: cannot find container", err.Error())
		return
	}
	containerID := insp.Container.ID
	var fsChanges []map[string]any
	if diff, err := c.ContainerDiff(ctx, containerID, client.ContainerDiffOptions{}); err == nil {
		for _, ch := range diff.Changes {
			fsChanges = append(fsChanges, map[string]any{"path": ch.Path, "kind": int64(ch.Kind)})
		}
		sort.SliceStable(fsChanges, func(i, j int) bool { return fsChanges[i]["path"].(string) < fsChanges[j]["path"].(string) })
	} else {
		resp.Diagnostics.AddWarning("docker_image_commit: cannot list container changes", err.Error())
	}
	cfg := imgCommitExpandConfig(ctx, m.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// Owner label (inject-only): into the body's Labels when `config` is
	// set, else as a LABEL change so the container's configuration is kept.
	if cfg != nil {
		cfg.Labels = a.pd.ownerInject(cfg.Labels)
	} else {
		changes = rOwnerLabelChanges(a.pd, changes)
	}
	ref := m.Reference.ValueString()
	actProgress(ctx, resp, "committing container %s", ctr)
	res, err := c.ContainerCommit(ctx, containerID, client.ContainerCommitOptions{
		Config:    cfg,
		Reference: ref,
		Comment:   m.Message.ValueString(),
		Author:    m.Author.ValueString(),
		Changes:   changes,
		NoPause:   !actBool(m.Pause, true),
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_image_commit failed", err.Error())
		return
	}
	actProgress(ctx, resp, "committed %s as %s", ctr, res.ID)
	if fsChanges == nil {
		fsChanges = []map[string]any{}
	}
	values := map[string]any{
		"id":           res.ID,
		"container_id": containerID,
		"result":       map[string]any{"changes": fsChanges},
	}
	if ref != "" {
		values["image_ref"] = imgNormalizedRef(ref)
	}
	actFinishResult(ctx, resp, a.pd, "engine:image-commit", values, m.ResultPath)
}
