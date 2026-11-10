package provider

// Pass 3 Lane C: docker_buildx_builder, docker-container driver (Pass 5.3
// Lane B: the remote driver, buildx_builder_remote.go, and the owner
// label on the node container and its state volume).
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
//   - remote driver: implemented (buildx_builder_remote.go).
//   - kubernetes driver: buildx creates a Deployment through the Kubernetes
//     API (k8s.io/client-go), which is not a dependency of this provider
//     and is not reachable through the Engine API or BuildKit.
//
// PARTIAL(pass5) buildx:builder the kubernetes driver needs the Kubernetes API (k8s.io/client-go, not a dependency); the buildx instance store (`docker buildx ls`, `--use`) is buildx-private client files (github.com/docker/buildx/store, not a dependency), not BuildKit or Engine API

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

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                   = (*buildxBuilderResource)(nil)
	_ resource.ResourceWithConfigure      = (*buildxBuilderResource)(nil)
	_ resource.ResourceWithImportState    = (*buildxBuilderResource)(nil)
	_ resource.ResourceWithValidateConfig = (*buildxBuilderResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*buildxBuilderResource)(nil)
)

const (
	bxbItemID                = "buildx:builder"
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
	// bxbLabelPlatforms holds the `platform` pins (comma-separated);
	// buildx keeps them in its client-side store, the provider on the node.
	bxbLabelPlatforms = "io.terraform.provider.buildx-builder.platforms"
	// bxbLabelDefaultLoad is the default-load driver option.
	bxbLabelDefaultLoad = "io.terraform.provider.buildx-builder.default-load"
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
	contractBuildxBuilder
	ID                     types.String       `tfsdk:"id"`
	Name                   types.String       `tfsdk:"name"`
	Node                   types.String       `tfsdk:"node"`
	Append                 types.Bool         `tfsdk:"append"`
	Driver                 types.String       `tfsdk:"driver"`
	Platform               types.List         `tfsdk:"platform"`
	BuildkitdFlags         types.String       `tfsdk:"buildkitd_flags"`
	BuildkitdConfig        types.String       `tfsdk:"buildkitd_config"`
	BuildkitdConfigContent types.String       `tfsdk:"buildkitd_config_content"`
	Bootstrap              types.Bool         `tfsdk:"bootstrap"`
	KeepState              types.Bool         `tfsdk:"keep_state"`
	TCPPort                types.Int64        `tfsdk:"tcp_port"`
	Endpoint               types.String       `tfsdk:"endpoint"`
	TCPAddress             types.String       `tfsdk:"tcp_address"`
	ContainerName          types.String       `tfsdk:"container_name"`
	VolumeName             types.String       `tfsdk:"volume_name"`
	Platforms              types.List         `tfsdk:"platforms"`
	Labels                 types.Map          `tfsdk:"labels"`
	DockerContainer        *bxbContainerModel `tfsdk:"docker_container"`
	Remote                 *bxbRemoteModel    `tfsdk:"remote"`
	Adopt                  types.Bool         `tfsdk:"adopt"`
}

type bxbContainerModel struct {
	Image         types.String `tfsdk:"image"`
	Network       types.String `tfsdk:"network"`
	Env           types.Map    `tfsdk:"env"`
	RestartPolicy types.String `tfsdk:"restart_policy"`
	Memory        types.Int64  `tfsdk:"memory"`
	MemorySwap    types.Int64  `tfsdk:"memory_swap"`
	CPUQuota      types.Int64  `tfsdk:"cpu_quota"`
	CPUPeriod     types.Int64  `tfsdk:"cpu_period"`
	CPUShares     types.Int64  `tfsdk:"cpu_shares"`
	CpusetCpus    types.String `tfsdk:"cpuset_cpus"`
	CpusetMems    types.String `tfsdk:"cpuset_mems"`
	CgroupParent  types.String `tfsdk:"cgroup_parent"`
	DefaultLoad   types.Bool   `tfsdk:"default_load"`
}

func (r *buildxBuilderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_builder"
}

