package provider

// Conversion between svcSpecModel and swarm.ServiceSpec. Flattening is
// "prior aware": it takes the value Terraform already holds (plan after
// create/update, state on refresh) and keeps that spelling whenever the
// daemon's value means the same thing (durations, names vs IDs, defaults the
// daemon fills, the digest pin), so a refresh never produces a spurious diff.

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// ---------------------------------------------------------------------------
// Reference lookup

// svcLookup resolves network/secret/config references (ID, name, ID prefix,
// or a stack-local short name) without further API calls.
type svcLookup struct {
	networks, secrets, configs []namedObject
	// stackPrefix ("<stack>_") makes a short name resolve to the stack's
	// object of that name before any other match.
	stackPrefix string
}

func svcLoadLookup(ctx context.Context, c *client.Client, stackPrefix string) (*svcLookup, error) {
	lk := &svcLookup{stackPrefix: stackPrefix}
	netsRes, err := c.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing networks: %w", err)
	}
	nets := netsRes.Items
	for _, n := range nets {
		lk.networks = append(lk.networks, namedObject{ID: n.ID, Name: n.Name})
	}
	secsRes, err := c.SecretList(ctx, client.SecretListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	secs := secsRes.Items
	for _, s := range secs {
		lk.secrets = append(lk.secrets, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	cfgsRes, err := c.ConfigList(ctx, client.ConfigListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing configs: %w", err)
	}
	cfgs := cfgsRes.Items
	for _, s := range cfgs {
		lk.configs = append(lk.configs, namedObject{ID: s.ID, Name: s.Spec.Name})
	}
	return lk, nil
}

func (l *svcLookup) objs(kind string) []namedObject {
	switch kind {
	case "network":
		return l.networks
	case "secret":
		return l.secrets
	default:
		return l.configs
	}
}

// resolve returns the ID ref denotes.
func (l *svcLookup) resolve(kind, ref string) (string, error) {
	if l.stackPrefix != "" {
		for _, o := range l.objs(kind) {
			if o.Name == l.stackPrefix+ref {
				return o.ID, nil
			}
		}
	}
	return resolveNameOrID(kind, ref, l.objs(kind))
}

// nameOf returns the name of the object with id, or "".
func (l *svcLookup) nameOf(kind, id string) string {
	for _, o := range l.objs(kind) {
		if o.ID == id {
			return o.Name
		}
	}
	return ""
}

// displayRef is how an ID with no prior reference is shown: the short name
// of a stack-local object, otherwise the ID itself.
func (l *svcLookup) displayRef(kind, id string) string {
	if l == nil || l.stackPrefix == "" {
		return id
	}
	if short, ok := strings.CutPrefix(l.nameOf(kind, id), l.stackPrefix); ok && short != "" {
		return short
	}
	return id
}

// matches reports whether ref denotes id.
func (l *svcLookup) matches(kind, ref, id string) bool {
	got, err := l.resolve(kind, ref)
	return err == nil && got == id
}

// ---------------------------------------------------------------------------
// Expand

// svcExpandOpts carries what differs between docker_service and a stack.
type svcExpandOpts struct {
	// FullName is the Swarm service name (the stack prefixes it).
	FullName string
	// Namespace is the stack name, "" for docker_service.
	Namespace string
}

// svcExpand builds the ServiceSpec for m.
func svcExpand(ctx context.Context, m *svcSpecModel, lk *svcLookup, o svcExpandOpts) (swarm.ServiceSpec, diag.Diagnostics) {
	var diags diag.Diagnostics
	extra := map[string]string{labelManagedBy: labelManagedByValue}
	if o.Namespace != "" {
		extra[labelStackNamespace] = o.Namespace
	}
	cs := &swarm.ContainerSpec{
		Image:   m.Image.ValueString(),
		Command: stringListFrom(ctx, m.Command, &diags),
		Args:    stringListFrom(ctx, m.Args, &diags),
		Env:     svcEnvList(stringMapFrom(ctx, m.Env, &diags)),
	}
	cl := stringMapFrom(ctx, m.ContainerLabels, &diags)
	if o.Namespace != "" {
		cl = mergeLabels(cl, map[string]string{labelStackNamespace: o.Namespace})
	}
	if len(cl) > 0 {
		cs.Labels = cl
	}
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name:   o.FullName,
			Labels: mergeLabels(stringMapFrom(ctx, m.Labels, &diags), extra),
		},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: cs},
	}
	if spec.Name == "" {
		spec.Name = m.Name.ValueString()
	}

	// Mode
	svcExpandMode(m, &spec, &diags)

	// Networks
	svcExpandNetworks(ctx, m, lk, o, &spec, &diags)
	svcExpandP2(ctx, m, &spec, &diags)
	svcExpandP5(ctx, m, &spec, &diags)

	// Mounts
	for i, mm := range m.Mounts {
		cs.Mounts = append(cs.Mounts, svcExpandMount(ctx, i, mm, &diags))
	}

	// Secrets / configs
	for _, s := range m.Secrets {
		ref := s.SecretID.ValueString()
		id, err := lk.resolve("secret", ref)
		if err != nil {
			diags.AddError("Cannot resolve secret", err.Error())
			continue
		}
		name := s.SecretName.ValueString()
		if name == "" {
			name = lk.nameOf("secret", id)
		}
		file := s.FileName.ValueString()
		if file == "" {
			file = svcDefaultFileName(ref, id, name)
		}
		cs.Secrets = append(cs.Secrets, &swarm.SecretReference{
			SecretID: id, SecretName: name,
			File: &swarm.SecretReferenceFileTarget{Name: file, UID: svcStrOr(s.UID, "0"), GID: svcStrOr(s.GID, "0"), Mode: svcFileMode(s.Mode)},
		})
	}
	for _, s := range m.Configs {
		ref := s.ConfigID.ValueString()
		id, err := lk.resolve("config", ref)
		if err != nil {
			diags.AddError("Cannot resolve config", err.Error())
			continue
		}
		name := s.ConfigName.ValueString()
		if name == "" {
			name = lk.nameOf("config", id)
		}
		file := s.FileName.ValueString()
		if file == "" {
			file = svcDefaultFileName(ref, id, name)
		}
		cs.Configs = append(cs.Configs, &swarm.ConfigReference{
			ConfigID: id, ConfigName: name,
			File: &swarm.ConfigReferenceFileTarget{Name: file, UID: svcStrOr(s.UID, "0"), GID: svcStrOr(s.GID, "0"), Mode: svcFileMode(s.Mode)},
		})
	}

	// Endpoint
	if e := m.EndpointSpec; e != nil {
		es := &swarm.EndpointSpec{Mode: swarm.ResolutionMode(svcStrOr(e.Mode, "vip"))}
		for _, p := range e.Ports {
			es.Ports = append(es.Ports, swarm.PortConfig{
				Name:          p.Name.ValueString(),
				TargetPort:    uint32(p.TargetPort.ValueInt64()),
				PublishedPort: uint32(p.PublishedPort.ValueInt64()),
				Protocol:      network.IPProtocol(svcStrOr(p.Protocol, "tcp")),
				PublishMode:   swarm.PortConfigPublishMode(svcStrOr(p.PublishMode, "ingress")),
			})
		}
		spec.EndpointSpec = es
	}

	// Update / rollback
	spec.UpdateConfig = svcExpandUpdate(m.UpdateConfig, "update_config", &diags)
	spec.RollbackConfig = svcExpandUpdate(m.RollbackConfig, "rollback_config", &diags)

	// Restart
	if r := m.RestartPolicy; r != nil {
		rp := &swarm.RestartPolicy{Condition: swarm.RestartPolicyCondition(svcStrOr(r.Condition, "any"))}
		rp.Delay = svcDurPtr(r.Delay, "restart_policy.delay", &diags)
		rp.Window = svcDurPtr(r.Window, "restart_policy.window", &diags)
		if v, ok := knownInt64(r.MaxAttempts); ok {
			u := uint64(v)
			rp.MaxAttempts = &u
		}
		spec.TaskTemplate.RestartPolicy = rp
	}

	// Placement
	if p := m.Placement; p != nil {
		pl := &swarm.Placement{Constraints: stringListFrom(ctx, p.Constraints, &diags)}
		for _, pref := range stringListFrom(ctx, p.Preferences, &diags) {
			kind, desc, ok := strings.Cut(pref, "=")
			if !ok || kind != "spread" || desc == "" {
				diags.AddError("Invalid placement preference", fmt.Sprintf("%q: want spread=<descriptor>", pref))
				continue
			}
			pl.Preferences = append(pl.Preferences, swarm.PlacementPreference{Spread: &swarm.SpreadOver{SpreadDescriptor: desc}})
		}
		if v, ok := knownInt64(p.MaxReplicas); ok {
			pl.MaxReplicas = uint64(v)
		}
		for _, pf := range p.Platforms {
			pl.Platforms = append(pl.Platforms, swarm.Platform{Architecture: pf.Architecture.ValueString(), OS: pf.OS.ValueString()})
		}
		spec.TaskTemplate.Placement = pl
	}

	// Resources
	if r := m.Resources; r != nil {
		rr := &swarm.ResourceRequirements{}
		if l := r.Limits; l != nil {
			rr.Limits = &swarm.Limit{NanoCPUs: l.NanoCPUs.ValueInt64(), MemoryBytes: l.MemoryBytes.ValueInt64()}
		}
		if l := r.Reservations; l != nil {
			rr.Reservations = &swarm.Resources{NanoCPUs: l.NanoCPUs.ValueInt64(), MemoryBytes: l.MemoryBytes.ValueInt64(),
				GenericResources: svcExpandGenericResources(l.GenericResources, &diags)}
		}
		spec.TaskTemplate.Resources = rr
	}

	// Healthcheck
	if h := m.Healthcheck; h != nil {
		hc := &container.HealthConfig{Test: stringListFrom(ctx, h.Test, &diags)}
		hc.Interval = svcDur(h.Interval, "healthcheck.interval", &diags)
		hc.Timeout = svcDur(h.Timeout, "healthcheck.timeout", &diags)
		hc.StartPeriod = svcDur(h.StartPeriod, "healthcheck.start_period", &diags)
		hc.Retries = int(h.Retries.ValueInt64())
		cs.Healthcheck = hc
	}

	// Logging
	if lg := m.Logging; lg != nil {
		spec.TaskTemplate.LogDriver = &swarm.Driver{Name: lg.Driver.ValueString(), Options: stringMapFrom(ctx, lg.Options, &diags)}
	}
	return spec, diags
}

