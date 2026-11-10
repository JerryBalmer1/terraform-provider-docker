package provider

// Pass 5: docker_container attributes added so every kreuzwerker attribute
// has a migration target (renames.json `gap:` rows), plus the `logging`
// block (formerly log_driver / log_opts). They map one to one onto
// Config / HostConfig fields; most are create-time only (Forces replacement),
// cpu_period, cpu_quota, cpuset_cpus and memory_reservation are updated in
// place with ContainerUpdate.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/blkiodev"
	"github.com/moby/moby/api/types/container"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ctrLoggingModel is the logging block (HostConfig.LogConfig).
type ctrLoggingModel struct {
	Driver  types.String `tfsdk:"driver"`
	Options types.Map    `tfsdk:"options"`
}

// ctrThrottleModel is one blkio_device_* block (blkiodev.ThrottleDevice).
type ctrThrottleModel struct {
	Path types.String `tfsdk:"path"`
	Rate types.Int64  `tfsdk:"rate"`
}

// ctrDeviceRequestModel is one device_requests block (container.DeviceRequest).
type ctrDeviceRequestModel struct {
	Driver       types.String `tfsdk:"driver"`
	Count        types.Int64  `tfsdk:"count"`
	DeviceIDs    types.List   `tfsdk:"device_ids"`
	Capabilities types.List   `tfsdk:"capabilities"`
	Options      types.Map    `tfsdk:"options"`
}

