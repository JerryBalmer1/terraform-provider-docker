package provider

// Pass 3 Lane C: docker_buildx_builder, docker-container driver only.
//
// What buildx does for `docker buildx create --driver docker-container
// --name X --bootstrap`, and what of it is reachable through the Engine API:
//   - a privileged container `buildx_buildkit_X0` from moby/buildkit, with a
//     named state volume `buildx_buildkit_X0_state` on /var/lib/buildkit,
//     buildkitd flags as the command and buildkitd.toml copied to
//     /etc/buildkit: all Engine API (ContainerCreate, VolumeCreate,
//     CopyToContainer, ContainerStart). Implemented.
//   - the BuildKit endpoint: buildx dials it with `buildctl dial-stdio`
//     inside the container over an exec attach (Engine API exec + hijack).
//     The provider does the same for its readiness check (ListWorkers
//     through github.com/moby/buildkit/client). Implemented.
//   - the builder *instance* record in ~/.docker/buildx/instances/X (the
//     list `docker buildx ls` shows, `--use`, `--append` nodes, node
//     platforms): buildx-private local files, no Engine API. Not
//     implemented (see the [BLOCKED] log line); the container and volume
//     names match buildx's, so `docker buildx create --name X --driver
//     docker-container` afterwards reuses them.
//   - kubernetes and remote drivers: not Engine API objects. Not implemented.

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	bkclient "github.com/moby/buildkit/client"
)

var (
	_ resource.Resource                   = (*buildxBuilderResource)(nil)
	_ resource.ResourceWithConfigure      = (*buildxBuilderResource)(nil)
	_ resource.ResourceWithImportState    = (*buildxBuilderResource)(nil)
	_ resource.ResourceWithValidateConfig = (*buildxBuilderResource)(nil)
)

const (
	bxbDriverDockerContainer = "docker-container"
	bxbDefaultImage          = "moby/buildkit:buildx-stable-1"
	bxbDefaultFlags          = "--allow-insecure-entitlement=network.host"
	bxbStateDir              = "/var/lib/buildkit"
	bxbConfigDir             = "etc/buildkit"
	bxbConfigFile            = "buildkitd.toml"
	bxbSocket                = "unix:///run/buildkit/buildkitd.sock"
	bxbTCPContainerPort      = 1234
	bxbContainerPrefix       = "buildx_buildkit_"
	bxbLabelBuilder          = "io.terraform.provider.buildx-builder"
)

// bxbBootstrapTimeout bounds the readiness wait; a var so tests can shorten it.
var bxbBootstrapTimeout = 2 * time.Minute

// bxbReady is the readiness probe (ListWorkers over an exec-attached
// `buildctl dial-stdio`); tests replace it.
var bxbReady = bxbListWorkers

var bxbNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// NewBuildxBuilderResource is the docker_buildx_builder constructor.
func NewBuildxBuilderResource() resource.Resource { return &buildxBuilderResource{} }

type buildxBuilderResource struct {
	pd *providerData
}

type bxbModel struct {
	ID                    types.String       `tfsdk:"id"`
	Name                  types.String       `tfsdk:"name"`
	Driver                types.String       `tfsdk:"driver"`
	BuildkitFlags         types.String       `tfsdk:"buildkit_flags"`
	BuildkitConfig        types.String       `tfsdk:"buildkit_config"`
	BuildkitConfigContent types.String       `tfsdk:"buildkit_config_content"`
	Bootstrap             types.Bool         `tfsdk:"bootstrap"`
	KeepState             types.Bool         `tfsdk:"keep_state"`
	TCPPort               types.Int64        `tfsdk:"tcp_port"`
	Endpoint              types.String       `tfsdk:"endpoint"`
	TCPAddress            types.String       `tfsdk:"tcp_address"`
	ContainerName         types.String       `tfsdk:"container_name"`
	VolumeName            types.String       `tfsdk:"volume_name"`
	BuildkitVersion       types.String       `tfsdk:"buildkit_version"`
	Platforms             types.List         `tfsdk:"platforms"`
	DockerContainer       *bxbContainerModel `tfsdk:"docker_container"`
}

type bxbContainerModel struct {
	Image         types.String `tfsdk:"image"`
	Network       types.String `tfsdk:"network"`
	Env           types.Map    `tfsdk:"env"`
	RestartPolicy types.String `tfsdk:"restart_policy"`
	Memory        types.Int64  `tfsdk:"memory"`
}

func (r *buildxBuilderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_builder"
}

