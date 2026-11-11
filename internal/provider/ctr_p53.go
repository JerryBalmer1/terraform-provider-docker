package provider

// Pass 5.3 Lane A: docker_container field coverage (every writable
// ContainerCreate / ContainerUpdate field of Engine API v1.56 that a user
// can set, under its upstream name) and the D1 lifecycle `state`
// (running | created | exited, named from Docker's State.Status values; see
// templates/guides/container-state.md.tmpl).

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/blkiodev"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Container lifecycle states (Docker's State.Status names).
const (
	ctrStateRunning = "running"
	ctrStateCreated = "created"
	ctrStateExited  = "exited"
)

// ctrExitedDefaultWaitTimeout is the wait_timeout default for state = "exited".
const ctrExitedDefaultWaitTimeout = "5m"

// ctrP53Model holds the Pass 5.3 attributes; containerResourceModel embeds it.
type ctrP53Model struct {
	State             types.String `tfsdk:"state"`
	ExpectedExitCodes types.List   `tfsdk:"expected_exit_codes"`
	LogsTail          types.Int64  `tfsdk:"logs_tail"`
	FinishedAt        types.String `tfsdk:"finished_at"`
	Logs              types.String `tfsdk:"logs"`
	LogEntries        types.List   `tfsdk:"log_entries"`
	Adopt             types.Bool   `tfsdk:"adopt"`

	Annotations        types.Map              `tfsdk:"annotations"`
	BlkioWeight        types.Int64            `tfsdk:"blkio_weight"`
	BlkioWeightDevice  []ctrWeightDeviceModel `tfsdk:"blkio_weight_device"`
	Cgroup             types.String           `tfsdk:"cgroup"`
	ConsoleSize        types.List             `tfsdk:"console_size"`
	CPUCount           types.Int64            `tfsdk:"cpu_count"`
	CPUPercent         types.Int64            `tfsdk:"cpu_percent"`
	CPURealtimePeriod  types.Int64            `tfsdk:"cpu_realtime_period"`
	CPURealtimeRuntime types.Int64            `tfsdk:"cpu_realtime_runtime"`
	CpusetMems         types.String           `tfsdk:"cpuset_mems"`
	IOMaximumBandwidth types.Int64            `tfsdk:"io_maximum_bandwidth"`
	IOMaximumIOps      types.Int64            `tfsdk:"io_maximum_iops"`
	Isolation          types.String           `tfsdk:"isolation"`
	Links              types.List             `tfsdk:"links"`
	MaskedPaths        types.List             `tfsdk:"masked_paths"`
	ReadonlyPaths      types.List             `tfsdk:"readonly_paths"`
	MemorySwappiness   types.Int64            `tfsdk:"memory_swappiness"`
	NetworkDisabled    types.Bool             `tfsdk:"network_disabled"`
	OomKillDisable     types.Bool             `tfsdk:"oom_kill_disable"`
	OomScoreAdj        types.Int64            `tfsdk:"oom_score_adj"`
	PidsLimit          types.Int64            `tfsdk:"pids_limit"`
	Umask              types.String           `tfsdk:"umask"`
	UTSMode            types.String           `tfsdk:"uts_mode"`
	VolumeDriver       types.String           `tfsdk:"volume_driver"`
	Shell              types.List             `tfsdk:"shell"`
	Volumes            types.Set              `tfsdk:"volumes"`
}

// ctrWeightDeviceModel is one blkio_weight_device block (blkiodev.WeightDevice).
type ctrWeightDeviceModel struct {
	Path   types.String `tfsdk:"path"`
	Weight types.Int64  `tfsdk:"weight"`
}

// ctrP53Null is a ctrP53Model with every value null (typed), for tests and
// for state that predates Pass 5.3.
func ctrP53Null() ctrP53Model {
	return ctrP53Model{
		State: types.StringValue(ctrStateRunning), ExpectedExitCodes: types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(0)}),
		LogsTail: types.Int64Value(logDefaultTail), FinishedAt: types.StringNull(), Logs: types.StringNull(),
		LogEntries: contractNull(contractOutputNode(contractMustItem("engine:container-logs"),
			contractFindOutput("engine:container-logs", "log_entries"))).(types.List),
		Adopt:       types.BoolNull(),
		Annotations: types.MapNull(types.StringType), BlkioWeight: types.Int64Null(), BlkioWeightDevice: []ctrWeightDeviceModel{}, Cgroup: types.StringNull(),
		ConsoleSize: types.ListNull(types.Int64Type), CPUCount: types.Int64Null(), CPUPercent: types.Int64Null(),
		CPURealtimePeriod: types.Int64Null(), CPURealtimeRuntime: types.Int64Null(), CpusetMems: types.StringNull(),
		IOMaximumBandwidth: types.Int64Null(), IOMaximumIOps: types.Int64Null(), Isolation: types.StringNull(),
		Links: types.ListNull(types.StringType), MaskedPaths: types.ListNull(types.StringType), ReadonlyPaths: types.ListNull(types.StringType),
		MemorySwappiness: types.Int64Null(), NetworkDisabled: types.BoolNull(), OomKillDisable: types.BoolNull(),
		OomScoreAdj: types.Int64Null(), PidsLimit: types.Int64Null(), Umask: types.StringNull(), UTSMode: types.StringNull(),
		VolumeDriver: types.StringNull(), Shell: types.ListNull(types.StringType), Volumes: types.SetNull(types.StringType),
	}
}