// ctrGapAttributes are the plain gap attributes.
func ctrGapAttributes() map[string]schema.Attribute {
	rrStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	rrList := []planmodifier.List{listplanmodifier.RequiresReplace()}
	rrMap := []planmodifier.Map{mapplanmodifier.RequiresReplace()}
	rrInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	optStr := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: desc + " Forces replacement.", PlanModifiers: rrStr}
	}
	return map[string]schema.Attribute{
		"cgroup_parent": optStr("Parent cgroup of the container (`HostConfig.CgroupParent`)."),
		"cgroupns_mode": optStr("Cgroup namespace mode (`HostConfig.CgroupnsMode`): `private` or `host`. Unset uses the daemon default, which is not reported."),
		"cpu_period": schema.Int64Attribute{Optional: true, PlanModifiers: []planmodifier.Int64{containerRequiresReplaceIfCleared()},
			MarkdownDescription: "CPU CFS period in microseconds (`HostConfig.CpuPeriod`). Updated in place; removing it forces a new resource."},
		"cpu_quota": schema.Int64Attribute{Optional: true, PlanModifiers: []planmodifier.Int64{containerRequiresReplaceIfCleared()},
			MarkdownDescription: "CPU CFS quota in microseconds per `cpu_period` (`HostConfig.CpuQuota`). Updated in place; removing it forces a new resource."},
		"cpuset_cpus": schema.StringAttribute{Optional: true, PlanModifiers: []planmodifier.String{ctrRequiresReplaceIfClearedString()},
			MarkdownDescription: "CPUs the container may use, e.g. `0-2` or `0,1` (`HostConfig.CpusetCpus`). Updated in place; removing it forces a new resource."},
		"memory_reservation": schema.Int64Attribute{Optional: true, PlanModifiers: []planmodifier.Int64{containerRequiresReplaceIfCleared()},
			MarkdownDescription: "Memory soft limit in bytes (`HostConfig.MemoryReservation`). Updated in place; removing it forces a new resource."},
		"device_cgroup_rules": schema.ListAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: rrList,
			MarkdownDescription: "Rules added to the device cgroup, e.g. `c 1:3 mr` (`HostConfig.DeviceCgroupRules`). Forces replacement."},
		"dns_options": schema.ListAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: rrList,
			MarkdownDescription: "Resolver options, e.g. `ndots:2` (`HostConfig.DnsOptions`). Forces replacement."},
		"domainname": optStr("Domain name of the container (`Config.Domainname`)."),
		"exit_code": schema.Int64Attribute{Computed: true,
			MarkdownDescription: "Exit code of the container's main process at the last refresh (`State.ExitCode`); 0 while it runs."},
		"group_add": schema.SetAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			MarkdownDescription: "Additional groups the container process runs as (`HostConfig.GroupAdd`). Forces replacement."},
		"init": schema.BoolAttribute{Optional: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			MarkdownDescription: "Run an init process inside the container that forwards signals and reaps processes (`HostConfig.Init`, `docker run --init`). Unset uses the daemon setting. Forces replacement."},
		"ipc_mode":     optStr("IPC namespace (`HostConfig.IpcMode`): `none`, `private`, `shareable`, `host` or `container:<name|id>`. Unset uses the daemon default, which is not reported."),
		"network_mode": optStr("Network mode (`HostConfig.NetworkMode`): `bridge`, `host`, `none`, `container:<name|id>` or a network name. Unset: the alphabetically first `networks` key, else the daemon default `bridge`."),
		"pid_mode":     optStr("PID namespace (`HostConfig.PidMode`): `host` or `container:<name|id>`."),
		"platform":     optStr("Platform of the image to run, `os[/arch[/variant]]`, e.g. `linux/arm64` (the create `platform` parameter)."),
		"publish_all_ports": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			MarkdownDescription: "Publish every exposed port on a random host port (`HostConfig.PublishAllPorts`, `docker run -P`). Default `false`. Forces replacement."},
		"auto_remove": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			MarkdownDescription: "Remove the container when its process exits (`HostConfig.AutoRemove`, `docker run --rm`); Terraform then plans a new one. " +
				"Default `false`. Forces replacement."},
		"runtime": optStr("OCI runtime, e.g. `runc` or `nvidia` (`HostConfig.Runtime`). Unset uses the daemon default, which is not reported."),
		"shm_size": schema.Int64Attribute{Optional: true, PlanModifiers: rrInt,
			MarkdownDescription: "Size of `/dev/shm` in bytes (`HostConfig.ShmSize`). Unset uses the daemon default (64 MiB), which is not reported. Forces replacement."},
		"storage_opt": schema.MapAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: rrMap,
			MarkdownDescription: "Storage driver options, e.g. `{ size = \"20G\" }` (`HostConfig.StorageOpt`). Forces replacement."},
		"tmpfs": schema.MapAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: rrMap,
			MarkdownDescription: "tmpfs mounts keyed by container path, values are mount options such as `rw,size=64m` (`HostConfig.Tmpfs`, `docker run --tmpfs`). " +
				"`mounts` with `type = \"tmpfs\"` is the structured alternative. Forces replacement."},
		"userns_mode": optStr("User namespace mode (`HostConfig.UsernsMode`), e.g. `host`."),
		"volumes_from": schema.ListAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: rrList,
			MarkdownDescription: "Mount the volumes of other containers, `<name|id>[:ro|:rw]` (`HostConfig.VolumesFrom`). Forces replacement."},
		"cap_add": schema.SetAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			MarkdownDescription: "Linux capabilities to add, e.g. `NET_ADMIN` (`HostConfig.CapAdd`). Forces replacement."},
		"cap_drop": schema.SetAttribute{ElementType: types.StringType, Optional: true, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
			MarkdownDescription: "Linux capabilities to drop, e.g. `ALL` (`HostConfig.CapDrop`). Forces replacement."},
	}
}