func (r *buildxBuilderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A BuildKit builder run by the daemon as a `docker-container` driver node, the same container and state volume " +
			"`docker buildx create --driver docker-container --bootstrap` makes, created through the Engine API without the buildx CLI. " +
			"The container `buildx_buildkit_<name>0` runs `moby/buildkit` privileged with the state volume `buildx_buildkit_<name>0_state`; " +
			"readiness is checked by listing BuildKit workers over `buildctl dial-stdio` in the container. Use `endpoint` with " +
			"`buildctl --addr`, or `tcp_address` when `tcp_port` is set. The buildx CLI's own instance list (`~/.docker/buildx/instances`) " +
			"is not written, so `docker buildx ls` does not show the builder until `docker buildx create --name <name> --driver docker-container` " +
			"is run, which adopts the same container. Only the `docker-container` driver is supported. Every argument except `keep_state` " +
			"forces a new resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "ID of the BuildKit container.", PlanModifiers: keep,
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Builder name, as for `docker buildx create --name`. Changing this forces a new resource.",
				PlanModifiers:       rr,
				Validators:          []validator.String{stringvalidator.RegexMatches(bxbNameRe, "letters, digits, '_', '.' and '-', starting with a letter or digit")},
			},
			"driver": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(bxbDriverDockerContainer),
				MarkdownDescription: "Builder driver. Only `docker-container` (default) is supported: `docker` is the daemon's built-in builder " +
					"(use `docker_image` with `build.builder = \"buildkit\"`), and `kubernetes`/`remote` are not Engine API objects. Forces replacement.",
				PlanModifiers: rr,
				Validators:    []validator.String{stringvalidator.OneOf(bxbDriverDockerContainer)},
			},
			"buildkit_flags": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Flags for buildkitd (`--buildkitd-flags`), whitespace-separated with no shell quoting, e.g. " +
					"`\"--debug --allow-insecure-entitlement=security.insecure\"`. Default `" + bxbDefaultFlags + "`, like buildx. " +
					"Changing this forces a new resource.",
				PlanModifiers: rr,
			},
			"buildkit_config": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Path of a local `buildkitd.toml` (`--buildkitd-config`), read at apply and copied to " +
					"`/etc/buildkit/buildkitd.toml` in the container. Conflicts with `buildkit_config_content`. Changing this forces a new resource.",
				PlanModifiers: rr,
				Validators:    []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("buildkit_config_content"))},
			},
			"buildkit_config_content": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "`buildkitd.toml` content, copied like `buildkit_config`. Changing this forces a new resource.",
				PlanModifiers:       rr,
			},
			"bootstrap": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Start the builder and wait until BuildKit lists its workers (`--bootstrap`). With `false` the container is " +
					"only created. Default `true`. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"keep_state": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Keep the state volume (build cache) on destroy (`docker buildx rm --keep-state`). Default `false`. Updated in place.",
			},
			"tcp_port": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: fmt.Sprintf("Also serve BuildKit on TCP: buildkitd listens on `tcp://0.0.0.0:%d` in the container and the port "+
					"is published on this host port. Unauthenticated and unencrypted; bind it only on trusted networks. Changing this forces a new resource.", bxbTCPContainerPort),
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"endpoint": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "BuildKit address for `buildctl --addr`: `docker-container://buildx_buildkit_<name>0`.",
			},
			"tcp_address": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "`tcp://<daemon host>:<tcp_port>` when `tcp_port` is set: the host of the provider's `tcp://`/`ssh://` " +
					"daemon address, else `127.0.0.1`. Null without `tcp_port`.",
			},
			"container_name": schema.StringAttribute{
				Computed: true, PlanModifiers: keep, MarkdownDescription: "`buildx_buildkit_<name>0`.",
			},
			"volume_name": schema.StringAttribute{
				Computed: true, PlanModifiers: keep, MarkdownDescription: "`buildx_buildkit_<name>0_state`, mounted on `/var/lib/buildkit`.",
			},
			"buildkit_version": schema.StringAttribute{
				Computed: true, PlanModifiers: keep,
				MarkdownDescription: "BuildKit version the worker reported at bootstrap. Null when `bootstrap` is false.",
			},
			"platforms": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Platforms the BuildKit worker supports, e.g. `[\"linux/amd64\", \"linux/386\"]`. Empty when `bootstrap` is false.",
			},
		},
		Blocks: map[string]schema.Block{
			"docker_container": schema.SingleNestedBlock{
				MarkdownDescription: "Settings for the BuildKit container (`--driver-opt`). Changing anything here forces a new resource.",
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"image": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "BuildKit image (`image=`). Default `" + bxbDefaultImage + "`. Pulled with `registry_auth` when missing.",
					},
					"network": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Network mode or network name for the container (`network=`), e.g. `host`. Default: the daemon's bridge.",
					},
					"env": schema.MapAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Environment variables for buildkitd (`env.<KEY>=`), e.g. proxy settings.",
					},
					"restart_policy": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Restart policy (`restart-policy=`): `no`, `always`, `unless-stopped` (default, like buildx) or `on-failure`.",
						Validators:          []validator.String{stringvalidator.OneOf(containerRestartPolicies...)},
					},
					"memory": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "Memory limit in bytes (`memory=`).",
					},
				},
			},
		},
	}
}

