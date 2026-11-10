package provider

// Schema and model shared by docker_service and docker_stack's service block.

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// svcSpecModel is every service attribute except docker_service's id.
type svcSpecModel struct {
	Name            types.String        `tfsdk:"name"`
	Image           types.String        `tfsdk:"image"`
	Mode            types.String        `tfsdk:"mode"`
	Replicas        types.Int64         `tfsdk:"replicas"`
	Env             types.Map           `tfsdk:"env"`
	Args            types.List          `tfsdk:"args"`
	Command         types.List          `tfsdk:"command"`
	Networks        types.Map           `tfsdk:"networks"`
	Labels          types.Map           `tfsdk:"labels"`
	ContainerLabels types.Map           `tfsdk:"container_labels"`
	Mounts          []svcMountModel     `tfsdk:"mounts"`
	EndpointSpec    *svcEndpointModel   `tfsdk:"endpoint_spec"`
	Secrets         []svcSecretRefModel `tfsdk:"secrets"`
	Configs         []svcConfigRefModel `tfsdk:"configs"`
	UpdateConfig    *svcUpdateModel     `tfsdk:"update_config"`
	RollbackConfig  *svcUpdateModel     `tfsdk:"rollback_config"`
	RestartPolicy   *svcRestartModel    `tfsdk:"restart_policy"`
	Placement       *svcPlacementModel  `tfsdk:"placement"`
	Resources       *svcResourcesModel  `tfsdk:"resources"`
	Healthcheck     *svcHealthModel     `tfsdk:"healthcheck"`
	Logging         *svcLoggingModel    `tfsdk:"logging"`
	ConvergeConfig  *svcConvergeModel   `tfsdk:"converge_config"`

	// Pass 2 additions.
	MaxConcurrent    types.Int64      `tfsdk:"max_concurrent"`
	TotalCompletions types.Int64      `tfsdk:"total_completions"`
	Hostname         types.String     `tfsdk:"hostname"`
	User             types.String     `tfsdk:"user"`
	Dir              types.String     `tfsdk:"dir"`
	Hosts            types.List       `tfsdk:"hosts"`
	DNSConfig        *svcDNSModel     `tfsdk:"dns_config"`
	CapAdd           types.Set        `tfsdk:"cap_add"`
	CapDrop          types.Set        `tfsdk:"cap_drop"`
	Sysctls          types.Map        `tfsdk:"sysctls"`
	Ulimits          []svcUlimitModel `tfsdk:"ulimits"`
	StopGracePeriod  types.String     `tfsdk:"stop_grace_period"`
	ForceUpdate      types.Int64      `tfsdk:"force_update"`

	// Pass 5 additions (renames.json gap rows; upstream ContainerSpec /
	// TaskSpec names).
	Groups     types.List          `tfsdk:"groups"`
	Init       types.Bool          `tfsdk:"init"`
	Isolation  types.String        `tfsdk:"isolation"`
	ReadOnly   types.Bool          `tfsdk:"read_only"`
	StopSignal types.String        `tfsdk:"stop_signal"`
	Runtime    types.String        `tfsdk:"runtime"`
	Privileges *svcPrivilegesModel `tfsdk:"privileges"`

	// Pass 5.3 additions (field-gaps/S.json; upstream ContainerSpec names).
	TTY         types.Bool  `tfsdk:"tty"`
	OpenStdin   types.Bool  `tfsdk:"open_stdin"`
	OomScoreAdj types.Int64 `tfsdk:"oom_score_adj"`
}

// svcPrivilegesModel is ContainerSpec.Privileges (credential spec and
// SELinux context).
type svcPrivilegesModel struct {
	CredentialSpec  *svcCredentialSpecModel `tfsdk:"credential_spec"`
	SELinuxContext  *svcSELinuxContextModel `tfsdk:"selinux_context"`
	Seccomp         *svcSeccompModel        `tfsdk:"seccomp"`
	AppArmor        *svcAppArmorModel       `tfsdk:"app_armor"`
	NoNewPrivileges types.Bool              `tfsdk:"no_new_privileges"`
}

// svcSeccompModel is Privileges.Seccomp.
type svcSeccompModel struct {
	Mode    types.String `tfsdk:"mode"`
	Profile types.String `tfsdk:"profile"`
}

// svcAppArmorModel is Privileges.AppArmor.
type svcAppArmorModel struct {
	Mode types.String `tfsdk:"mode"`
}

type svcCredentialSpecModel struct {
	Config   types.String `tfsdk:"config"`
	File     types.String `tfsdk:"file"`
	Registry types.String `tfsdk:"registry"`
}

type svcSELinuxContextModel struct {
	Disable types.Bool   `tfsdk:"disable"`
	User    types.String `tfsdk:"user"`
	Role    types.String `tfsdk:"role"`
	Type    types.String `tfsdk:"type"`
	Level   types.String `tfsdk:"level"`
}

type svcDNSModel struct {
	Nameservers types.List `tfsdk:"nameservers"`
	Search      types.List `tfsdk:"search"`
	Options     types.List `tfsdk:"options"`
}