// ctrGapBlocks are the gap blocks and the logging block.
func ctrGapBlocks() map[string]schema.Block {
	throttle := func(field, unit string) schema.ListNestedBlock {
		return schema.ListNestedBlock{
			MarkdownDescription: fmt.Sprintf("Per-device limit in %s (`HostConfig.%s`). Forces replacement.", unit, field),
			PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
			NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
				"path": schema.StringAttribute{Required: true, MarkdownDescription: "Device path on the host, e.g. `/dev/sda`."},
				"rate": schema.Int64Attribute{Required: true, MarkdownDescription: "Limit in " + unit + "."},
			}},
		}
	}
	return map[string]schema.Block{
		"blkio_device_read_bps":   throttle("BlkioDeviceReadBps", "bytes per second"),
		"blkio_device_write_bps":  throttle("BlkioDeviceWriteBps", "bytes per second"),
		"blkio_device_read_iops":  throttle("BlkioDeviceReadIOps", "IO operations per second"),
		"blkio_device_write_iops": throttle("BlkioDeviceWriteIOps", "IO operations per second"),
		"device_requests": schema.ListNestedBlock{
			MarkdownDescription: "Device requests to device drivers (`HostConfig.DeviceRequests`), e.g. GPUs from a driver other than the one `gpus` uses. Forces replacement.",
			PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
			NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
				"driver": schema.StringAttribute{Optional: true, MarkdownDescription: "Device driver, e.g. `nvidia`."},
				"count": schema.Int64Attribute{Optional: true,
					MarkdownDescription: "Number of devices to request; `-1` requests all. Conflicts with `device_ids` in the daemon."},
				"device_ids": schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Device IDs or indexes to request."},
				"capabilities": schema.ListAttribute{ElementType: types.ListType{ElemType: types.StringType}, Optional: true,
					MarkdownDescription: "Capability sets: the request matches a device having every capability of any one inner list, e.g. `[[\"gpu\"]]`."},
				"options": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Driver-specific options."},
			}},
		},
		"logging": schema.SingleNestedBlock{
			MarkdownDescription: "Logging driver and its options (`HostConfig.LogConfig`). Without this block the daemon defaults apply and are not reported. " +
				"Adding or changing the block forces a new resource; removing it keeps the container as it is.",
			PlanModifiers: []planmodifier.Object{ctrLoggingReplaceIf()},
			Attributes: map[string]schema.Attribute{
				"driver": schema.StringAttribute{Optional: true, Computed: true,
					PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
					MarkdownDescription: "Logging driver, e.g. `json-file`, `local` or `syslog` (`LogConfig.Type`). Unset uses the daemon default, which is then reported."},
				"options": schema.MapAttribute{ElementType: types.StringType, Optional: true, Computed: true,
					PlanModifiers:       []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
					MarkdownDescription: "Driver options, e.g. `{ max-size = \"10m\" }` (`LogConfig.Config`). Unset uses the daemon defaults."},
			},
		},
	}
}

// ctrLoggingReplaceIf replaces the container when the logging block is added
// or changed, but not when it is removed.
func ctrLoggingReplaceIf() planmodifier.Object {
	desc := "Adding or changing logging forces a new container; removing the block does not."
	return objectplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
		if req.PlanValue.IsNull() {
			return
		}
		if req.StateValue.IsNull() {
			resp.RequiresReplace = true
			return
		}
		p, s := req.PlanValue.Attributes(), req.StateValue.Attributes()
		for _, k := range []string{"driver", "options"} {
			pv := p[k]
			if pv == nil || pv.IsUnknown() || pv.IsNull() {
				continue
			}
			if !pv.Equal(s[k]) {
				resp.RequiresReplace = true
			}
		}
	}, desc, desc)
}

// ctrRequiresReplaceIfClearedString is containerRequiresReplaceIfCleared for
// a string ContainerUpdate field ("" means unchanged).
func ctrRequiresReplaceIfClearedString() planmodifier.String {
	desc := "Removing the value forces a new container."
	return stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.StateValue.ValueString() == "" {
			return
		}
		resp.RequiresReplace = !req.PlanValue.IsUnknown() && (req.PlanValue.IsNull() || req.PlanValue.ValueString() == "")
	}, desc, desc)
}

// ctrParsePlatform parses "os[/arch[/variant]]".
func ctrParsePlatform(s string) (*ocispec.Platform, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, "/")
	if len(parts) > 3 || parts[0] == "" {
		return nil, fmt.Errorf("%q: want os[/arch[/variant]], e.g. linux/arm64", s)
	}
	p := &ocispec.Platform{OS: parts[0]}
	if len(parts) > 1 {
		p.Architecture = parts[1]
	}
	if len(parts) > 2 {
		p.Variant = parts[2]
	}
	return p, nil
}

