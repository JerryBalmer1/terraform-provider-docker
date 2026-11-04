package provider

// Lane 2: pure expand/flatten helpers for docker_container. Nothing here
// talks to the daemon, so every function is unit-tested in
// container_expand_test.go.
//
// Read strategy ("diff against inspect, minus image defaults"):
//   - Values the daemon stores exactly as requested (HostConfig.PortBindings,
//     HostConfig.Mounts, HostConfig.Ulimits, CapAdd/CapDrop, endpoint IPAM
//     config) are read back from ContainerInspect as-is.
//   - Values the daemon merges with the image config (Env, Labels, Cmd,
//     Entrypoint, User, WorkingDir, Healthcheck) are compared with
//     ImageInspect(container.Image).Config: an entry is reported when it
//     differs from the image's value OR when the prior state/plan already
//     holds it (so a user who explicitly sets a value equal to the image
//     default does not see a perpetual diff).
//   - Lists are ordered like the prior state so block order in HCL survives
//     a refresh; entries the daemon has but the prior state does not are
//     appended in a stable sorted order (drift).

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type containerPortModel struct {
	Internal types.Int64  `tfsdk:"internal"`
	External types.Int64  `tfsdk:"external"`
	IP       types.String `tfsdk:"ip"`
	Protocol types.String `tfsdk:"protocol"`
}

type containerMountModel struct {
	Type      types.String `tfsdk:"type"`
	Source    types.String `tfsdk:"source"`
	Target    types.String `tfsdk:"target"`
	ReadOnly  types.Bool   `tfsdk:"read_only"`
	TmpfsSize types.Int64  `tfsdk:"tmpfs_size"`
}

type containerNetworkModel struct {
	Name        types.String `tfsdk:"name"`
	Aliases     types.Set    `tfsdk:"aliases"`
	IPv4Address types.String `tfsdk:"ipv4_address"`
}

type containerHealthcheckModel struct {
	Test        types.List   `tfsdk:"test"`
	Interval    types.String `tfsdk:"interval"`
	Timeout     types.String `tfsdk:"timeout"`
	StartPeriod types.String `tfsdk:"start_period"`
	Retries     types.Int64  `tfsdk:"retries"`
}

type containerCapabilitiesModel struct {
	Add  types.Set `tfsdk:"add"`
	Drop types.Set `tfsdk:"drop"`
}

type containerUlimitModel struct {
	Name types.String `tfsdk:"name"`
	Soft types.Int64  `tfsdk:"soft"`
	Hard types.Int64  `tfsdk:"hard"`
}

// ---------------------------------------------------------------------------
// Generic helpers

// containerOrderByPrior returns got re-ordered so items whose key appears in
// prior come first, in prior's order; the rest follow sorted by key. The
// result is never nil.
func containerOrderByPrior[T any](prior, got []T, key func(T) string) []T {
	out := make([]T, 0, len(got))
	used := make([]bool, len(got))
	for _, p := range prior {
		pk := key(p)
		for i, g := range got {
			if !used[i] && key(g) == pk {
				out = append(out, g)
				used[i] = true
				break
			}
		}
	}
	var rest []T
	for i, g := range got {
		if !used[i] {
			rest = append(rest, g)
		}
	}
	sort.SliceStable(rest, func(a, b int) bool { return key(rest[a]) < key(rest[b]) })
	return append(out, rest...)
}

