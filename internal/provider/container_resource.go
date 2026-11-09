package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*containerResource)(nil)
	_ resource.ResourceWithConfigure   = (*containerResource)(nil)
	_ resource.ResourceWithImportState = (*containerResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*containerResource)(nil)
	_ resource.ResourceWithIdentity    = (*containerResource)(nil)
)

// containerDefaultWaitTimeout is the wait_timeout default.
const containerDefaultWaitTimeout = "60s"

// containerPollInterval is a var so unit tests can shorten it.
var containerPollInterval = 2 * time.Second

// NewContainerResource is the docker_container constructor.
func NewContainerResource() resource.Resource { return &containerResource{} }

type containerResource struct {
	pd *providerData
}

type containerResourceModel struct {
	ID                   types.String               `tfsdk:"id"`
	Name                 types.String               `tfsdk:"name"`
	Image                types.String               `tfsdk:"image"`
	ImageID              types.String               `tfsdk:"image_id"`
	Command              types.List                 `tfsdk:"command"`
	Entrypoint           types.List                 `tfsdk:"entrypoint"`
	Env                  types.Map                  `tfsdk:"env"`
	Ports                []containerPortModel       `tfsdk:"ports"`
	Mounts               []containerMountModel      `tfsdk:"mounts"`
	Networks             types.Map                  `tfsdk:"networks"`
	Restart              types.String               `tfsdk:"restart"`
	MaximumRetryCount    types.Int64                `tfsdk:"maximum_retry_count"`
	Healthcheck          *containerHealthcheckModel `tfsdk:"healthcheck"`
	Labels               types.Map                  `tfsdk:"labels"`
	CapAdd               types.Set                  `tfsdk:"cap_add"`
	CapDrop              types.Set                  `tfsdk:"cap_drop"`
	Privileged           types.Bool                 `tfsdk:"privileged"`
	User                 types.String               `tfsdk:"user"`
	WorkingDir           types.String               `tfsdk:"working_dir"`
	Logging              *ctrLoggingModel           `tfsdk:"logging"`
	Ulimits              []containerUlimitModel     `tfsdk:"ulimits"`
	Memory               types.Int64                `tfsdk:"memory"`
	MemorySwap           types.Int64                `tfsdk:"memory_swap"`
	MemoryReservation    types.Int64                `tfsdk:"memory_reservation"`
	CPUShares            types.Int64                `tfsdk:"cpu_shares"`
	NanoCPUs             types.Int64                `tfsdk:"nano_cpus"`
	CPUPeriod            types.Int64                `tfsdk:"cpu_period"`
	CPUQuota             types.Int64                `tfsdk:"cpu_quota"`
	CpusetCpus           types.String               `tfsdk:"cpuset_cpus"`
	MustRun              types.Bool                 `tfsdk:"must_run"`
	Wait                 types.Bool                 `tfsdk:"wait"`
	WaitTimeout          types.String               `tfsdk:"wait_timeout"`
	Running              types.Bool                 `tfsdk:"running"`
	ExitCode             types.Int64                `tfsdk:"exit_code"`
	Hostname             types.String               `tfsdk:"hostname"`
	Domainname           types.String               `tfsdk:"domainname"`
	DNS                  types.List                 `tfsdk:"dns"`
	DNSSearch            types.List                 `tfsdk:"dns_search"`
	DNSOptions           types.List                 `tfsdk:"dns_options"`
	ExtraHosts           types.List                 `tfsdk:"extra_hosts"`
	Devices              []containerDeviceModel     `tfsdk:"devices"`
	DeviceCgroupRules    types.List                 `tfsdk:"device_cgroup_rules"`
	DeviceRequests       []ctrDeviceRequestModel    `tfsdk:"device_requests"`
	BlkioDeviceReadBps   []ctrThrottleModel         `tfsdk:"blkio_device_read_bps"`
	BlkioDeviceWriteBps  []ctrThrottleModel         `tfsdk:"blkio_device_write_bps"`
	BlkioDeviceReadIOps  []ctrThrottleModel         `tfsdk:"blkio_device_read_iops"`
	BlkioDeviceWriteIOps []ctrThrottleModel         `tfsdk:"blkio_device_write_iops"`
	Tty                  types.Bool                 `tfsdk:"tty"`
	StdinOpen            types.Bool                 `tfsdk:"stdin_open"`
	StopSignal           types.String               `tfsdk:"stop_signal"`
	StopTimeout          types.Int64                `tfsdk:"stop_timeout"`
	Sysctls              types.Map                  `tfsdk:"sysctls"`
	SecurityOpt          types.Set                  `tfsdk:"security_opt"`
	ReadOnly             types.Bool                 `tfsdk:"read_only"`
	GPUs                 types.String               `tfsdk:"gpus"`
	Files                []containerUploadModel     `tfsdk:"files"`
	CgroupParent         types.String               `tfsdk:"cgroup_parent"`
	CgroupnsMode         types.String               `tfsdk:"cgroupns_mode"`
	GroupAdd             types.Set                  `tfsdk:"group_add"`
	Init                 types.Bool                 `tfsdk:"init"`
	IpcMode              types.String               `tfsdk:"ipc_mode"`
	NetworkMode          types.String               `tfsdk:"network_mode"`
	PidMode              types.String               `tfsdk:"pid_mode"`
	Platform             types.String               `tfsdk:"platform"`
	PublishAllPorts      types.Bool                 `tfsdk:"publish_all_ports"`
	AutoRemove           types.Bool                 `tfsdk:"auto_remove"`
	Runtime              types.String               `tfsdk:"runtime"`
	ShmSize              types.Int64                `tfsdk:"shm_size"`
	StorageOpt           types.Map                  `tfsdk:"storage_opt"`
	Tmpfs                types.Map                  `tfsdk:"tmpfs"`
	UsernsMode           types.String               `tfsdk:"userns_mode"`
	VolumesFrom          types.List                 `tfsdk:"volumes_from"`

	// Outputs contract (engine:container): inspect_json, inspect,
	// engine_api_version, docker_server_version, content_sha256.
	contractEngineContainer
}