type svcUlimitModel struct {
	Name types.String `tfsdk:"name"`
	Soft types.Int64  `tfsdk:"soft"`
	Hard types.Int64  `tfsdk:"hard"`
}

// svcNetworkAttachModel is one value of the networks map (keyed by network
// name or ID): TaskTemplate.Networks[] without its Target.
type svcNetworkAttachModel struct {
	Aliases    types.Set `tfsdk:"aliases"`
	DriverOpts types.Map `tfsdk:"driver_opts"`
}

// svcNetworkAttachAttrTypes is the object type of a networks map value.
var svcNetworkAttachAttrTypes = map[string]attr.Type{
	"aliases":     types.SetType{ElemType: types.StringType},
	"driver_opts": types.MapType{ElemType: types.StringType},
}

type svcMountModel struct {
	Type          types.String           `tfsdk:"type"`
	Source        types.String           `tfsdk:"source"`
	Target        types.String           `tfsdk:"target"`
	ReadOnly      types.Bool             `tfsdk:"read_only"`
	BindOptions   *svcBindOptionsModel   `tfsdk:"bind_options"`
	TmpfsOptions  *svcTmpfsOptionsModel  `tfsdk:"tmpfs_options"`
	VolumeOptions *svcVolumeOptionsModel `tfsdk:"volume_options"`
}

// svcBindOptionsModel is Mount.BindOptions.
type svcBindOptionsModel struct {
	Propagation  types.String `tfsdk:"propagation"`
	NonRecursive types.Bool   `tfsdk:"non_recursive"`
}

// svcTmpfsOptionsModel is Mount.TmpfsOptions.
type svcTmpfsOptionsModel struct {
	SizeBytes types.Int64 `tfsdk:"size_bytes"`
	Mode      types.Int64 `tfsdk:"mode"`
	Options   types.List  `tfsdk:"options"`
}

// svcVolumeOptionsModel is Mount.VolumeOptions.
type svcVolumeOptionsModel struct {
	NoCopy       types.Bool            `tfsdk:"no_copy"`
	Labels       types.Map             `tfsdk:"labels"`
	DriverConfig *svcDriverConfigModel `tfsdk:"driver_config"`
}

// svcDriverConfigModel is Mount.VolumeOptions.DriverConfig.
type svcDriverConfigModel struct {
	Name    types.String `tfsdk:"name"`
	Options types.Map    `tfsdk:"options"`
}

type svcEndpointModel struct {
	Mode  types.String   `tfsdk:"mode"`
	Ports []svcPortModel `tfsdk:"ports"`
}

type svcPortModel struct {
	Name          types.String `tfsdk:"name"`
	TargetPort    types.Int64  `tfsdk:"target_port"`
	PublishedPort types.Int64  `tfsdk:"published_port"`
	Protocol      types.String `tfsdk:"protocol"`
	PublishMode   types.String `tfsdk:"publish_mode"`
}

type svcSecretRefModel struct {
	SecretID   types.String `tfsdk:"secret_id"`
	SecretName types.String `tfsdk:"secret_name"`
	FileName   types.String `tfsdk:"file_name"`
	UID        types.String `tfsdk:"uid"`
	GID        types.String `tfsdk:"gid"`
	Mode       types.Int64  `tfsdk:"mode"`
}

type svcConfigRefModel struct {
	ConfigID   types.String `tfsdk:"config_id"`
	ConfigName types.String `tfsdk:"config_name"`
	FileName   types.String `tfsdk:"file_name"`
	UID        types.String `tfsdk:"uid"`
	GID        types.String `tfsdk:"gid"`
	Mode       types.Int64  `tfsdk:"mode"`
	Runtime    types.Bool   `tfsdk:"runtime"`
}

type svcUpdateModel struct {
	Parallelism     types.Int64   `tfsdk:"parallelism"`
	Delay           types.String  `tfsdk:"delay"`
	FailureAction   types.String  `tfsdk:"failure_action"`
	Monitor         types.String  `tfsdk:"monitor"`
	MaxFailureRatio types.Float64 `tfsdk:"max_failure_ratio"`
	Order           types.String  `tfsdk:"order"`
}

type svcRestartModel struct {
	Condition   types.String `tfsdk:"condition"`
	Delay       types.String `tfsdk:"delay"`
	MaxAttempts types.Int64  `tfsdk:"max_attempts"`
	Window      types.String `tfsdk:"window"`
}

type svcPlacementModel struct {
	Constraints types.List         `tfsdk:"constraints"`
	Preferences types.List         `tfsdk:"preferences"`
	MaxReplicas types.Int64        `tfsdk:"max_replicas"`
	Platforms   []svcPlatformModel `tfsdk:"platforms"`
}

// svcPlatformModel is one Placement.Platforms entry.
type svcPlatformModel struct {
	Architecture types.String `tfsdk:"architecture"`
	OS           types.String `tfsdk:"os"`
}

type svcResourcesModel struct {
	Limits           *svcResourceAmountModel `tfsdk:"limits"`
	Reservations     *svcReservationModel    `tfsdk:"reservations"`
	SwapBytes        types.Int64             `tfsdk:"swap_bytes"`
	MemorySwappiness types.Int64             `tfsdk:"memory_swappiness"`
}