func containerKnown(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

// containerInt64Read maps a daemon int to state: 0 stays null when the prior
// value was null (the user did not set it).
func containerInt64Read(prior types.Int64, v int64) types.Int64 {
	if v == 0 && !containerKnown(prior) {
		return types.Int64Null()
	}
	return types.Int64Value(v)
}

// containerStringRead reports v, but null when the prior was null and v is
// empty or equal to the image default.
func containerStringRead(prior types.String, v, imageDefault string) types.String {
	if containerKnown(prior) {
		return types.StringValue(v)
	}
	if v == "" || v == imageDefault {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// containerListRead reports got, but null when the prior was null and got is
// empty or equal to the image default.
func containerListRead(prior types.List, got, imageDefault []string) types.List {
	if containerKnown(prior) {
		return stringListValue(got, false)
	}
	if len(got) == 0 || containerStringsEqual(got, imageDefault) {
		return types.ListNull(types.StringType)
	}
	return stringListValue(got, false)
}

// containerMapValue converts m to map(string). Empty gives null unless the
// prior was a known empty map (the user wrote `{}`).
func containerMapValue(prior types.Map, m map[string]string) types.Map {
	if len(m) == 0 {
		if containerKnown(prior) && len(prior.Elements()) == 0 {
			return prior
		}
		return types.MapNull(types.StringType)
	}
	return stringMapValue(m, false)
}

// containerSetValue converts s to set(string). Empty gives null unless the
// prior was a known empty set.
func containerSetValue(prior types.Set, s []string) types.Set {
	if len(s) == 0 {
		if containerKnown(prior) && len(prior.Elements()) == 0 {
			return prior
		}
		return types.SetNull(types.StringType)
	}
	return stringSetValue(s, false)
}

func containerStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// containerMapKeys returns the keys of a known map attribute.
func containerMapKeys(m types.Map) map[string]bool {
	out := map[string]bool{}
	if !containerKnown(m) {
		return out
	}
	for k := range m.Elements() {
		out[k] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// Env and labels

// containerEnvToSlice turns an env map into sorted KEY=VALUE entries.
func containerEnvToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// containerEnvToMap parses KEY=VALUE entries; an entry without "=" maps to "".
func containerEnvToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		out[k] = v
	}
	return out
}

// containerSubtractDefaults keeps entries of got that differ from defaults or
// whose key is in keep. When onlyKeep is set (the image config could not be
// inspected) only keys in keep are reported.
func containerSubtractDefaults(got, defaults map[string]string, keep map[string]bool, onlyKeep bool) map[string]string {
	out := map[string]string{}
	for k, v := range got {
		if keep[k] {
			out[k] = v
			continue
		}
		if onlyKeep {
			continue
		}
		if dv, ok := defaults[k]; ok && dv == v {
			continue
		}
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// Ports

func containerPortProtocol(p containerPortModel) string {
	if s, ok := knownString(p.Protocol); ok && s != "" {
		return s
	}
	return "tcp"
}

// containerExpandPorts builds Config.ExposedPorts and HostConfig.PortBindings.
func containerExpandPorts(ports []containerPortModel) (nat.PortSet, nat.PortMap, error) {
	if len(ports) == 0 {
		return nil, nil, nil
	}
	set := nat.PortSet{}
	pm := nat.PortMap{}
	for i, p := range ports {
		internal, ok := knownInt64(p.Internal)
		if !ok || internal < 1 || internal > 65535 {
			return nil, nil, fmt.Errorf("ports[%d].internal must be 1-65535", i)
		}
		port, err := nat.NewPort(containerPortProtocol(p), strconv.FormatInt(internal, 10))
		if err != nil {
			return nil, nil, fmt.Errorf("ports[%d]: %w", i, err)
		}
		set[port] = struct{}{}
		b := nat.PortBinding{}
		if ext, ok := knownInt64(p.External); ok && ext > 0 {
			b.HostPort = strconv.FormatInt(ext, 10)
		}
		if ip, ok := knownString(p.IP); ok {
			b.HostIP = ip
		}
		pm[port] = append(pm[port], b)
	}
	return set, pm, nil
}

func containerPortKey(p containerPortModel) string {
	return fmt.Sprintf("%05d/%s/%s/%d", p.Internal.ValueInt64(), containerPortProtocol(p), p.IP.ValueString(), p.External.ValueInt64())
}

// containerFlattenPorts maps HostConfig.PortBindings back to blocks, ordered
// like prior.
func containerFlattenPorts(pm nat.PortMap, prior []containerPortModel) []containerPortModel {
	var got []containerPortModel
	for port, bindings := range pm {
		if len(bindings) == 0 {
			bindings = []nat.PortBinding{{}}
		}
		for _, b := range bindings {
			m := containerPortModel{
				Internal: types.Int64Value(int64(port.Int())),
				External: types.Int64Null(),
				IP:       stringOrNull(b.HostIP),
				Protocol: types.StringValue(port.Proto()),
			}
			if b.HostPort != "" {
				if n, err := strconv.ParseInt(b.HostPort, 10, 64); err == nil {
					m.External = types.Int64Value(n)
				}
			}
			got = append(got, m)
		}
	}
	return containerOrderByPrior(prior, got, containerPortKey)
}

// ---------------------------------------------------------------------------
// Mounts

// containerExpandMounts builds HostConfig.Mounts.
func containerExpandMounts(in []containerMountModel) ([]mount.Mount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]mount.Mount, 0, len(in))
	for i, m := range in {
		t := m.Type.ValueString()
		mm := mount.Mount{
			Type:     mount.Type(t),
			Source:   m.Source.ValueString(),
			Target:   m.Target.ValueString(),
			ReadOnly: m.ReadOnly.ValueBool(),
		}
		if mm.Target == "" {
			return nil, fmt.Errorf("mounts[%d].target is required", i)
		}
		switch mm.Type {
		case mount.TypeBind:
			if mm.Source == "" {
				return nil, fmt.Errorf("mounts[%d]: a bind mount needs source (host path)", i)
			}
		case mount.TypeVolume:
		case mount.TypeTmpfs:
			if mm.Source != "" {
				return nil, fmt.Errorf("mounts[%d]: a tmpfs mount takes no source", i)
			}
			if sz, ok := knownInt64(m.TmpfsSize); ok && sz > 0 {
				mm.TmpfsOptions = &mount.TmpfsOptions{SizeBytes: sz}
			}
		default:
			return nil, fmt.Errorf("mounts[%d].type %q: must be bind, volume or tmpfs", i, t)
		}
		if mm.Type != mount.TypeTmpfs && containerKnown(m.TmpfsSize) && m.TmpfsSize.ValueInt64() != 0 {
			return nil, fmt.Errorf("mounts[%d].tmpfs_size only applies to type tmpfs", i)
		}
		out = append(out, mm)
	}
	return out, nil
}

func containerMountKey(m containerMountModel) string {
	return m.Target.ValueString() + "|" + m.Type.ValueString() + "|" + m.Source.ValueString()
}

// containerFlattenMounts maps HostConfig.Mounts back to blocks.
func containerFlattenMounts(in []mount.Mount, prior []containerMountModel) []containerMountModel {
	got := make([]containerMountModel, 0, len(in))
	for _, m := range in {
		mm := containerMountModel{
			Type:      types.StringValue(string(m.Type)),
			Source:    stringOrNull(m.Source),
			Target:    types.StringValue(m.Target),
			ReadOnly:  types.BoolValue(m.ReadOnly),
			TmpfsSize: types.Int64Null(),
		}
		if m.TmpfsOptions != nil && m.TmpfsOptions.SizeBytes > 0 {
			mm.TmpfsSize = types.Int64Value(m.TmpfsOptions.SizeBytes)
		}
		got = append(got, mm)
	}
	return containerOrderByPrior(prior, got, containerMountKey)
}

// ---------------------------------------------------------------------------
// Ulimits

func containerExpandUlimits(in []containerUlimitModel) []*container.Ulimit {
	if len(in) == 0 {
		return nil
	}
	out := make([]*container.Ulimit, 0, len(in))
	for _, u := range in {
		out = append(out, &container.Ulimit{Name: u.Name.ValueString(), Soft: u.Soft.ValueInt64(), Hard: u.Hard.ValueInt64()})
	}
	return out
}

func containerUlimitKey(u containerUlimitModel) string { return u.Name.ValueString() }

// containerFlattenUlimits maps HostConfig.Ulimits back to blocks. Unless
// importing, only names the prior state lists are reported, because the
// daemon may merge its default-ulimits into HostConfig.
func containerFlattenUlimits(in []*container.Ulimit, prior []containerUlimitModel, importing bool) []containerUlimitModel {
	want := map[string]bool{}
	for _, p := range prior {
		want[p.Name.ValueString()] = true
	}
	got := make([]containerUlimitModel, 0, len(in))
	for _, u := range in {
		if u == nil || (!importing && !want[u.Name]) {
			continue
		}
		got = append(got, containerUlimitModel{Name: types.StringValue(u.Name), Soft: types.Int64Value(u.Soft), Hard: types.Int64Value(u.Hard)})
	}
	return containerOrderByPrior(prior, got, containerUlimitKey)
}

// ---------------------------------------------------------------------------
// Healthcheck

// containerExpandHealthcheck builds Config.Healthcheck; nil when h is nil.
func containerExpandHealthcheck(test []string, h *containerHealthcheckModel) (*container.HealthConfig, error) {
	if h == nil {
		return nil, nil
	}
	hc := &container.HealthConfig{Test: test}
	var err error
	for _, d := range []struct {
		name string
		v    types.String
		dst  *time.Duration
	}{
		{"interval", h.Interval, &hc.Interval},
		{"timeout", h.Timeout, &hc.Timeout},
		{"start_period", h.StartPeriod, &hc.StartPeriod},
	} {
		if *d.dst, err = parseDurationOr(d.v.ValueString(), 0); err != nil {
			return nil, fmt.Errorf("healthcheck.%s: %w", d.name, err)
		}
	}
	if r, ok := knownInt64(h.Retries); ok {
		hc.Retries = int(r)
	}
	return hc, nil
}

// containerDurationValue reports d as a duration string, keeping the prior
// spelling ("1m" rather than "1m0s") when it parses to the same value.
func containerDurationValue(prior types.String, d time.Duration) types.String {
	if s, ok := knownString(prior); ok {
		if pd, err := time.ParseDuration(s); err == nil && pd == d {
			return prior
		}
	}
	if d == 0 {
		return types.StringNull()
	}
	return types.StringValue(d.String())
}

func containerHealthEqual(a, b *container.HealthConfig) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return containerStringsEqual(a.Test, b.Test) && a.Interval == b.Interval && a.Timeout == b.Timeout &&
		a.StartPeriod == b.StartPeriod && a.Retries == b.Retries
}

// containerFlattenHealthcheck maps Config.Healthcheck to the block. With no
// prior block, a healthcheck equal to the image's is not reported.
func containerFlattenHealthcheck(got, imageDefault *container.HealthConfig, prior *containerHealthcheckModel) *containerHealthcheckModel {
	if got == nil || len(got.Test) == 0 {
		return nil
	}
	if prior == nil && containerHealthEqual(got, imageDefault) {
		return nil
	}
	p := prior
	if p == nil {
		p = &containerHealthcheckModel{Retries: types.Int64Null()}
	}
	return &containerHealthcheckModel{
		Test:        stringListValue(got.Test, false),
		Interval:    containerDurationValue(p.Interval, got.Interval),
		Timeout:     containerDurationValue(p.Timeout, got.Timeout),
		StartPeriod: containerDurationValue(p.StartPeriod, got.StartPeriod),
		Retries:     containerInt64Read(p.Retries, int64(got.Retries)),
	}
}

// ---------------------------------------------------------------------------
// Capabilities

// containerCapNormalize upper-cases and strips the CAP_ prefix.
func containerCapNormalize(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, strings.TrimPrefix(strings.ToUpper(c), "CAP_"))
	}
	sort.Strings(out)
	return out
}