func svcExpandUpdate(u *svcUpdateModel, attr string, diags *diag.Diagnostics) *swarm.UpdateConfig {
	if u == nil {
		return nil
	}
	uc := &swarm.UpdateConfig{
		Parallelism:   1,
		FailureAction: swarm.FailureAction(svcStrOr(u.FailureAction, "pause")),
		Order:         swarm.UpdateOrder(svcStrOr(u.Order, "stop-first")),
	}
	if v, ok := knownInt64(u.Parallelism); ok {
		uc.Parallelism = uint64(v)
	}
	uc.Delay = svcDur(u.Delay, attr+".delay", diags)
	uc.Monitor = svcDur(u.Monitor, attr+".monitor", diags)
	if !u.MaxFailureRatio.IsNull() && !u.MaxFailureRatio.IsUnknown() {
		uc.MaxFailureRatio = float32(u.MaxFailureRatio.ValueFloat64())
	}
	return uc
}

func svcEnvList(env map[string]string) []string {
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

// svcDefaultFileName is the file a secret/config is mounted as when
// file_name is unset: the reference as the user wrote it when it is a name,
// otherwise the object's name.
func svcDefaultFileName(ref, id, name string) string {
	if ref != "" && ref != id && !strings.HasPrefix(id, ref) {
		return ref
	}
	if name != "" {
		return name
	}
	return ref
}

func svcStrOr(v types.String, def string) string {
	if s, ok := knownString(v); ok && s != "" {
		return s
	}
	return def
}

func svcFileMode(v types.Int64) os.FileMode {
	if n, ok := knownInt64(v); ok {
		return os.FileMode(n)
	}
	return svcDefaultSecretMode
}

func svcDur(v types.String, attr string, diags *diag.Diagnostics) time.Duration {
	s, ok := knownString(v)
	if !ok || s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		diags.AddError("Invalid duration in "+attr, err.Error())
	}
	return d
}