func (r *buildxBuilderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true, MarkdownDescription: "ID of the BuildKit node container.", PlanModifiers: keep,
		},
		"name": schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Builder name, as for `docker buildx create --name`; pass it to `docker_image.build.builder`. Forces replacement.",
			PlanModifiers:       rr,
			Validators:          []validator.String{stringvalidator.RegexMatches(bxbNameRe, "letters, digits, '_', '.' and '-', starting with a letter or digit")},
		},
		"node": schema.StringAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Node name (`--node`); the node container is `buildx_buildkit_<node>`. Default `<name>0`, like buildx. Forces replacement.",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			Validators:          []validator.String{stringvalidator.RegexMatches(bxbNameRe, "letters, digits, '_', '.' and '-', starting with a letter or digit")},
		},
		"append": schema.BoolAttribute{
			Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			MarkdownDescription: "Whether this node joins an existing builder of the same `name` (`--append`). The builder must already have a node " +
				"on the daemon, and `node` must differ from its nodes. With `false` (default) creating fails when the builder already exists. " +
				"Destroy removes only this node. Forces replacement.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
		},
		"driver": schema.StringAttribute{
			Optional: true, Computed: true, Default: stringdefault.StaticString(bxbDriverDockerContainer),
			MarkdownDescription: "Builder driver: `docker-container` (default), a BuildKit container on the provider's daemon, or `remote`, a " +
				"BuildKit daemon run elsewhere and reached at `endpoint` (TLS in the `remote` block). `docker` is the daemon's built-in builder " +
				"(use `docker_image` without `build.builder`); `kubernetes` is not supported (it needs the Kubernetes API). Forces replacement.",
			PlanModifiers: rr,
			Validators:    []validator.String{stringvalidator.OneOf(bxbDriverDockerContainer, bxbDriverRemote)},
		},
		"platform": schema.ListAttribute{
			ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Platforms this node is pinned to (`--platform`), listed first in `platforms`. buildx keeps pins in its " +
				"client-side store; the provider stores them as a label on the node container. Forces replacement.",
			PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
		},
		"buildkitd_flags": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Flags for buildkitd (`--buildkitd-flags`), whitespace-separated with no shell quoting, e.g. " +
				"`\"--debug --allow-insecure-entitlement=security.insecure\"`. Default `" + bxbDefaultFlags + "`, like buildx. Forces replacement.",
			PlanModifiers: rr,
		},
		"buildkitd_config": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Path of a local `buildkitd.toml` (`--buildkitd-config`), read at apply and copied to " +
				"`/etc/buildkit/buildkitd.toml` in the container. Conflicts with `buildkitd_config_content`. Forces replacement.",
			PlanModifiers: rr,
			Validators:    []validator.String{stringvalidator.ConflictsWith(path.MatchRoot("buildkitd_config_content"))},
		},
		"buildkitd_config_content": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Content of `buildkitd.toml`, copied like `buildkitd_config`. Forces replacement.",
			PlanModifiers:       rr,
		},
		"bootstrap": schema.BoolAttribute{
			Optional: true, Computed: true, Default: booldefault.StaticBool(true),
			MarkdownDescription: "Whether to start the builder and wait until BuildKit lists its workers (`--bootstrap`). With `false` the container is " +
				"only created. Default `true`. Forces replacement.",
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
		},
		"keep_state": schema.BoolAttribute{
			Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			MarkdownDescription: "Whether to keep the state volume (build cache) on destroy (`docker buildx rm --keep-state`). Default `false`. Updated in place.",
		},
		"tcp_port": schema.Int64Attribute{
			Optional: true,
			MarkdownDescription: fmt.Sprintf("Host port on which BuildKit is also served over TCP: buildkitd listens on `tcp://0.0.0.0:%d` in the container and the port "+
				"is published on this host port. Unauthenticated and unencrypted; bind it only on trusted networks. Forces replacement.", bxbTCPContainerPort),
			PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
		},
		"endpoint": schema.StringAttribute{
			Optional: true, Computed: true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			MarkdownDescription: "BuildKit address of the builder, for `buildctl --addr` and `docker_image.build.builder`. With `driver = \"remote\"` " +
				"it is required: the remote BuildKit's `tcp://host:port` or `unix:///path` (the `docker buildx create --driver remote` " +
				"endpoint argument). With `docker-container` it is computed: `docker-container://buildx_buildkit_<node>`. Forces replacement.",
		},
		"adopt": ownerAdoptAttribute(),
		"tcp_address": schema.StringAttribute{
			Computed: true, PlanModifiers: keep,
			MarkdownDescription: "TCP address of BuildKit, `tcp://<daemon host>:<tcp_port>`, when `tcp_port` is set: the host of the provider's `tcp://`/`ssh://` " +
				"daemon address, else `127.0.0.1`. Null without `tcp_port`.",
		},
		"container_name": schema.StringAttribute{
			Computed: true, PlanModifiers: keep, MarkdownDescription: "Name of the node container, `buildx_buildkit_<node>`.",
		},
		"volume_name": schema.StringAttribute{
			Computed: true, PlanModifiers: keep, MarkdownDescription: "Name of the state volume, `buildx_buildkit_<node>_state`, mounted on `/var/lib/buildkit`.",
		},
		"platforms": schema.ListAttribute{
			ElementType: types.StringType, Computed: true,
			PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "Platforms the builder can build for: the `platform` pins, then what the BuildKit workers report, " +
				"e.g. `[\"linux/amd64\", \"linux/386\"]`. Only the pins when `bootstrap` is false.",
		},
		"labels": schema.MapAttribute{
			ElementType: types.StringType, Computed: true,
			PlanModifiers:       []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "BuildKit worker labels of the node (`org.mobyproject.buildkit.worker.*`). Empty when `bootstrap` is false.",
		},
	}
	contractAddResourceAttributes(attrs, bxbItemID)
	bxbKeepContractState(attrs)
	resp.Schema = schema.Schema{
		MarkdownDescription: "A BuildKit builder run by the daemon as a `docker-container` driver node, the same container and state volume " +
			"`docker buildx create --driver docker-container --bootstrap` makes, created through the Engine API without the buildx CLI. " +
			"The container `buildx_buildkit_<node>` runs `moby/buildkit` privileged with the state volume `buildx_buildkit_<node>_state`; " +
			"readiness is checked by listing BuildKit workers over `buildctl dial-stdio` in the container. Pass `name` to " +
			"`docker_image.build.builder`, or use `endpoint` with `buildctl --addr` (`tcp_address` when `tcp_port` is set). The buildx CLI's own " +
			"instance list (`~/.docker/buildx/instances`) is not written, so `docker buildx ls` does not show the builder until " +
			"`docker buildx create --name <name> --driver docker-container` is run, which adopts the same container. Only the `docker-container` " +
			"driver creates a container; the `remote` driver only checks and reports a BuildKit daemon run elsewhere, selected in builds by its " +
			"`endpoint`. With the provider's `owner` set, the node container and state volume carry the owner label. Every argument except " +
			"`keep_state` and `adopt` forces a new resource.",
		Attributes: attrs,
		Blocks: map[string]schema.Block{
			"remote": bxbRemoteBlock(),
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
					"memory_swap": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "Memory plus swap limit in bytes (`memory-swap=`); `-1` is unlimited swap.",
					},
					"cpu_quota": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "CPU CFS quota in microseconds per `cpu_period` (`cpu-quota=`).",
					},
					"cpu_period": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "CPU CFS period in microseconds (`cpu-period=`).",
					},
					"cpu_shares": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "Relative CPU weight (`cpu-shares=`).",
					},
					"cpuset_cpus": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "CPUs the container may use, e.g. `0-3` (`cpuset-cpus=`).",
					},
					"cpuset_mems": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "NUMA memory nodes the container may use, e.g. `0` (`cpuset-mems=`).",
					},
					"cgroup_parent": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Parent cgroup of the container (`cgroup-parent=`).",
					},
					"default_load": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Whether builds that ask for no output load their result into the daemon's image store (`default-load=`). " +
							"Stored as a label on the node container and honoured by the provider's builds on this builder.",
					},
				},
			},
		},
	}
}