// contractFindOutput returns output name of itemID (panics when absent: a
// schema-build-time programming error every unit test exercises).
func contractFindOutput(itemID, name string) contractOutput {
	for _, o := range contractModelOutputs(contractMustItem(itemID)) {
		if o.Name == name {
			return o
		}
	}
	panic(fmt.Sprintf("outputs contract %s has no output %q", itemID, name))
}

// ctrP53Attributes are the Pass 5.3 attributes of docker_container.
func ctrP53Attributes() map[string]schema.Attribute {
	rrStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	rrList := []planmodifier.List{listplanmodifier.RequiresReplace()}
	rrInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	rrBool := []planmodifier.Bool{boolplanmodifier.RequiresReplace()}
	inPlaceInt := []planmodifier.Int64{containerRequiresReplaceIfCleared()}
	intRR := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, PlanModifiers: rrInt, MarkdownDescription: desc + " Forces replacement."}
	}
	strRR := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, PlanModifiers: rrStr, MarkdownDescription: desc + " Forces replacement."}
	}
	listRR := func(desc string) schema.ListAttribute {
		return schema.ListAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: rrList, MarkdownDescription: desc + " Forces replacement."}
	}
	logsNode := contractMustItem("engine:container-logs")
	logs := contractResourceAttr(contractOutputNode(logsNode, contractFindOutput("engine:container-logs", "logs"))).(schema.StringAttribute)
	logs.MarkdownDescription = "With `state = \"exited\"`: the container's output when it exited, as text, one line per entry " +
		"(`<timestamp> <stream> <source> <message>`), the last `logs_tail` lines. Null for the other states. Sensitive: log lines can print secrets."
	logs.PlanModifiers = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	entries := contractResourceAttr(contractOutputNode(logsNode, contractFindOutput("engine:container-logs", "log_entries"))).(schema.ListNestedAttribute)
	entries.MarkdownDescription = "With `state = \"exited\"`: one object per log line (`timestamp`, `stream`, `source`, `message`) of the " +
		"run, the last `logs_tail` lines; only `message` is sensitive. Null for the other states."
	entries.PlanModifiers = []planmodifier.List{listplanmodifier.UseStateForUnknown()}

	return map[string]schema.Attribute{
		"state": schema.StringAttribute{
			Optional: true, Computed: true, Default: stringdefault.StaticString(ctrStateRunning),
			Validators:    []validator.String{stringvalidator.OneOf(ctrStateRunning, ctrStateCreated, ctrStateExited)},
			PlanModifiers: []planmodifier.String{ctrStateReplaceIf()},
			MarkdownDescription: "Lifecycle the provider keeps the container in, named after Docker's `State.Status`: `running` (default: started " +
				"at create, and started again in place when found stopped), `created` (created only, never started by the provider; found " +
				"running, the plan warns and changes nothing) or `exited` (a one-shot run: created, started and waited for until it exits, " +
				"within `wait_timeout`; an exit code outside `expected_exit_codes` fails the apply, and a later refresh never runs it again). " +
				"Switching between `running` and `created` is applied in place; a change to or from `exited` forces replacement. " +
				"See the container-state guide.",
		},
		"expected_exit_codes": schema.ListAttribute{
			ElementType: types.Int64Type, Optional: true, Computed: true,
			Default:             listdefault.StaticValue(types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(0)})),
			Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
			MarkdownDescription: "Exit codes that count as success for `state = \"exited\"`. Default `[0]`. Checked once, when the run finishes.",
		},
		"logs_tail": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(logDefaultTail),
			Validators: []validator.Int64{int64validator.AtLeast(0)},
			MarkdownDescription: "With `state = \"exited\"`: how many lines from the end of the run's output `logs` and `log_entries` keep " +
				"(the Engine's `tail`). Default `1000`. Read once, when the run finishes.",
		},
		"finished_at": schema.StringAttribute{
			Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			MarkdownDescription: "With `state = \"exited\"`: when the run's process exited (`State.FinishedAt`, RFC 3339). Null for the other states.",
		},
		"logs":        logs,
		"log_entries": entries,
		"adopt":       ownerAdoptAttribute(),

		"annotations": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			MarkdownDescription: "Arbitrary non-identifying metadata passed to the OCI runtime when the container starts " +
				"(`HostConfig.Annotations`, `docker run --annotation`). Forces replacement."},
		"blkio_weight": schema.Int64Attribute{Optional: true, PlanModifiers: inPlaceInt,
			Validators: []validator.Int64{int64validator.Between(10, 1000)},
			MarkdownDescription: "Block IO weight, relative to other containers, 10 to 1000 (`HostConfig.BlkioWeight`). Updated in place " +
				"(`ContainerUpdate`); removing it forces a new resource."},
		"cgroup": strRR("Cgroup the container joins (`HostConfig.Cgroup`), e.g. `container:<name|id>`."),
		"console_size": schema.ListAttribute{ElementType: types.Int64Type, Optional: true, PlanModifiers: rrList,
			Validators:          []validator.List{listvalidator.SizeBetween(2, 2)},
			MarkdownDescription: "Initial console size as `[height, width]` (`HostConfig.ConsoleSize`); used with `tty = true`. Forces replacement."},
		"cpu_count": intRR("Number of usable CPUs (`HostConfig.CpuCount`). Windows daemons only; a Linux daemon ignores it."),
		"cpu_percent": intRR("Usable percentage of the available CPUs (`HostConfig.CpuPercent`). Windows daemons only; " +
			"a Linux daemon ignores it."),
		"cpu_realtime_period": schema.Int64Attribute{Optional: true, PlanModifiers: inPlaceInt,
			MarkdownDescription: "CPU real-time period in microseconds (`HostConfig.CpuRealtimePeriod`). Updated in place; removing it forces a new resource."},
		"cpu_realtime_runtime": schema.Int64Attribute{Optional: true, PlanModifiers: inPlaceInt,
			MarkdownDescription: "CPU real-time runtime in microseconds (`HostConfig.CpuRealtimeRuntime`). Updated in place; removing it forces a new resource."},
		"cpuset_mems": schema.StringAttribute{Optional: true, PlanModifiers: []planmodifier.String{ctrRequiresReplaceIfClearedString()},
			MarkdownDescription: "Memory nodes (NUMA) the container may use, e.g. `0-3` or `0,1` (`HostConfig.CpusetMems`). Updated in place; removing it forces a new resource."},
		"io_maximum_bandwidth": intRR("Maximum IO in bytes per second for the container's system drive (`HostConfig.IOMaximumBandwidth`). " +
			"Windows daemons only."),
		"io_maximum_iops": intRR("Maximum IO operations per second for the container's system drive (`HostConfig.IOMaximumIOps`). Windows daemons only."),
		"isolation": schema.StringAttribute{Optional: true, PlanModifiers: rrStr,
			Validators: []validator.String{stringvalidator.OneOf("default", "process", "hyperv")},
			MarkdownDescription: "Isolation technology (`HostConfig.Isolation`): `default`, `process` or `hyperv`. Windows daemons only; " +
				"unset uses the daemon default, which is not reported. Forces replacement."},
		"links": listRR("Legacy container links, each `<name|id>[:<alias>]` (`HostConfig.Links`, `docker run --link`). Prefer user-defined " +
			"`networks`, whose DNS resolves container names."),
		"masked_paths": listRR("Paths masked inside the container (`HostConfig.MaskedPaths`); setting it replaces the daemon's default " +
			"list. Unset keeps the default, which is not reported."),
		"readonly_paths": listRR("Paths made read-only inside the container (`HostConfig.ReadonlyPaths`); setting it replaces the " +
			"daemon's default list. Unset keeps the default, which is not reported."),
		"memory_swappiness": schema.Int64Attribute{Optional: true, PlanModifiers: rrInt,
			Validators:          []validator.Int64{int64validator.Between(0, 100)},
			MarkdownDescription: "Memory swappiness, 0 to 100 (`HostConfig.MemorySwappiness`). Unset uses the host's. Forces replacement."},
		"network_disabled": schema.BoolAttribute{Optional: true, PlanModifiers: rrBool,
			MarkdownDescription: "Disable networking for the container (`Config.NetworkDisabled`). Forces replacement."},
		"oom_kill_disable": schema.BoolAttribute{Optional: true, PlanModifiers: rrBool,
			MarkdownDescription: "Disable the OOM killer for the container (`HostConfig.OomKillDisable`); set `memory` with it. Forces replacement."},
		"oom_score_adj": schema.Int64Attribute{Optional: true, PlanModifiers: rrInt,
			Validators:          []validator.Int64{int64validator.Between(-1000, 1000)},
			MarkdownDescription: "OOM killer score adjustment, -1000 to 1000 (`HostConfig.OomScoreAdj`). Forces replacement."},
		"pids_limit": schema.Int64Attribute{Optional: true,
			MarkdownDescription: "Maximum number of processes in the container (`HostConfig.PidsLimit`); `0` or `-1` is unlimited. Updated in " +
				"place (`ContainerUpdate`); removing it lifts the limit in place."},
		"umask": schema.StringAttribute{Optional: true, PlanModifiers: rrStr,
			MarkdownDescription: "Initial umask of the container's processes (entrypoint, exec processes and healthchecks) as an octal string, " +
				"e.g. `\"0027\"` (`HostConfig.Umask`). Unset leaves the runtime default. Forces replacement." + apiSince("1.56")},
		"uts_mode": strRR("UTS namespace (`HostConfig.UTSMode`), e.g. `host`."),
		"shell": listRR("Shell the daemon runs `CMD-SHELL` healthchecks with (`Config.Shell`), e.g. `[\"/bin/bash\", \"-c\"]`. " +
			"Unset uses the image's `SHELL` (else `/bin/sh -c`), which is not reported."),
		"volumes": schema.SetAttribute{ElementType: types.StringType, Optional: true,
			PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			MarkdownDescription: "Container paths that get an anonymous volume (`Config.Volumes`, like `docker run -v /data`). A " +
				"`mounts` entry with `type = \"volume\"` and no `source` is the structured alternative. The image's own `VOLUME` paths " +
				"are not reported. Forces replacement."},
		"volume_driver": strRR("Volume driver for the volumes this container creates through `HostConfig.Binds` (`HostConfig.VolumeDriver`); `mounts` entries take their driver from `volume_options.driver_config`."),
	}
}