func svcDurPtr(v types.String, attr string, diags *diag.Diagnostics) *time.Duration {
	s, ok := knownString(v)
	if !ok || s == "" {
		return nil
	}
	d := svcDur(v, attr, diags)
	return &d
}

// ---------------------------------------------------------------------------
// Flatten

// svcFlattenOpts carries what differs between docker_service and a stack.
type svcFlattenOpts struct {
	// ShortName overrides spec.Name (stacks strip the "<stack>_" prefix).
	ShortName string
	// StripStack removes the stack namespace label from labels.
	StripStack bool
}

// svcFlatten converts spec into a model, using prior to keep the user's
// spelling wherever the daemon's value is equivalent.
func svcFlatten(spec swarm.ServiceSpec, prior svcSpecModel, lk *svcLookup, o svcFlattenOpts) svcSpecModel {
	m := svcSpecModel{ConvergeConfig: prior.ConvergeConfig}
	m.Name = types.StringValue(spec.Name)
	if o.ShortName != "" {
		m.Name = types.StringValue(o.ShortName)
	}
	m.Labels = svcMapValue(prior.Labels, userLabels(spec.Labels, o.StripStack))

	svcFlattenMode(spec, prior, &m)

	cs := spec.TaskTemplate.ContainerSpec
	if cs == nil {
		cs = &swarm.ContainerSpec{}
	}
	m.Image = svcImageValue(prior.Image, cs.Image)
	m.Command = svcListValue(prior.Command, cs.Command)
	m.Args = svcListValue(prior.Args, cs.Args)
	m.Env = svcMapValue(prior.Env, svcEnvMap(cs.Env))
	m.ContainerLabels = svcMapValue(prior.ContainerLabels, userLabels(cs.Labels, o.StripStack))

	svcFlattenNetworks(spec, prior, lk, o, &m)
	svcFlattenP2(spec, cs, prior, &m)
	svcFlattenP5(spec, cs, prior, &m)

	// Mounts (positional against prior).
	for i, mt := range cs.Mounts {
		var p svcMountModel
		if i < len(prior.Mounts) {
			p = prior.Mounts[i]
		}
		m.Mounts = append(m.Mounts, svcFlattenMount(mt, p))
	}

	for i, s := range cs.Secrets {
		var p svcSecretRefModel
		if i < len(prior.Secrets) {
			p = prior.Secrets[i]
		}
		ref := lk.displayRef("secret", s.SecretID)
		if r, ok := knownString(p.SecretID); ok && (r == s.SecretID || (lk != nil && lk.matches("secret", r, s.SecretID))) {
			ref = r
		}
		f := s.File
		if f == nil {
			f = &swarm.SecretReferenceFileTarget{}
		}
		m.Secrets = append(m.Secrets, svcSecretRefModel{
			SecretID:   types.StringValue(ref),
			SecretName: svcOptionalKeep(p.SecretName, s.SecretName),
			FileName:   svcStrValue(p.FileName, f.Name, svcDefaultFileName(ref, s.SecretID, s.SecretName)),
			UID:        types.StringValue(f.UID),
			GID:        types.StringValue(f.GID),
			Mode:       types.Int64Value(int64(f.Mode)),
		})
	}
	for i, s := range cs.Configs {
		var p svcConfigRefModel
		if i < len(prior.Configs) {
			p = prior.Configs[i]
		}
		ref := lk.displayRef("config", s.ConfigID)
		if r, ok := knownString(p.ConfigID); ok && (r == s.ConfigID || (lk != nil && lk.matches("config", r, s.ConfigID))) {
			ref = r
		}
		f := s.File
		if f == nil {
			f = &swarm.ConfigReferenceFileTarget{}
		}
		m.Configs = append(m.Configs, svcConfigRefModel{
			ConfigID:   types.StringValue(ref),
			ConfigName: svcOptionalKeep(p.ConfigName, s.ConfigName),
			FileName:   svcStrValue(p.FileName, f.Name, svcDefaultFileName(ref, s.ConfigID, s.ConfigName)),
			UID:        types.StringValue(f.UID),
			GID:        types.StringValue(f.GID),
			Mode:       types.Int64Value(int64(f.Mode)),
		})
	}

	// Endpoint
	if es := spec.EndpointSpec; es != nil && (prior.EndpointSpec != nil || len(es.Ports) > 0 || (es.Mode != "" && es.Mode != swarm.ResolutionModeVIP)) {
		em := &svcEndpointModel{Mode: types.StringValue(svcOr(string(es.Mode), "vip"))}
		for i, pc := range es.Ports {
			var p svcPortModel
			if prior.EndpointSpec != nil && i < len(prior.EndpointSpec.Ports) {
				p = prior.EndpointSpec.Ports[i]
			}
			em.Ports = append(em.Ports, svcPortModel{
				Name:          svcStrValue(p.Name, pc.Name, ""),
				TargetPort:    types.Int64Value(int64(pc.TargetPort)),
				PublishedPort: svcIntValue(p.PublishedPort, int64(pc.PublishedPort)),
				Protocol:      types.StringValue(svcOr(string(pc.Protocol), "tcp")),
				PublishMode:   types.StringValue(svcOr(string(pc.PublishMode), "ingress")),
			})
		}
		m.EndpointSpec = em
	}

	m.UpdateConfig = svcFlattenUpdate(spec.UpdateConfig, prior.UpdateConfig)
	m.RollbackConfig = svcFlattenUpdate(spec.RollbackConfig, prior.RollbackConfig)

	if rp := spec.TaskTemplate.RestartPolicy; rp != nil {
		p := prior.RestartPolicy
		if p == nil {
			p = &svcRestartModel{}
		}
		var ma int64
		if rp.MaxAttempts != nil {
			ma = int64(*rp.MaxAttempts)
		}
		m.RestartPolicy = &svcRestartModel{
			Condition:   types.StringValue(svcOr(string(rp.Condition), "any")),
			Delay:       svcDurPtrValue(p.Delay, rp.Delay),
			MaxAttempts: svcIntValue(p.MaxAttempts, ma),
			Window:      svcDurPtrValue(p.Window, rp.Window),
		}
	}

	if pl := spec.TaskTemplate.Placement; pl != nil && (prior.Placement != nil || len(pl.Constraints) > 0 || len(pl.Preferences) > 0 || pl.MaxReplicas > 0) {
		p := prior.Placement
		if p == nil {
			p = &svcPlacementModel{Constraints: types.ListNull(types.StringType), Preferences: types.ListNull(types.StringType), MaxReplicas: types.Int64Null()}
		}
		var prefs []string
		for _, pp := range pl.Preferences {
			if pp.Spread != nil {
				prefs = append(prefs, "spread="+pp.Spread.SpreadDescriptor)
			}
		}
		m.Placement = &svcPlacementModel{
			Constraints: svcListValue(p.Constraints, pl.Constraints),
			Preferences: svcListValue(p.Preferences, prefs),
			MaxReplicas: svcIntValue(p.MaxReplicas, int64(pl.MaxReplicas)),
		}
		// The daemon fills Platforms from the image manifest; they are only
		// shown when the configuration sets them.
		if len(p.Platforms) > 0 {
			for _, pf := range pl.Platforms {
				m.Placement.Platforms = append(m.Placement.Platforms, svcPlatformModel{
					Architecture: types.StringValue(pf.Architecture), OS: types.StringValue(pf.OS)})
			}
		}
	}

	if rr := spec.TaskTemplate.Resources; rr != nil {
		p := prior.Resources
		if p == nil {
			p = &svcResourcesModel{}
		}
		res := &svcResourcesModel{}
		if l := rr.Limits; l != nil && (p.Limits != nil || l.NanoCPUs != 0 || l.MemoryBytes != 0) {
			res.Limits = svcFlattenAmount(p.Limits, l.NanoCPUs, l.MemoryBytes)
		}
		if l := rr.Reservations; l != nil && (p.Reservations != nil || l.NanoCPUs != 0 || l.MemoryBytes != 0 || len(l.GenericResources) > 0) {
			res.Reservations = svcFlattenReservation(p.Reservations, *l)
		}
		if prior.Resources != nil || res.Limits != nil || res.Reservations != nil {
			m.Resources = res
		}
	}

	if hc := cs.Healthcheck; hc != nil {
		p := prior.Healthcheck
		if p == nil {
			p = &svcHealthModel{Test: types.ListNull(types.StringType)}
		}
		m.Healthcheck = &svcHealthModel{
			Test:        svcListValue(p.Test, hc.Test),
			Interval:    svcDurValue(p.Interval, hc.Interval),
			Timeout:     svcDurValue(p.Timeout, hc.Timeout),
			StartPeriod: svcDurValue(p.StartPeriod, hc.StartPeriod),
			Retries:     svcIntValue(p.Retries, int64(hc.Retries)),
		}
	}

	if ld := spec.TaskTemplate.LogDriver; ld != nil {
		p := prior.Logging
		if p == nil {
			p = &svcLoggingModel{Options: types.MapNull(types.StringType)}
		}
		m.Logging = &svcLoggingModel{
			Driver:  svcStrValue(p.Driver, ld.Name, ""),
			Options: svcMapValue(p.Options, ld.Options),
		}
	}
	return m
}

