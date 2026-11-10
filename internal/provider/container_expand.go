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
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"net/netip"
)

// containerPortModel is one ports block (PortBindings: the PortMap key's
// port and protocol, PortBinding.HostIp / HostPort).
type containerPortModel struct {
	ContainerPort types.Int64  `tfsdk:"container_port"`
	HostPort      types.Int64  `tfsdk:"host_port"`
	HostIP        types.String `tfsdk:"host_ip"`
	Protocol      types.String `tfsdk:"protocol"`
}

type containerMountModel struct {
	Type     types.String `tfsdk:"type"`
	Source   types.String `tfsdk:"source"`
	Target   types.String `tfsdk:"target"`
	ReadOnly types.Bool   `tfsdk:"read_only"`
	// SelinuxRelabel ("z" or "Z") sends the entry as a HostConfig.Binds
	// string instead of a mount.Mount (container_upload.go).
	SelinuxRelabel types.String           `tfsdk:"selinux_relabel"`
	Consistency    types.String           `tfsdk:"consistency"`
	TmpfsOptions   *ctrTmpfsOptionsModel  `tfsdk:"tmpfs_options"`
	BindOptions    *ctrBindOptionsModel   `tfsdk:"bind_options"`
	VolumeOptions  *ctrVolumeOptionsModel `tfsdk:"volume_options"`
	ImageOptions   *ctrImageOptionsModel  `tfsdk:"image_options"`
}

// ctrTmpfsOptionsModel is mounts.tmpfs_options (mount.TmpfsOptions).
type ctrTmpfsOptionsModel struct {
	SizeBytes types.Int64  `tfsdk:"size_bytes"`
	Mode      types.String `tfsdk:"mode"`
	Options   types.List   `tfsdk:"options"`
}

// ctrBindOptionsModel is mounts.bind_options (mount.BindOptions).
type ctrBindOptionsModel struct {
	Propagation            types.String `tfsdk:"propagation"`
	NonRecursive           types.Bool   `tfsdk:"non_recursive"`
	CreateMountpoint       types.Bool   `tfsdk:"create_mountpoint"`
	ReadOnlyNonRecursive   types.Bool   `tfsdk:"read_only_non_recursive"`
	ReadOnlyForceRecursive types.Bool   `tfsdk:"read_only_force_recursive"`
}

// ctrImageOptionsModel is mounts.image_options (mount.ImageOptions).
type ctrImageOptionsModel struct {
	Subpath types.String `tfsdk:"subpath"`
}

// ctrVolumeOptionsModel is mounts.volume_options (mount.VolumeOptions).
type ctrVolumeOptionsModel struct {
	NoCopy       types.Bool            `tfsdk:"no_copy"`
	Labels       types.Map             `tfsdk:"labels"`
	Subpath      types.String          `tfsdk:"subpath"`
	DriverConfig *ctrDriverConfigModel `tfsdk:"driver_config"`
}

// ctrDriverConfigModel is mounts.volume_options.driver_config (mount.Driver).
type ctrDriverConfigModel struct {
	Name    types.String `tfsdk:"name"`
	Options types.Map    `tfsdk:"options"`
}

