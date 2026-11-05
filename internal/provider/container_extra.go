package provider

// Pass 2: expand/flatten helpers for the docker_container attributes added
// in Pass 2 (hostname, dns, extra_hosts, devices, tty, stdin_open,
// stop_signal, stop_timeout, sysctls, security_opt, read_only, gpus). All of
// them are create-time only (ContainerUpdate cannot change them), so the
// schema marks them RequiresReplace; Read is prior-aware so daemon defaults
// never show as drift.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type containerDeviceModel struct {
	HostPath      types.String `tfsdk:"host_path"`
	ContainerPath types.String `tfsdk:"container_path"`
	Permissions   types.String `tfsdk:"permissions"`
}

// containerDefaultDevicePerms is the cgroup permission string the docker CLI
// uses when none is given.
const containerDefaultDevicePerms = "rwm"

// containerGPUDriver is the device driver gpus requests go to (as in
// `docker run --gpus`).
const containerGPUDriver = "nvidia"

// containerExpandDevices builds HostConfig.Devices. container_path defaults
// to host_path and permissions to "rwm", like `docker run --device`.
func containerExpandDevices(in []containerDeviceModel) ([]container.DeviceMapping, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]container.DeviceMapping, 0, len(in))
	for i, d := range in {
		host := d.HostPath.ValueString()
		if host == "" {
			return nil, fmt.Errorf("devices[%d].host_path is required", i)
		}
		dm := container.DeviceMapping{PathOnHost: host, PathInContainer: host, CgroupPermissions: containerDefaultDevicePerms}
		if v, ok := knownString(d.ContainerPath); ok && v != "" {
			dm.PathInContainer = v
		}
		if v, ok := knownString(d.Permissions); ok && v != "" {
			for _, c := range v {
				if c != 'r' && c != 'w' && c != 'm' {
					return nil, fmt.Errorf("devices[%d].permissions %q: only the letters r, w and m are allowed", i, v)
				}
			}
			dm.CgroupPermissions = v
		}
		out = append(out, dm)
	}
	return out, nil
}

func containerDeviceKey(d containerDeviceModel) string {
	cp := d.ContainerPath.ValueString()
	if cp == "" {
		cp = d.HostPath.ValueString()
	}
	return d.HostPath.ValueString() + "|" + cp
}

// containerFlattenDevices maps HostConfig.Devices back to blocks, ordered like
// prior. container_path stays null when it equals host_path and the prior
// entry did not set it.
func containerFlattenDevices(in []container.DeviceMapping, prior []containerDeviceModel) []containerDeviceModel {
	priorCP := map[string]types.String{}
	for _, p := range prior {
		priorCP[containerDeviceKey(p)] = p.ContainerPath
	}
	got := make([]containerDeviceModel, 0, len(in))
	for _, d := range in {
		m := containerDeviceModel{
			HostPath:      types.StringValue(d.PathOnHost),
			ContainerPath: types.StringValue(d.PathInContainer),
			Permissions:   types.StringValue(d.CgroupPermissions),
		}
		if d.CgroupPermissions == "" {
			m.Permissions = types.StringValue(containerDefaultDevicePerms)
		}
		pcp, seen := priorCP[containerDeviceKey(m)]
		if d.PathInContainer == "" || (d.PathInContainer == d.PathOnHost && (!seen || !containerKnown(pcp))) {
			m.ContainerPath = types.StringNull()
		}
		got = append(got, m)
	}
	return containerOrderByPrior(prior, got, containerDeviceKey)
}

// containerExpandGPUs turns the gpus string into a DeviceRequest:
// "all" (every GPU), a positive count such as "2", or "device=0,1" (GPU
// indexes or UUIDs). Empty gives nil.
func containerExpandGPUs(s string) (*container.DeviceRequest, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	req := &container.DeviceRequest{Driver: containerGPUDriver, Capabilities: [][]string{{"gpu"}}}
	switch {
	case s == "all":
		req.Count = -1
	case strings.HasPrefix(s, "device="):
		for _, id := range strings.Split(strings.TrimPrefix(s, "device="), ",") {
			if id = strings.TrimSpace(id); id != "" {
				req.DeviceIDs = append(req.DeviceIDs, id)
			}
		}
		if len(req.DeviceIDs) == 0 {
			return nil, fmt.Errorf("gpus %q: device= needs at least one GPU index or UUID", s)
		}
	default:
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("gpus %q: want \"all\", a positive count, or \"device=<id>[,<id>...]\"", s)
		}
		req.Count = n
	}
	return req, nil
}

// containerIsGPURequest reports whether r asks for GPUs.
func containerIsGPURequest(r container.DeviceRequest) bool {
	for _, and := range r.Capabilities {
		for _, c := range and {
			if c == "gpu" {
				return true
			}
		}
	}
	return false
}

func containerGPUString(r container.DeviceRequest) string {
	switch {
	case len(r.DeviceIDs) > 0:
		return "device=" + strings.Join(r.DeviceIDs, ",")
	case r.Count < 0:
		return "all"
	default:
		return strconv.Itoa(r.Count)
	}
}

// containerFlattenGPUs reads the GPU device request back, keeping the prior
// spelling when it expands to the same request.
func containerFlattenGPUs(reqs []container.DeviceRequest, prior types.String) types.String {
	for _, r := range reqs {
		if !containerIsGPURequest(r) {
			continue
		}
		if p, ok := knownString(prior); ok {
			if want, err := containerExpandGPUs(p); err == nil && want != nil &&
				want.Count == r.Count && containerStringsEqual(want.DeviceIDs, r.DeviceIDs) {
				return prior
			}
		}
		return types.StringValue(containerGPUString(r))
	}
	return types.StringNull()
}

// containerHostnameRead reports the hostname, but null when the user did not
// set it and it is the daemon default (the first 12 characters of the ID).
func containerHostnameRead(prior types.String, got, containerID string) types.String {
	if containerKnown(prior) {
		return types.StringValue(got)
	}
	if got == "" || (len(containerID) >= 12 && got == containerID[:12]) {
		return types.StringNull()
	}
	return types.StringValue(got)
}

// containerStopTimeoutRead maps Config.StopTimeout to state.
func containerStopTimeoutRead(got *int) types.Int64 {
	if got == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*got))
}

// containerSysctlsRead reports sysctls. Outside import only keys already in
// prior are reported, in case the daemon adds network-namespace sysctls.
func containerSysctlsRead(prior types.Map, got map[string]string, importing bool) types.Map {
	return containerMapValue(prior, containerSubtractDefaults(got, nil, containerMapKeys(prior), !importing))
}

// containerSecurityOptRead reports security options, keeping the prior set
// when it holds the same entries.
func containerSecurityOptRead(prior types.Set, got []string) types.Set {
	if containerKnown(prior) {
		p := append([]string(nil), containerSetStrings(prior)...)
		g := append([]string(nil), got...)
		sort.Strings(p)
		sort.Strings(g)
		if containerStringsEqual(p, g) {
			return prior
		}
	}
	return containerSetValue(prior, got)
}