func svcFlattenUpdate(uc *swarm.UpdateConfig, p *svcUpdateModel) *svcUpdateModel {
	if uc == nil {
		return nil
	}
	if p == nil {
		p = &svcUpdateModel{}
	}
	out := &svcUpdateModel{
		Parallelism:   types.Int64Value(int64(uc.Parallelism)),
		Delay:         svcDurValue(p.Delay, uc.Delay),
		FailureAction: types.StringValue(svcOr(string(uc.FailureAction), "pause")),
		Monitor:       svcDurValue(p.Monitor, uc.Monitor),
		Order:         types.StringValue(svcOr(string(uc.Order), "stop-first")),
	}
	switch {
	case !p.MaxFailureRatio.IsNull() && !p.MaxFailureRatio.IsUnknown() && float32(p.MaxFailureRatio.ValueFloat64()) == uc.MaxFailureRatio:
		out.MaxFailureRatio = p.MaxFailureRatio
	case uc.MaxFailureRatio == 0:
		out.MaxFailureRatio = types.Float64Null()
	default:
		// Round the float32 to a short decimal so 0.1 reads back as 0.1.
		out.MaxFailureRatio = types.Float64Value(math.Round(float64(uc.MaxFailureRatio)*1e6) / 1e6)
	}
	return out
}