// ctrP53Blocks are the Pass 5.3 blocks of docker_container.
func ctrP53Blocks() map[string]schema.Block {
	return map[string]schema.Block{
		"blkio_weight_device": schema.ListNestedBlock{
			MarkdownDescription: "Per-device block IO weight (`HostConfig.BlkioWeightDevice`). Forces replacement.",
			PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
			NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
				"path": schema.StringAttribute{Required: true, MarkdownDescription: "Device path on the host, e.g. `/dev/sda` (`Path`)."},
				"weight": schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(10, 1000)},
					MarkdownDescription: "Relative weight, 10 to 1000 (`Weight`)."},
			}},
		},
	}
}

// ctrStateReplaceIf replaces the container when state changes to or from
// "exited" (a one-shot run is never converted in place).
func ctrStateReplaceIf() planmodifier.String {
	desc := "Changing state to or from \"exited\" forces replacement."
	return stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.PlanValue.IsUnknown() {
			return
		}
		from, to := req.StateValue.ValueString(), req.PlanValue.ValueString()
		resp.RequiresReplace = from != to && (from == ctrStateExited || to == ctrStateExited)
	}, desc, desc)
}

// ctrParseUmask parses an octal umask string.
func ctrParseUmask(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil || v > 0o777 {
		return 0, fmt.Errorf("%q: want an octal umask such as \"0022\"", s)
	}
	return uint32(v), nil
}