// bxbKeepContractState gives the contract outputs UseStateForUnknown: the
// only in-place change (keep_state) does not touch the builder.
func bxbKeepContractState(attrs map[string]schema.Attribute) {
	for _, name := range contractOutputNames(bxbItemID, contractRaw, contractHash, contractStructured, contractVersion) {
		switch a := attrs[name].(type) {
		case schema.StringAttribute:
			a.PlanModifiers = append(a.PlanModifiers, stringplanmodifier.UseStateForUnknown())
			attrs[name] = a
		case schema.SingleNestedAttribute:
			a.PlanModifiers = append(a.PlanModifiers, objectplanmodifier.UseStateForUnknown())
			attrs[name] = a
		case schema.ObjectAttribute:
			a.PlanModifiers = append(a.PlanModifiers, objectplanmodifier.UseStateForUnknown())
			attrs[name] = a
		}
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
	bxbValidateRemote(m, &resp.Diagnostics)
	if p, ok := knownInt64(m.TCPPort); ok && (p < 1 || p > 65535) {
		resp.Diagnostics.AddAttributeError(path.Root("tcp_port"), "Invalid tcp_port", "tcp_port must be 1-65535.")
	}
	if !m.Platform.IsUnknown() && !m.Platform.IsNull() {
		for i, p := range stringListFrom(ctx, m.Platform, &resp.Diagnostics) {
			if strings.Count(p, "/") < 1 || strings.Contains(p, ",") {
				resp.Diagnostics.AddAttributeError(path.Root("platform").AtListIndex(i), "Invalid platform",
					fmt.Sprintf("%q is not os/arch[/variant].", p))
			}
		}
	}
}

func bxbContainerName(node string) string { return bxbContainerPrefix + node }
func bxbVolumeName(node string) string    { return bxbContainerName(node) + "_state" }

// bxbNodeName is the node name: node, else <name>0.
func bxbNodeName(m bxbModel) string {
	if v, ok := knownString(m.Node); ok && v != "" {
		return v
	}
	return m.Name.ValueString() + "0"
}

// bxbFlags returns the buildkitd arguments.
func bxbFlags(m bxbModel) []string {
	flags := bxbDefaultFlags
	if v, ok := knownString(m.BuildkitdFlags); ok {
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
	if v, ok := knownString(m.BuildkitdConfigContent); ok {
		return []byte(v), nil
	}
	if p, ok := knownString(m.BuildkitdConfig); ok && p != "" {
		b, err := os.ReadFile(expandHome(p))
		if err != nil {
			return nil, fmt.Errorf("reading buildkitd_config: %w", err)
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
	node := bxbNodeName(m)
	dc := m.DockerContainer
	if dc == nil {
		dc = &bxbContainerModel{}
	}
	img := bxbDefaultImage
	if v, ok := knownString(dc.Image); ok && v != "" {
		img = v
	}
	labels := map[string]string{labelManagedBy: labelManagedByValue, bxbLabelBuilder: name}
	if ps := stringListFrom(ctx, m.Platform, diags); len(ps) > 0 {
		labels[bxbLabelPlatforms] = strings.Join(ps, ",")
	}
	if !dc.DefaultLoad.IsNull() && !dc.DefaultLoad.IsUnknown() {
		labels[bxbLabelDefaultLoad] = strconv.FormatBool(dc.DefaultLoad.ValueBool())
	}
	cfg := &container.Config{
		Image:  img,
		Cmd:    bxbFlags(m),
		Env:    containerEnvToSlice(stringMapFrom(ctx, dc.Env, diags)),
		Labels: labels,
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
		Mounts:        []mount.Mount{{Type: mount.TypeVolume, Source: bxbVolumeName(node), Target: bxbStateDir}},
	}
	if v, ok := knownString(dc.Network); ok && v != "" {
		hc.NetworkMode = container.NetworkMode(v)
	}
	if v, ok := knownInt64(dc.Memory); ok {
		hc.Memory = v
	}
	if v, ok := knownInt64(dc.MemorySwap); ok {
		hc.MemorySwap = v
	}
	if v, ok := knownInt64(dc.CPUQuota); ok {
		hc.CPUQuota = v
	}
	if v, ok := knownInt64(dc.CPUPeriod); ok {
		hc.CPUPeriod = v
	}
	if v, ok := knownInt64(dc.CPUShares); ok {
		hc.CPUShares = v
	}
	if v, ok := knownString(dc.CpusetCpus); ok {
		hc.CpusetCpus = v
	}
	if v, ok := knownString(dc.CpusetMems); ok {
		hc.CpusetMems = v
	}
	if v, ok := knownString(dc.CgroupParent); ok {
		hc.CgroupParent = v
	}
	if p, ok := knownInt64(m.TCPPort); ok {
		port, _ := network.PortFrom(bxbTCPContainerPort, network.TCP)
		cfg.ExposedPorts = network.PortSet{port: {}}
		hc.PortBindings = network.PortMap{port: {{HostPort: strconv.FormatInt(p, 10)}}}
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
	ex, err := pd.Client.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd: []string{"buildctl", "dial-stdio"}, AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}
	hj, err := pd.Client.ExecAttach(ctx, ex.ID, client.ExecAttachOptions{})
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
		inspRes, err := pd.Client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return false, err
		}
		insp := inspRes.Container
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

// bxbCheckAppend enforces `append`: a new builder must not exist yet, an
// appended node needs an existing builder and a new node name.
func (r *buildxBuilderResource) bxbCheckAppend(ctx context.Context, m bxbModel) error {
	nodes, err := bkListNodes(ctx, r.pd)
	if err != nil {
		return err
	}
	name, node := m.Name.ValueString(), bxbNodeName(m)
	var existing []string
	for _, n := range nodes {
		if n.Driver == bkDriverDocker {
			continue
		}
		if n.Name == node {
			return fmt.Errorf("a builder node container %s already exists (builder %q); remove it, import it, or choose another node name", bxbContainerName(node), n.Builder)
		}
		if n.Builder == name {
			existing = append(existing, n.Name)
		}
	}
	if m.Append.ValueBool() && len(existing) == 0 {
		return fmt.Errorf("append = true needs an existing builder %q on the daemon, and none has nodes", name)
	}
	if !m.Append.ValueBool() && len(existing) > 0 {
		return fmt.Errorf("builder %q already exists with node(s) %s; set append = true to add a node", name, strings.Join(existing, ", "))
	}
	return nil
}

func (r *buildxBuilderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bxbModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if plan.Driver.ValueString() == bxbDriverRemote {
		resp.Diagnostics.Append(r.bxbRemoteCreate(ctx, &plan)...)
		if !resp.Diagnostics.HasError() {
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
		return
	}
	node := bxbNodeName(plan)
	plan.Node = types.StringValue(node)
	cname := bxbContainerName(node)
	if res, err := r.pd.Client.ContainerInspect(ctx, cname, client.ContainerInspectOptions{}); err == nil && res.Container.Config != nil {
		if err := ownerCreateCheck(r.pd, "builder node container", cname, res.Container.Config.Labels); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Builder node owned by another configuration", err.Error())
			return
		}
	}
	if err := r.bxbCheckAppend(ctx, plan); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("append"), "Cannot create builder node", err.Error())
		return
	}
	cfgBytes, err := bxbConfigBytes(plan)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("buildkitd_config"), "Cannot read buildkitd_config", err.Error())
		return
	}
	cfg, hc := bxbBuildCreate(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg.Labels = r.pd.ownerInject(cfg.Labels)
	if err := containerEnsureImage(ctx, r.pd, cfg.Image); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("docker_container").AtName("image"), "Cannot pull BuildKit image", fmt.Sprintf("%s: %v", cfg.Image, err))
		return
	}
	vol := bxbVolumeName(node)
	if _, err := r.pd.Client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: vol, Labels: cfg.Labels}); err != nil {
		resp.Diagnostics.AddError("Cannot create BuildKit state volume", fmt.Sprintf("%s: %v", vol, err))
		return
	}
	created, err := r.pd.Client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hc, NetworkingConfig: nil, Platform: nil, Name: cname})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create BuildKit container", fmt.Sprintf("%s: %v", cname, err))
		return
	}
	plan.ID = types.StringValue(created.ID)
	// Track the container at once so a failure below leaves it in state.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), plan.Name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("node"), plan.Node)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("keep_state"), plan.KeepState)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_name"), types.StringValue(vol))...)

	if cfgBytes != nil {
		tb, err := bxbConfigTar(cfgBytes, time.Now())
		if err == nil {
			_, err = r.pd.Client.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: bytes.NewReader(tb)})
		}
		if err != nil {
			resp.Diagnostics.AddError("Cannot copy buildkitd.toml into the builder", err.Error())
			return
		}
	}

	plan.ContainerName = types.StringValue(cname)
	plan.VolumeName = types.StringValue(vol)
	plan.Endpoint = types.StringValue(bkDockerContainerEP + cname)
	plan.TCPAddress = types.StringNull()
	if p, ok := knownInt64(plan.TCPPort); ok {
		plan.TCPAddress = types.StringValue(fmt.Sprintf("tcp://%s:%d", bxbTCPHost(r.pd.Endpoint.Host), p))
	}

	var ws []*bkclient.WorkerInfo
	if plan.Bootstrap.ValueBool() {
		if _, err := r.pd.Client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			resp.Diagnostics.AddError("Cannot start BuildKit container", err.Error())
			return
		}
		w, err := bxbReady(ctx, r.pd, created.ID)
		if err != nil {
			resp.Diagnostics.AddError("BuildKit did not become ready", fmt.Sprintf("%s: %v", cname, err))
			return
		}
		if w != nil {
			ws = []*bkclient.WorkerInfo{w}
		}
	}
	resp.Diagnostics.Append(r.bxbFillOutputs(ctx, &plan, ws, true)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// bxbNode is the bkNode of the resource's own node.
func bxbNode(m bxbModel) bkNode {
	node := bxbNodeName(m)
	return bkNode{
		Builder: m.Name.ValueString(), Name: node, Driver: bxbDriverDockerContainer,
		Endpoint: bkDockerContainerEP + bxbContainerName(node), ContainerID: m.ID.ValueString(),
		ContainerName: bxbContainerName(node), Status: bkStatusRunning,
	}
}

// bxbFillOutputs sets platforms, labels and the contract outputs from the
// builder's nodes. ws, when known (haveWS), are this node's workers.
func (r *buildxBuilderResource) bxbFillOutputs(ctx context.Context, m *bxbModel, ws []*bkclient.WorkerInfo, haveWS bool) diag.Diagnostics {
	own := bxbNode(*m)
	nodes := []bkNode{own}
	if all, err := bkBuilderNodes(ctx, r.pd, own.Builder); err == nil && len(all) > 0 {
		nodes = all
		found := false
		for i := range nodes {
			if nodes[i].Name == own.Name {
				found = true
				nodes[i].ContainerID = own.ContainerID
			}
		}
		if !found {
			nodes = append([]bkNode{own}, nodes...)
		}
	}
	known := map[string][]*bkclient.WorkerInfo{}
	if haveWS {
		known[own.Name] = ws
		if ws == nil {
			known[own.Name] = []*bkclient.WorkerInfo{}
		}
	}
	view := bxBuilderViewOf(ctx, r.pd, nodes, known)
	var nv bxNodeView
	for _, n := range view.Nodes {
		if n.Name == own.Name {
			nv = n
		}
	}
	m.Platforms = stringListValue(bxStrings(nv.Platforms), false)
	m.Labels = stringMapValue(bxStringMap(nv.Labels), false)
	return m.contractFill(ctx, r.pd, contractFillInput{
		Raw:             map[string][]byte{"inspect_json": view.json()},
		Structured:      map[string]any{"builder": view.structured()},
		BuildKitVersion: nv.Version,
	})
}

func (r *buildxBuilderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state bxbModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if state.Driver.ValueString() == bxbDriverRemote {
		n := bxbRemoteNode(state)
		ws, err := bxbRemoteWorkers(ctx, r.pd, n)
		if err != nil {
			resp.Diagnostics.AddWarning("Remote builder not reachable", fmt.Sprintf("%s: %v; keeping the last known state.", n.Endpoint, err))
			return
		}
		resp.Diagnostics.Append(r.bxbRemoteFill(ctx, &state, ws, true)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	inspRes, err := r.pd.Client.ContainerInspect(ctx, state.ID.ValueString(), client.ContainerInspectOptions{})
	insp := inspRes.Container
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect BuildKit container", err.Error())
		return
	}
	bxbFromInspect(&state, insp, r.pd.Endpoint.Host)
	if insp.Config != nil && resp.Private != nil {
		resp.Diagnostics.Append(ownerRecord(ctx, resp.Private, insp.Config.Labels)...)
	}
	resp.Diagnostics.Append(r.bxbFillOutputs(ctx, &state, nil, false)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// bxbFromInspect fills computed and (on import) configured values from the
// container.
func bxbFromInspect(m *bxbModel, insp container.InspectResponse, dockerHost string) {
	if insp.ID == "" {
		return
	}
	m.ID = types.StringValue(insp.ID)
	cname := strings.TrimPrefix(insp.Name, "/")
	m.ContainerName = types.StringValue(cname)
	m.Endpoint = types.StringValue(bkDockerContainerEP + cname)
	node := strings.TrimPrefix(cname, bxbContainerPrefix)
	if !containerKnown(m.Node) {
		m.Node = types.StringValue(node)
	}
	var labels map[string]string
	if insp.Config != nil {
		labels = insp.Config.Labels
	}
	if !containerKnown(m.Name) {
		name := labels[bxbLabelBuilder]
		if name == "" {
			name = bkNodeFromContainer(cname, insp.ID, labels, "", nil, time.Time{}).Builder
		}
		m.Name = types.StringValue(name)
	}
	vol := bxbVolumeName(node)
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
	if !containerKnown(m.Append) {
		m.Append = types.BoolValue(false)
	}
	if m.Platform.IsUnknown() || (m.Platform.IsNull() && labels[bxbLabelPlatforms] != "") {
		m.Platform = types.ListNull(types.StringType)
		if v := labels[bxbLabelPlatforms]; v != "" {
			m.Platform = stringListValue(strings.Split(v, ","), true)
		}
	}
	if m.TCPAddress.IsUnknown() {
		m.TCPAddress = types.StringNull()
		if p, ok := knownInt64(m.TCPPort); ok {
			m.TCPAddress = types.StringValue(fmt.Sprintf("tcp://%s:%d", bxbTCPHost(dockerHost), p))
		}
	}
}

func (r *buildxBuilderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Only keep_state changes in place; it is used at destroy. Everything
	// computed keeps its state value.
	var plan, state bxbModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.KeepState = plan.KeepState
	state.Adopt = plan.Adopt
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ModifyPlan checks the owner label of an existing node container (after
// import, or when `owner` changed): planning it needs adopt = true when it
// names another owner.
func (r *buildxBuilderResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan bxbModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(ownerPlanCheck(ctx, r.pd, req.Private, "docker_buildx_builder", plan.Name.ValueString(), plan.Adopt)...)
}

func (r *buildxBuilderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state bxbModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if state.Driver.ValueString() == bxbDriverRemote {
		return // nothing was created on the daemon
	}
	_, err := r.pd.Client.ContainerRemove(ctx, state.ID.ValueString(), client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove BuildKit container", err.Error())
		return
	}
	if state.KeepState.ValueBool() {
		return
	}
	vol := state.VolumeName.ValueString()
	if vol == "" {
		vol = bxbVolumeName(bxbNodeName(state))
	}
	if _, err := r.pd.Client.VolumeRemove(ctx, vol, client.VolumeRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove BuildKit state volume", fmt.Sprintf("%s: %v", vol, err))
	}
}

// ImportState accepts the builder name (its node 0), the node name, or the
// container name/ID.
func (r *buildxBuilderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "Cannot import before the provider is configured.")
		return
	}
	var insp container.InspectResponse
	var err error
	for _, cand := range []string{req.ID, bxbContainerName(req.ID + "0"), bxbContainerName(req.ID)} {
		var res client.ContainerInspectResult
		res, err = r.pd.Client.ContainerInspect(ctx, cand, client.ContainerInspectOptions{})
		if err == nil {
			insp = res.Container
			break
		}
		if !isNotFound(err) {
			break
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot find BuildKit container", fmt.Sprintf("%q: %v", req.ID, err))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), insp.ID)...)
}