func svcFlattenAmount(p *svcResourceAmountModel, cpus, mem int64) *svcResourceAmountModel {
	if p == nil {
		p = &svcResourceAmountModel{NanoCPUs: types.Int64Null(), MemoryBytes: types.Int64Null()}
	}
	return &svcResourceAmountModel{NanoCPUs: svcIntValue(p.NanoCPUs, cpus), MemoryBytes: svcIntValue(p.MemoryBytes, mem)}
}

func svcEnvMap(env []string) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		out[k] = v
	}
	return out
}

func svcOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ---------------------------------------------------------------------------
// Prior-aware value helpers

func svcPriorSet(v attr.Value) bool { return v != nil && !v.IsNull() && !v.IsUnknown() }

// svcMapValue returns null for an empty map unless prior was a known
// (possibly empty) map, so `labels = {}` survives a round trip.
func svcMapValue(prior types.Map, in map[string]string) types.Map {
	if len(in) == 0 {
		if svcPriorSet(prior) && len(prior.Elements()) == 0 {
			return types.MapValueMust(types.StringType, map[string]attr.Value{})
		}
		return types.MapNull(types.StringType)
	}
	return stringMapValue(in, false)
}

func svcListValue(prior types.List, in []string) types.List {
	if len(in) == 0 {
		if svcPriorSet(prior) && len(prior.Elements()) == 0 {
			return types.ListValueMust(types.StringType, []attr.Value{})
		}
		return types.ListNull(types.StringType)
	}
	return stringListValue(in, false)
}