func (r *containerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container"
}

// containerRequiresReplaceIfCleared forces replacement when a resource limit
// goes from a non-zero value to unset: ContainerUpdate treats 0 as "leave
// unchanged", so a limit cannot be removed in place.
func containerRequiresReplaceIfCleared() planmodifier.Int64 {
	return int64planmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
		if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.StateValue.ValueInt64() == 0 {
			return
		}
		resp.RequiresReplace = !req.PlanValue.IsUnknown() && (req.PlanValue.IsNull() || req.PlanValue.ValueInt64() == 0)
	}, "Removing a limit forces a new container.", "Removing a limit forces a new container.")
}

func (r *containerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rrStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	rrList := []planmodifier.List{listplanmodifier.RequiresReplace()}
	rrMap := []planmodifier.Map{mapplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A single-host Docker container, created with `ContainerCreate`, attached to its networks and started. " +
			"Refresh diffs against `ContainerInspect`: values the image already supplies (env, labels, command, entrypoint, user, " +
			"working_dir, healthcheck) are only reported when they differ from the image or you set them, so image defaults never show as drift. " +
			"`name`, `restart`, `maximum_retry_count`, `memory`, `memory_swap`, `memory_reservation`, `cpu_shares`, `nano_cpus`, " +
			"`cpu_period`, `cpu_quota` and `cpuset_cpus` update in place (`ContainerRename`/`ContainerUpdate`); `networks` entries are " +
			"connected and disconnected in place (`NetworkConnect`/`NetworkDisconnect`); `files` blocks are copied in with " +
			"`PutContainerArchive` before start. Every other argument forces a new container. Destroy force-removes the container " +
			"but keeps its volumes. Import by container name or ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Full container ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Container name. Changing it renames the container in place.",
			},
			"image": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Image reference to run, e.g. `nginx:1.27` or a `docker_image` resource's `name`. Pulled (with matching " +
					"`registry_auth`) when not present locally. Changing this forces a new resource.",
				PlanModifiers: rrStr,
			},
			"image_id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "ID (`sha256:...`) of the image the container runs.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"command": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Command (`CMD`) override. Unset uses the image's. Changing this forces a new resource.",
				PlanModifiers:       rrList,
			},
			"entrypoint": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Entrypoint override. Unset uses the image's; setting it also drops the image's `CMD` unless `command` is set. " +
					"Changing this forces a new resource.",
				PlanModifiers: rrList,
			},
			"env": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Environment variables, merged over the image's. Image-provided variables are not reported in state. " +
					"Changing this forces a new resource.",
				PlanModifiers: rrMap,
			},
			"restart": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("no"),
				MarkdownDescription: "Restart policy: `no` (default), `always`, `unless-stopped` or `on-failure`. Updated in place.",
				Validators:          []validator.String{stringvalidator.OneOf(containerRestartPolicies...)},
			},
			"maximum_retry_count": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(0),
				MarkdownDescription: "Maximum restart attempts for `restart = \"on-failure\"` (0 = unlimited). Ignored by other policies. Updated in place.",
			},
			"labels": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Container labels. Image labels are not reported in state. Docker cannot change labels of an existing " +
					"container, so changing this forces a new resource.",
				PlanModifiers: rrMap,
			},
			"privileged": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Run the container privileged. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"user": schema.StringAttribute{
				Optional: true, MarkdownDescription: "User (and optionally `:group`) to run as. Unset uses the image's. Changing this forces a new resource.",
				PlanModifiers: rrStr,
			},
			"working_dir": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Working directory. Unset uses the image's. Changing this forces a new resource.",
				PlanModifiers: rrStr,
			},
			"memory": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Memory limit in bytes. Updated in place; removing the limit forces a new resource.",
				PlanModifiers: []planmodifier.Int64{containerRequiresReplaceIfCleared()},
			},
			"memory_swap": schema.Int64Attribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Total memory + swap limit in bytes (`-1` = unlimited swap). Unset: the daemon uses twice `memory` when `memory` " +
					"is set. Updated in place.",
			},
			"cpu_shares": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Relative CPU weight (default 1024 in the daemon). Updated in place; removing it forces a new resource.",
				PlanModifiers: []planmodifier.Int64{containerRequiresReplaceIfCleared()},
			},
			"nano_cpus": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "CPU quota in units of 1e-9 CPUs (`1500000000` = 1.5 CPUs). Updated in place; removing it forces a new resource.",
				PlanModifiers: []planmodifier.Int64{containerRequiresReplaceIfCleared()},
			},
			"must_run": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "When `true` (default) a container found stopped on refresh is planned for start (`running` goes to `true`) and " +
					"started by the next apply.",
			},
			"wait": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Wait after start until the container's healthcheck reports `healthy` (or, without a healthcheck, until it is running). " +
					"Fails if the container exits or `wait_timeout` elapses.",
			},
			"wait_timeout": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString(containerDefaultWaitTimeout),
				MarkdownDescription: "How long `wait` waits, as a Go duration. Default `" + containerDefaultWaitTimeout + "`.",
			},
			"running": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the container was running at the last refresh. Planned `true` when `must_run` is set.",
			},
			"networks": ctrNetworksSchema(),
			"hostname": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Container hostname (`Config.Hostname`). Unset: the daemon uses the first 12 characters of the container ID, " +
					"which is not reported in state. Forces replacement.",
				PlanModifiers: rrStr,
			},
			"dns": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "DNS server IPs for the container's resolver (`HostConfig.Dns`). Unset uses the daemon/network default. Forces replacement.",
				PlanModifiers:       rrList,
			},
			"dns_search": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "DNS search domains (`HostConfig.DnsSearch`). Forces replacement.",
				PlanModifiers:       rrList,
			},
			"extra_hosts": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Extra `/etc/hosts` entries, each `\"<hostname>:<ip>\"` exactly like `docker run --add-host` " +
					"(e.g. `\"db.local:10.0.0.5\"`, or `\"host.docker.internal:host-gateway\"` for the host's gateway IP). Forces replacement.",
				PlanModifiers: rrList,
			},
			"tty": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Allocate a pseudo-TTY (`docker run -t`). Default `false`. Forces replacement.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"stdin_open": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Keep stdin open (`docker run -i`); with `tty` this keeps shells such as `sh` from exiting immediately. Default `false`. Forces replacement.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"stop_signal": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Signal sent to stop the container, e.g. `SIGTERM` or `SIGQUIT`. Unset uses the image's `STOPSIGNAL` " +
					"(else `SIGTERM`); the image value is not reported in state. Forces replacement.",
				PlanModifiers: rrStr,
			},
			"stop_timeout": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Seconds to wait after the stop signal before killing the container. Unset uses the daemon default (10s). Forces replacement.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"sysctls": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Namespaced kernel parameters, e.g. `{ \"net.core.somaxconn\" = \"1024\" }`. Only keys you set are " +
					"reported (except on import). Forces replacement.",
				PlanModifiers: rrMap,
			},
			"security_opt": schema.SetAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Security options (`HostConfig.SecurityOpt`), e.g. `no-new-privileges:true`, `apparmor=unconfined`, " +
					"`seccomp=unconfined`, `label=disable`. Forces replacement.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			},
			"read_only": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Mount the container's root filesystem read-only (`docker run --read-only`); use `mounts` (e.g. tmpfs) for " +
					"writable paths. Default `false`. Forces replacement.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"gpus": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "GPUs to expose, like `docker run --gpus`: `\"all\"`, a count such as `\"2\"`, or `\"device=0,1\"` " +
					"(indexes or UUIDs). Sent as a `HostConfig.DeviceRequests` entry with driver `nvidia` and capabilities `[[\"gpu\"]]`; " +
					"the host needs the NVIDIA container toolkit. Forces replacement.",
				PlanModifiers: rrStr,
			},
		},
		Blocks: map[string]schema.Block{
			"ports": schema.ListNestedBlock{
				MarkdownDescription: "Published ports (`HostConfig.PortBindings`). Changing ports forces a new resource.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"container_port": schema.Int64Attribute{Required: true, MarkdownDescription: "Container port (the `PortBindings` key's port)."},
					"host_port":      schema.Int64Attribute{Optional: true, MarkdownDescription: "Host port (`PortBinding.HostPort`). Unset lets the daemon pick an ephemeral port."},
					"host_ip":        schema.StringAttribute{Optional: true, MarkdownDescription: "Host IP to bind (`PortBinding.HostIp`). Unset binds all interfaces."},
					"protocol": schema.StringAttribute{
						Optional: true, Computed: true, Default: stringdefault.StaticString("tcp"),
						MarkdownDescription: "`tcp` (default), `udp` or `sctp`.",
						Validators:          []validator.String{stringvalidator.OneOf("tcp", "udp", "sctp")},
					},
				}},
			},
			"mounts": schema.ListNestedBlock{
				MarkdownDescription: "Mounts (`HostConfig.Mounts`). Changing mounts forces a new resource.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Required: true, MarkdownDescription: "`bind`, `volume` or `tmpfs`.",
						Validators: []validator.String{stringvalidator.OneOf("bind", "volume", "tmpfs")},
					},
					"source": schema.StringAttribute{Optional: true, MarkdownDescription: "Host path (bind) or volume name (volume; unset = anonymous volume). Not allowed for tmpfs."},
					"target": schema.StringAttribute{Required: true, MarkdownDescription: "Path inside the container."},
					"read_only": schema.BoolAttribute{
						Optional: true, Computed: true, Default: booldefault.StaticBool(false),
						MarkdownDescription: "Mount read-only. Default `false`.",
					},
					"selinux_relabel": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "SELinux relabel for a `bind` or named `volume` mount: `z` (shared label) or `Z` (private label), " +
							"like the `docker run -v src:dst:z` suffix. The entry is then sent as a `HostConfig.Binds` string, because the " +
							"`Mounts` API has no relabel option. Forces replacement like any mounts change.",
						Validators: []validator.String{stringvalidator.OneOf(containerSELinuxModes...)},
					},
				}, Blocks: ctrMountOptionBlocks()},
			},
			"healthcheck": schema.SingleNestedBlock{
				MarkdownDescription: "Healthcheck override. Without this block the image's healthcheck (if any) applies. Changing it forces a new resource.",
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"test": schema.ListAttribute{
						ElementType: types.StringType, Optional: true,
						MarkdownDescription: "Test command, e.g. `[\"CMD\", \"curl\", \"-f\", \"http://localhost/\"]`, `[\"CMD-SHELL\", \"...\"]` or `[\"NONE\"]`. Required when the block is present.",
					},
					"interval":     schema.StringAttribute{Optional: true, MarkdownDescription: "Time between checks, Go duration (e.g. `30s`)."},
					"timeout":      schema.StringAttribute{Optional: true, MarkdownDescription: "Time before a check is considered hung."},
					"start_period": schema.StringAttribute{Optional: true, MarkdownDescription: "Grace period before failures count."},
					"start_interval": schema.StringAttribute{Optional: true,
						MarkdownDescription: "Time between checks during the start period, Go duration (`StartInterval`)."},
					"retries": schema.Int64Attribute{Optional: true, MarkdownDescription: "Consecutive failures before `unhealthy`."},
				},
			},
			"devices": schema.ListNestedBlock{
				MarkdownDescription: "Host devices mapped into the container (`HostConfig.Devices`, like `docker run --device`). Forces replacement.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"path_on_host": schema.StringAttribute{Required: true, MarkdownDescription: "Device path on the host, e.g. `/dev/fuse` (`PathOnHost`)."},
					"path_in_container": schema.StringAttribute{Optional: true,
						MarkdownDescription: "Path inside the container (`PathInContainer`). Unset uses `path_on_host`."},
					"cgroup_permissions": schema.StringAttribute{
						Optional: true, Computed: true, Default: stringdefault.StaticString(containerDefaultDevicePerms),
						MarkdownDescription: "Cgroup permissions (`CgroupPermissions`), a combination of `r` (read), `w` (write) and `m` (mknod). Default `rwm`.",
					},
				}},
			},
			"files": containerUploadBlockSchema(),
			"ulimits": schema.ListNestedBlock{
				MarkdownDescription: "Resource ulimits. Changing them forces a new resource.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{Required: true, MarkdownDescription: "Ulimit name, e.g. `nofile`."},
					"soft": schema.Int64Attribute{Required: true, MarkdownDescription: "Soft limit."},
					"hard": schema.Int64Attribute{Required: true, MarkdownDescription: "Hard limit."},
				}},
			},
		},
	}
	for k, v := range ctrGapAttributes() {
		resp.Schema.Attributes[k] = v
	}
	for k, v := range ctrGapBlocks() {
		resp.Schema.Blocks[k] = v
	}
	contractAddResourceAttributes(resp.Schema.Attributes, "engine:container")
}