// ctrExpandThrottle builds a blkio throttle list.
func ctrExpandThrottle(in []ctrThrottleModel) []*blkiodev.ThrottleDevice {
	if len(in) == 0 {
		return nil
	}
	out := make([]*blkiodev.ThrottleDevice, 0, len(in))
	for _, t := range in {
		out = append(out, &blkiodev.ThrottleDevice{Path: t.Path.ValueString(), Rate: uint64(t.Rate.ValueInt64())})
	}
	return out
}

func ctrFlattenThrottle(in []*blkiodev.ThrottleDevice, prior []ctrThrottleModel) []ctrThrottleModel {
	got := make([]ctrThrottleModel, 0, len(in))
	for _, t := range in {
		if t == nil {
			continue
		}
		got = append(got, ctrThrottleModel{Path: types.StringValue(t.Path), Rate: types.Int64Value(int64(t.Rate))})
	}
	return containerOrderByPrior(prior, got, func(t ctrThrottleModel) string { return t.Path.ValueString() })
}

// ctrExpandDeviceRequests builds the device_requests entries.
func ctrExpandDeviceRequests(ctx context.Context, in []ctrDeviceRequestModel, diags *diag.Diagnostics) []container.DeviceRequest {
	out := make([]container.DeviceRequest, 0, len(in))
	for _, d := range in {
		r := container.DeviceRequest{
			Driver:    d.Driver.ValueString(),
			Count:     int(d.Count.ValueInt64()),
			DeviceIDs: stringListFrom(ctx, d.DeviceIDs, diags),
			Options:   ctrStringMap(d.Options),
		}
		if containerKnown(d.Capabilities) {
			diags.Append(d.Capabilities.ElementsAs(ctx, &r.Capabilities, false)...)
		}
		out = append(out, r)
	}
	return out
}

func ctrDeviceRequestKey(d ctrDeviceRequestModel) string {
	return fmt.Sprintf("%s|%d|%s|%s", d.Driver.ValueString(), d.Count.ValueInt64(), d.DeviceIDs.String(), d.Capabilities.String())
}

func ctrFlattenDeviceRequests(in []container.DeviceRequest, prior []ctrDeviceRequestModel) []ctrDeviceRequestModel {
	got := make([]ctrDeviceRequestModel, 0, len(in))
	capsType := types.ListType{ElemType: types.StringType}
	for _, r := range in {
		caps := make([]attr.Value, 0, len(r.Capabilities))
		for _, c := range r.Capabilities {
			caps = append(caps, stringListValue(c, false))
		}
		m := ctrDeviceRequestModel{
			Driver:       stringOrNull(r.Driver),
			Count:        types.Int64Null(),
			DeviceIDs:    types.ListNull(types.StringType),
			Capabilities: types.ListNull(capsType),
			Options:      types.MapNull(types.StringType),
		}
		if r.Count != 0 {
			m.Count = types.Int64Value(int64(r.Count))
		}
		if len(r.DeviceIDs) > 0 {
			m.DeviceIDs = stringListValue(r.DeviceIDs, false)
		}
		if len(caps) > 0 {
			m.Capabilities = types.ListValueMust(capsType, caps)
		}
		if len(r.Options) > 0 {
			m.Options = stringMapValue(r.Options, false)
		}
		got = append(got, m)
	}
	return containerOrderByPrior(prior, got, ctrDeviceRequestKey)
}

// ctrSplitDeviceRequests separates the request that `gpus` produced from the
// device_requests entries: when gpus was set before (or on import), the
// first GPU request belongs to gpus.
func ctrSplitDeviceRequests(reqs []container.DeviceRequest, priorGPUs bool) (gpu, rest []container.DeviceRequest) {
	taken := !priorGPUs
	for _, r := range reqs {
		if !taken && containerIsGPURequest(r) {
			gpu = append(gpu, r)
			taken = true
			continue
		}
		rest = append(rest, r)
	}
	return gpu, rest
}