func (r *buildxBuilderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *buildxBuilderResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m bxbModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if p, ok := knownInt64(m.TCPPort); ok && (p < 1 || p > 65535) {
		resp.Diagnostics.AddAttributeError(path.Root("tcp_port"), "Invalid tcp_port", "tcp_port must be 1-65535.")
	}
}

func bxbContainerName(name string) string { return bxbContainerPrefix + name + "0" }
func bxbVolumeName(name string) string    { return bxbContainerName(name) + "_state" }

// bxbFlags returns the buildkitd arguments.
func bxbFlags(m bxbModel) []string {
	flags := bxbDefaultFlags
	if v, ok := knownString(m.BuildkitFlags); ok {
		flags = v
	}
	out := strings.Fields(flags)
	if containerKnown(m.TCPPort) {
		out = append(out, "--addr", bxbSocket, "--addr", fmt.Sprintf("tcp://0.0.0.0:%d", bxbTCPContainerPort))
	}
	return out
}

// bxbConfigBytes returns buildkitd.toml content, or nil.
func bxbConfigBytes(m bxbModel) ([]byte, error) {
	if v, ok := knownString(m.BuildkitConfigContent); ok {
		return []byte(v), nil
	}
	if p, ok := knownString(m.BuildkitConfig); ok && p != "" {
		b, err := os.ReadFile(expandHome(p))
		if err != nil {
			return nil, fmt.Errorf("reading buildkit_config: %w", err)
		}
		return b, nil
	}
	return nil, nil
}

// bxbConfigTar packs buildkitd.toml as etc/buildkit/buildkitd.toml, with the
// directory entry, for extraction at "/".
func bxbConfigTar(cfg []byte, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: bxbConfigDir + "/", Mode: 0o755, ModTime: now}); err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: bxbConfigDir + "/" + bxbConfigFile, Mode: 0o644, Size: int64(len(cfg)), ModTime: now}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(cfg); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// bxbBuildCreate turns the plan into ContainerCreate arguments.
func bxbBuildCreate(ctx context.Context, m bxbModel, diags *diag.Diagnostics) (*container.Config, *container.HostConfig) {
	name := m.Name.ValueString()
	dc := m.DockerContainer
	if dc == nil {
		dc = &bxbContainerModel{}
	}
	img := bxbDefaultImage
	if v, ok := knownString(dc.Image); ok && v != "" {
		img = v
	}
	cfg := &container.Config{
		Image:  img,
		Cmd:    bxbFlags(m),
		Env:    containerEnvToSlice(stringMapFrom(ctx, dc.Env, diags)),
		Labels: map[string]string{labelManagedBy: labelManagedByValue, bxbLabelBuilder: name},
	}
	rp := "unless-stopped"
	if v, ok := knownString(dc.RestartPolicy); ok && v != "" {
		rp = v
	}
	useInit := true
	hc := &container.HostConfig{
		Privileged:    true,
		Init:          &useInit,
		RestartPolicy: containerRestartPolicy(rp, 0),
		Mounts:        []mount.Mount{{Type: mount.TypeVolume, Source: bxbVolumeName(name), Target: bxbStateDir}},
	}
	if v, ok := knownString(dc.Network); ok && v != "" {
		hc.NetworkMode = container.NetworkMode(v)
	}
	if v, ok := knownInt64(dc.Memory); ok {
		hc.Memory = v
	}
	if p, ok := knownInt64(m.TCPPort); ok {
		port := nat.Port(fmt.Sprintf("%d/tcp", bxbTCPContainerPort))
		cfg.ExposedPorts = nat.PortSet{port: {}}
		hc.PortBindings = nat.PortMap{port: {{HostPort: strconv.FormatInt(p, 10)}}}
	}
	return cfg, hc
}