func containerCapSet(prior types.Set, priorVals, got []string) types.Set {
	if containerKnown(prior) && containerStringsEqual(containerCapNormalize(priorVals), containerCapNormalize(got)) {
		return prior
	}
	return containerSetValue(prior, got)
}

// containerFlattenCapabilities maps CapAdd/CapDrop to the block, keeping the
// prior spelling when the daemon normalised capability names.
func containerFlattenCapabilities(add, drop, priorAdd, priorDrop []string, prior *containerCapabilitiesModel) *containerCapabilitiesModel {
	if prior == nil && len(add) == 0 && len(drop) == 0 {
		return nil
	}
	p := prior
	if p == nil {
		p = &containerCapabilitiesModel{Add: types.SetNull(types.StringType), Drop: types.SetNull(types.StringType)}
	}
	return &containerCapabilitiesModel{
		Add:  containerCapSet(p.Add, priorAdd, add),
		Drop: containerCapSet(p.Drop, priorDrop, drop),
	}
}

// ---------------------------------------------------------------------------
// Networks

// containerEndpointSettings builds the endpoint config for one
// networks_advanced entry.
func containerEndpointSettings(aliases []string, ipv4 string) *network.EndpointSettings {
	ep := &network.EndpointSettings{Aliases: aliases}
	if ipv4 != "" {
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: ipv4}
	}
	return ep
}

