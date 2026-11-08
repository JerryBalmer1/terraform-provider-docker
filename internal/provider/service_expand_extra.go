package provider

// Pass 2: expand/flatten of the service attributes added in Pass 2 (job
// modes, hostname, user, dir, hosts, dns, cap_add/cap_drop, sysctls, ulimits,
// stop_grace_period, force_update, networks_advanced). Shared by
// docker_service and docker_stack through svcExpand/svcFlatten.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/swarm"
)

// svcModeOf names the mode of a spec the way the mode attribute does.
func svcModeOf(spec swarm.ServiceSpec) string {
	switch {
	case spec.Mode.Global != nil:
		return "global"
	case spec.Mode.ReplicatedJob != nil:
		return "replicated_job"
	case spec.Mode.GlobalJob != nil:
		return "global_job"
	default:
		return "replicated"
	}
}

// svcIsJobMode reports whether mode runs tasks to completion.
func svcIsJobMode(mode string) bool { return mode == "replicated_job" || mode == "global_job" }

// svcExpandMode fills spec.Mode from m and validates the mode-specific
// attributes.
func svcExpandMode(m *svcSpecModel, spec *swarm.ServiceSpec, diags *diag.Diagnostics) {
	mode := m.Mode.ValueString()
	if mode != "replicated_job" {
		if containerKnown(m.MaxConcurrent) || containerKnown(m.TotalCompletions) {
			diags.AddError("Invalid job settings", "max_concurrent and total_completions only apply to mode = \"replicated_job\".")
		}
	}
	if svcIsJobMode(mode) && (m.UpdateConfig != nil || m.RollbackConfig != nil) {
		diags.AddError("Invalid job settings", "update_config and rollback_config are not supported for job modes (replicated_job, global_job).")
	}
	switch mode {
	case "global":
		spec.Mode.Global = &swarm.GlobalService{}
	case "global_job":
		spec.Mode.GlobalJob = &swarm.GlobalJob{}
	case "replicated_job":
		j := &swarm.ReplicatedJob{}
		if v, ok := knownInt64(m.MaxConcurrent); ok {
			if v < 1 {
				diags.AddError("Invalid max_concurrent", "max_concurrent must be >= 1")
			}
			u := uint64(v)
			j.MaxConcurrent = &u
		}
		if v, ok := knownInt64(m.TotalCompletions); ok {
			if v < 1 {
				diags.AddError("Invalid total_completions", "total_completions must be >= 1")
			}
			u := uint64(v)
			j.TotalCompletions = &u
		}
		spec.Mode.ReplicatedJob = j
	default:
		n := uint64(1)
		if v, ok := knownInt64(m.Replicas); ok {
			if v < 0 {
				diags.AddError("Invalid replicas", "replicas must be >= 0")
			}
			n = uint64(v)
		}
		spec.Mode.Replicated = &swarm.ReplicatedService{Replicas: &n}
	}
}

// svcFlattenMode maps spec.Mode onto m (mode, replicas, max_concurrent,
// total_completions). The daemon always returns MaxConcurrent and
// TotalCompletions for replicated jobs; they stay null when unset in prior
// and equal to the daemon defaults (1, and MaxConcurrent).
func svcFlattenMode(spec swarm.ServiceSpec, prior svcSpecModel, m *svcSpecModel) {
	m.Mode = types.StringValue(svcModeOf(spec))
	m.MaxConcurrent, m.TotalCompletions = types.Int64Null(), types.Int64Null()
	if spec.Mode.Replicated != nil {
		n := int64(1)
		if spec.Mode.Replicated.Replicas != nil {
			n = int64(*spec.Mode.Replicated.Replicas)
		}
		m.Replicas = types.Int64Value(n)
		return
	}
	if spec.Mode.Global == nil && spec.Mode.ReplicatedJob == nil && spec.Mode.GlobalJob == nil {
		m.Replicas = types.Int64Value(1)
		return
	}
	m.Replicas = prior.Replicas
	if !svcPriorSet(m.Replicas) {
		m.Replicas = types.Int64Value(1)
	}
	if j := spec.Mode.ReplicatedJob; j != nil {
		mc := int64(1)
		if j.MaxConcurrent != nil {
			mc = int64(*j.MaxConcurrent)
		}
		tc := mc
		if j.TotalCompletions != nil {
			tc = int64(*j.TotalCompletions)
		}
		if svcPriorSet(prior.MaxConcurrent) || mc != 1 {
			m.MaxConcurrent = types.Int64Value(mc)
		}
		if svcPriorSet(prior.TotalCompletions) || tc != mc {
			m.TotalCompletions = types.Int64Value(tc)
		}
	}
}

// svcHostToSwarm converts "host:ip" to the daemon's "ip host" format. An
// entry containing whitespace is taken to be in daemon format already.
func svcHostToSwarm(h string) (string, error) {
	if strings.ContainsAny(h, " \t") {
		return strings.Join(strings.Fields(h), " "), nil
	}
	host, ip, ok := strings.Cut(h, ":")
	if !ok || host == "" || ip == "" {
		return "", fmt.Errorf("hosts entry %q: want \"<hostname>:<ip>\"", h)
	}
	return ip + " " + host, nil
}