// bxbTCPHost is the host part of tcp_address: the provider daemon's host
// for tcp:// and ssh:// endpoints, else 127.0.0.1.
func bxbTCPHost(dockerHost string) string {
	u, err := url.Parse(dockerHost)
	if err == nil && (u.Scheme == "tcp" || u.Scheme == "ssh" || u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" {
		return u.Hostname()
	}
	return "127.0.0.1"
}

// ---------------------------------------------------------------------------
// BuildKit connection through the Engine API

// bxbDemuxConn reads the stdcopy-multiplexed output of a non-TTY exec
// attach: stdout is the BuildKit byte stream, stderr is dropped.
type bxbDemuxConn struct {
	net.Conn
	r *io.PipeReader
}

func (c *bxbDemuxConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func bxbDemux(conn net.Conn, src io.Reader) net.Conn {
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, io.Discard, src)
		_ = pw.CloseWithError(err)
	}()
	return &bxbDemuxConn{Conn: conn, r: pr}
}

// bxbDialStdio runs `buildctl dial-stdio` in the container and returns the
// attached stream as a connection to buildkitd, as buildx's docker-container
// driver does.
func bxbDialStdio(ctx context.Context, pd *providerData, containerID string) (net.Conn, error) {
	ex, err := pd.Client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{"buildctl", "dial-stdio"}, AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}
	hj, err := pd.Client.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	var src io.Reader = hj.Conn
	if hj.Reader != nil {
		src = hj.Reader // may hold bytes read past the HTTP upgrade
	}
	return bxbDemux(hj.Conn, src), nil
}

// bxbListWorkers waits until buildkitd answers ListWorkers and returns the
// first worker.
func bxbListWorkers(ctx context.Context, pd *providerData, containerID string) (*bkclient.WorkerInfo, error) {
	var info *bkclient.WorkerInfo
	err := waitFor(ctx, bxbBootstrapTimeout, time.Second, func(ctx context.Context) (bool, error) {
		insp, err := pd.Client.ContainerInspect(ctx, containerID)
		if err != nil {
			return false, err
		}
		if insp.State != nil && !insp.State.Running && !insp.State.Restarting && (insp.State.Status == "exited" || insp.State.Status == "dead") {
			return false, fmt.Errorf("buildkit container exited (code %d): %s", insp.State.ExitCode, insp.State.Error)
		}
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		c, err := bkclient.New(dialCtx, "", bkclient.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return bxbDialStdio(ctx, pd, containerID)
		}))
		if err != nil {
			return false, nil
		}
		defer c.Close()
		ws, err := c.ListWorkers(dialCtx)
		if err != nil || len(ws) == 0 {
			return false, nil // not up yet
		}
		info = ws[0]
		return true, nil
	})
	return info, err
}

// ---------------------------------------------------------------------------
// CRUD

func (r *buildxBuilderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bxbModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	name := plan.Name.ValueString()
	cfgBytes, err := bxbConfigBytes(plan)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("buildkit_config"), "Cannot read buildkit_config", err.Error())
		return
	}
	cfg, hc := bxbBuildCreate(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := containerEnsureImage(ctx, r.pd, cfg.Image); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("docker_container").AtName("image"), "Cannot pull BuildKit image", fmt.Sprintf("%s: %v", cfg.Image, err))
		return
	}
	vol := bxbVolumeName(name)
	if _, err := r.pd.Client.VolumeCreate(ctx, volume.CreateOptions{Name: vol, Labels: cfg.Labels}); err != nil {
		resp.Diagnostics.AddError("Cannot create BuildKit state volume", fmt.Sprintf("%s: %v", vol, err))
		return
	}
	cname := bxbContainerName(name)
	created, err := r.pd.Client.ContainerCreate(ctx, cfg, hc, nil, nil, cname)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create BuildKit container", fmt.Sprintf("%s: %v", cname, err))
		return
	}
	plan.ID = types.StringValue(created.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), plan.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("keep_state"), plan.KeepState)...)

	if cfgBytes != nil {
		tb, err := bxbConfigTar(cfgBytes, time.Now())
		if err == nil {
			err = r.pd.Client.CopyToContainer(ctx, created.ID, "/", bytes.NewReader(tb), container.CopyToContainerOptions{})
		}
		if err != nil {
			resp.Diagnostics.AddError("Cannot copy buildkitd.toml into the builder", err.Error())
			return
		}
	}

	plan.ContainerName = types.StringValue(cname)
	plan.VolumeName = types.StringValue(vol)
	plan.Endpoint = types.StringValue("docker-container://" + cname)
	plan.TCPAddress = types.StringNull()
	if p, ok := knownInt64(plan.TCPPort); ok {
		plan.TCPAddress = types.StringValue(fmt.Sprintf("tcp://%s:%d", bxbTCPHost(r.pd.Endpoint.Host), p))
	}
	plan.BuildkitVersion = types.StringNull()
	plan.Platforms = types.ListValueMust(types.StringType, nil)

	if plan.Bootstrap.ValueBool() {
		if err := r.pd.Client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
			resp.Diagnostics.AddError("Cannot start BuildKit container", err.Error())
			return
		}
		w, err := bxbReady(ctx, r.pd, created.ID)
		if err != nil {
			resp.Diagnostics.AddError("BuildKit did not become ready", fmt.Sprintf("%s: %v", cname, err))
			return
		}
		bxbApplyWorker(&plan, w)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func bxbApplyWorker(m *bxbModel, w *bkclient.WorkerInfo) {
	if w == nil {
		return
	}
	m.BuildkitVersion = stringOrNull(w.BuildkitVersion.Version)
	ps := make([]string, 0, len(w.Platforms))
	for _, p := range w.Platforms {
		s := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			s += "/" + p.Variant
		}
		ps = append(ps, s)
	}
	m.Platforms = stringListValue(ps, false)
}