// containerNetworkMatches reports whether endpoint key/networkID is what the
// user's ref (name, ID or ID prefix) names.
func containerNetworkMatches(ref, key, networkID string) bool {
	if ref == key || (networkID != "" && ref == networkID) {
		return true
	}
	return len(ref) >= 12 && strings.HasPrefix(networkID, ref)
}

// containerFlattenNetworks maps NetworkSettings.Networks to blocks. The
// default network the daemon attaches when networks_advanced is empty
// (HostConfig.NetworkMode "default"/"bridge"/"host"/"none") is not reported
// unless the prior state lists it. Aliases the daemon adds (container name,
// ID, short ID) are dropped.
func containerFlattenNetworks(eps map[string]*network.EndpointSettings, networkMode, containerID, containerName string, prior []containerNetworkModel) []containerNetworkModel {
	keys := make([]string, 0, len(eps))
	for k := range eps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	used := map[string]bool{}
	out := make([]containerNetworkModel, 0, len(eps))

	flat := func(name types.String, priorAliases types.Set, ep *network.EndpointSettings) containerNetworkModel {
		var aliases []string
		short := containerID
		if len(short) > 12 {
			short = short[:12]
		}
		for _, a := range ep.Aliases {
			if a == containerName || a == containerID || a == short {
				continue
			}
			aliases = append(aliases, a)
		}
		ipv4 := ""
		if ep.IPAMConfig != nil {
			ipv4 = ep.IPAMConfig.IPv4Address
		}
		return containerNetworkModel{Name: name, Aliases: containerSetValue(priorAliases, aliases), IPv4Address: stringOrNull(ipv4)}
	}

	for _, p := range prior {
		ref := p.Name.ValueString()
		for _, k := range keys {
			ep := eps[k]
			if used[k] || ep == nil || !containerNetworkMatches(ref, k, ep.NetworkID) {
				continue
			}
			used[k] = true
			out = append(out, flat(p.Name, p.Aliases, ep))
			break
		}
	}
	for _, k := range keys {
		ep := eps[k]
		if used[k] || ep == nil {
			continue
		}
		if containerIsDefaultNetwork(k, networkMode) {
			continue
		}
		out = append(out, flat(types.StringValue(k), types.SetNull(types.StringType), ep))
	}
	return out
}

// containerIsDefaultNetwork reports whether endpoint key is the built-in
// network the daemon attached because of HostConfig.NetworkMode.
func containerIsDefaultNetwork(key, networkMode string) bool {
	switch key {
	case "bridge":
		return networkMode == "" || networkMode == "default" || networkMode == "bridge"
	case "host", "none":
		return networkMode == key
	}
	return false
}

// ---------------------------------------------------------------------------
// Restart policy

var containerRestartPolicies = []string{"no", "always", "unless-stopped", "on-failure"}

func containerRestartPolicy(name string, maxRetry int64) container.RestartPolicy {
	if name == "" {
		name = "no"
	}
	rp := container.RestartPolicy{Name: container.RestartPolicyMode(name)}
	if name == "on-failure" {
		rp.MaximumRetryCount = int(maxRetry)
	}
	return rp
}