// ctrPriorOnlyString reports v only when the prior value was set: for fields
// the daemon fills with its own default.
func ctrPriorOnlyString(prior types.String, v string) types.String {
	if !containerKnown(prior) {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// ctrInitRead maps HostConfig.Init.
func ctrInitRead(prior types.Bool, v *bool) types.Bool {
	if v == nil {
		if containerKnown(prior) && !prior.ValueBool() {
			return prior
		}
		return types.BoolNull()
	}
	if !*v && !containerKnown(prior) {
		return types.BoolNull()
	}
	return types.BoolValue(*v)
}

// ctrSetRead reports a set, keeping prior when it holds the same elements.
func ctrSetRead(prior types.Set, got []string) types.Set {
	return containerSecurityOptRead(prior, got)
}

// ctrLoggingRead maps LogConfig onto the logging block. It is reported only
// when the prior block was set, or on import when the daemon reports a
// driver.
func ctrLoggingRead(lc container.LogConfig, prior *ctrLoggingModel, importing bool) *ctrLoggingModel {
	if prior == nil && !(importing && lc.Type != "") {
		return nil
	}
	p := prior
	if p == nil {
		p = &ctrLoggingModel{Driver: types.StringNull(), Options: types.MapNull(types.StringType)}
	}
	out := &ctrLoggingModel{Driver: stringOrNull(lc.Type), Options: containerMapValue(p.Options, lc.Config)}
	if out.Driver.IsNull() && containerKnown(p.Driver) {
		out.Driver = p.Driver
	}
	return out
}

// ctrApplyGap copies the gap attributes into the create arguments.
func ctrApplyGap(ctx context.Context, m containerResourceModel, cfg *container.Config, hc *container.HostConfig, diags *diag.Diagnostics) {
	cfg.Domainname = m.Domainname.ValueString()
	hc.CgroupParent = m.CgroupParent.ValueString()
	hc.CgroupnsMode = container.CgroupnsMode(m.CgroupnsMode.ValueString())
	hc.CPUPeriod = m.CPUPeriod.ValueInt64()
	hc.CPUQuota = m.CPUQuota.ValueInt64()
	hc.CpusetCpus = m.CpusetCpus.ValueString()
	hc.MemoryReservation = m.MemoryReservation.ValueInt64()
	hc.DeviceCgroupRules = stringListFrom(ctx, m.DeviceCgroupRules, diags)
	hc.DNSOptions = stringListFrom(ctx, m.DNSOptions, diags)
	hc.GroupAdd = stringSetFrom(ctx, m.GroupAdd, diags)
	if v, ok := knownBool(m.Init); ok {
		hc.Init = &v
	}
	hc.IpcMode = container.IpcMode(m.IpcMode.ValueString())
	hc.PidMode = container.PidMode(m.PidMode.ValueString())
	hc.PublishAllPorts = m.PublishAllPorts.ValueBool()
	hc.AutoRemove = m.AutoRemove.ValueBool()
	hc.Runtime = m.Runtime.ValueString()
	hc.ShmSize = m.ShmSize.ValueInt64()
	if so := ctrStringMap(m.StorageOpt); len(so) > 0 {
		hc.StorageOpt = so
	}
	if t := ctrStringMap(m.Tmpfs); len(t) > 0 {
		hc.Tmpfs = t
	}
	hc.UsernsMode = container.UsernsMode(m.UsernsMode.ValueString())
	hc.VolumesFrom = stringListFrom(ctx, m.VolumesFrom, diags)
	hc.CapAdd = stringSetFrom(ctx, m.CapAdd, diags)
	hc.CapDrop = stringSetFrom(ctx, m.CapDrop, diags)
	hc.BlkioDeviceReadBps = ctrExpandThrottle(m.BlkioDeviceReadBps)
	hc.BlkioDeviceWriteBps = ctrExpandThrottle(m.BlkioDeviceWriteBps)
	hc.BlkioDeviceReadIOps = ctrExpandThrottle(m.BlkioDeviceReadIOps)
	hc.BlkioDeviceWriteIOps = ctrExpandThrottle(m.BlkioDeviceWriteIOps)
	if l := m.Logging; l != nil {
		if d, ok := knownString(l.Driver); ok {
			hc.LogConfig.Type = d
		}
		if containerKnown(l.Options) {
			hc.LogConfig.Config = ctrStringMap(l.Options)
		}
	}
	if len(m.DeviceRequests) > 0 {
		hc.DeviceRequests = append(hc.DeviceRequests, ctrExpandDeviceRequests(ctx, m.DeviceRequests, diags)...)
	}
}

// ctrReadGap maps the gap fields of an inspect response onto out.
func ctrReadGap(insp container.InspectResponse, cfg *container.Config, hc *container.HostConfig, prior containerResourceModel, out *containerResourceModel, importing bool) {
	out.Domainname = containerStringRead(prior.Domainname, cfg.Domainname, "")
	out.CgroupParent = containerStringRead(prior.CgroupParent, hc.CgroupParent, "")
	out.CgroupnsMode = ctrPriorOnlyString(prior.CgroupnsMode, string(hc.CgroupnsMode))
	out.CPUPeriod = containerInt64Read(prior.CPUPeriod, hc.CPUPeriod)
	out.CPUQuota = containerInt64Read(prior.CPUQuota, hc.CPUQuota)
	out.CpusetCpus = containerStringRead(prior.CpusetCpus, hc.CpusetCpus, "")
	out.MemoryReservation = containerInt64Read(prior.MemoryReservation, hc.MemoryReservation)
	out.DeviceCgroupRules = containerListRead(prior.DeviceCgroupRules, hc.DeviceCgroupRules, nil)
	out.DNSOptions = containerListRead(prior.DNSOptions, hc.DNSOptions, nil)
	out.GroupAdd = ctrSetRead(prior.GroupAdd, hc.GroupAdd)
	out.Init = ctrInitRead(prior.Init, hc.Init)
	out.IpcMode = ctrPriorOnlyString(prior.IpcMode, string(hc.IpcMode))
	if !containerKnown(prior.NetworkMode) {
		out.NetworkMode = types.StringNull()
	}
	out.PidMode = containerStringRead(prior.PidMode, string(hc.PidMode), "")
	if !containerKnown(prior.Platform) {
		out.Platform = types.StringNull()
	}
	out.PublishAllPorts = types.BoolValue(hc.PublishAllPorts)
	out.AutoRemove = types.BoolValue(hc.AutoRemove)
	out.Runtime = ctrPriorOnlyString(prior.Runtime, hc.Runtime)
	if containerKnown(prior.ShmSize) {
		out.ShmSize = types.Int64Value(hc.ShmSize)
	} else {
		out.ShmSize = types.Int64Null()
	}
	out.StorageOpt = containerMapValue(prior.StorageOpt, hc.StorageOpt)
	out.Tmpfs = containerMapValue(prior.Tmpfs, hc.Tmpfs)
	out.UsernsMode = containerStringRead(prior.UsernsMode, string(hc.UsernsMode), "")
	out.VolumesFrom = containerListRead(prior.VolumesFrom, hc.VolumesFrom, nil)
	out.CapAdd = containerCapSet(prior.CapAdd, containerSetStrings(prior.CapAdd), hc.CapAdd)
	out.CapDrop = containerCapSet(prior.CapDrop, containerSetStrings(prior.CapDrop), hc.CapDrop)
	out.BlkioDeviceReadBps = ctrFlattenThrottle(hc.BlkioDeviceReadBps, prior.BlkioDeviceReadBps)
	out.BlkioDeviceWriteBps = ctrFlattenThrottle(hc.BlkioDeviceWriteBps, prior.BlkioDeviceWriteBps)
	out.BlkioDeviceReadIOps = ctrFlattenThrottle(hc.BlkioDeviceReadIOps, prior.BlkioDeviceReadIOps)
	out.BlkioDeviceWriteIOps = ctrFlattenThrottle(hc.BlkioDeviceWriteIOps, prior.BlkioDeviceWriteIOps)
	out.Logging = ctrLoggingRead(hc.LogConfig, prior.Logging, importing)
	gpu, rest := ctrSplitDeviceRequests(hc.DeviceRequests, containerKnown(prior.GPUs) || importing)
	out.GPUs = containerFlattenGPUs(gpu, prior.GPUs)
	out.DeviceRequests = ctrFlattenDeviceRequests(rest, prior.DeviceRequests)
	out.ExitCode = types.Int64Value(0)
	if insp.State != nil {
		out.ExitCode = types.Int64Value(int64(insp.State.ExitCode))
	}
}

// ctrMountOptionBlocks are the option blocks of a mounts entry
// (mount.TmpfsOptions, BindOptions, VolumeOptions).
func ctrMountOptionBlocks() map[string]schema.Block {
	return map[string]schema.Block{
		"tmpfs_options": schema.SingleNestedBlock{
			MarkdownDescription: "tmpfs options (`TmpfsOptions`); `type = \"tmpfs\"` only.",
			Attributes: map[string]schema.Attribute{
				"size_bytes": schema.Int64Attribute{Optional: true, MarkdownDescription: "Size of the tmpfs in bytes (`SizeBytes`). Unset is unlimited."},
				"mode": schema.StringAttribute{Optional: true,
					MarkdownDescription: "Permission bits of the tmpfs root as an octal string, e.g. `\"1777\"` (`Mode`)."},
				"options": schema.ListAttribute{ElementType: types.ListType{ElemType: types.StringType}, Optional: true,
					MarkdownDescription: "Options passed to the tmpfs mount (`Options`): a flag as a one-element list, a key and value as two, " +
						"e.g. `[[\"noexec\"], [\"nr_inodes\", \"1000\"]]`." + apiSince("1.46")},
			},
		},
		"bind_options": schema.SingleNestedBlock{
			MarkdownDescription: "Bind mount options (`BindOptions`); `type = \"bind\"` only.",
			Attributes: map[string]schema.Attribute{
				"propagation": schema.StringAttribute{Optional: true,
					MarkdownDescription: "Mount propagation (`Propagation`): `private`, `rprivate`, `shared`, `rshared`, `slave` or `rslave`.",
					Validators:          []validator.String{stringvalidator.OneOf("private", "rprivate", "shared", "rshared", "slave", "rslave")}},
				"non_recursive": schema.BoolAttribute{Optional: true,
					MarkdownDescription: "Do not bind-mount the submounts of the source recursively (`NonRecursive`)."},
				"create_mountpoint": schema.BoolAttribute{Optional: true,
					MarkdownDescription: "Create the source path on the host when it is missing (`CreateMountpoint`)."},
				"read_only_non_recursive": schema.BoolAttribute{Optional: true,
					MarkdownDescription: "With `read_only`, make only the mount itself read-only, not its submounts (`ReadOnlyNonRecursive`)."},
				"read_only_force_recursive": schema.BoolAttribute{Optional: true,
					MarkdownDescription: "With `read_only`, fail when the mount cannot be made recursively read-only (`ReadOnlyForceRecursive`)."},
			},
		},
		"image_options": schema.SingleNestedBlock{
			MarkdownDescription: "Image mount options (`ImageOptions`); `type = \"image\"` only." + apiSince("1.48"),
			Attributes: map[string]schema.Attribute{
				"subpath": schema.StringAttribute{Optional: true,
					MarkdownDescription: "Path inside the image to mount instead of its root (`Subpath`)."},
			},
		},
		"volume_options": schema.SingleNestedBlock{
			MarkdownDescription: "Volume mount options (`VolumeOptions`); `type = \"volume\"` only.",
			Attributes: map[string]schema.Attribute{
				"no_copy": schema.BoolAttribute{Optional: true,
					MarkdownDescription: "Do not copy the image's content at the target into a new volume (`NoCopy`)."},
				"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
					MarkdownDescription: "Labels for a volume the mount creates (`Labels`)."},
				"subpath": schema.StringAttribute{Optional: true,
					MarkdownDescription: "Path inside the volume to mount instead of its root (`Subpath`)."},
			},
			Blocks: map[string]schema.Block{
				"driver_config": schema.SingleNestedBlock{
					MarkdownDescription: "Volume driver for a volume the mount creates (`DriverConfig`).",
					Attributes: map[string]schema.Attribute{
						"name":    schema.StringAttribute{Optional: true, MarkdownDescription: "Volume driver name (`Name`)."},
						"options": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Driver options (`Options`)."},
					},
				},
			},
		},
	}
}