// svcReservationModel is Resources.Reservations: an amount plus generic
// resources.
type svcReservationModel struct {
	NanoCPUs         types.Int64               `tfsdk:"nano_cpus"`
	MemoryBytes      types.Int64               `tfsdk:"memory_bytes"`
	GenericResources []svcGenericResourceModel `tfsdk:"generic_resources"`
}

// svcGenericResourceModel is one GenericResources entry: exactly one of the
// named (string value) and discrete (integer value) specs.
type svcGenericResourceModel struct {
	NamedResourceSpec    *svcNamedResourceModel    `tfsdk:"named_resource_spec"`
	DiscreteResourceSpec *svcDiscreteResourceModel `tfsdk:"discrete_resource_spec"`
}

type svcNamedResourceModel struct {
	Kind  types.String `tfsdk:"kind"`
	Value types.String `tfsdk:"value"`
}

type svcDiscreteResourceModel struct {
	Kind  types.String `tfsdk:"kind"`
	Value types.Int64  `tfsdk:"value"`
}

type svcResourceAmountModel struct {
	NanoCPUs    types.Int64 `tfsdk:"nano_cpus"`
	MemoryBytes types.Int64 `tfsdk:"memory_bytes"`
	Pids        types.Int64 `tfsdk:"pids"`
}

type svcHealthModel struct {
	Test          types.List   `tfsdk:"test"`
	Interval      types.String `tfsdk:"interval"`
	Timeout       types.String `tfsdk:"timeout"`
	StartPeriod   types.String `tfsdk:"start_period"`
	StartInterval types.String `tfsdk:"start_interval"`
	Retries       types.Int64  `tfsdk:"retries"`
}

type svcLoggingModel struct {
	Driver  types.String `tfsdk:"driver"`
	Options types.Map    `tfsdk:"options"`
}

type svcConvergeModel struct {
	Timeout types.String `tfsdk:"timeout"`
	Delay   types.String `tfsdk:"delay"`
}

// svcModes are the accepted values of mode.
var svcModes = []string{"replicated", "global", "replicated_job", "global_job"}

const (
	svcDefaultSecretMode   = 0o444
	svcDefaultConvergeWait = "3m"
	svcDefaultConvergeTick = "5s"
)

func svcDurationValidator() validator.String { return svcDurationStringValidator{} }