type containerHealthcheckModel struct {
	Test          types.List   `tfsdk:"test"`
	Interval      types.String `tfsdk:"interval"`
	Timeout       types.String `tfsdk:"timeout"`
	StartPeriod   types.String `tfsdk:"start_period"`
	StartInterval types.String `tfsdk:"start_interval"`
	Retries       types.Int64  `tfsdk:"retries"`
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
func containerExpandPorts(ports []containerPortModel) (network.PortSet, network.PortMap, error) {
	if len(ports) == 0 {
		return nil, nil, nil
	}
	set := network.PortSet{}
	pm := network.PortMap{}
	for i, p := range ports {
		internal, ok := knownInt64(p.ContainerPort)
		if !ok || internal < 1 || internal > 65535 {
			return nil, nil, fmt.Errorf("ports[%d].container_port must be 1-65535", i)
		}
		port, ok := network.PortFrom(uint16(internal), network.IPProtocol(containerPortProtocol(p)))
		if !ok {
			return nil, nil, fmt.Errorf("ports[%d]: invalid port %d/%s", i, internal, containerPortProtocol(p))
		}
		set[port] = struct{}{}
		b := network.PortBinding{}
		if ext, ok := knownInt64(p.HostPort); ok && ext > 0 {
			b.HostPort = strconv.FormatInt(ext, 10)
		}
		if ip, ok := knownString(p.HostIP); ok && ip != "" {
			addr, err := netip.ParseAddr(ip)
			if err != nil {
				return nil, nil, fmt.Errorf("ports[%d].host_ip: %q is not an IP address", i, ip)
			}
			b.HostIP = addr
		}
		pm[port] = append(pm[port], b)
	}
	return set, pm, nil
}

func containerPortKey(p containerPortModel) string {
	return fmt.Sprintf("%05d/%s/%s/%d", p.ContainerPort.ValueInt64(), containerPortProtocol(p), p.HostIP.ValueString(), p.HostPort.ValueInt64())
}

// containerFlattenPorts maps HostConfig.PortBindings back to blocks, ordered
// like prior.
func containerFlattenPorts(pm network.PortMap, prior []containerPortModel) []containerPortModel {
	var got []containerPortModel
	for port, bindings := range pm {
		if len(bindings) == 0 {
			bindings = []network.PortBinding{{}}
		}
		for _, b := range bindings {
			m := containerPortModel{
				ContainerPort: types.Int64Value(int64(port.Num())),
				HostPort:      types.Int64Null(),
				HostIP:        stringOrNull(addrString(b.HostIP)),
				Protocol:      types.StringValue(string(port.Proto())),
			}
			if b.HostPort != "" {
				if n, err := strconv.ParseInt(b.HostPort, 10, 64); err == nil {
					m.HostPort = types.Int64Value(n)
				}
			}
			got = append(got, m)
		}
	}
	return containerOrderByPrior(prior, got, containerPortKey)
}

// ---------------------------------------------------------------------------
// Mounts

// containerExpandMounts builds HostConfig.Mounts. Entries with
// selinux_relabel are validated but left out; containerExpandMountsBinds
// returns them as HostConfig.Binds.
func containerExpandMounts(in []containerMountModel) ([]mount.Mount, error) {
	out, _, err := containerExpandMountsBinds(in)
	return out, err
}

// containerExpandMountsBinds builds HostConfig.Mounts and, for entries with
// selinux_relabel, HostConfig.Binds.
func containerExpandMountsBinds(in []containerMountModel) ([]mount.Mount, []string, error) {
	if len(in) == 0 {
		return nil, nil, nil
	}
	out := make([]mount.Mount, 0, len(in))
	var binds []string
	for i, m := range in {
		t := m.Type.ValueString()
		mm := mount.Mount{
			Type:        mount.Type(t),
			Source:      m.Source.ValueString(),
			Target:      m.Target.ValueString(),
			ReadOnly:    m.ReadOnly.ValueBool(),
			Consistency: mount.Consistency(m.Consistency.ValueString()),
		}
		if mm.Target == "" {
			return nil, nil, fmt.Errorf("mounts[%d].target is required", i)
		}
		switch mm.Type {
		case mount.TypeBind:
			if mm.Source == "" {
				return nil, nil, fmt.Errorf("mounts[%d]: a bind mount needs source (host path)", i)
			}
		case mount.TypeVolume:
		case mount.TypeTmpfs:
			if mm.Source != "" {
				return nil, nil, fmt.Errorf("mounts[%d]: a tmpfs mount takes no source", i)
			}
		case mount.TypeImage:
			if mm.Source == "" {
				return nil, nil, fmt.Errorf("mounts[%d]: an image mount needs source (image reference)", i)
			}
		default:
			return nil, nil, fmt.Errorf("mounts[%d].type %q: must be bind, volume, tmpfs or image", i, t)
		}
		if o := m.TmpfsOptions; o != nil {
			if mm.Type != mount.TypeTmpfs {
				return nil, nil, fmt.Errorf("mounts[%d].tmpfs_options only applies to type tmpfs", i)
			}
			to := &mount.TmpfsOptions{}
			if sz, ok := knownInt64(o.SizeBytes); ok {
				to.SizeBytes = sz
			}
			if ms, ok := knownString(o.Mode); ok && ms != "" {
				mode, err := ctrParseMode(ms)
				if err != nil {
					return nil, nil, fmt.Errorf("mounts[%d].tmpfs_options.mode: %w", i, err)
				}
				to.Mode = ctrFileMode(mode)
			}
			if containerKnown(o.Options) {
				var opts [][]string
				var d diag.Diagnostics
				d.Append(o.Options.ElementsAs(context.Background(), &opts, false)...)
				if d.HasError() {
					return nil, nil, fmt.Errorf("mounts[%d].tmpfs_options.options: %v", i, d)
				}
				for j, kv := range opts {
					if len(kv) < 1 || len(kv) > 2 {
						return nil, nil, fmt.Errorf("mounts[%d].tmpfs_options.options[%d]: want [flag] or [key, value]", i, j)
					}
				}
				to.Options = opts
			}
			if to.SizeBytes != 0 || to.Mode != 0 || len(to.Options) > 0 {
				mm.TmpfsOptions = to
			}
		}
		if o := m.BindOptions; o != nil {
			if mm.Type != mount.TypeBind {
				return nil, nil, fmt.Errorf("mounts[%d].bind_options only applies to type bind", i)
			}
			bo := &mount.BindOptions{
				Propagation:            mount.Propagation(o.Propagation.ValueString()),
				NonRecursive:           o.NonRecursive.ValueBool(),
				CreateMountpoint:       o.CreateMountpoint.ValueBool(),
				ReadOnlyNonRecursive:   o.ReadOnlyNonRecursive.ValueBool(),
				ReadOnlyForceRecursive: o.ReadOnlyForceRecursive.ValueBool(),
			}
			if *bo != (mount.BindOptions{}) {
				mm.BindOptions = bo
			}
		}
		if o := m.VolumeOptions; o != nil {
			if mm.Type != mount.TypeVolume {
				return nil, nil, fmt.Errorf("mounts[%d].volume_options only applies to type volume", i)
			}
			vo := &mount.VolumeOptions{NoCopy: o.NoCopy.ValueBool(), Subpath: o.Subpath.ValueString(), Labels: ctrStringMap(o.Labels)}
			if dc := o.DriverConfig; dc != nil && dc.Name.ValueString() != "" {
				vo.DriverConfig = &mount.Driver{Name: dc.Name.ValueString(), Options: ctrStringMap(dc.Options)}
			}
			mm.VolumeOptions = vo
		}
		if o := m.ImageOptions; o != nil {
			if mm.Type != mount.TypeImage {
				return nil, nil, fmt.Errorf("mounts[%d].image_options only applies to type image", i)
			}
			if sp := o.Subpath.ValueString(); sp != "" {
				mm.ImageOptions = &mount.ImageOptions{Subpath: sp}
			}
		}
		if rl, ok := knownString(m.SelinuxRelabel); ok && rl != "" {
			if mm.TmpfsOptions != nil || mm.BindOptions != nil || mm.VolumeOptions != nil || mm.ImageOptions != nil || mm.Consistency != "" {
				return nil, nil, fmt.Errorf("mounts[%d]: selinux_relabel cannot be combined with consistency, tmpfs_options, bind_options, volume_options or image_options", i)
			}
			spec, err := containerBindSpec(i, m, rl)
			if err != nil {
				return nil, nil, err
			}
			binds = append(binds, spec)
			continue
		}
		out = append(out, mm)
	}
	return out, binds, nil
}

func containerMountKey(m containerMountModel) string {
	return m.Target.ValueString() + "|" + m.Type.ValueString() + "|" + m.Source.ValueString()
}

// containerFlattenMounts maps HostConfig.Mounts and HostConfig.Binds back to
// blocks. An option block is reported when the daemon holds a value for it,
// or when the prior entry with the same key had the block (so an empty block
// the user wrote does not show as drift).
func containerFlattenMounts(in []mount.Mount, binds []string, prior []containerMountModel) []containerMountModel {
	priorBy := map[string]containerMountModel{}
	for _, p := range prior {
		priorBy[containerMountKey(p)] = p
	}
	got := make([]containerMountModel, 0, len(in)+len(binds))
	for _, m := range in {
		mm := containerMountModel{
			Type:     types.StringValue(string(m.Type)),
			Source:   stringOrNull(m.Source),
			Target:   types.StringValue(m.Target),
			ReadOnly: types.BoolValue(m.ReadOnly),

			SelinuxRelabel: types.StringNull(),
		}
		p := priorBy[containerMountKey(mm)]
		mm.Consistency = containerStringRead(p.Consistency, string(m.Consistency), "")
		mm.TmpfsOptions = ctrFlattenTmpfsOptions(m.TmpfsOptions, p.TmpfsOptions)
		mm.BindOptions = ctrFlattenBindOptions(m.BindOptions, p.BindOptions)
		mm.VolumeOptions = ctrFlattenVolumeOptions(m.VolumeOptions, p.VolumeOptions)
		if o := m.ImageOptions; (o != nil && o.Subpath != "") || p.ImageOptions != nil {
			io := &ctrImageOptionsModel{Subpath: types.StringNull()}
			if o != nil {
				io.Subpath = stringOrNull(o.Subpath)
			}
			mm.ImageOptions = io
		}
		got = append(got, mm)
	}
	got = append(got, containerFlattenBinds(binds)...)
	return containerOrderByPrior(prior, got, containerMountKey)
}

func ctrFlattenTmpfsOptions(o *mount.TmpfsOptions, prior *ctrTmpfsOptionsModel) *ctrTmpfsOptionsModel {
	if (o == nil || (o.SizeBytes == 0 && o.Mode == 0 && len(o.Options) == 0)) && prior == nil {
		return nil
	}
	if o == nil {
		o = &mount.TmpfsOptions{}
	}
	p := prior
	if p == nil {
		p = &ctrTmpfsOptionsModel{SizeBytes: types.Int64Null(), Mode: types.StringNull(), Options: types.ListNull(ctrTmpfsOptionsType)}
	}
	return &ctrTmpfsOptionsModel{
		SizeBytes: containerInt64Read(p.SizeBytes, o.SizeBytes),
		Mode:      ctrModeValue(p.Mode, ctrModeBits(o.Mode)),
		Options:   ctrTmpfsOptionsValue(p.Options, o.Options),
	}
}

// ctrTmpfsOptionsType is the type of tmpfs_options.options.
var ctrTmpfsOptionsType = types.ListType{ElemType: types.StringType}

// ctrTmpfsOptionsValue maps TmpfsOptions.Options; empty stays null unless
// the prior was a known empty list.
func ctrTmpfsOptionsValue(prior types.List, opts [][]string) types.List {
	if len(opts) == 0 {
		if containerKnown(prior) && len(prior.Elements()) == 0 {
			return prior
		}
		return types.ListNull(ctrTmpfsOptionsType)
	}
	vals := make([]attr.Value, 0, len(opts))
	for _, kv := range opts {
		vals = append(vals, stringListValue(kv, false))
	}
	return types.ListValueMust(ctrTmpfsOptionsType, vals)
}

// ctrOptBoolRead reports a daemon bool option: false stays null when the
// prior did not set it.
func ctrOptBoolRead(prior types.Bool, v bool) types.Bool {
	if !v && !containerKnown(prior) {
		return types.BoolNull()
	}
	return types.BoolValue(v)
}

// ctrFlattenBindOptions maps BindOptions onto bind_options. The block is
// reported when the daemon holds a value or the prior entry had it.
func ctrFlattenBindOptions(o *mount.BindOptions, prior *ctrBindOptionsModel) *ctrBindOptionsModel {
	if (o == nil || *o == (mount.BindOptions{})) && prior == nil {
		return nil
	}
	if o == nil {
		o = &mount.BindOptions{}
	}
	p := prior
	if p == nil {
		p = &ctrBindOptionsModel{}
	}
	return &ctrBindOptionsModel{
		Propagation:            stringOrNull(string(o.Propagation)),
		NonRecursive:           ctrOptBoolRead(p.NonRecursive, o.NonRecursive),
		CreateMountpoint:       ctrOptBoolRead(p.CreateMountpoint, o.CreateMountpoint),
		ReadOnlyNonRecursive:   ctrOptBoolRead(p.ReadOnlyNonRecursive, o.ReadOnlyNonRecursive),
		ReadOnlyForceRecursive: ctrOptBoolRead(p.ReadOnlyForceRecursive, o.ReadOnlyForceRecursive),
	}
}

func ctrFlattenVolumeOptions(o *mount.VolumeOptions, prior *ctrVolumeOptionsModel) *ctrVolumeOptionsModel {
	if o == nil && prior == nil {
		return nil
	}
	if o == nil {
		o = &mount.VolumeOptions{}
	}
	p := prior
	if p == nil {
		p = &ctrVolumeOptionsModel{NoCopy: types.BoolNull(), Labels: types.MapNull(types.StringType), Subpath: types.StringNull()}
	}
	vo := &ctrVolumeOptionsModel{
		NoCopy:  types.BoolValue(o.NoCopy),
		Labels:  containerMapValue(p.Labels, o.Labels),
		Subpath: stringOrNull(o.Subpath),
	}
	if !o.NoCopy && !containerKnown(p.NoCopy) {
		vo.NoCopy = types.BoolNull()
	}
	if o.DriverConfig != nil || p.DriverConfig != nil {
		dc := &ctrDriverConfigModel{Name: types.StringNull(), Options: types.MapNull(types.StringType)}
		var pdc ctrDriverConfigModel
		if p.DriverConfig != nil {
			pdc = *p.DriverConfig
		}
		if o.DriverConfig != nil {
			dc.Name = stringOrNull(o.DriverConfig.Name)
			dc.Options = containerMapValue(pdc.Options, o.DriverConfig.Options)
		}
		vo.DriverConfig = dc
	}
	return vo
}

// ctrParseMode parses an octal file mode such as "0644" or "1777".
func ctrParseMode(s string) (uint32, error) {
	m, err := strconv.ParseUint(s, 8, 32)
	if err != nil || m > 0o7777 {
		return 0, fmt.Errorf("%q: want an octal mode such as 0644", s)
	}
	return uint32(m), nil
}

// ctrFileMode turns octal permission bits (with setuid/setgid/sticky in
// 0o7000) into an os.FileMode.
func ctrFileMode(mode uint32) os.FileMode {
	fm := os.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		fm |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		fm |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		fm |= os.ModeSticky
	}
	return fm
}