// ctrApplyP53 copies the Pass 5.3 create-time attributes into the create
// arguments.
func ctrApplyP53(ctx context.Context, m containerResourceModel, cfg *container.Config, hc *container.HostConfig, diags *diag.Diagnostics) {
	if a := ctrStringMap(m.Annotations); len(a) > 0 {
		hc.Annotations = a
	}
	hc.BlkioWeight = uint16(m.BlkioWeight.ValueInt64())
	for _, d := range m.BlkioWeightDevice {
		hc.BlkioWeightDevice = append(hc.BlkioWeightDevice, &blkiodev.WeightDevice{Path: d.Path.ValueString(), Weight: uint16(d.Weight.ValueInt64())})
	}
	hc.Cgroup = container.CgroupSpec(m.Cgroup.ValueString())
	if containerKnown(m.ConsoleSize) {
		var cs []int64
		diags.Append(m.ConsoleSize.ElementsAs(ctx, &cs, false)...)
		if len(cs) == 2 {
			if cs[0] < 0 || cs[1] < 0 {
				diags.AddAttributeError(path.Root("console_size"), "Invalid console_size", "height and width must not be negative")
			} else {
				hc.ConsoleSize = [2]uint{uint(cs[0]), uint(cs[1])}
			}
		}
	}
	hc.CPUCount = m.CPUCount.ValueInt64()
	hc.CPUPercent = m.CPUPercent.ValueInt64()
	hc.CPURealtimePeriod = m.CPURealtimePeriod.ValueInt64()
	hc.CPURealtimeRuntime = m.CPURealtimeRuntime.ValueInt64()
	hc.CpusetMems = m.CpusetMems.ValueString()
	hc.IOMaximumBandwidth = uint64(max(m.IOMaximumBandwidth.ValueInt64(), 0))
	hc.IOMaximumIOps = uint64(max(m.IOMaximumIOps.ValueInt64(), 0))
	hc.Isolation = container.Isolation(m.Isolation.ValueString())
	hc.Links = stringListFrom(ctx, m.Links, diags)
	hc.MaskedPaths = stringListFrom(ctx, m.MaskedPaths, diags)
	hc.ReadonlyPaths = stringListFrom(ctx, m.ReadonlyPaths, diags)
	if v, ok := knownInt64(m.MemorySwappiness); ok {
		hc.MemorySwappiness = &v
	}
	cfg.NetworkDisabled = m.NetworkDisabled.ValueBool()
	if v, ok := knownBool(m.OomKillDisable); ok {
		hc.OomKillDisable = &v
	}
	hc.OomScoreAdj = int(m.OomScoreAdj.ValueInt64())
	if v, ok := knownInt64(m.PidsLimit); ok {
		hc.PidsLimit = &v
	}
	if s, ok := knownString(m.Umask); ok && s != "" {
		u, err := ctrParseUmask(s)
		if err != nil {
			diags.AddAttributeError(path.Root("umask"), "Invalid umask", err.Error())
		} else {
			hc.Umask = &u
		}
	}
	hc.UTSMode = container.UTSMode(m.UTSMode.ValueString())
	hc.VolumeDriver = m.VolumeDriver.ValueString()
	cfg.Shell = stringListFrom(ctx, m.Shell, diags)
	if vols := stringSetFrom(ctx, m.Volumes, diags); len(vols) > 0 {
		cfg.Volumes = make(map[string]struct{}, len(vols))
		for _, v := range vols {
			cfg.Volumes[v] = struct{}{}
		}
	}
}