// svcSchema returns the attributes and blocks of a service. topLevel is true
// for docker_service (adds id, RequiresReplace on name/mode); false for the
// repeated service block of docker_stack, where names are short and the
// stack itself decides when a service must be recreated.
func svcSchema(topLevel bool) (map[string]schema.Attribute, map[string]schema.Block) {
	nameDesc := "Service name. Changing this forces a new resource."
	modeDesc := "`replicated` (default; runs `replicas` tasks), `global` (one task per eligible node), `replicated_job` (runs " +
		"`total_completions` tasks to completion, `max_concurrent` at a time) or `global_job` (one run-to-completion task per eligible " +
		"node). The Engine API cannot change a service's mode type, so changing this forces replacement."
	var namePM, modePM []planmodifier.String
	if topLevel {
		namePM = []planmodifier.String{stringplanmodifier.RequiresReplace()}
		modePM = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	} else {
		nameDesc = "Short service name inside the stack; the Swarm service is named `<stack>_<name>`. Renaming removes the old service and creates a new one."
		modeDesc = "`replicated` (default), `global`, `replicated_job` or `global_job` (see `docker_service`). Changing it recreates this service."
	}
	attrs := map[string]schema.Attribute{
		"name": schema.StringAttribute{Required: true, MarkdownDescription: nameDesc, PlanModifiers: namePM},
		"image": schema.StringAttribute{
			Required: true,
			MarkdownDescription: "Image reference the tasks run. The provider asks the registry for the digest and the daemon pins " +
				"tasks to it (`@sha256:...`); that pin is ignored on refresh when this value has no digest. Credentials come from " +
				"the provider's `registry_auth`. Updated in place (rolling update).",
		},
		"mode": schema.StringAttribute{
			Optional: true, Computed: true, Default: stringdefault.StaticString("replicated"),
			MarkdownDescription: modeDesc,
			PlanModifiers:       modePM,
			Validators:          []validator.String{stringvalidator.OneOf(svcModes...)},
		},
		"replicas": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(1),
			MarkdownDescription: "Number of tasks for `replicated` mode (default 1); ignored by the other modes. Updated in place.",
		},
		"max_concurrent": schema.Int64Attribute{Optional: true,
			MarkdownDescription: "`replicated_job` only: tasks run at the same time (daemon default 1). Updated in place (re-runs the job)."},
		"total_completions": schema.Int64Attribute{Optional: true,
			MarkdownDescription: "`replicated_job` only: tasks that must complete successfully (daemon default: `max_concurrent`). " +
				"Updated in place (re-runs the job)."},
		"hostname": schema.StringAttribute{Optional: true,
			MarkdownDescription: "Hostname of the task containers (`ContainerSpec.Hostname`; Go templates such as `{{.Task.Name}}` work). " +
				"Unset: the container ID prefix. Updated in place (rolling update)."},
		"user": schema.StringAttribute{Optional: true,
			MarkdownDescription: "User (`uid`, `name`, optionally `:group`) the task process runs as. Unset uses the image's. Updated in place."},
		"dir": schema.StringAttribute{Optional: true,
			MarkdownDescription: "Working directory of the task process. Unset uses the image's. Updated in place."},
		"hosts": schema.ListAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Extra `/etc/hosts` entries, each `\"<hostname>:<ip>\"` like `docker service create --host`. The provider " +
				"sends them in the daemon's `\"<ip> <hostname>\"` (hosts(5)) format; an entry that already contains a space is sent as is. " +
				"Updated in place."},
		"cap_add": schema.SetAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Linux capabilities to add to the task containers, e.g. `NET_ADMIN` (case and `CAP_` prefix are ignored when " +
				"comparing). Updated in place."},
		"cap_drop": schema.SetAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Linux capabilities to drop, e.g. `ALL`. Updated in place."},
		"sysctls": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Namespaced kernel parameters for the task containers, e.g. `{ \"net.core.somaxconn\" = \"1024\" }`. Updated in place."},
		"stop_grace_period": schema.StringAttribute{Optional: true, Validators: []validator.String{svcDurationValidator()},
			MarkdownDescription: "Time to wait after the stop signal before killing a task (Go duration, e.g. `30s`; daemon default 10s). Updated in place."},
		"force_update": schema.Int64Attribute{Optional: true,
			MarkdownDescription: "Counter (`TaskTemplate.ForceUpdate`): change it (e.g. increment) to force a rolling restart of every task even " +
				"when nothing else changed, like `docker service update --force`. Unset leaves the daemon's counter alone and ignores it on " +
				"refresh. Updated in place."},
		"env": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Environment variables of the task containers."},
		"args": schema.ListAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Arguments passed to the command (overrides the image `CMD`)."},
		"command": schema.ListAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Command to run (overrides the image `ENTRYPOINT`)."},
		"networks": schema.MapNestedAttribute{Optional: true,
			MarkdownDescription: "Networks the tasks attach to (`TaskTemplate.Networks`), keyed by network name or ID (resolved to the ID, the " +
				"attachment's `Target`); use `{}` as the value for a plain attachment. In a stack, the short name of a stack `network` block " +
				"resolves to `<stack>_<name>`. Updated in place.",
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"aliases": schema.SetAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Extra DNS names the service gets on this network (`Aliases`); the service name always resolves."},
				"driver_opts": schema.MapAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Driver options of this attachment (`DriverOpts`)."},
			}}},
		"groups": schema.ListAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Additional groups the task process runs with (`ContainerSpec.Groups`). Updated in place."},
		"init": schema.BoolAttribute{Optional: true,
			MarkdownDescription: "Run an init process inside the task containers that forwards signals and reaps processes (`ContainerSpec.Init`). " +
				"Unset uses the daemon default. Updated in place."},
		"isolation": schema.StringAttribute{Optional: true,
			Validators: []validator.String{stringvalidator.OneOf("default", "process", "hyperv")},
			MarkdownDescription: "Isolation technology of the task containers (`ContainerSpec.Isolation`): `default`, `process` or `hyperv` " +
				"(Windows only). Updated in place."},
		"read_only": schema.BoolAttribute{Optional: true,
			MarkdownDescription: "Mount the task containers' root filesystem read-only (`ContainerSpec.ReadOnly`). Updated in place."},
		"stop_signal": schema.StringAttribute{Optional: true,
			MarkdownDescription: "Signal that stops the task containers, e.g. `SIGTERM` (`ContainerSpec.StopSignal`). Unset uses the image's. Updated in place."},
		"runtime": schema.StringAttribute{Optional: true,
			Validators: []validator.String{stringvalidator.OneOf("container", "plugin", "attachment")},
			MarkdownDescription: "Runtime of the tasks (`TaskTemplate.Runtime`). Only `container` (the default) is useful here: this type always " +
				"sends a `ContainerSpec`, and the `plugin` and `attachment` runtimes need a plugin or network-attachment spec instead. Updated in place."},
		"tty": schema.BoolAttribute{Optional: true,
			MarkdownDescription: "Allocate a pseudo-TTY for the task containers (`ContainerSpec.TTY`, `docker service create --tty`). Updated in place."},
		"open_stdin": schema.BoolAttribute{Optional: true,
			MarkdownDescription: "Keep the task containers' standard input open (`ContainerSpec.OpenStdin`). Updated in place."},
		"oom_score_adj": schema.Int64Attribute{Optional: true,
			Validators: []validator.Int64{int64validator.Between(-1000, 1000)},
			MarkdownDescription: "OOM killer score adjustment of the task containers, -1000 to 1000 (`ContainerSpec.OomScoreAdj`, " +
				"`docker service create --oom-score-adj`). Updated in place." + apiSince("1.46")},
		"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Labels on the service object."},
		"container_labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Labels on the task containers."},
	}
	blocks := map[string]schema.Block{
		"privileges": schema.SingleNestedBlock{
			MarkdownDescription: "Security options of the task containers (`ContainerSpec.Privileges`). Updated in place.",
			Blocks: map[string]schema.Block{
				"credential_spec": schema.SingleNestedBlock{
					MarkdownDescription: "Managed service account credential spec (`CredentialSpec`, Windows only); set one of `config`, `file` and `registry`.",
					Attributes: map[string]schema.Attribute{
						"config":   schema.StringAttribute{Optional: true, MarkdownDescription: "ID of a swarm config holding the credential spec (`Config`)."},
						"file":     schema.StringAttribute{Optional: true, MarkdownDescription: "Credential spec file name under the daemon's `CredentialSpecs` directory (`File`)."},
						"registry": schema.StringAttribute{Optional: true, MarkdownDescription: "Registry value name holding the credential spec (`Registry`)."},
					},
				},
				"selinux_context": schema.SingleNestedBlock{
					MarkdownDescription: "SELinux labels of the task containers (`SELinuxContext`).",
					Attributes: map[string]schema.Attribute{
						"disable": schema.BoolAttribute{Optional: true, MarkdownDescription: "Disable SELinux labeling (`Disable`)."},
						"user":    schema.StringAttribute{Optional: true, MarkdownDescription: "SELinux user label (`User`)."},
						"role":    schema.StringAttribute{Optional: true, MarkdownDescription: "SELinux role label (`Role`)."},
						"type":    schema.StringAttribute{Optional: true, MarkdownDescription: "SELinux type label (`Type`)."},
						"level":   schema.StringAttribute{Optional: true, MarkdownDescription: "SELinux level label (`Level`)."},
					},
				},
				"seccomp": schema.SingleNestedBlock{
					MarkdownDescription: "Seccomp filtering of the task containers (`Seccomp`)." + apiSince("1.44"),
					Attributes: map[string]schema.Attribute{
						"mode": schema.StringAttribute{Optional: true,
							Validators:          []validator.String{stringvalidator.OneOf("default", "unconfined", "custom")},
							MarkdownDescription: "`default` (the daemon's profile), `unconfined` (no filtering) or `custom` (the JSON in `profile`) (`Mode`)."},
						"profile": schema.StringAttribute{Optional: true,
							MarkdownDescription: "Seccomp profile JSON, used with `mode = \"custom\"` (`Profile`); e.g. `file(\"seccomp.json\")`."},
					},
				},
				"app_armor": schema.SingleNestedBlock{
					MarkdownDescription: "AppArmor confinement of the task containers (`AppArmor`); custom profiles are not supported by Swarm." + apiSince("1.44"),
					Attributes: map[string]schema.Attribute{
						"mode": schema.StringAttribute{Optional: true,
							Validators:          []validator.String{stringvalidator.OneOf("default", "disabled")},
							MarkdownDescription: "`default` (the daemon's profile) or `disabled` (`Mode`)."},
					},
				},
			},
			Attributes: map[string]schema.Attribute{
				"no_new_privileges": schema.BoolAttribute{Optional: true,
					MarkdownDescription: "Set the no_new_privs bit, so task processes cannot gain privileges (setuid, file capabilities) " +
						"(`NoNewPrivileges`)." + apiSince("1.44")},
			},
		},
		"dns_config": schema.SingleNestedBlock{
			MarkdownDescription: "Resolver configuration of the task containers (`ContainerSpec.DNSConfig`). Updated in place.",
			Attributes: map[string]schema.Attribute{
				"nameservers": schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "DNS server IPs."},
				"search":      schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "DNS search domains."},
				"options":     schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "resolv.conf options, e.g. `ndots:2`."},
			},
		},
		"ulimits": schema.ListNestedBlock{
			MarkdownDescription: "Resource ulimits of the task containers. Updated in place.",
			NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{Required: true, MarkdownDescription: "Ulimit name, e.g. `nofile`."},
				"soft": schema.Int64Attribute{Required: true, MarkdownDescription: "Soft limit."},
				"hard": schema.Int64Attribute{Required: true, MarkdownDescription: "Hard limit."},
			}},
		},
		"mounts": schema.ListNestedBlock{
			MarkdownDescription: "Mounts in the task containers.",
			NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
				"type": schema.StringAttribute{Required: true, MarkdownDescription: "`bind`, `volume` or `tmpfs`.",
					Validators: []validator.String{stringvalidator.OneOf("bind", "volume", "tmpfs")}},
				"source": schema.StringAttribute{Optional: true, MarkdownDescription: "Host path (`bind`) or volume name (`volume`; " +
					"created on each node by the volume driver if missing). In a stack, the short name of a stack `volume` block " +
					"resolves to `<stack>_<name>` with that block's driver, options and labels."},
				"target":    schema.StringAttribute{Required: true, MarkdownDescription: "Path inside the container."},
				"read_only": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Mount read-only."},
			}, Blocks: map[string]schema.Block{
				"bind_options": schema.SingleNestedBlock{
					MarkdownDescription: "Options of a `bind` mount (`Mount.BindOptions`).",
					Attributes: map[string]schema.Attribute{
						"propagation": schema.StringAttribute{Optional: true,
							Validators:          []validator.String{stringvalidator.OneOf("private", "rprivate", "shared", "rshared", "slave", "rslave")},
							MarkdownDescription: "Bind propagation: `private`, `rprivate`, `shared`, `rshared`, `slave` or `rslave` (`Propagation`)."},
						"non_recursive": schema.BoolAttribute{Optional: true,
							MarkdownDescription: "Do not bind-mount submounts of the source recursively (`NonRecursive`, `bind-nonrecursive`)." + apiSince("1.41")},
					},
				},
				"tmpfs_options": schema.SingleNestedBlock{
					MarkdownDescription: "Options of a `tmpfs` mount (`Mount.TmpfsOptions`).",
					Attributes: map[string]schema.Attribute{
						"size_bytes": schema.Int64Attribute{Optional: true, MarkdownDescription: "Size of the tmpfs in bytes (`SizeBytes`); unset is unlimited."},
						"mode":       schema.Int64Attribute{Optional: true, MarkdownDescription: "Permission bits of the tmpfs as a decimal number, e.g. `448` for `0700` (`Mode`)."},
						"options": schema.ListAttribute{ElementType: types.ListType{ElemType: types.StringType}, Optional: true,
							MarkdownDescription: "Extra tmpfs mount options (`Options`): each entry is a one-element list for a flag, e.g. " +
								"`[\"exec\"]`, or a two-element key/value list, e.g. `[\"nr_inodes\", \"1000\"]`." + apiSince("1.46")},
					},
				},
				"volume_options": schema.SingleNestedBlock{
					MarkdownDescription: "Options of a `volume` mount, used when the volume does not exist yet on the node (`Mount.VolumeOptions`).",
					Attributes: map[string]schema.Attribute{
						"no_copy": schema.BoolAttribute{Optional: true, MarkdownDescription: "Do not populate a new volume with the image's data at the target (`NoCopy`)."},
						"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
							MarkdownDescription: "Labels for a volume created on first use (`Labels`)."},
					},
					Blocks: map[string]schema.Block{
						"driver_config": schema.SingleNestedBlock{
							MarkdownDescription: "Volume driver for a volume created on first use (`DriverConfig`).",
							Attributes: map[string]schema.Attribute{
								"name":    schema.StringAttribute{Optional: true, MarkdownDescription: "Volume driver name, e.g. `local` (`Name`)."},
								"options": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Driver options (`Options`)."},
							},
						},
					},
				},
			}},
		},
		"endpoint_spec": schema.SingleNestedBlock{
			MarkdownDescription: "How the service is reached: resolution mode and published ports.",
			Attributes: map[string]schema.Attribute{
				"mode": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("vip"),
					MarkdownDescription: "`vip` (default, a virtual IP load-balances tasks) or `dnsrr` (DNS round robin).",
					Validators:          []validator.String{stringvalidator.OneOf("vip", "dnsrr")}},
			},
			Blocks: map[string]schema.Block{
				"ports": schema.ListNestedBlock{
					MarkdownDescription: "Published ports.",
					NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
						"name":           schema.StringAttribute{Optional: true, MarkdownDescription: "Name of the port mapping (`Name`)."},
						"target_port":    schema.Int64Attribute{Required: true, MarkdownDescription: "Port inside the tasks."},
						"published_port": schema.Int64Attribute{Optional: true, MarkdownDescription: "Port published on the nodes. Unset lets Swarm choose one (30000+)."},
						"protocol": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("tcp"),
							MarkdownDescription: "`tcp` (default), `udp` or `sctp`.",
							Validators:          []validator.String{stringvalidator.OneOf("tcp", "udp", "sctp")}},
						"publish_mode": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("ingress"),
							MarkdownDescription: "`ingress` (default, routing mesh on every node) or `host` (only on nodes running a task).",
							Validators:          []validator.String{stringvalidator.OneOf("ingress", "host")}},
					}},
				},
			},
		},
		"secrets":         svcFileRefBlock("secret"),
		"configs":         svcConfigRefBlock(),
		"update_config":   svcUpdateBlock("How a changed service rolls out to its tasks."),
		"rollback_config": svcUpdateBlock("How a failed update rolls back."),
		"restart_policy": schema.SingleNestedBlock{
			MarkdownDescription: "When Swarm restarts tasks that exit.",
			Attributes: map[string]schema.Attribute{
				"condition": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("any"),
					MarkdownDescription: "`none`, `on-failure` or `any` (default).",
					Validators:          []validator.String{stringvalidator.OneOf("none", "on-failure", "any")}},
				"delay":        schema.StringAttribute{Optional: true, MarkdownDescription: "Delay between restart attempts (Go duration, e.g. `5s`).", Validators: []validator.String{svcDurationValidator()}},
				"max_attempts": schema.Int64Attribute{Optional: true, MarkdownDescription: "Maximum restarts before giving up; 0 or unset means unlimited."},
				"window":       schema.StringAttribute{Optional: true, MarkdownDescription: "Window used to evaluate the restart policy (Go duration).", Validators: []validator.String{svcDurationValidator()}},
			},
		},
		"placement": schema.SingleNestedBlock{
			MarkdownDescription: "Where tasks may run.",
			Attributes: map[string]schema.Attribute{
				"constraints": schema.ListAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Constraints such as `node.role==manager` or `node.labels.zone==a`."},
				"preferences": schema.ListAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Spread preferences, each `spread=<descriptor>` such as `spread=node.labels.zone`."},
				"max_replicas": schema.Int64Attribute{Optional: true, MarkdownDescription: "Maximum tasks per node; 0 or unset means unlimited."},
			},
			Blocks: map[string]schema.Block{
				"platforms": schema.ListNestedBlock{
					MarkdownDescription: "Platforms the tasks may run on (`Placement.Platforms`). Unset lets the daemon fill them from the image " +
						"manifest; that daemon value is then not shown.",
					NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
						"architecture": schema.StringAttribute{Required: true, MarkdownDescription: "CPU architecture, e.g. `amd64` or `arm64` (`Architecture`)."},
						"os":           schema.StringAttribute{Required: true, MarkdownDescription: "Operating system, e.g. `linux` (`OS`)."},
					}},
				},
			},
		},
		"resources": schema.SingleNestedBlock{
			MarkdownDescription: "CPU and memory limits and reservations per task.",
			Attributes: map[string]schema.Attribute{
				"swap_bytes": schema.Int64Attribute{Optional: true,
					MarkdownDescription: "Swap per task in bytes, only together with `limits.memory_bytes` (`SwapBytes`); `-1` is unlimited swap. " +
						"Unset: twice the memory limit." + apiSince("1.52")},
				"memory_swappiness": schema.Int64Attribute{Optional: true,
					Validators: []validator.Int64{int64validator.Between(-1, 100)},
					MarkdownDescription: "Memory swappiness of the task containers, 0 to 100 (`MemorySwappiness`); `-1` unsets a previous value. " +
						"Unset: the OS default." + apiSince("1.52")},
			},
			Blocks: map[string]schema.Block{
				"limits":       svcLimitsBlock(),
				"reservations": svcReservationBlock(),
			},
		},
		"healthcheck": schema.SingleNestedBlock{
			MarkdownDescription: "Container healthcheck; Swarm only counts healthy tasks as running.",
			Attributes: map[string]schema.Attribute{
				"test": schema.ListAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Test command, e.g. `[\"CMD\", \"curl\", \"-f\", \"http://localhost/\"]`, `[\"CMD-SHELL\", \"...\"]` or `[\"NONE\"]`."},
				"interval":     schema.StringAttribute{Optional: true, MarkdownDescription: "Time between checks (Go duration).", Validators: []validator.String{svcDurationValidator()}},
				"timeout":      schema.StringAttribute{Optional: true, MarkdownDescription: "Time before a check counts as hung (Go duration).", Validators: []validator.String{svcDurationValidator()}},
				"start_period": schema.StringAttribute{Optional: true, MarkdownDescription: "Start-up grace period (Go duration).", Validators: []validator.String{svcDurationValidator()}},
				"start_interval": schema.StringAttribute{Optional: true, Validators: []validator.String{svcDurationValidator()},
					MarkdownDescription: "Time between checks during the start period (Go duration, `StartInterval`)." + apiSince("1.44")},
				"retries": schema.Int64Attribute{Optional: true, MarkdownDescription: "Consecutive failures before unhealthy."},
			},
		},
		"logging": schema.SingleNestedBlock{
			MarkdownDescription: "Log driver of the task containers.",
			Attributes: map[string]schema.Attribute{
				"driver":  schema.StringAttribute{Optional: true, MarkdownDescription: "Log driver name, e.g. `json-file`."},
				"options": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Log driver options."},
			},
		},
		"converge_config": schema.SingleNestedBlock{
			MarkdownDescription: "When present, create and update wait until the service has converged: the wanted number of " +
				"tasks running (every desired task for `global`) and no update paused or rolling back; for `replicated_job`, " +
				"`total_completions` tasks completed; for `global_job`, every task of the current run finished with at least one " +
				"completed. On timeout the apply fails with the task errors. Not sent to the daemon.",
			Attributes: map[string]schema.Attribute{
				"timeout": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(svcDefaultConvergeWait),
					MarkdownDescription: "Maximum wait (Go duration, default `3m`).", Validators: []validator.String{svcDurationValidator()}},
				"delay": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(svcDefaultConvergeTick),
					MarkdownDescription: "Poll interval (Go duration, default `5s`).", Validators: []validator.String{svcDurationValidator()}},
			},
		},
	}
	if topLevel {
		attrs["id"] = schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "The Swarm service ID.",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	return attrs, blocks
}