// svcHostsFromSwarm converts daemon "ip host [alias...]" entries back to
// "host:ip" (one per hostname), keeping prior spellings that expand to the
// same daemon entry.
func svcHostsFromSwarm(prior types.List, in []string) types.List {
	var priorVals []string
	if svcPriorSet(prior) {
		var d diag.Diagnostics
		priorVals = stringListFrom(context.Background(), prior, &d)
	}
	var out []string
	for i, e := range in {
		if i < len(priorVals) {
			if s, err := svcHostToSwarm(priorVals[i]); err == nil && s == strings.Join(strings.Fields(e), " ") {
				out = append(out, priorVals[i])
				continue
			}
		}
		f := strings.Fields(e)
		if len(f) < 2 {
			out = append(out, e)
			continue
		}
		for _, h := range f[1:] {
			out = append(out, h+":"+f[0])
		}
	}
	return svcListValue(prior, out)
}

// svcExpandP2 applies the Pass 2 container/task attributes to spec.
func svcExpandP2(ctx context.Context, m *svcSpecModel, spec *swarm.ServiceSpec, diags *diag.Diagnostics) {
	cs := spec.TaskTemplate.ContainerSpec
	cs.Hostname = m.Hostname.ValueString()
	cs.User = m.User.ValueString()
	cs.Dir = m.Dir.ValueString()
	for _, h := range stringListFrom(ctx, m.Hosts, diags) {
		s, err := svcHostToSwarm(h)
		if err != nil {
			diags.AddError("Invalid hosts entry", err.Error())
			continue
		}
		cs.Hosts = append(cs.Hosts, s)
	}
	if d := m.DNS; d != nil {
		ns, err := parseAddrs("dns.nameservers", stringListFrom(ctx, d.Nameservers, diags))
		if err != nil {
			diags.AddError("Invalid dns.nameservers", err.Error())
		}
		cs.DNSConfig = &swarm.DNSConfig{
			Nameservers: ns,
			Search:      stringListFrom(ctx, d.Search, diags),
			Options:     stringListFrom(ctx, d.Options, diags),
		}
	}
	cs.CapabilityAdd = stringSetFrom(ctx, m.CapAdd, diags)
	cs.CapabilityDrop = stringSetFrom(ctx, m.CapDrop, diags)
	if sc := stringMapFrom(ctx, m.Sysctls, diags); len(sc) > 0 {
		cs.Sysctls = sc
	}
	for _, u := range m.Ulimits {
		cs.Ulimits = append(cs.Ulimits, &container.Ulimit{Name: u.Name.ValueString(), Soft: u.Soft.ValueInt64(), Hard: u.Hard.ValueInt64()})
	}
	cs.StopGracePeriod = svcDurPtr(m.StopGracePeriod, "stop_grace_period", diags)
	if v, ok := knownInt64(m.ForceUpdate); ok {
		if v < 0 {
			diags.AddError("Invalid force_update", "force_update must be >= 0")
		}
		spec.TaskTemplate.ForceUpdate = uint64(v)
	}
}

// svcExpandNetworksAdvanced appends networks_advanced attachments. In a
// stack the service's short name is always added as an alias (like the
// plain networks attribute).
func svcExpandNetworksAdvanced(ctx context.Context, m *svcSpecModel, lk *svcLookup, o svcExpandOpts, spec *swarm.ServiceSpec, diags *diag.Diagnostics) {
	if len(m.NetworksAdvanced) > 0 && len(m.Networks.Elements()) > 0 {
		diags.AddError("Conflicting network settings", "Use either networks or networks_advanced, not both.")
		return
	}
	for _, n := range m.NetworksAdvanced {
		id, err := lk.resolve("network", n.Name.ValueString())
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
		spec.TaskTemplate.Networks = append(spec.TaskTemplate.Networks, nac)
	}
}