// ctrReadP53 maps the Pass 5.3 config fields of an inspect response onto out.
// They come from Config / HostConfig only (never from runtime state).
func ctrReadP53(cfg *container.Config, hc *container.HostConfig, img *container.Config, prior containerResourceModel, out *containerResourceModel) {
	out.Shell = containerListRead(prior.Shell, cfg.Shell, img.Shell)
	var vols []string
	for v := range cfg.Volumes {
		if _, fromImage := img.Volumes[v]; !fromImage || containerKnown(prior.Volumes) {
			vols = append(vols, v)
		}
	}
	if containerKnown(prior.Volumes) {
		want := map[string]bool{}
		for _, v := range containerSetStrings(prior.Volumes) {
			want[v] = true
		}
		kept := vols[:0]
		for _, v := range vols {
			if _, fromImage := img.Volumes[v]; !fromImage || want[v] {
				kept = append(kept, v)
			}
		}
		vols = kept
	}
	out.Volumes = containerSetValue(prior.Volumes, vols)
	out.Annotations = containerMapValue(prior.Annotations, hc.Annotations)
	out.BlkioWeight = containerInt64Read(prior.BlkioWeight, int64(hc.BlkioWeight))
	got := make([]ctrWeightDeviceModel, 0, len(hc.BlkioWeightDevice))
	for _, d := range hc.BlkioWeightDevice {
		if d != nil {
			got = append(got, ctrWeightDeviceModel{Path: types.StringValue(d.Path), Weight: types.Int64Value(int64(d.Weight))})
		}
	}
	out.BlkioWeightDevice = containerOrderByPrior(prior.BlkioWeightDevice, got, func(d ctrWeightDeviceModel) string { return d.Path.ValueString() })
	out.Cgroup = containerStringRead(prior.Cgroup, string(hc.Cgroup), "")
	out.ConsoleSize = ctrConsoleSizeRead(prior.ConsoleSize, hc.ConsoleSize)
	out.CPUCount = containerInt64Read(prior.CPUCount, hc.CPUCount)
	out.CPUPercent = containerInt64Read(prior.CPUPercent, hc.CPUPercent)
	out.CPURealtimePeriod = containerInt64Read(prior.CPURealtimePeriod, hc.CPURealtimePeriod)
	out.CPURealtimeRuntime = containerInt64Read(prior.CPURealtimeRuntime, hc.CPURealtimeRuntime)
	out.CpusetMems = containerStringRead(prior.CpusetMems, hc.CpusetMems, "")
	out.IOMaximumBandwidth = containerInt64Read(prior.IOMaximumBandwidth, int64(hc.IOMaximumBandwidth))
	out.IOMaximumIOps = containerInt64Read(prior.IOMaximumIOps, int64(hc.IOMaximumIOps))
	out.Isolation = ctrPriorOnlyString(prior.Isolation, string(hc.Isolation))
	out.Links = ctrLinksRead(prior.Links, hc.Links)
	out.MaskedPaths = ctrPriorOnlyList(prior.MaskedPaths, hc.MaskedPaths)
	out.ReadonlyPaths = ctrPriorOnlyList(prior.ReadonlyPaths, hc.ReadonlyPaths)
	out.MemorySwappiness = ctrPtrInt64Read(prior.MemorySwappiness, hc.MemorySwappiness, -1)
	out.NetworkDisabled = types.BoolNull()
	if cfg.NetworkDisabled || containerKnown(prior.NetworkDisabled) {
		out.NetworkDisabled = types.BoolValue(cfg.NetworkDisabled)
	}
	out.OomKillDisable = ctrInitRead(prior.OomKillDisable, hc.OomKillDisable)
	out.OomScoreAdj = containerInt64Read(prior.OomScoreAdj, int64(hc.OomScoreAdj))
	out.PidsLimit = ctrPtrInt64Read(prior.PidsLimit, hc.PidsLimit, 0)
	out.Umask = types.StringNull()
	if hc.Umask != nil {
		out.Umask = ctrModeValue(prior.Umask, *hc.Umask)
		if !containerKnown(out.Umask) {
			out.Umask = types.StringValue(fmt.Sprintf("%04o", *hc.Umask))
		}
	} else if containerKnown(prior.Umask) {
		out.Umask = types.StringNull()
	}
	out.UTSMode = containerStringRead(prior.UTSMode, string(hc.UTSMode), "")
	out.VolumeDriver = containerStringRead(prior.VolumeDriver, hc.VolumeDriver, "")
}