func svcFileRefBlock(kind string) schema.ListNestedBlock {
	return schema.ListNestedBlock{
		MarkdownDescription: "Swarm " + kind + "s mounted as files in the task containers (default path `/run/secrets/<file_name>` for " +
			"secrets, `/<file_name>` for configs).",
		NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
			kind + "_id": schema.StringAttribute{Required: true,
				MarkdownDescription: "ID or name of the " + kind + " (resolved to its ID). In a stack, the short name of a stack `" + kind + "` block resolves to `<stack>_<name>`."},
			kind + "_name": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Name of the " + kind + "; looked up from the ID when unset."},
			"file_name": schema.StringAttribute{Optional: true,
				MarkdownDescription: "File name (or absolute path) in the container. Defaults to the reference as written when it is a name, otherwise the " + kind + " name."},
			"uid":  schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("0"), MarkdownDescription: "File owner UID (default `0`)."},
			"gid":  schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("0"), MarkdownDescription: "File group GID (default `0`)."},
			"mode": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(svcDefaultSecretMode), MarkdownDescription: "File mode as a decimal number (default `292`, i.e. `0444`)."},
		}},
	}
}

func svcUpdateBlock(desc string) schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"parallelism": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "Tasks updated at once (default 1; 0 means all at once)."},
			"delay": schema.StringAttribute{Optional: true, MarkdownDescription: "Delay between batches (Go duration).", Validators: []validator.String{svcDurationValidator()}},
			"failure_action": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("pause"),
				MarkdownDescription: "`pause` (default), `continue` or `rollback`.",
				Validators:          []validator.String{stringvalidator.OneOf("pause", "continue", "rollback")}},
			"monitor": schema.StringAttribute{Optional: true, MarkdownDescription: "Time to watch each task for failure after it starts (Go duration).", Validators: []validator.String{svcDurationValidator()}},
			"max_failure_ratio": schema.Float64Attribute{Optional: true,
				MarkdownDescription: "Fraction of tasks that may fail before `failure_action` triggers (0 to 1)."},
			"order": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("stop-first"),
				MarkdownDescription: "`stop-first` (default) or `start-first`.",
				Validators:          []validator.String{stringvalidator.OneOf("stop-first", "start-first")}},
		},
	}
}