func (r *containerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan checks Engine API minimums of the config, plans `running`
// (true when must_run) and keeps the computed memory_swap stable while
// memory is unchanged.
func (r *containerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.Config.Raw.IsNull() {
		r.pd.gateAPISince(ctx, "docker_container", req.Config, &resp.Diagnostics)
	}
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state containerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	var cfgSwap types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("memory_swap"), &cfgSwap)...)
	if resp.Diagnostics.HasError() {
		return
	}
	running := state.Running
	if plan.MustRun.ValueBool() {
		running = types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("running"), running)...)
	if running.ValueBool() && containerKnown(state.Running) && !state.Running.ValueBool() {
		containerPlanStartUnknown(ctx, req, resp)
	}
	if cfgSwap.IsNull() && plan.Memory.Equal(state.Memory) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("memory_swap"), state.MemorySwap)...)
	}
}

// containerPlanStartUnknown marks the runtime outputs unknown when the
// apply will start a stopped container (must_run). The framework only does
// this for a plan that already differs from state, and here the only
// difference is `running`, set by ModifyPlan: without it, the plan would
// keep the stopped container's exit_code, inspect, inspect_json and
// content_sha256, and the started container's values would be an
// inconsistent result. id and image_id do not change on a start.
func containerPlanStartUnknown(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	keep := map[string]bool{"id": true, "image_id": true, "running": true}
	for name, a := range req.Plan.Schema.GetAttributes() {
		if keep[name] || !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			continue
		}
		t := a.GetType()
		v, err := t.ValueFromTerraform(ctx, tftypes.NewValue(t.TerraformType(ctx), tftypes.UnknownValue))
		if err != nil {
			resp.Diagnostics.AddError("Cannot plan container start", fmt.Sprintf("%s: %v", name, err))
			return
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), v)...)
	}
}