// ctrConsoleSizeRead reports [height, width]; [0, 0] stays null unless the
// prior was set.
func ctrConsoleSizeRead(prior types.List, cs [2]uint) types.List {
	if cs == [2]uint{} && !containerKnown(prior) {
		return types.ListNull(types.Int64Type)
	}
	return types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(int64(cs[0])), types.Int64Value(int64(cs[1]))})
}

// ctrPriorOnlyList reports a list field the daemon fills with its own
// default only when the prior value was set.
func ctrPriorOnlyList(prior types.List, got []string) types.List {
	if !containerKnown(prior) {
		return types.ListNull(types.StringType)
	}
	return stringListValue(got, false)
}

// ctrPtrInt64Read maps an optional daemon integer: nil (or unset) is
// reported as unset when the prior was null, else as unsetValue.
func ctrPtrInt64Read(prior types.Int64, v *int64, unsetValue int64) types.Int64 {
	if v == nil {
		if containerKnown(prior) {
			if prior.ValueInt64() == unsetValue || prior.ValueInt64() == 0 {
				return prior
			}
			return types.Int64Value(unsetValue)
		}
		return types.Int64Null()
	}
	if !containerKnown(prior) && (*v == unsetValue || *v == 0) {
		return types.Int64Null()
	}
	return types.Int64Value(*v)
}

// ctrLinkCanonical turns a link into "name:alias": the user form
// "name[:alias]" or the daemon form "/name:/container/alias".
func ctrLinkCanonical(l string) string {
	name, alias, ok := strings.Cut(l, ":")
	name = strings.TrimPrefix(name, "/")
	if !ok || alias == "" {
		alias = name
	}
	if i := strings.LastIndex(alias, "/"); i >= 0 {
		alias = alias[i+1:]
	}
	return name + ":" + alias
}

// ctrLinksRead maps HostConfig.Links (daemon form "/name:/container/alias")
// back to the user's form: the prior list when it names the same links.
func ctrLinksRead(prior types.List, got []string) types.List {
	canon := make([]string, 0, len(got))
	for _, l := range got {
		canon = append(canon, ctrLinkCanonical(l))
	}
	if containerKnown(prior) {
		var p []string
		for _, e := range prior.Elements() {
			if s, ok := e.(types.String); ok {
				p = append(p, ctrLinkCanonical(s.ValueString()))
			}
		}
		if ctrSameStrings(p, canon) {
			return prior
		}
	}
	return containerListRead(prior, canon, nil)
}

// ctrSameStrings compares two string lists as multisets.
func ctrSameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	n := map[string]int{}
	for _, s := range a {
		n[s]++
	}
	for _, s := range b {
		n[s]--
		if n[s] < 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// state = "exited": the one-shot run

// ctrRunResult is what a finished one-shot run produced.
type ctrRunResult struct {
	ExitCode   int64
	FinishedAt string
	Entries    []logEntry
	Stdout     string
	Stderr     string
	Error      string
}

// ctrStartAndWaitExit starts a created container, waits until it is not
// running (within timeout) and reads its exit code, finish time and the
// last tail log lines.
func ctrStartAndWaitExit(ctx context.Context, pd *providerData, id string, timeout time.Duration, tail int64) (ctrRunResult, error) {
	var out ctrRunResult
	if _, err := pd.Client.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return out, fmt.Errorf("starting: %w", err)
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	w := pd.Client.ContainerWait(wctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case res := <-w.Result:
		out.ExitCode = res.StatusCode
		if res.Error != nil {
			out.Error = res.Error.Message
		}
	case err := <-w.Error:
		if wctx.Err() != nil {
			return out, fmt.Errorf("the container did not exit within %s", timeout)
		}
		return out, fmt.Errorf("waiting: %w", err)
	}
	inspRes, err := pd.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return out, fmt.Errorf("inspecting: %w", err)
	}
	insp := inspRes.Container
	if insp.State != nil {
		out.ExitCode = int64(insp.State.ExitCode)
		out.FinishedAt = insp.State.FinishedAt
	}
	tty := insp.Config != nil && insp.Config.Tty
	rc, err := pd.Client.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true,
		Tail: logTailQuery(tail)})
	if err != nil {
		return out, fmt.Errorf("reading logs: %w", err)
	}
	defer rc.Close()
	entries, err := logReadEntries(rc, tty, copName(insp.Name))
	if err != nil {
		return out, fmt.Errorf("reading logs: %w", err)
	}
	out.Entries = logApplyTail(entries, tail)
	var so, se strings.Builder
	for _, e := range out.Entries {
		b := &so
		if e.Stream == logStreamStderr {
			b = &se
		}
		b.WriteString(e.Message)
		b.WriteByte('\n')
	}
	out.Stdout, out.Stderr = so.String(), se.String()
	return out, nil
}

