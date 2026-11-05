package provider

// Schema and model shared by docker_service and docker_stack's service block.

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	Networks        types.Set           `tfsdk:"networks"`
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
	MaxConcurrent    types.Int64             `tfsdk:"max_concurrent"`
	TotalCompletions types.Int64             `tfsdk:"total_completions"`
	Hostname         types.String            `tfsdk:"hostname"`
	User             types.String            `tfsdk:"user"`
	Dir              types.String            `tfsdk:"dir"`
	Hosts            types.List              `tfsdk:"hosts"`
	DNS              *svcDNSModel            `tfsdk:"dns"`
	CapAdd           types.Set               `tfsdk:"cap_add"`
	CapDrop          types.Set               `tfsdk:"cap_drop"`
	Sysctls          types.Map               `tfsdk:"sysctls"`
	Ulimits          []svcUlimitModel        `tfsdk:"ulimits"`
	StopGracePeriod  types.String            `tfsdk:"stop_grace_period"`
	ForceUpdate      types.Int64             `tfsdk:"force_update"`
	NetworksAdvanced []svcNetworkAttachModel `tfsdk:"networks_advanced"`
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

type svcNetworkAttachModel struct {
	Name    types.String `tfsdk:"name"`
	Aliases types.Set    `tfsdk:"aliases"`
}

type svcMountModel struct {
	Type             types.String `tfsdk:"type"`
	Source           types.String `tfsdk:"source"`
	Target           types.String `tfsdk:"target"`
	ReadOnly         types.Bool   `tfsdk:"read_only"`
	TmpfsSize        types.Int64  `tfsdk:"tmpfs_size"`
	VolumeDriver     types.String `tfsdk:"volume_driver"`
	VolumeDriverOpts types.Map    `tfsdk:"volume_driver_opts"`
	VolumeLabels     types.Map    `tfsdk:"volume_labels"`
}

type svcEndpointModel struct {
	Mode  types.String   `tfsdk:"mode"`
	Ports []svcPortModel `tfsdk:"ports"`
}

type svcPortModel struct {
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
	Constraints types.List  `tfsdk:"constraints"`
	Preferences types.List  `tfsdk:"preferences"`
	MaxReplicas types.Int64 `tfsdk:"max_replicas"`
}

type svcResourcesModel struct {
	Limits       *svcResourceAmountModel `tfsdk:"limits"`
	Reservations *svcResourceAmountModel `tfsdk:"reservations"`
}

type svcResourceAmountModel struct {
	NanoCPUs    types.Int64 `tfsdk:"nano_cpus"`
	MemoryBytes types.Int64 `tfsdk:"memory_bytes"`
}

type svcHealthModel struct {
	Test        types.List   `tfsdk:"test"`
	Interval    types.String `tfsdk:"interval"`
	Timeout     types.String `tfsdk:"timeout"`
	StartPeriod types.String `tfsdk:"start_period"`
	Retries     types.Int64  `tfsdk:"retries"`
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
		"networks": schema.SetAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Networks to attach the tasks to, by ID or name (resolved to IDs; overlay networks). In a stack, " +
				"the short name of a stack `network` block resolves to `<stack>_<name>`. Use `networks_advanced` instead for per-network " +
				"aliases; the two cannot be combined. Updated in place."},
		"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Labels on the service object."},
		"container_labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Labels on the task containers."},
	}
	blocks := map[string]schema.Block{
		"networks_advanced": schema.ListNestedBlock{
			MarkdownDescription: "Networks to attach, with per-network DNS aliases (`TaskTemplate.Networks[].Aliases`). An alternative to " +
				"`networks`: setting both is an error. Updated in place.",
			Validators: []validator.List{svcNetworksConflictValidator{}},
			NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{Required: true,
					MarkdownDescription: "Network ID or name (resolved to its ID). In a stack, the short name of a stack `network` block resolves to `<stack>_<name>`."},
				"aliases": schema.SetAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Extra DNS names the service gets on this network (the service name always resolves)."},
			}},
		},
		"dns": schema.SingleNestedBlock{
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
				"target":     schema.StringAttribute{Required: true, MarkdownDescription: "Path inside the container."},
				"read_only":  schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Mount read-only."},
				"tmpfs_size": schema.Int64Attribute{Optional: true, MarkdownDescription: "Size of a `tmpfs` mount in bytes."},
				"volume_driver": schema.StringAttribute{Optional: true,
					MarkdownDescription: "Volume driver used when the volume does not exist yet on the node."},
				"volume_driver_opts": schema.MapAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Driver options for a volume created on first use."},
				"volume_labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Labels for a volume created on first use."},
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
		"configs":         svcFileRefBlock("config"),
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
		},
		"resources": schema.SingleNestedBlock{
			MarkdownDescription: "CPU and memory limits and reservations per task.",
			Blocks: map[string]schema.Block{
				"limits":       svcResourceAmountBlock("Hard limits."),
				"reservations": svcResourceAmountBlock("Reservations the scheduler must find on a node."),
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
				"retries":      schema.Int64Attribute{Optional: true, MarkdownDescription: "Consecutive failures before unhealthy."},
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

func svcResourceAmountBlock(desc string) schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"nano_cpus":    schema.Int64Attribute{Optional: true, MarkdownDescription: "CPU in units of 1e-9 CPUs (1000000000 = one CPU)."},
			"memory_bytes": schema.Int64Attribute{Optional: true, MarkdownDescription: "Memory in bytes."},
		},
	}
}