// containerBuildCreate turns the plan into ContainerCreate arguments.
func containerBuildCreate(ctx context.Context, m containerResourceModel, diags *diag.Diagnostics) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfg := &container.Config{
		Image:      m.Image.ValueString(),
		Cmd:        stringListFrom(ctx, m.Command, diags),
		Entrypoint: stringListFrom(ctx, m.Entrypoint, diags),
		Env:        containerEnvToSlice(stringMapFrom(ctx, m.Env, diags)),
		Labels:     mergeLabels(stringMapFrom(ctx, m.Labels, diags), map[string]string{labelManagedBy: labelManagedByValue}),
		User:       m.User.ValueString(),
		WorkingDir: m.WorkingDir.ValueString(),
		Hostname:   m.Hostname.ValueString(),
		Tty:        m.Tty.ValueBool(),
		OpenStdin:  m.StdinOpen.ValueBool(),
		StopSignal: m.StopSignal.ValueString(),
	}
	if v, ok := knownInt64(m.StopTimeout); ok {
		n := int(v)
		cfg.StopTimeout = &n
	}
	hc := &container.HostConfig{
		Privileged:     m.Privileged.ValueBool(),
		RestartPolicy:  containerRestartPolicy(m.Restart.ValueString(), m.MaximumRetryCount.ValueInt64()),
		DNS:            containerParseDNS(stringListFrom(ctx, m.DNS, diags), diags),
		DNSSearch:      stringListFrom(ctx, m.DNSSearch, diags),
		ExtraHosts:     stringListFrom(ctx, m.ExtraHosts, diags),
		SecurityOpt:    stringSetFrom(ctx, m.SecurityOpt, diags),
		ReadonlyRootfs: m.ReadOnly.ValueBool(),
	}
	if sc := stringMapFrom(ctx, m.Sysctls, diags); len(sc) > 0 {
		hc.Sysctls = sc
	}
	for i, h := range hc.ExtraHosts {
		if name, ip, ok := strings.Cut(h, ":"); !ok || name == "" || ip == "" {
			diags.AddAttributeError(path.Root("extra_hosts").AtListIndex(i), "Invalid extra_hosts entry", fmt.Sprintf("%q: want \"<hostname>:<ip>\"", h))
		}
	}
	hc.Memory = m.Memory.ValueInt64()
	if v, ok := knownInt64(m.MemorySwap); ok {
		hc.MemorySwap = v
	}
	hc.CPUShares = m.CPUShares.ValueInt64()
	hc.NanoCPUs = m.NanoCPUs.ValueInt64()

	var err error
	if cfg.ExposedPorts, hc.PortBindings, err = containerExpandPorts(m.Ports); err != nil {
		diags.AddAttributeError(path.Root("ports"), "Invalid ports", err.Error())
	}
	if hc.Mounts, hc.Binds, err = containerExpandMountsBinds(m.Mounts); err != nil {
		diags.AddAttributeError(path.Root("mounts"), "Invalid mounts", err.Error())
	}
	hc.Ulimits = containerExpandUlimits(m.Ulimits)
	if hc.Devices, err = containerExpandDevices(m.Devices); err != nil {
		diags.AddAttributeError(path.Root("devices"), "Invalid devices", err.Error())
	}
	if gpu, err := containerExpandGPUs(m.GPUs.ValueString()); err != nil {
		diags.AddAttributeError(path.Root("gpus"), "Invalid gpus", err.Error())
	} else if gpu != nil {
		hc.DeviceRequests = []container.DeviceRequest{*gpu}
	}
	if m.Healthcheck != nil {
		test := stringListFrom(ctx, m.Healthcheck.Test, diags)
		if len(test) == 0 {
			diags.AddAttributeError(path.Root("healthcheck").AtName("test"), "Missing healthcheck test", "healthcheck.test is required when the healthcheck block is present.")
		}
		if cfg.Healthcheck, err = containerExpandHealthcheck(test, m.Healthcheck); err != nil {
			diags.AddAttributeError(path.Root("healthcheck"), "Invalid healthcheck", err.Error())
		}
	}
	ctrApplyGap(ctx, m, cfg, hc, diags)

	netCfg, mode := ctrNetworkingConfig(ctx, ctrNetworksFrom(ctx, m.Networks, diags), m.NetworkMode.ValueString(), diags)
	hc.NetworkMode = container.NetworkMode(mode)
	return cfg, hc, netCfg
}