// ctrExitCodeExpected reports whether code is in the expected list.
func ctrExitCodeExpected(ctx context.Context, expected types.List, code int64) (bool, []int64) {
	want := []int64{0}
	if containerKnown(expected) {
		want = nil
		_ = expected.ElementsAs(ctx, &want, false)
	}
	for _, w := range want {
		if w == code {
			return true, want
		}
	}
	return false, want
}

// ctrLogTailText is the last lines of a run for an error message.
func ctrLogTailText(entries []logEntry, n int) string {
	if len(entries) == 0 {
		return ""
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.Stream+": "+e.Message)
	}
	return "\nLast output:\n" + strings.Join(lines, "\n")
}

// ctrFillRunOutputs stores the run's logs / log_entries / finished_at on m.
func ctrFillRunOutputs(ctx context.Context, pd *providerData, m *containerResourceModel, r ctrRunResult) diag.Diagnostics {
	vals, diags := contractValues(ctx, pd, "engine:container-logs", logFillInput(r.Entries))
	if l, ok := vals["logs"].(types.String); ok {
		m.Logs = l
	}
	if l, ok := vals["log_entries"].(types.List); ok {
		m.LogEntries = l
	}
	m.FinishedAt = stringOrNull(r.FinishedAt)
	m.ExitCode = types.Int64Value(r.ExitCode)
	return diags
}

// ctrValidateState checks the state combinations the decision rules out.
func ctrValidateState(m containerResourceModel, diags *diag.Diagnostics) {
	st, ok := knownString(m.State)
	if !ok {
		return
	}
	if st == ctrStateExited && m.AutoRemove.ValueBool() {
		diags.AddAttributeError(path.Root("auto_remove"), "auto_remove conflicts with state = \"exited\"",
			"A container that removes itself on exit cannot be kept as an exited one-shot run. For run-once-and-remove, use the "+
				"docker_container_run action instead.")
	}
	if st != ctrStateRunning && m.Wait.ValueBool() {
		diags.AddAttributeError(path.Root("wait"), "wait needs state = \"running\"",
			"wait waits for a started container to become healthy; with state = \""+st+"\" there is nothing to wait for "+
				"(an exited run always waits for its exit, within wait_timeout).")
	}
	if rp := m.Restart.ValueString(); st == ctrStateExited && rp != "" && rp != "no" {
		diags.AddAttributeError(path.Root("restart"), "restart conflicts with state = \"exited\"",
			"A restart policy would start the one-shot container again after it exits; use restart = \"no\".")
	}
}

// ValidateConfig checks state against auto_remove, wait and restart.
func (r *containerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var st, restart types.String
	var autoRemove, wait types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("state"), &st)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("auto_remove"), &autoRemove)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("wait"), &wait)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("restart"), &restart)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if st.IsNull() {
		st = types.StringValue(ctrStateRunning)
	}
	m := containerResourceModel{AutoRemove: autoRemove, Wait: wait, Restart: restart}
	m.State = st
	ctrValidateState(m, &resp.Diagnostics)
}

// ---------------------------------------------------------------------------
// State upgrade v0 -> v1 (Pass 5.3): must_run is gone, state is new.

// UpgradeState upgrades docker_container state from schema version 0.
func (r *containerResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			var sr resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			b, err := ctrUpgradeV0(ctx, req.RawState.JSON, sr.Schema.Type().TerraformType(ctx))
			if err == nil {
				b, err = contractUpgradeNullOutputs("engine:container", b)
			}
			if err != nil {
				resp.Diagnostics.AddError("Cannot upgrade docker_container state", err.Error())
				return
			}
			resp.DynamicValue = &tfprotov6.DynamicValue{JSON: b}
		}},
		// v1 -> v2 (Pass 6c): structured outputs changed shape; null them, the next Read refills them.
		1: {StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			p6cUpgradeOutputs("docker_container", "engine:container", req, resp)
		}},
	}
}