// svcReservationBlock is resources.reservations: an amount plus generic
// resources.
func svcReservationBlock() schema.SingleNestedBlock {
	b := svcResourceAmountBlock("Reservations the scheduler must find on a node.")
	b.Blocks = map[string]schema.Block{
		"generic_resources": schema.ListNestedBlock{
			MarkdownDescription: "User-defined resources the node must advertise (`GenericResources`); each entry sets one of " +
				"`named_resource_spec` and `discrete_resource_spec`.",
			NestedObject: schema.NestedBlockObject{Blocks: map[string]schema.Block{
				"named_resource_spec": schema.SingleNestedBlock{
					MarkdownDescription: "A resource with a string value, e.g. a GPU UUID (`NamedResourceSpec`).",
					Attributes: map[string]schema.Attribute{
						"kind":  schema.StringAttribute{Optional: true, MarkdownDescription: "Resource kind, e.g. `GPU` (`Kind`)."},
						"value": schema.StringAttribute{Optional: true, MarkdownDescription: "Resource value (`Value`)."},
					},
				},
				"discrete_resource_spec": schema.SingleNestedBlock{
					MarkdownDescription: "A countable resource (`DiscreteResourceSpec`).",
					Attributes: map[string]schema.Attribute{
						"kind":  schema.StringAttribute{Optional: true, MarkdownDescription: "Resource kind, e.g. `SSD` (`Kind`)."},
						"value": schema.Int64Attribute{Optional: true, MarkdownDescription: "Amount (`Value`)."},
					},
				},
			}},
		},
	}
	return b
}