// containerEnsureImage pulls ref when it is not present locally.
func containerEnsureImage(ctx context.Context, pd *providerData, ref string) error {
	_, err := pd.Client.ImageInspect(ctx, ref)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	auth, err := pd.registryAuthFor(ref)
	if err != nil {
		return err
	}
	rc, err := pd.Client.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: auth})
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
	}
}

// containerWait waits until the container is healthy (or running when it has
// no healthcheck).
func containerWait(ctx context.Context, pd *providerData, id string, timeout time.Duration) error {
	return waitFor(ctx, timeout, containerPollInterval, func(ctx context.Context) (bool, error) {
		inspRes, err := pd.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return false, err
		}
		insp := inspRes.Container
		st := insp.State
		if st == nil {
			return false, nil
		}
		if !st.Running && !st.Restarting && (st.Status == "exited" || st.Status == "dead") {
			return false, fmt.Errorf("container exited (code %d) before becoming ready: %s", st.ExitCode, st.Error)
		}
		if !st.Running {
			return false, nil
		}
		if st.Health == nil || st.Health.Status == container.NoHealthcheck {
			return true, nil
		}
		return st.Health.Status == container.Healthy, nil
	})
}

func (r *containerResource) startAndWait(ctx context.Context, m containerResourceModel, diags *diag.Diagnostics) {
	id := m.ID.ValueString()
	if _, err := r.pd.Client.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		diags.AddError("Cannot start container", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
		return
	}
	if !m.Wait.ValueBool() {
		return
	}
	timeout, err := parseDurationOr(m.WaitTimeout.ValueString(), time.Minute)
	if err != nil {
		diags.AddAttributeError(path.Root("wait_timeout"), "Invalid wait_timeout", err.Error())
		return
	}
	if err := containerWait(ctx, r.pd, id, timeout); err != nil {
		diags.AddError("Container did not become ready", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
	}
}

func (r *containerResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

func (r *containerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan containerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	cfg, hc, netCfg := containerBuildCreate(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	platform, err := ctrParsePlatform(plan.Platform.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("platform"), "Invalid platform", err.Error())
		return
	}
	uploads, err := containerPrepareUploads(plan.Files)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("files"), "Invalid files", err.Error())
		return
	}
	if err := containerEnsureImage(ctx, r.pd, cfg.Image); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("image"), "Cannot pull image", fmt.Sprintf("%s: %v", cfg.Image, err))
		return
	}
	created, err := r.pd.Client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hc, NetworkingConfig: netCfg, Platform: platform, Name: plan.Name.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create container", err.Error())
		return
	}
	plan.ID = types.StringValue(created.ID)
	// Save the ID first so a failure below leaves a tainted, destroyable resource.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, created.ID)...)

	if err := containerApplyUploads(ctx, r.pd, created.ID, uploads); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("files"), "Cannot copy file into container", err.Error())
		return
	}
	r.startAndWait(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	state, found := r.refresh(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Container disappeared", "The container was removed right after it was created (is the image's process exiting with AutoRemove?).")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

func (r *containerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior containerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	prior.Running = types.BoolNull() // always report the real run state on refresh
	state, found := r.refresh(ctx, prior, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

// refresh inspects the container and maps it onto prior (state on Read, plan
// after Create/Update), including the outputs contract. found is false when
// the container is gone.
func (r *containerResource) refresh(ctx context.Context, prior containerResourceModel, diags *diag.Diagnostics) (containerResourceModel, bool) {
	inspRes, err := r.pd.Client.ContainerInspect(ctx, prior.ID.ValueString(), client.ContainerInspectOptions{})
	insp := inspRes.Container
	if isNotFound(err) {
		return prior, false
	}
	if err != nil {
		diags.AddError("Cannot inspect container", err.Error())
		return prior, false
	}
	var imgCfg *container.Config
	if img, err := r.pd.Client.ImageInspect(ctx, insp.Image); err == nil && img.Config != nil {
		ic := img.Config
		imgCfg = &container.Config{
			User: ic.User, Env: ic.Env, Cmd: ic.Cmd, Entrypoint: ic.Entrypoint, WorkingDir: ic.WorkingDir,
			Labels: ic.Labels, Healthcheck: ic.Healthcheck, StopSignal: ic.StopSignal,
		}
	}
	out := containerFromInspect(insp, imgCfg, prior)
	diags.Append(ctrFillContract(ctx, r.pd, &out, inspRes.Raw, insp)...)
	return out, true
}

// containerFromInspect maps ContainerInspect output onto prior. imgCfg is the
// image's config (nil when it could not be inspected: then only env/label
// keys already in prior are reported). An import is detected by a null
// prior image.
func containerFromInspect(insp container.InspectResponse, imgCfg *container.Config, prior containerResourceModel) containerResourceModel {
	out := prior
	importing := prior.Image.IsNull()
	cfg := insp.Config
	if cfg == nil {
		cfg = &container.Config{}
	}
	hc := &container.HostConfig{}
	if insp.ID != "" {
		out.ID = types.StringValue(insp.ID)
		out.Name = types.StringValue(strings.TrimPrefix(insp.Name, "/"))
		out.ImageID = types.StringValue(insp.Image)
		if insp.HostConfig != nil {
			hc = insp.HostConfig
		}
		if insp.State != nil {
			out.Running = types.BoolValue(insp.State.Running)
		}
	}
	if !containerKnown(prior.Running) && out.Running.IsUnknown() {
		out.Running = types.BoolValue(false)
	}
	img := imgCfg
	noImage := img == nil
	if noImage {
		img = &container.Config{}
	}
	if importing || cfg.Image != "" {
		out.Image = types.StringValue(cfg.Image)
	}
	out.Command = containerListRead(prior.Command, cfg.Cmd, img.Cmd)
	out.Entrypoint = containerListRead(prior.Entrypoint, cfg.Entrypoint, img.Entrypoint)
	out.Env = containerMapValue(prior.Env, containerSubtractDefaults(
		containerEnvToMap(cfg.Env), containerEnvToMap(img.Env), containerMapKeys(prior.Env), noImage && !importing))
	labels := userLabels(cfg.Labels, false)
	out.Labels = containerMapValue(prior.Labels, containerSubtractDefaults(labels, img.Labels, containerMapKeys(prior.Labels), noImage && !importing))
	out.User = containerStringRead(prior.User, cfg.User, img.User)
	out.WorkingDir = containerStringRead(prior.WorkingDir, cfg.WorkingDir, img.WorkingDir)
	out.Healthcheck = containerFlattenHealthcheck(cfg.Healthcheck, img.Healthcheck, prior.Healthcheck)

	out.Ports = containerFlattenPorts(hc.PortBindings, prior.Ports)
	out.Mounts = containerFlattenMounts(hc.Mounts, hc.Binds, prior.Mounts)
	out.Ulimits = containerFlattenUlimits(hc.Ulimits, prior.Ulimits, importing)
	var eps map[string]*network.EndpointSettings
	if insp.NetworkSettings != nil {
		eps = insp.NetworkSettings.Networks
	}
	out.Networks = ctrFlattenNetworks(eps, string(hc.NetworkMode), containerKnown(prior.NetworkMode), out.ID.ValueString(), out.Name.ValueString(), prior.Networks)

	rp := string(hc.RestartPolicy.Name)
	if rp == "" {
		rp = "no"
	}
	out.Restart = types.StringValue(rp)
	switch {
	case rp == "on-failure":
		out.MaximumRetryCount = types.Int64Value(int64(hc.RestartPolicy.MaximumRetryCount))
	case containerKnown(prior.MaximumRetryCount):
		out.MaximumRetryCount = prior.MaximumRetryCount // ignored by the daemon for other policies
	default:
		out.MaximumRetryCount = types.Int64Value(0)
	}
	out.Privileged = types.BoolValue(hc.Privileged)
	out.Memory = containerInt64Read(prior.Memory, hc.Memory)
	out.MemorySwap = types.Int64Value(hc.MemorySwap)
	out.CPUShares = containerInt64Read(prior.CPUShares, hc.CPUShares)
	out.NanoCPUs = containerInt64Read(prior.NanoCPUs, hc.NanoCPUs)

	out.Hostname = containerHostnameRead(prior.Hostname, cfg.Hostname, out.ID.ValueString())
	out.DNS = containerListRead(prior.DNS, addrStrings(hc.DNS), nil)
	out.DNSSearch = containerListRead(prior.DNSSearch, hc.DNSSearch, nil)
	out.ExtraHosts = containerListRead(prior.ExtraHosts, hc.ExtraHosts, nil)
	out.Devices = containerFlattenDevices(hc.Devices, prior.Devices)
	out.Tty = types.BoolValue(cfg.Tty)
	out.StdinOpen = types.BoolValue(cfg.OpenStdin)
	out.StopSignal = containerStringRead(prior.StopSignal, cfg.StopSignal, img.StopSignal)
	out.StopTimeout = containerStopTimeoutRead(cfg.StopTimeout)
	out.Sysctls = containerSysctlsRead(prior.Sysctls, hc.Sysctls, importing)
	out.SecurityOpt = containerSecurityOptRead(prior.SecurityOpt, hc.SecurityOpt)
	out.ReadOnly = types.BoolValue(hc.ReadonlyRootfs)
	ctrReadGap(insp, cfg, hc, prior, &out, importing)

	if !containerKnown(out.MustRun) {
		out.MustRun = types.BoolValue(true)
	}
	if !containerKnown(out.Wait) {
		out.Wait = types.BoolValue(false)
	}
	if !containerKnown(out.WaitTimeout) {
		out.WaitTimeout = types.StringValue(containerDefaultWaitTimeout)
	}
	return out
}

func containerSetStrings(s types.Set) []string {
	if !containerKnown(s) {
		return nil
	}
	out := make([]string, 0, len(s.Elements()))
	for _, e := range s.Elements() {
		if v, ok := e.(types.String); ok {
			out = append(out, v.ValueString())
		}
	}
	return out
}

func (r *containerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state containerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	id := state.ID.ValueString()
	plan.ID = state.ID

	if !plan.Name.Equal(state.Name) {
		if _, err := r.pd.Client.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: plan.Name.ValueString()}); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Cannot rename container", err.Error())
			return
		}
	}

	if !plan.Restart.Equal(state.Restart) || !plan.MaximumRetryCount.Equal(state.MaximumRetryCount) ||
		!plan.Memory.Equal(state.Memory) || !plan.MemorySwap.Equal(state.MemorySwap) ||
		!plan.CPUShares.Equal(state.CPUShares) || !plan.NanoCPUs.Equal(state.NanoCPUs) ||
		!plan.CPUPeriod.Equal(state.CPUPeriod) || !plan.CPUQuota.Equal(state.CPUQuota) ||
		!plan.CpusetCpus.Equal(state.CpusetCpus) || !plan.MemoryReservation.Equal(state.MemoryReservation) {
		uc := container.UpdateConfig{RestartPolicy: containerRestartPolicy(plan.Restart.ValueString(), plan.MaximumRetryCount.ValueInt64())}
		uc.Memory = plan.Memory.ValueInt64()
		if v, ok := knownInt64(plan.MemorySwap); ok {
			uc.MemorySwap = v
		} else if uc.Memory > 0 {
			// memory changed and memory_swap is unset: mirror the daemon's
			// create-time default (2x memory) so the new limit is accepted.
			uc.MemorySwap = 2 * uc.Memory
		}
		uc.CPUShares = plan.CPUShares.ValueInt64()
		uc.NanoCPUs = plan.NanoCPUs.ValueInt64()
		uc.CPUPeriod = plan.CPUPeriod.ValueInt64()
		uc.CPUQuota = plan.CPUQuota.ValueInt64()
		uc.CpusetCpus = plan.CpusetCpus.ValueString()
		uc.MemoryReservation = plan.MemoryReservation.ValueInt64()
		if _, err := r.pd.Client.ContainerUpdate(ctx, id, client.ContainerUpdateOptions{Resources: &uc.Resources, RestartPolicy: &uc.RestartPolicy}); err != nil {
			resp.Diagnostics.AddError("Cannot update container", err.Error())
			return
		}
	}

	if !plan.Networks.Equal(state.Networks) {
		ctrUpdateNetworks(ctx, r.pd, id, ctrNetworksFrom(ctx, plan.Networks, &resp.Diagnostics), ctrNetworksFrom(ctx, state.Networks, &resp.Diagnostics), &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	if !containerUploadsEqual(plan.Files, state.Files) {
		// Reached only for a stopped container (containerUploadReplaceIf).
		uploads, err := containerPrepareUploads(plan.Files)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("files"), "Invalid files", err.Error())
			return
		}
		if err := containerApplyUploads(ctx, r.pd, id, uploads); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("files"), "Cannot copy file into container", err.Error())
			return
		}
	}

	inspRes, err := r.pd.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect container", err.Error())
		return
	}
	insp := inspRes.Container
	if plan.MustRun.ValueBool() && (insp.State == nil || !insp.State.Running) {
		r.startAndWait(ctx, plan, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	planned := plan.Running
	out, found := r.refresh(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Container disappeared", "The container was removed during update.")
		return
	}
	if containerKnown(planned) {
		// Keep the planned value; the next refresh reports the real state.
		out.Running = planned
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, out.ID.ValueString())...)
}

func (r *containerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state containerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	_, err := r.pd.Client.ContainerRemove(ctx, state.ID.ValueString(), client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove container", err.Error())
	}
}

func (r *containerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "Cannot import before the provider is configured.")
		return
	}
	ref := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	inspRes, err := r.pd.Client.ContainerInspect(ctx, ref, client.ContainerInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot find container", fmt.Sprintf("%q: %v", ref, err))
		return
	}
	insp := inspRes.Container
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), insp.ID)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, insp.ID)...)
}