func (r *buildxBuilderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bxbModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	insp, err := r.pd.Client.ContainerInspect(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect BuildKit container", err.Error())
		return
	}
	bxbFromInspect(&state, insp, r.pd.Endpoint.Host)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// bxbFromInspect fills computed and (on import) configured values from the
// container.
func bxbFromInspect(m *bxbModel, insp container.InspectResponse, dockerHost string) {
	if insp.ContainerJSONBase == nil {
		return
	}
	m.ID = types.StringValue(insp.ID)
	cname := strings.TrimPrefix(insp.Name, "/")
	m.ContainerName = types.StringValue(cname)
	m.Endpoint = types.StringValue("docker-container://" + cname)
	if !containerKnown(m.Name) {
		name := strings.TrimSuffix(strings.TrimPrefix(cname, bxbContainerPrefix), "0")
		if insp.Config != nil && insp.Config.Labels[bxbLabelBuilder] != "" {
			name = insp.Config.Labels[bxbLabelBuilder]
		}
		m.Name = types.StringValue(name)
	}
	vol := bxbVolumeName(m.Name.ValueString())
	if insp.HostConfig != nil {
		for _, mt := range insp.HostConfig.Mounts {
			if mt.Target == bxbStateDir && mt.Source != "" {
				vol = mt.Source
			}
		}
	}
	m.VolumeName = types.StringValue(vol)
	if !containerKnown(m.Driver) {
		m.Driver = types.StringValue(bxbDriverDockerContainer)
	}
	if !containerKnown(m.Bootstrap) {
		m.Bootstrap = types.BoolValue(true)
	}
	if !containerKnown(m.KeepState) {
		m.KeepState = types.BoolValue(false)
	}
	if !containerKnown(m.Platforms) {
		m.Platforms = types.ListValueMust(types.StringType, nil)
	}
	if m.BuildkitVersion.IsUnknown() {
		m.BuildkitVersion = types.StringNull()
	}
	if m.TCPAddress.IsUnknown() {
		m.TCPAddress = types.StringNull()
		if p, ok := knownInt64(m.TCPPort); ok {
			m.TCPAddress = types.StringValue(fmt.Sprintf("tcp://%s:%d", bxbTCPHost(dockerHost), p))
		}
	}
}

func (r *buildxBuilderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Only keep_state changes in place; it is used at destroy.
	var plan bxbModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *buildxBuilderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bxbModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	err := r.pd.Client.ContainerRemove(ctx, state.ID.ValueString(), container.RemoveOptions{Force: true, RemoveVolumes: false})
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove BuildKit container", err.Error())
		return
	}
	if state.KeepState.ValueBool() {
		return
	}
	vol := state.VolumeName.ValueString()
	if vol == "" {
		vol = bxbVolumeName(state.Name.ValueString())
	}
	if err := r.pd.Client.VolumeRemove(ctx, vol, true); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove BuildKit state volume", fmt.Sprintf("%s: %v", vol, err))
	}
}

// ImportState accepts the builder name or the container name/ID.
func (r *buildxBuilderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "Cannot import before the provider is configured.")
		return
	}
	insp, err := r.pd.Client.ContainerInspect(ctx, req.ID)
	if isNotFound(err) {
		insp, err = r.pd.Client.ContainerInspect(ctx, bxbContainerName(req.ID))
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot find BuildKit container", fmt.Sprintf("%q: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), insp.ID)...)
}
