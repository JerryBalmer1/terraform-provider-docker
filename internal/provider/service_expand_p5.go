package provider

// Pass 5: expand/flatten of the service attributes renamed or added in
// Pass 5 (renames.json): the networks map (TaskTemplate.Networks keyed by
// network), mounts.bind_options / tmpfs_options / volume_options, and the
// gap attributes groups, init, isolation, read_only, stop_signal, runtime,
// privileges, endpoint_spec.ports.name, placement.platforms and
// resources.reservations.generic_resources. Shared by docker_service and
// docker_stack through svcExpand/svcFlatten.

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
)

// ---------------------------------------------------------------------------
// networks map

// svcNetworksFrom decodes the networks map (nil when null or unknown).
func svcNetworksFrom(ctx context.Context, v types.Map, diags *diag.Diagnostics) map[string]svcNetworkAttachModel {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	out := map[string]svcNetworkAttachModel{}
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// svcNetworksValue builds the networks map value. An empty result is null
// unless prior was a known empty map.
func svcNetworksValue(prior types.Map, in map[string]svcNetworkAttachModel) types.Map {
	objType := types.ObjectType{AttrTypes: svcNetworkAttachAttrTypes}
	if len(in) == 0 {
		if svcPriorSet(prior) && len(prior.Elements()) == 0 {
			return types.MapValueMust(objType, map[string]attr.Value{})
		}
		return types.MapNull(objType)
	}
	elems := make(map[string]attr.Value, len(in))
	for k, v := range in {
		elems[k] = types.ObjectValueMust(svcNetworkAttachAttrTypes, map[string]attr.Value{
			"aliases":     v.Aliases,
			"driver_opts": v.DriverOpts,
		})
	}
	return types.MapValueMust(objType, elems)
}

// svcNetworkAttachment is a networks map entry from plain values (tests,
// imports): nil aliases / opts are null.
func svcNetworkAttachment(aliases []string, opts map[string]string) svcNetworkAttachModel {
	return svcNetworkAttachModel{Aliases: stringSetValue(aliases, true), DriverOpts: stringMapValue(opts, true)}
}

// svcExpandNetworks fills TaskTemplate.Networks from the networks map, in
// key order. In a stack the service's short name is always the first alias.
func svcExpandNetworks(ctx context.Context, m *svcSpecModel, lk *svcLookup, o svcExpandOpts, spec *swarm.ServiceSpec, diags *diag.Diagnostics) {
	nets := svcNetworksFrom(ctx, m.Networks, diags)
	keys := make([]string, 0, len(nets))
	for k := range nets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, ref := range keys {
		n := nets[ref]
		id, err := lk.resolve("network", ref)
		if err != nil {
			diags.AddError("Cannot resolve network", err.Error())
			continue
		}
		nac := swarm.NetworkAttachmentConfig{Target: id}
		seen := map[string]bool{}
		if o.Namespace != "" {
			nac.Aliases = append(nac.Aliases, m.Name.ValueString())
			seen[m.Name.ValueString()] = true
		}
		for _, a := range stringSetFrom(ctx, n.Aliases, diags) {
			if !seen[a] {
				nac.Aliases = append(nac.Aliases, a)
				seen[a] = true
			}
		}
		if opts := stringMapFrom(ctx, n.DriverOpts, diags); len(opts) > 0 {
			nac.DriverOpts = opts
		}
		spec.TaskTemplate.Networks = append(spec.TaskTemplate.Networks, nac)
	}
}

// svcFlattenNetworks maps TaskTemplate.Networks onto the networks map,
// keeping the prior key (name or ID) that denotes each attached network.
func svcFlattenNetworks(spec swarm.ServiceSpec, prior svcSpecModel, lk *svcLookup, o svcFlattenOpts, m *svcSpecModel) {
	var d diag.Diagnostics
	priorNets := svcNetworksFrom(context.Background(), prior.Networks, &d)
	priorKeys := make([]string, 0, len(priorNets))
	for k := range priorNets {
		priorKeys = append(priorKeys, k)
	}
	sort.Strings(priorKeys)
	used := map[string]bool{}
	out := map[string]svcNetworkAttachModel{}
	for _, n := range spec.TaskTemplate.Networks {
		key := ""
		for _, k := range priorKeys {
			if !used[k] && (k == n.Target || (lk != nil && lk.matches("network", k, n.Target))) {
				key = k
				break
			}
		}
		p := svcNetworkAttachModel{Aliases: types.SetNull(types.StringType), DriverOpts: types.MapNull(types.StringType)}
		if key != "" {
			used[key] = true
			p = priorNets[key]
		} else {
			key = lk.displayRef("network", n.Target)
		}
		var keep map[string]bool
		if svcPriorSet(p.Aliases) {
			keep = map[string]bool{}
			for _, a := range containerSetStrings(p.Aliases) {
				keep[a] = true
			}
		}
		aliases := svcSetValue(p.Aliases, svcUserAliases(n.Aliases, o, keep))
		if svcPriorSet(p.Aliases) && aliases.Equal(p.Aliases) {
			aliases = p.Aliases // same members; keep the prior element order
		}
		out[key] = svcNetworkAttachModel{Aliases: aliases, DriverOpts: svcMapValue(p.DriverOpts, n.DriverOpts)}
	}
	m.Networks = svcNetworksValue(prior.Networks, out)
}

// svcUserAliases drops the alias a stack adds (the short service name)
// unless keep lists it.
func svcUserAliases(aliases []string, o svcFlattenOpts, keep map[string]bool) []string {
	var out []string
	for _, a := range aliases {
		if o.StripStack && o.ShortName != "" && a == o.ShortName && !keep[a] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ---------------------------------------------------------------------------
// Container / task attributes added in Pass 5

// svcExpandP5 applies groups, init, isolation, read_only, stop_signal,
// runtime and privileges.
func svcExpandP5(ctx context.Context, m *svcSpecModel, spec *swarm.ServiceSpec, diags *diag.Diagnostics) {
	cs := spec.TaskTemplate.ContainerSpec
	cs.Groups = stringListFrom(ctx, m.Groups, diags)
	if v, ok := knownBool(m.Init); ok {
		cs.Init = &v
	}
	cs.Isolation = container.Isolation(m.Isolation.ValueString())
	cs.ReadOnly = m.ReadOnly.ValueBool()
	cs.StopSignal = m.StopSignal.ValueString()
	spec.TaskTemplate.Runtime = swarm.RuntimeType(m.Runtime.ValueString())
	if p := m.Privileges; p != nil {
		pr := &swarm.Privileges{}
		if c := p.CredentialSpec; c != nil {
			pr.CredentialSpec = &swarm.CredentialSpec{Config: c.Config.ValueString(), File: c.File.ValueString(), Registry: c.Registry.ValueString()}
			set := 0
			for _, v := range []string{pr.CredentialSpec.Config, pr.CredentialSpec.File, pr.CredentialSpec.Registry} {
				if v != "" {
					set++
				}
			}
			if set > 1 {
				diags.AddError("Invalid privileges.credential_spec", "set only one of config, file and registry")
			}
		}
		if s := p.SELinuxContext; s != nil {
			pr.SELinuxContext = &swarm.SELinuxContext{Disable: s.Disable.ValueBool(), User: s.User.ValueString(),
				Role: s.Role.ValueString(), Type: s.Type.ValueString(), Level: s.Level.ValueString()}
		}
		if s := p.Seccomp; s != nil {
			pr.Seccomp = &swarm.SeccompOpts{Mode: swarm.SeccompMode(s.Mode.ValueString()), Profile: []byte(s.Profile.ValueString())}
			if len(pr.Seccomp.Profile) == 0 {
				pr.Seccomp.Profile = nil
			}
			if pr.Seccomp.Profile != nil && pr.Seccomp.Mode != swarm.SeccompModeCustom {
				diags.AddError("Invalid privileges.seccomp", `profile is only used with mode = "custom"`)
			}
			if pr.Seccomp.Profile == nil && pr.Seccomp.Mode == swarm.SeccompModeCustom {
				diags.AddError("Invalid privileges.seccomp", `mode = "custom" needs profile`)
			}
		}
		if a := p.AppArmor; a != nil {
			pr.AppArmor = &swarm.AppArmorOpts{Mode: swarm.AppArmorMode(a.Mode.ValueString())}
		}
		pr.NoNewPrivileges = p.NoNewPrivileges.ValueBool()
		cs.Privileges = pr
	}
	// Pass 5.3: tty, open_stdin, oom_score_adj.
	cs.TTY = m.TTY.ValueBool()
	cs.OpenStdin = m.OpenStdin.ValueBool()
	cs.OomScoreAdj = m.OomScoreAdj.ValueInt64()
}

// svcFlattenP5 maps the Pass 5 container/task attributes onto m.
func svcFlattenP5(spec swarm.ServiceSpec, cs *swarm.ContainerSpec, prior svcSpecModel, m *svcSpecModel) {
	m.Groups = svcListValue(prior.Groups, cs.Groups)
	m.Init = types.BoolNull()
	if cs.Init != nil {
		m.Init = svcBoolValue(prior.Init, *cs.Init)
	}
	m.Isolation = svcStrValue(prior.Isolation, string(cs.Isolation), "default")
	m.ReadOnly = svcBoolValue(prior.ReadOnly, cs.ReadOnly)
	m.StopSignal = svcStrValue(prior.StopSignal, cs.StopSignal, "")
	m.Runtime = svcStrValue(prior.Runtime, string(spec.TaskTemplate.Runtime), "container")
	m.TTY = svcBoolValue(prior.TTY, cs.TTY)
	m.OpenStdin = svcBoolValue(prior.OpenStdin, cs.OpenStdin)
	m.OomScoreAdj = svcIntValue(prior.OomScoreAdj, cs.OomScoreAdj)
	m.Privileges = nil
	pr := cs.Privileges
	if pr == nil {
		return
	}
	p := prior.Privileges
	if p == nil {
		p = &svcPrivilegesModel{}
	}
	out := &svcPrivilegesModel{}
	if c := pr.CredentialSpec; c != nil && (p.CredentialSpec != nil || c.Config != "" || c.File != "" || c.Registry != "") {
		pc := p.CredentialSpec
		if pc == nil {
			pc = &svcCredentialSpecModel{}
		}
		out.CredentialSpec = &svcCredentialSpecModel{
			Config: svcStrValue(pc.Config, c.Config, ""), File: svcStrValue(pc.File, c.File, ""), Registry: svcStrValue(pc.Registry, c.Registry, ""),
		}
	}
	if s := pr.SELinuxContext; s != nil && (p.SELinuxContext != nil || s.Disable || s.User != "" || s.Role != "" || s.Type != "" || s.Level != "") {
		ps := p.SELinuxContext
		if ps == nil {
			ps = &svcSELinuxContextModel{}
		}
		out.SELinuxContext = &svcSELinuxContextModel{
			Disable: svcBoolValue(ps.Disable, s.Disable),
			User:    svcStrValue(ps.User, s.User, ""), Role: svcStrValue(ps.Role, s.Role, ""),
			Type: svcStrValue(ps.Type, s.Type, ""), Level: svcStrValue(ps.Level, s.Level, ""),
		}
	}
	if s := pr.Seccomp; s != nil && (p.Seccomp != nil || (s.Mode != "" && s.Mode != swarm.SeccompModeDefault) || len(s.Profile) > 0) {
		ps := p.Seccomp
		if ps == nil {
			ps = &svcSeccompModel{}
		}
		out.Seccomp = &svcSeccompModel{Mode: svcStrValue(ps.Mode, string(s.Mode), string(swarm.SeccompModeDefault)),
			Profile: svcStrValue(ps.Profile, string(s.Profile), "")}
	}
	if a := pr.AppArmor; a != nil && (p.AppArmor != nil || (a.Mode != "" && a.Mode != swarm.AppArmorModeDefault)) {
		pa := p.AppArmor
		if pa == nil {
			pa = &svcAppArmorModel{}
		}
		out.AppArmor = &svcAppArmorModel{Mode: svcStrValue(pa.Mode, string(a.Mode), string(swarm.AppArmorModeDefault))}
	}
	out.NoNewPrivileges = svcBoolValue(p.NoNewPrivileges, pr.NoNewPrivileges)
	if prior.Privileges != nil || out.CredentialSpec != nil || out.SELinuxContext != nil || out.Seccomp != nil || out.AppArmor != nil ||
		pr.NoNewPrivileges {
		m.Privileges = out
	}
}

// svcBoolValue returns v, or null when prior is unset and v is false.
func svcBoolValue(prior types.Bool, v bool) types.Bool {
	if !svcPriorSet(prior) && !v {
		return types.BoolNull()
	}
	return types.BoolValue(v)
}

// ---------------------------------------------------------------------------
// Mounts

// svcExpandMount converts one mounts entry. The option blocks must match
// the mount type (the daemon rejects the others).
func svcExpandMount(ctx context.Context, i int, mm svcMountModel, diags *diag.Diagnostics) mount.Mount {
	mt := mount.Mount{
		Type:     mount.Type(mm.Type.ValueString()),
		Source:   mm.Source.ValueString(),
		Target:   mm.Target.ValueString(),
		ReadOnly: mm.ReadOnly.ValueBool(),
	}
	wrong := func(block, want string) {
		diags.AddError("Invalid mounts entry", fmt.Sprintf("mounts[%d]: %s only applies to type = %q, not %q", i, block, want, mt.Type))
	}
	if b := mm.BindOptions; b != nil {
		if known, ok := knownString(mm.Type); ok && known != string(mount.TypeBind) {
			wrong("bind_options", "bind")
		}
		mt.BindOptions = &mount.BindOptions{Propagation: mount.Propagation(b.Propagation.ValueString()), NonRecursive: b.NonRecursive.ValueBool()}
	}
	if t := mm.TmpfsOptions; t != nil {
		if known, ok := knownString(mm.Type); ok && known != string(mount.TypeTmpfs) {
			wrong("tmpfs_options", "tmpfs")
		}
		mt.TmpfsOptions = &mount.TmpfsOptions{SizeBytes: t.SizeBytes.ValueInt64()}
		if v, ok := knownInt64(t.Mode); ok {
			mt.TmpfsOptions.Mode = os.FileMode(v)
		}
		mt.TmpfsOptions.Options = svcTmpfsOptionsFrom(ctx, i, t.Options, diags)
	}
	if v := mm.VolumeOptions; v != nil {
		if known, ok := knownString(mm.Type); ok && known != string(mount.TypeVolume) {
			wrong("volume_options", "volume")
		}
		vo := &mount.VolumeOptions{NoCopy: v.NoCopy.ValueBool()}
		if lbl := stringMapFrom(ctx, v.Labels, diags); len(lbl) > 0 {
			vo.Labels = lbl
		}
		if dc := v.DriverConfig; dc != nil {
			vo.DriverConfig = &mount.Driver{Name: dc.Name.ValueString()}
			if opts := stringMapFrom(ctx, dc.Options, diags); len(opts) > 0 {
				vo.DriverConfig.Options = opts
			}
		}
		mt.VolumeOptions = vo
	}
	return mt
}

// svcFlattenMount converts one daemon mount, keeping p's spellings. An
// option block is shown when the configuration had it or it carries a
// non-default value.
func svcFlattenMount(mt mount.Mount, p svcMountModel) svcMountModel {
	mm := svcMountModel{
		Type:     types.StringValue(string(mt.Type)),
		Source:   svcStrValue(p.Source, mt.Source, ""),
		Target:   types.StringValue(mt.Target),
		ReadOnly: types.BoolValue(mt.ReadOnly),
	}
	if b := mt.BindOptions; b != nil && (p.BindOptions != nil || (b.Propagation != "" && b.Propagation != mount.PropagationRPrivate) || b.NonRecursive) {
		pb := p.BindOptions
		if pb == nil {
			pb = &svcBindOptionsModel{}
		}
		mm.BindOptions = &svcBindOptionsModel{Propagation: svcStrValue(pb.Propagation, string(b.Propagation), string(mount.PropagationRPrivate)),
			NonRecursive: svcBoolValue(pb.NonRecursive, b.NonRecursive)}
	}
	if t := mt.TmpfsOptions; t != nil && (p.TmpfsOptions != nil || t.SizeBytes != 0 || t.Mode != 0 || len(t.Options) > 0) {
		pt := p.TmpfsOptions
		if pt == nil {
			pt = &svcTmpfsOptionsModel{}
		}
		mm.TmpfsOptions = &svcTmpfsOptionsModel{SizeBytes: svcIntValue(pt.SizeBytes, t.SizeBytes), Mode: svcIntValue(pt.Mode, int64(t.Mode)),
			Options: svcTmpfsOptionsValue(pt.Options, t.Options)}
	}
	if v := mt.VolumeOptions; v != nil && (p.VolumeOptions != nil || v.NoCopy || len(v.Labels) > 0 || v.DriverConfig != nil) {
		pv := p.VolumeOptions
		if pv == nil {
			pv = &svcVolumeOptionsModel{Labels: types.MapNull(types.StringType)}
		}
		vo := &svcVolumeOptionsModel{NoCopy: svcBoolValue(pv.NoCopy, v.NoCopy), Labels: svcMapValue(pv.Labels, v.Labels)}
		if dc := v.DriverConfig; dc != nil && (pv.DriverConfig != nil || dc.Name != "" || len(dc.Options) > 0) {
			pd := pv.DriverConfig
			if pd == nil {
				pd = &svcDriverConfigModel{Options: types.MapNull(types.StringType)}
			}
			vo.DriverConfig = &svcDriverConfigModel{Name: svcStrValue(pd.Name, dc.Name, ""), Options: svcMapValue(pd.Options, dc.Options)}
		}
		mm.VolumeOptions = vo
	}
	return mm
}

// ---------------------------------------------------------------------------
// Reservations

func svcExpandGenericResources(in []svcGenericResourceModel, diags *diag.Diagnostics) []swarm.GenericResource {
	var out []swarm.GenericResource
	for i, g := range in {
		if (g.NamedResourceSpec == nil) == (g.DiscreteResourceSpec == nil) {
			diags.AddError("Invalid generic_resources entry",
				fmt.Sprintf("resources.reservations.generic_resources[%d]: set exactly one of named_resource_spec and discrete_resource_spec", i))
			continue
		}
		var r swarm.GenericResource
		if n := g.NamedResourceSpec; n != nil {
			r.NamedResourceSpec = &swarm.NamedGenericResource{Kind: n.Kind.ValueString(), Value: n.Value.ValueString()}
		}
		if d := g.DiscreteResourceSpec; d != nil {
			r.DiscreteResourceSpec = &swarm.DiscreteGenericResource{Kind: d.Kind.ValueString(), Value: d.Value.ValueInt64()}
		}
		out = append(out, r)
	}
	return out
}

func svcFlattenReservation(p *svcReservationModel, l swarm.Resources) *svcReservationModel {
	if p == nil {
		p = &svcReservationModel{NanoCPUs: types.Int64Null(), MemoryBytes: types.Int64Null()}
	}
	out := &svcReservationModel{NanoCPUs: svcIntValue(p.NanoCPUs, l.NanoCPUs), MemoryBytes: svcIntValue(p.MemoryBytes, l.MemoryBytes)}
	for _, g := range l.GenericResources {
		var r svcGenericResourceModel
		if n := g.NamedResourceSpec; n != nil {
			r.NamedResourceSpec = &svcNamedResourceModel{Kind: types.StringValue(n.Kind), Value: types.StringValue(n.Value)}
		}
		if d := g.DiscreteResourceSpec; d != nil {
			r.DiscreteResourceSpec = &svcDiscreteResourceModel{Kind: types.StringValue(d.Kind), Value: types.Int64Value(d.Value)}
		}
		out.GenericResources = append(out.GenericResources, r)
	}
	return out
}

// svcTmpfsOptionsType is the element type of mounts.tmpfs_options.options.
var svcTmpfsOptionsType = types.ListType{ElemType: types.StringType}

// svcTmpfsOptionsFrom decodes mounts[i].tmpfs_options.options: each entry is
// a flag ([name]) or a key/value pair ([name, value]).
func svcTmpfsOptionsFrom(ctx context.Context, i int, v types.List, diags *diag.Diagnostics) [][]string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var rows []types.List
	diags.Append(v.ElementsAs(ctx, &rows, false)...)
	var out [][]string
	for j, r := range rows {
		e := stringListFrom(ctx, r, diags)
		if len(e) != 1 && len(e) != 2 {
			diags.AddError("Invalid mounts entry", fmt.Sprintf("mounts[%d].tmpfs_options.options[%d]: want [flag] or [key, value], got %d elements", i, j, len(e)))
			continue
		}
		out = append(out, e)
	}
	return out
}

// svcTmpfsOptionsValue is the daemon's tmpfs options as a list of lists
// (null when empty and unset before).
func svcTmpfsOptionsValue(prior types.List, in [][]string) types.List {
	if len(in) == 0 {
		if svcPriorSet(prior) && len(prior.Elements()) == 0 {
			return types.ListValueMust(svcTmpfsOptionsType, []attr.Value{})
		}
		return types.ListNull(svcTmpfsOptionsType)
	}
	rows := make([]attr.Value, 0, len(in))
	for _, r := range in {
		rows = append(rows, stringListValue(r, false))
	}
	return types.ListValueMust(svcTmpfsOptionsType, rows)
}