// svcFlattenNetworks maps TaskTemplate.Networks onto networks or
// networks_advanced: attachments go to networks_advanced when the prior used
// it, or (with no prior network config, e.g. import) when an attachment has
// user aliases; otherwise to networks.
func svcFlattenNetworks(spec swarm.ServiceSpec, prior svcSpecModel, lk *svcLookup, o svcFlattenOpts, m *svcSpecModel) {
	priorPlain := svcPriorSet(prior.Networks) && len(prior.Networks.Elements()) > 0
	useAdv := len(prior.NetworksAdvanced) > 0
	if !useAdv && !priorPlain {
		for _, n := range spec.TaskTemplate.Networks {
			if len(svcUserAliases(n.Aliases, o, nil)) > 0 {
				useAdv = true
				break
			}
		}
	}
	if !useAdv {
		svcFlattenPlainNetworks(spec, prior, lk, m)
		m.NetworksAdvanced = nil
		return
	}
	m.Networks = svcSetValue(prior.Networks, nil)
	used := make([]bool, len(spec.TaskTemplate.Networks))
	flat := func(p svcNetworkAttachModel, ref string, n swarm.NetworkAttachmentConfig) svcNetworkAttachModel {
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
		return svcNetworkAttachModel{Name: types.StringValue(ref), Aliases: aliases}
	}
	var out []svcNetworkAttachModel
	for _, p := range prior.NetworksAdvanced {
		ref := p.Name.ValueString()
		for i, n := range spec.TaskTemplate.Networks {
			if used[i] || !(ref == n.Target || (lk != nil && lk.matches("network", ref, n.Target))) {
				continue
			}
			used[i] = true
			out = append(out, flat(p, ref, n))
			break
		}
	}
	for i, n := range spec.TaskTemplate.Networks {
		if !used[i] {
			out = append(out, flat(svcNetworkAttachModel{Aliases: types.SetNull(types.StringType)}, lk.displayRef("network", n.Target), n))
		}
	}
	m.NetworksAdvanced = out
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

// svcFlattenPlainNetworks is the networks set: keep the prior reference that
// denotes each attached ID.
func svcFlattenPlainNetworks(spec swarm.ServiceSpec, prior svcSpecModel, lk *svcLookup, m *svcSpecModel) {
	var priorNets []string
	if svcPriorSet(prior.Networks) {
		var d diag.Diagnostics
		priorNets = stringSetFrom(context.Background(), prior.Networks, &d)
	}
	var nets []string
	for _, n := range spec.TaskTemplate.Networks {
		ref := lk.displayRef("network", n.Target)
		for _, p := range priorNets {
			if p == n.Target || (lk != nil && lk.matches("network", p, n.Target)) {
				ref = p
				break
			}
		}
		nets = append(nets, ref)
	}
	m.Networks = svcSetValue(prior.Networks, nets)
}

// svcFlattenP2 maps the Pass 2 container/task attributes onto m.
func svcFlattenP2(spec swarm.ServiceSpec, cs *swarm.ContainerSpec, prior svcSpecModel, m *svcSpecModel) {
	m.Hostname = svcStrValue(prior.Hostname, cs.Hostname, "")
	m.User = svcStrValue(prior.User, cs.User, "")
	m.Dir = svcStrValue(prior.Dir, cs.Dir, "")
	m.Hosts = svcHostsFromSwarm(prior.Hosts, cs.Hosts)
	if d := cs.DNSConfig; d != nil && (prior.DNS != nil || len(d.Nameservers)+len(d.Search)+len(d.Options) > 0) {
		p := prior.DNS
		if p == nil {
			p = &svcDNSModel{Nameservers: types.ListNull(types.StringType), Search: types.ListNull(types.StringType), Options: types.ListNull(types.StringType)}
		}
		m.DNS = &svcDNSModel{
			Nameservers: svcListValue(p.Nameservers, addrStrings(d.Nameservers)),
			Search:      svcListValue(p.Search, d.Search),
			Options:     svcListValue(p.Options, d.Options),
		}
	}
	m.CapAdd = containerCapSet(prior.CapAdd, containerSetStrings(prior.CapAdd), cs.CapabilityAdd)
	m.CapDrop = containerCapSet(prior.CapDrop, containerSetStrings(prior.CapDrop), cs.CapabilityDrop)
	m.Sysctls = svcMapValue(prior.Sysctls, cs.Sysctls)
	m.Ulimits = nil
	for _, u := range cs.Ulimits {
		if u == nil {
			continue
		}
		m.Ulimits = append(m.Ulimits, svcUlimitModel{Name: types.StringValue(u.Name), Soft: types.Int64Value(u.Soft), Hard: types.Int64Value(u.Hard)})
	}
	m.StopGracePeriod = svcDurPtrValue(prior.StopGracePeriod, cs.StopGracePeriod)
	m.ForceUpdate = types.Int64Null()
	if svcPriorSet(prior.ForceUpdate) {
		m.ForceUpdate = types.Int64Value(int64(spec.TaskTemplate.ForceUpdate))
	}
}

// svcNetworksConflictValidator rejects networks_advanced blocks next to a
// non-empty networks attribute (works for docker_service and inside a stack
// service block, via the sibling path).
type svcNetworksConflictValidator struct{}

func (svcNetworksConflictValidator) Description(context.Context) string {
	return "networks_advanced cannot be combined with networks"
}

func (v svcNetworksConflictValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (svcNetworksConflictValidator) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || len(req.ConfigValue.Elements()) == 0 {
		return
	}
	var nets types.Set
	sibling := req.Path.ParentPath().AtName("networks")
	if req.Path.ParentPath().Equal(path.Empty()) {
		sibling = path.Root("networks")
	}
	if d := req.Config.GetAttribute(ctx, sibling, &nets); d.HasError() {
		return
	}
	if !nets.IsNull() && !nets.IsUnknown() && len(nets.Elements()) > 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Conflicting network settings",
			"Use either networks or networks_advanced, not both (move each network into a networks_advanced block).")
	}
}