// ctrUpgradeV0 rewrites a version 0 state JSON for version 1: must_run is
// dropped (subsumed by state = "running"), state is "running",
// expected_exit_codes and logs_tail get their defaults, wait_timeout keeps
// its value, and keys the current schema lacks (attributes removed by
// earlier passes) are dropped at every level.
func ctrUpgradeV0(_ context.Context, raw []byte, typ tftypes.Type) ([]byte, error) {
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("decoding version 0 state: %w", err)
	}
	if st == nil {
		return nil, fmt.Errorf("version 0 state is null")
	}
	delete(st, "must_run")
	if s, _ := st["state"].(string); s == "" {
		st["state"] = ctrStateRunning
	}
	if st["expected_exit_codes"] == nil {
		st["expected_exit_codes"] = []any{0}
	}
	if st["logs_tail"] == nil {
		st["logs_tail"] = logDefaultTail
	}
	return json.Marshal(ctrPruneJSON(st, typ))
}

// ctrPruneJSON drops object keys that typ does not have, recursively.
func ctrPruneJSON(v any, typ tftypes.Type) any {
	switch t := typ.(type) {
	case tftypes.Object:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(m))
		for k, e := range m {
			at, ok := t.AttributeTypes[k]
			if !ok {
				continue
			}
			out[k] = ctrPruneJSON(e, at)
		}
		return out
	case tftypes.List:
		return ctrPruneSlice(v, t.ElementType)
	case tftypes.Set:
		return ctrPruneSlice(v, t.ElementType)
	case tftypes.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		for k, e := range m {
			m[k] = ctrPruneJSON(e, t.ElementType)
		}
		return m
	}
	return v
}

func ctrPruneSlice(v any, et tftypes.Type) any {
	s, ok := v.([]any)
	if !ok {
		return v
	}
	for i := range s {
		s[i] = ctrPruneJSON(s[i], et)
	}
	return s
}

// ctrNullLogEntries is a typed null log_entries value.
func ctrNullLogEntries() types.List {
	return contractNull(contractOutputNode(contractMustItem("engine:container-logs"),
		contractFindOutput("engine:container-logs", "log_entries"))).(types.List)
}

// ctrReadLifecycle fills state (on import), the state defaults and the
// exited-run outputs. state is configuration: a refresh never derives it
// from the runtime state, except on import (created stays created,
// anything else becomes running).
func ctrReadLifecycle(insp container.InspectResponse, prior containerResourceModel, out *containerResourceModel) {
	if !containerKnown(out.State) {
		st := ctrStateRunning
		if insp.State != nil && insp.State.Status == container.StateCreated {
			st = ctrStateCreated
		}
		out.State = types.StringValue(st)
	}
	if !containerKnown(out.ExpectedExitCodes) {
		out.ExpectedExitCodes = types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(0)})
	}
	if !containerKnown(out.LogsTail) {
		out.LogsTail = types.Int64Value(logDefaultTail)
	}
	if out.Adopt.IsUnknown() {
		out.Adopt = types.BoolNull()
	}
	if out.State.ValueString() != ctrStateExited {
		out.FinishedAt, out.Logs, out.LogEntries = types.StringNull(), types.StringNull(), ctrNullLogEntries()
		return
	}
	out.FinishedAt = types.StringNull()
	if insp.State != nil {
		out.FinishedAt = stringOrNull(insp.State.FinishedAt)
	}
	// The run's output is captured once, at create; a refresh keeps it.
	if !containerKnown(prior.Logs) {
		out.Logs = types.StringNull()
	}
	if !containerKnown(prior.LogEntries) {
		out.LogEntries = ctrNullLogEntries()
	}
}

// ctrUpdateP53 copies the Pass 5.3 fields ContainerUpdate changes in place.
func ctrUpdateP53(plan, state containerResourceModel, res *container.Resources) {
	res.BlkioWeight = uint16(plan.BlkioWeight.ValueInt64())
	res.CPURealtimePeriod = plan.CPURealtimePeriod.ValueInt64()
	res.CPURealtimeRuntime = plan.CPURealtimeRuntime.ValueInt64()
	res.CpusetMems = plan.CpusetMems.ValueString()
	if !plan.PidsLimit.Equal(state.PidsLimit) {
		v := plan.PidsLimit.ValueInt64() // null: 0, which lifts the limit
		res.PidsLimit = &v
	}
}

// ctrCheckMountsAPI fails a create with an image mount on a daemon older
// than Engine API 1.48, which introduced Mount type "image".
func ctrCheckMountsAPI(ctx context.Context, pd *providerData, hc *container.HostConfig) error {
	for _, m := range hc.Mounts {
		if m.Type != "image" {
			continue
		}
		ok, have, err := pd.APIVersionAtLeast(ctx, "1.48")
		if err == nil && !ok {
			return fmt.Errorf("a mounts entry with type = \"image\" needs Engine API >= 1.48, daemon speaks %s", have)
		}
		return nil
	}
	return nil
}

// ctrNilPrivate reports a missing private state handle (a nil interface or
// a nil pointer): unit tests call Create / Read / Update without one; the
// framework always passes an initialized handle.
func ctrNilPrivate(priv privateSetter) bool {
	if priv == nil {
		return true
	}
	v := reflect.ValueOf(priv)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