func svcSetValue(prior types.Set, in []string) types.Set {
	if len(in) == 0 {
		if svcPriorSet(prior) && len(prior.Elements()) == 0 {
			return types.SetValueMust(types.StringType, []attr.Value{})
		}
		return types.SetNull(types.StringType)
	}
	return stringSetValue(in, false)
}

// svcStrValue returns v, or null when prior is unset and v is "" or def.
func svcStrValue(prior types.String, v, def string) types.String {
	if !svcPriorSet(prior) && (v == "" || v == def) {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// svcOptionalKeep returns prior when it was set, otherwise null: for
// optional values the provider fills in itself (secret_name).
func svcOptionalKeep(prior types.String, v string) types.String {
	if svcPriorSet(prior) {
		return types.StringValue(v)
	}
	return types.StringNull()
}

// svcIntValue returns v, or null when prior is unset and v is 0.
func svcIntValue(prior types.Int64, v int64) types.Int64 {
	if !svcPriorSet(prior) && v == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(v)
}

// svcDurValue keeps the prior spelling ("1m") when it parses to d ("1m0s").
func svcDurValue(prior types.String, d time.Duration) types.String {
	if s, ok := knownString(prior); ok {
		if pd, err := time.ParseDuration(s); err == nil && pd == d {
			return prior
		}
	}
	if d == 0 && !svcPriorSet(prior) {
		return types.StringNull()
	}
	return types.StringValue(svcFormatDuration(d))
}

// svcFormatDuration is time.Duration.String without trailing zero units
// ("1m" rather than "1m0s", "2h" rather than "2h0m0s").
func svcFormatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func svcDurPtrValue(prior types.String, d *time.Duration) types.String {
	if d == nil {
		return types.StringNull()
	}
	return svcDurValue(prior, *d)
}

// svcImageValue keeps the configured image when the daemon's only differs by
// the digest pin and/or the implied :latest tag.
func svcImageValue(prior types.String, daemon string) types.String {
	if p, ok := knownString(prior); ok && svcImageMatches(p, daemon) {
		return prior
	}
	return types.StringValue(daemon)
}

// svcStripDigest removes an "@sha256:..." suffix.
func svcStripDigest(img string) string {
	if i := strings.Index(img, "@"); i >= 0 {
		return img[:i]
	}
	return img
}

// svcImageMatches reports whether the daemon image is what want asked for.
// When want has no digest, the daemon's digest pin is ignored, and an
// implicit :latest tag matches.
func svcImageMatches(want, daemon string) bool {
	if want == daemon {
		return true
	}
	if strings.Contains(want, "@") {
		return svcNormalizeImage(want) == svcNormalizeImage(daemon)
	}
	return svcNormalizeImage(want) == svcNormalizeImage(svcStripDigest(daemon))
}

func svcNormalizeImage(img string) string {
	named, err := reference.ParseNormalizedNamed(img)
	if err != nil {
		return img
	}
	if _, ok := named.(reference.Canonical); ok {
		return named.String()
	}
	return reference.TagNameOnly(named).String()
}

// ---------------------------------------------------------------------------
// Validators

type svcDurationStringValidator struct{}

func (svcDurationStringValidator) Description(context.Context) string {
	return "value must be a Go duration such as 30s or 5m"
}

func (v svcDurationStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (svcDurationStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.ParseDuration(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration", err.Error())
	}
}