// ctrModeBits is the inverse of ctrFileMode.
func ctrModeBits(fm os.FileMode) uint32 {
	m := uint32(fm.Perm())
	if fm&os.ModeSetuid != 0 {
		m |= 0o4000
	}
	if fm&os.ModeSetgid != 0 {
		m |= 0o2000
	}
	if fm&os.ModeSticky != 0 {
		m |= 0o1000
	}
	return m
}

// ctrModeValue renders mode as a 4-digit octal string, keeping the prior
// spelling when it parses to the same value. 0 gives null unless the prior
// was set.
func ctrModeValue(prior types.String, mode uint32) types.String {
	if s, ok := knownString(prior); ok {
		if pm, err := ctrParseMode(s); err == nil && pm == mode {
			return prior
		}
	}
	if mode == 0 && !containerKnown(prior) {
		return types.StringNull()
	}
	return types.StringValue(fmt.Sprintf("%04o", mode))
}

// ctrStringMap converts a known map(string) attribute to a Go map (nil when
// null, unknown or empty).
func ctrStringMap(m types.Map) map[string]string {
	if !containerKnown(m) || len(m.Elements()) == 0 {
		return nil
	}
	out := make(map[string]string, len(m.Elements()))
	for k, v := range m.Elements() {
		if s, ok := v.(types.String); ok {
			out[k] = s.ValueString()
		}
	}
	return out
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
		{"start_interval", h.StartInterval, &hc.StartInterval},
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
		a.StartPeriod == b.StartPeriod && a.StartInterval == b.StartInterval && a.Retries == b.Retries
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
		p = &containerHealthcheckModel{Retries: types.Int64Null(), StartInterval: types.StringNull()}
	}
	return &containerHealthcheckModel{
		Test:          stringListValue(got.Test, false),
		Interval:      containerDurationValue(p.Interval, got.Interval),
		Timeout:       containerDurationValue(p.Timeout, got.Timeout),
		StartPeriod:   containerDurationValue(p.StartPeriod, got.StartPeriod),
		StartInterval: containerDurationValue(p.StartInterval, got.StartInterval),
		Retries:       containerInt64Read(p.Retries, int64(got.Retries)),
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

// ---------------------------------------------------------------------------
// Networks

// containerEndpointSettings builds an endpoint config from aliases and
// static addresses (docker_compose local mode uses it too).
func containerEndpointSettings(aliases []string, ipv4, ipv6 string) (*network.EndpointSettings, error) {
	ep := &network.EndpointSettings{Aliases: aliases}
	if ipv4 != "" || ipv6 != "" {
		a4, err := parseOptAddr("ipv4_address", ipv4)
		if err != nil {
			return nil, err
		}
		a6, err := parseOptAddr("ipv6_address", ipv6)
		if err != nil {
			return nil, err
		}
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: a4, IPv6Address: a6}
	}
	return ep, nil
}

// containerNetworkMatches reports whether endpoint key/networkID is what the
// user's ref (name, ID or ID prefix) names.
func containerNetworkMatches(ref, key, networkID string) bool {
	if ref == key || (networkID != "" && ref == networkID) {
		return true
	}
	return len(ref) >= 12 && strings.HasPrefix(networkID, ref)
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