// svcLimitsBlock is resources.limits: an amount plus the pids limit.
func svcLimitsBlock() schema.SingleNestedBlock {
	b := svcResourceAmountBlock("Hard limits.")
	b.Attributes["pids"] = schema.Int64Attribute{Optional: true, Validators: []validator.Int64{int64validator.AtLeast(0)},
		MarkdownDescription: "Maximum number of processes per task container (`Pids`); 0 or unset is unlimited." + apiSince("1.41")}
	return b
}

// svcConfigRefBlock is the configs block: the file reference block plus
// the runtime target.
func svcConfigRefBlock() schema.ListNestedBlock {
	b := svcFileRefBlock("config")
	b.NestedObject.Attributes["runtime"] = schema.BoolAttribute{Optional: true,
		MarkdownDescription: "Reference the config as a runtime target instead of a file (`ConfigReference.Runtime`): it is not " +
			"mounted, the runtime uses it, e.g. the config named in `privileges.credential_spec.config`. `file_name`, `uid`, " +
			"`gid` and `mode` are then not sent." + apiSince("1.40")}
	return b
}

func svcResourceAmountBlock(desc string) schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"nano_cpus":    schema.Int64Attribute{Optional: true, MarkdownDescription: "CPU in units of 1e-9 CPUs (1000000000 = one CPU)."},
			"memory_bytes": schema.Int64Attribute{Optional: true, MarkdownDescription: "Memory in bytes."},
		},
	}
}
