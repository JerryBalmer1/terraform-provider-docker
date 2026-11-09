package provider

// docker_compose, mode = "swarm": a compose project expanded into the same
// primitives docker_stack manages (services, overlay networks, secrets,
// configs labelled com.docker.stack.namespace), reconciled with docker_stack's
// planning and removal helpers.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

type composeSwarmNetwork struct {
	Key, Name string
	External  bool
	Create    client.NetworkCreateOptions
	Hash      string
}

type composeSwarmBlob struct {
	Key, Name string
	External  bool
	Data      []byte
	Labels    map[string]string
	Hash      string
}

type composeSwarmService struct {
	Key  string
	Name string
	// Spec references networks, secrets and configs by name (Target,
	// SecretName, ConfigName); IDs are filled in at apply time.
	Spec swarm.ServiceSpec
	Mode string
	Hash string
}

type composeSwarmPlan struct {
	Namespace string
	Networks  []composeSwarmNetwork
	Secrets   []composeSwarmBlob
	Configs   []composeSwarmBlob
	Services  []composeSwarmService
	Warnings  []string
	// KeepOrphans is set when remove_orphans is false: services of the
	// project that are no longer configured are left alone.
	KeepOrphans bool
}

func (p *composeSwarmPlan) warnf(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

func composeSha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// composeSwarmExpand converts a project into stack objects.
func composeSwarmExpand(p *ctypes.Project) (*composeSwarmPlan, error) {
	ns := p.Name
	plan := &composeSwarmPlan{Namespace: ns}
	nsLabels := map[string]string{labelManagedBy: labelManagedByValue, labelStackNamespace: ns}

	for _, key := range composeSortedKeys(p.Networks) {
		n := p.Networks[key]
		sn := composeSwarmNetwork{Key: key, Name: n.Name, External: bool(n.External)}
		if !sn.External {
			opts := client.NetworkCreateOptions{
				Driver:     svcOr(n.Driver, "overlay"),
				Scope:      "swarm",
				Internal:   n.Internal,
				Attachable: n.Attachable,
				EnableIPv4: n.EnableIPv4,
				EnableIPv6: n.EnableIPv6,
				Options:    map[string]string(n.DriverOpts),
				Labels:     mergeLabels(n.Labels, nsLabels),
			}
			if n.Ipam.Driver != "" || len(n.Ipam.Config) > 0 {
				ipam := &network.IPAM{Driver: n.Ipam.Driver, Options: map[string]string(n.Ipam.Options)}
				for _, c := range n.Ipam.Config {
					if c != nil {
						pool, err := ipamConfigFrom(c.Subnet, c.IPRange, c.Gateway, map[string]string(c.AuxiliaryAddresses))
						if err != nil {
							return nil, fmt.Errorf("network %s ipam: %w", key, err)
						}
						ipam.Config = append(ipam.Config, pool)
					}
				}
				opts.IPAM = ipam
			}
			sn.Hash = composeNetworkHash(opts)
			opts.Labels[composeLabelConfigHash] = sn.Hash
			sn.Create = opts
		}
		plan.Networks = append(plan.Networks, sn)
	}

	blobs := func(kind string, objs map[string]ctypes.FileObjectConfig) ([]composeSwarmBlob, error) {
		var out []composeSwarmBlob
		for _, key := range composeSortedKeys(objs) {
			o := objs[key]
			b := composeSwarmBlob{Key: key, Name: o.Name, External: bool(o.External)}
			if !b.External {
				data, _, err := composeBlobData(o, p.Environment)
				if err != nil {
					return nil, fmt.Errorf("%s %s: %w", kind, key, err)
				}
				if o.Driver != "" || o.TemplateDriver != "" {
					plan.warnf("%s %s: driver and template_driver are not supported and are ignored", kind, key)
				}
				b.Data = data
				b.Labels = mergeLabels(o.Labels, nsLabels)
				b.Hash = composeHash([]any{composeSha(data), b.Labels})
				b.Labels[composeLabelConfigHash] = b.Hash
			}
			out = append(out, b)
		}
		return out, nil
	}
	secrets := map[string]ctypes.FileObjectConfig{}
	for k, v := range p.Secrets {
		secrets[k] = ctypes.FileObjectConfig(v)
	}
	configs := map[string]ctypes.FileObjectConfig{}
	for k, v := range p.Configs {
		configs[k] = ctypes.FileObjectConfig(v)
	}
	var err error
	if plan.Secrets, err = blobs("secret", secrets); err != nil {
		return nil, err
	}
	if plan.Configs, err = blobs("config", configs); err != nil {
		return nil, err
	}

	for _, key := range composeSortedKeys(p.Services) {
		s, err := composeSwarmServiceSpec(p, plan, p.Services[key])
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", key, err)
		}
		plan.Services = append(plan.Services, s)
	}
	return plan, nil
}

func composeSwarmUnsupported(plan *composeSwarmPlan, svc ctypes.ServiceConfig) {
	w := func(attr string) {
		plan.warnf("service %s: %s is not supported in swarm mode and is ignored", svc.Name, attr)
	}
	if svc.Build != nil {
		w("build (the image must be in a registry the nodes can pull from)")
	}
	if svc.ContainerName != "" {
		w("container_name")
	}
	if len(svc.DependsOn) > 0 {
		w("depends_on (swarm starts services independently)")
	}
	if svc.NetworkMode != "" {
		w("network_mode")
	}
	if svc.Privileged {
		w("privileged")
	}
	if len(svc.Devices) > 0 {
		w("devices")
	}
	if svc.Ipc != "" || svc.Pid != "" || svc.UserNSMode != "" || svc.Uts != "" || svc.CgroupParent != "" {
		w("ipc/pid/userns_mode/uts/cgroup_parent")
	}
	if len(svc.SecurityOpt) > 0 {
		w("security_opt")
	}
	if svc.ShmSize != 0 {
		w("shm_size")
	}
	if len(svc.VolumesFrom) > 0 {
		w("volumes_from")
	}
	if len(svc.Gpus) > 0 {
		w("gpus")
	}
	if len(svc.Links) > 0 || len(svc.ExternalLinks) > 0 {
		w("links/external_links")
	}
	if len(svc.PostStart) > 0 || len(svc.PreStop) > 0 || len(svc.PreStart) > 0 {
		w("lifecycle hooks")
	}
	if svc.Develop != nil {
		w("develop")
	}
	if len(svc.Tmpfs) > 0 {
		w("tmpfs (use a volume of type tmpfs)")
	}
	if svc.PullPolicy != "" && svc.PullPolicy != ctypes.PullPolicyMissing && svc.PullPolicy != ctypes.PullPolicyAlways {
		w("pull_policy " + svc.PullPolicy)
	}
}

func composeSwarmServiceSpec(p *ctypes.Project, plan *composeSwarmPlan, svc ctypes.ServiceConfig) (composeSwarmService, error) {
	ns := p.Name
	out := composeSwarmService{Key: svc.Name, Name: ns + "_" + svc.Name}
	if svc.Image == "" {
		return out, fmt.Errorf("no image: swarm services need an image from a registry")
	}
	composeSwarmUnsupported(plan, svc)

	cs := &swarm.ContainerSpec{
		Image:          svc.Image,
		Labels:         mergeLabels(svc.Labels, svc.CustomLabels, map[string]string{labelStackNamespace: ns}),
		Hostname:       svc.Hostname,
		Env:            composeEnvList(svc.Environment),
		Dir:            svc.WorkingDir,
		User:           svc.User,
		Groups:         svc.GroupAdd,
		TTY:            svc.Tty,
		OpenStdin:      svc.StdinOpen,
		ReadOnly:       svc.ReadOnly,
		StopSignal:     svc.StopSignal,
		Init:           svc.Init,
		Sysctls:        map[string]string(svc.Sysctls),
		CapabilityAdd:  svc.CapAdd,
		CapabilityDrop: svc.CapDrop,
		Isolation:      container.Isolation(svc.Isolation),
		Healthcheck:    composeHealthcheck(svc.HealthCheck),
	}
	if svc.Entrypoint != nil {
		cs.Command = []string(svc.Entrypoint)
	}
	if svc.Command != nil {
		cs.Args = []string(svc.Command)
	}
	if svc.StopGracePeriod != nil {
		d := time.Duration(*svc.StopGracePeriod)
		cs.StopGracePeriod = &d
	}
	for _, h := range composeSortedKeys(svc.ExtraHosts) {
		for _, ip := range svc.ExtraHosts[h] {
			cs.Hosts = append(cs.Hosts, ip+" "+h)
		}
	}
	if len(svc.DNS)+len(svc.DNSSearch)+len(svc.DNSOpts) > 0 {
		ns, err := parseAddrs("dns", []string(svc.DNS))
		if err != nil {
			return out, fmt.Errorf("service %s: %w", svc.Name, err)
		}
		cs.DNSConfig = &swarm.DNSConfig{Nameservers: ns, Search: []string(svc.DNSSearch), Options: svc.DNSOpts}
	}
	for _, name := range composeSortedKeys(svc.Ulimits) {
		u := svc.Ulimits[name]
		soft, hard := int64(u.Soft), int64(u.Hard)
		if u.Single != 0 {
			soft, hard = int64(u.Single), int64(u.Single)
		}
		cs.Ulimits = append(cs.Ulimits, &container.Ulimit{Name: name, Soft: soft, Hard: hard})
	}

	// Mounts
	for _, v := range svc.Volumes {
		m := mount.Mount{Target: v.Target, ReadOnly: v.ReadOnly, Consistency: mount.Consistency(v.Consistency)}
		switch v.Type {
		case ctypes.VolumeTypeVolume, "":
			m.Type = mount.TypeVolume
			if v.Source != "" {
				vol, ok := p.Volumes[v.Source]
				if !ok {
					return out, fmt.Errorf("volume %q is not defined", v.Source)
				}
				m.Source = vol.Name
				if !vol.External {
					m.VolumeOptions = &mount.VolumeOptions{Labels: mergeLabels(vol.Labels, map[string]string{labelStackNamespace: ns})}
					if vol.Driver != "" || len(vol.DriverOpts) > 0 {
						m.VolumeOptions.DriverConfig = &mount.Driver{Name: vol.Driver, Options: map[string]string(vol.DriverOpts)}
					}
				}
			}
			if v.Volume != nil && (v.Volume.NoCopy || v.Volume.Subpath != "") {
				if m.VolumeOptions == nil {
					m.VolumeOptions = &mount.VolumeOptions{}
				}
				m.VolumeOptions.NoCopy, m.VolumeOptions.Subpath = v.Volume.NoCopy, v.Volume.Subpath
			}
		case ctypes.VolumeTypeBind:
			m.Type, m.Source = mount.TypeBind, v.Source
			if v.Bind != nil && v.Bind.Propagation != "" {
				m.BindOptions = &mount.BindOptions{Propagation: mount.Propagation(v.Bind.Propagation)}
			}
		case ctypes.VolumeTypeTmpfs:
			m.Type = mount.TypeTmpfs
			if v.Tmpfs != nil {
				m.TmpfsOptions = &mount.TmpfsOptions{SizeBytes: int64(v.Tmpfs.Size)}
			}
		case ctypes.VolumeTypeNamedPipe:
			m.Type, m.Source = mount.TypeNamedPipe, v.Source
		default:
			return out, fmt.Errorf("volume type %q is not supported in swarm mode", v.Type)
		}
		cs.Mounts = append(cs.Mounts, m)
	}

	fileTarget := func(ref ctypes.FileReferenceConfig, def string) *swarm.SecretReferenceFileTarget {
		t := &swarm.SecretReferenceFileTarget{Name: ref.Target, UID: svcOr(ref.UID, "0"), GID: svcOr(ref.GID, "0"), Mode: 0o444}
		if t.Name == "" {
			t.Name = def
		}
		if ref.Mode != nil {
			t.Mode = os.FileMode(*ref.Mode) & os.ModePerm
		}
		return t
	}
	for _, ref := range svc.Secrets {
		obj, ok := p.Secrets[ref.Source]
		if !ok {
			return out, fmt.Errorf("secret %q is not defined", ref.Source)
		}
		ft := fileTarget(ctypes.FileReferenceConfig(ref), ref.Source)
		// Swarm secret targets are relative to /run/secrets.
		if rel, ok := strings.CutPrefix(ft.Name, "/run/secrets/"); ok {
			ft.Name = rel
		}
		cs.Secrets = append(cs.Secrets, &swarm.SecretReference{File: ft, SecretName: obj.Name})
	}
	for _, ref := range svc.Configs {
		obj, ok := p.Configs[ref.Source]
		if !ok {
			return out, fmt.Errorf("config %q is not defined", ref.Source)
		}
		ft := fileTarget(ctypes.FileReferenceConfig(ref), "/"+ref.Source)
		cs.Configs = append(cs.Configs, &swarm.ConfigReference{
			File:       &swarm.ConfigReferenceFileTarget{Name: ft.Name, UID: ft.UID, GID: ft.GID, Mode: ft.Mode},
			ConfigName: obj.Name,
		})
	}

	spec := swarm.ServiceSpec{
		Annotations:  swarm.Annotations{Name: out.Name},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: cs},
	}
	labels := map[string]string{}
	if svc.Deploy != nil {
		labels = map[string]string(svc.Deploy.Labels)
	}
	spec.Labels = mergeLabels(labels, map[string]string{labelManagedBy: labelManagedByValue, labelStackNamespace: ns})

	// Networks
	for _, key := range svc.NetworksByPriority() {
		n, ok := p.Networks[key]
		if !ok {
			return out, fmt.Errorf("network %q is not defined", key)
		}
		att := swarm.NetworkAttachmentConfig{Target: n.Name, Aliases: []string{svc.Name}}
		if nc := svc.Networks[key]; nc != nil {
			att.Aliases = append(att.Aliases, nc.Aliases...)
			att.DriverOpts = map[string]string(nc.DriverOpts)
		}
		spec.TaskTemplate.Networks = append(spec.TaskTemplate.Networks, att)
	}

	// Logging
	if svc.Logging != nil && svc.Logging.Driver != "" {
		spec.TaskTemplate.LogDriver = &swarm.Driver{Name: svc.Logging.Driver, Options: map[string]string(svc.Logging.Options)}
	} else if svc.LogDriver != "" {
		spec.TaskTemplate.LogDriver = &swarm.Driver{Name: svc.LogDriver, Options: svc.LogOpt}
	}

	// Mode and replicas
	replicas := uint64(svc.GetScale())
	mode := ""
	if svc.Deploy != nil {
		mode = svc.Deploy.Mode
	}
	switch mode {
	case "", "replicated":
		out.Mode = "replicated"
		spec.Mode.Replicated = &swarm.ReplicatedService{Replicas: &replicas}
	case "global":
		out.Mode = "global"
		spec.Mode.Global = &swarm.GlobalService{}
	case "replicated-job":
		out.Mode = "replicated_job"
		r := replicas
		spec.Mode.ReplicatedJob = &swarm.ReplicatedJob{MaxConcurrent: &replicas, TotalCompletions: &r}
	case "global-job":
		out.Mode = "global_job"
		spec.Mode.GlobalJob = &swarm.GlobalJob{}
	default:
		return out, fmt.Errorf("deploy.mode %q is not valid", mode)
	}

	// Resources: deploy.resources, falling back to cpus/mem_limit.
	var lim swarm.Limit
	var res swarm.Resources
	if svc.Deploy != nil {
		if l := svc.Deploy.Resources.Limits; l != nil {
			lim = swarm.Limit{NanoCPUs: int64(float64(l.NanoCPUs) * 1e9), MemoryBytes: int64(l.MemoryBytes), Pids: l.Pids}
		}
		if r := svc.Deploy.Resources.Reservations; r != nil {
			res = swarm.Resources{NanoCPUs: int64(float64(r.NanoCPUs) * 1e9), MemoryBytes: int64(r.MemoryBytes)}
			for _, g := range r.GenericResources {
				if g.DiscreteResourceSpec != nil {
					res.GenericResources = append(res.GenericResources, swarm.GenericResource{DiscreteResourceSpec: &swarm.DiscreteGenericResource{Kind: g.DiscreteResourceSpec.Kind, Value: g.DiscreteResourceSpec.Value}})
				}
			}
			if len(r.Devices) > 0 {
				plan.warnf("service %s: deploy.resources.reservations.devices is not supported in swarm mode and is ignored", svc.Name)
			}
		}
	}
	if lim.MemoryBytes == 0 {
		lim.MemoryBytes = int64(svc.MemLimit)
	}
	if lim.NanoCPUs == 0 {
		lim.NanoCPUs = int64(float64(svc.CPUS) * 1e9)
	}
	if lim.Pids == 0 {
		lim.Pids = svc.PidsLimit
	}
	if res.MemoryBytes == 0 {
		res.MemoryBytes = int64(svc.MemReservation)
	}
	if lim != (swarm.Limit{}) || res.NanoCPUs != 0 || res.MemoryBytes != 0 || len(res.GenericResources) > 0 {
		spec.TaskTemplate.Resources = &swarm.ResourceRequirements{}
		if lim != (swarm.Limit{}) {
			spec.TaskTemplate.Resources.Limits = &lim
		}
		if res.NanoCPUs != 0 || res.MemoryBytes != 0 || len(res.GenericResources) > 0 {
			spec.TaskTemplate.Resources.Reservations = &res
		}
	}

	// Restart policy
	if svc.Deploy != nil && svc.Deploy.RestartPolicy != nil {
		rp := svc.Deploy.RestartPolicy
		spec.TaskTemplate.RestartPolicy = &swarm.RestartPolicy{
			Condition:   swarm.RestartPolicyCondition(rp.Condition),
			MaxAttempts: rp.MaxAttempts,
		}
		if rp.Delay != nil {
			d := time.Duration(*rp.Delay)
			spec.TaskTemplate.RestartPolicy.Delay = &d
		}
		if rp.Window != nil {
			d := time.Duration(*rp.Window)
			spec.TaskTemplate.RestartPolicy.Window = &d
		}
	} else if svc.Restart != "" {
		name, retries, _ := strings.Cut(svc.Restart, ":")
		rp := &swarm.RestartPolicy{}
		switch name {
		case "no":
			rp.Condition = swarm.RestartPolicyConditionNone
		case "always", "unless-stopped":
			rp.Condition = swarm.RestartPolicyConditionAny
		case "on-failure":
			rp.Condition = swarm.RestartPolicyConditionOnFailure
			if retries != "" {
				n, err := strconv.ParseUint(retries, 10, 64)
				if err != nil {
					return out, fmt.Errorf("restart %q: %w", svc.Restart, err)
				}
				rp.MaxAttempts = &n
			}
		default:
			return out, fmt.Errorf("restart %q is not valid", svc.Restart)
		}
		spec.TaskTemplate.RestartPolicy = rp
	}

	if svc.Deploy != nil {
		d := svc.Deploy
		if len(d.Placement.Constraints)+len(d.Placement.Preferences) > 0 || d.Placement.MaxReplicas > 0 {
			pl := &swarm.Placement{Constraints: d.Placement.Constraints, MaxReplicas: d.Placement.MaxReplicas}
			for _, pref := range d.Placement.Preferences {
				pl.Preferences = append(pl.Preferences, swarm.PlacementPreference{Spread: &swarm.SpreadOver{SpreadDescriptor: pref.Spread}})
			}
			spec.TaskTemplate.Placement = pl
		}
		spec.UpdateConfig = composeSwarmUpdate(d.UpdateConfig)
		spec.RollbackConfig = composeSwarmUpdate(d.RollbackConfig)
	}

	// Endpoint
	if len(svc.Ports) > 0 || (svc.Deploy != nil && svc.Deploy.EndpointMode != "") {
		es := &swarm.EndpointSpec{}
		if svc.Deploy != nil && svc.Deploy.EndpointMode != "" {
			es.Mode = swarm.ResolutionMode(svc.Deploy.EndpointMode)
		}
		for _, pc := range svc.Ports {
			pp := swarm.PortConfig{Name: pc.Name, TargetPort: pc.Target, Protocol: network.IPProtocol(composeProto(pc.Protocol)), PublishMode: swarm.PortConfigPublishMode(pc.Mode)}
			if pc.Published != "" {
				n, err := strconv.ParseUint(pc.Published, 10, 32)
				if err != nil {
					return out, fmt.Errorf("published port %q: swarm mode needs a single port number", pc.Published)
				}
				pp.PublishedPort = uint32(n)
			}
			if pc.HostIP != "" {
				plan.warnf("service %s: port host_ip %s is not supported in swarm mode and is ignored", svc.Name, pc.HostIP)
			}
			es.Ports = append(es.Ports, pp)
		}
		spec.EndpointSpec = es
	}

	out.Spec = spec
	out.Hash = composeHash(spec)
	out.Spec.Labels[composeLabelConfigHash] = out.Hash
	return out, nil
}

func composeSwarmUpdate(u *ctypes.UpdateConfig) *swarm.UpdateConfig {
	if u == nil {
		return nil
	}
	out := &swarm.UpdateConfig{
		Parallelism:     1,
		Delay:           time.Duration(u.Delay),
		FailureAction:   swarm.FailureAction(u.FailureAction),
		Monitor:         time.Duration(u.Monitor),
		MaxFailureRatio: u.MaxFailureRatio,
		Order:           swarm.UpdateOrder(u.Order),
	}
	if u.Parallelism != nil {
		out.Parallelism = *u.Parallelism
	}
	return out
}

// ---------------------------------------------------------------------------
// Daemon side

// composeSwarmByName re-keys docker_stack's listing by full object name.
type composeSwarmByName struct {
	services map[string]swarm.Service
	networks map[string]network.Summary
	secrets  map[string]swarm.Secret
	configs  map[string]swarm.Config
}

func composeSwarmList(ctx context.Context, pd *providerData, ns string) (composeSwarmByName, error) {
	ex, err := stackListExisting(ctx, pd.Client, ns)
	out := composeSwarmByName{services: map[string]swarm.Service{}, networks: map[string]network.Summary{}, secrets: map[string]swarm.Secret{}, configs: map[string]swarm.Config{}}
	if err != nil {
		return out, err
	}
	for _, s := range ex.services {
		out.services[s.Spec.Name] = s
	}
	for _, n := range ex.networks {
		out.networks[n.Name] = n
	}
	for _, s := range ex.secrets {
		out.secrets[s.Spec.Name] = s
	}
	for _, s := range ex.configs {
		out.configs[s.Spec.Name] = s
	}
	return out, nil
}

func (e composeSwarmByName) empty() bool {
	return len(e.services)+len(e.networks)+len(e.secrets)+len(e.configs) == 0
}

// composeSwarmServiceDrift says why a live service differs, or "".
func composeSwarmServiceDrift(want composeSwarmService, got swarm.Service) string {
	if got.Spec.Labels[composeLabelConfigHash] != want.Hash {
		return "differs from the configuration"
	}
	if cs := got.Spec.TaskTemplate.ContainerSpec; cs == nil || !svcImageMatches(want.Spec.TaskTemplate.ContainerSpec.Image, cs.Image) {
		return "runs a different image"
	}
	if svcModeOf(got.Spec) != want.Mode {
		return "has a different mode"
	}
	if r := want.Spec.Mode.Replicated; r != nil && got.Spec.Mode.Replicated != nil && got.Spec.Mode.Replicated.Replicas != nil &&
		*got.Spec.Mode.Replicated.Replicas != *r.Replicas {
		return fmt.Sprintf("has %d replicas instead of %d", *got.Spec.Mode.Replicated.Replicas, *r.Replicas)
	}
	return ""
}

func composeSwarmDrift(plan *composeSwarmPlan, ex composeSwarmByName) []string {
	var out []string
	want := map[string]bool{}
	for _, n := range plan.Networks {
		if n.External {
			continue
		}
		want[n.Name] = true
		got, ok := ex.networks[n.Name]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("network %s is missing", n.Name))
		case got.Labels[composeLabelConfigHash] != n.Hash:
			out = append(out, fmt.Sprintf("network %s differs from the configuration", n.Name))
		}
	}
	for _, name := range composeSortedKeys(ex.networks) {
		if !want[name] {
			out = append(out, fmt.Sprintf("network %s is not in the configuration", name))
		}
	}
	blobDrift := func(kind string, desired []composeSwarmBlob, labels map[string]map[string]string) {
		want := map[string]bool{}
		for _, b := range desired {
			if b.External {
				continue
			}
			want[b.Name] = true
			l, ok := labels[b.Name]
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("%s %s is missing", kind, b.Name))
			case l[composeLabelConfigHash] != b.Hash:
				out = append(out, fmt.Sprintf("%s %s differs from the configuration", kind, b.Name))
			}
		}
		for _, name := range composeSortedKeys(labels) {
			if !want[name] {
				out = append(out, fmt.Sprintf("%s %s is not in the configuration", kind, name))
			}
		}
	}
	sl := map[string]map[string]string{}
	for n, s := range ex.secrets {
		sl[n] = s.Spec.Labels
	}
	blobDrift("secret", plan.Secrets, sl)
	cl := map[string]map[string]string{}
	for n, s := range ex.configs {
		cl[n] = s.Spec.Labels
	}
	blobDrift("config", plan.Configs, cl)
	want = map[string]bool{}
	for _, s := range plan.Services {
		want[s.Name] = true
		got, ok := ex.services[s.Name]
		if !ok {
			out = append(out, fmt.Sprintf("service %s is missing", s.Name))
			continue
		}
		if d := composeSwarmServiceDrift(s, got); d != "" {
			out = append(out, fmt.Sprintf("service %s %s", s.Name, d))
		}
	}
	for _, name := range composeSortedKeys(ex.services) {
		if !want[name] && !plan.KeepOrphans {
			out = append(out, fmt.Sprintf("service %s is not in the configuration", name))
		}
	}
	return out
}

// composeSwarmResolve fills the IDs of the networks, secrets and configs a
// spec names.
func composeSwarmResolve(spec *swarm.ServiceSpec, lk *svcLookup) error {
	spec.TaskTemplate.Networks = append([]swarm.NetworkAttachmentConfig(nil), spec.TaskTemplate.Networks...)
	for i := range spec.TaskTemplate.Networks {
		n := &spec.TaskTemplate.Networks[i]
		id, err := lk.resolve("network", n.Target)
		if err != nil {
			return err
		}
		n.Target = id
	}
	cs := spec.TaskTemplate.ContainerSpec
	secs := make([]*swarm.SecretReference, 0, len(cs.Secrets))
	for _, s := range cs.Secrets {
		c := *s
		id, err := lk.resolve("secret", s.SecretName)
		if err != nil {
			return err
		}
		c.SecretID = id
		secs = append(secs, &c)
	}
	cfgs := make([]*swarm.ConfigReference, 0, len(cs.Configs))
	for _, s := range cs.Configs {
		c := *s
		id, err := lk.resolve("config", s.ConfigName)
		if err != nil {
			return err
		}
		c.ConfigID = id
		cfgs = append(cfgs, &c)
	}
	cs2 := *cs
	cs2.Secrets, cs2.Configs = secs, cfgs
	if len(secs) == 0 {
		cs2.Secrets = nil
	}
	if len(cfgs) == 0 {
		cs2.Configs = nil
	}
	spec.TaskTemplate.ContainerSpec = &cs2
	return nil
}

// composeSwarmApply reconciles the stack: replaced or orphan objects are
// removed (after the services using them), missing ones are created, and
// changed services are updated in place or recreated when their mode type
// changes or an object they use was replaced.
func composeSwarmApply(ctx context.Context, pd *providerData, plan *composeSwarmPlan, wait bool, timeout time.Duration, warn func(string, string)) error {
	c := pd.Client
	ex, err := composeSwarmList(ctx, pd, plan.Namespace)
	if err != nil {
		return err
	}
	exists := func(m map[string]map[string]string) map[string]bool {
		out := map[string]bool{}
		for k := range m {
			out[k] = true
		}
		return out
	}
	netLabels, secLabels, cfgLabels := map[string]map[string]string{}, map[string]map[string]string{}, map[string]map[string]string{}
	for n, o := range ex.networks {
		netLabels[n] = o.Labels
	}
	for n, o := range ex.secrets {
		secLabels[n] = o.Spec.Labels
	}
	for n, o := range ex.configs {
		cfgLabels[n] = o.Spec.Labels
	}
	prior := func(m map[string]map[string]string) map[string]string {
		out := map[string]string{}
		for k, l := range m {
			out[k] = l[composeLabelConfigHash]
		}
		return out
	}
	wantNet, wantSec, wantCfg := map[string]string{}, map[string]string{}, map[string]string{}
	for _, n := range plan.Networks {
		if !n.External {
			wantNet[n.Name] = n.Hash
		}
	}
	for _, b := range plan.Secrets {
		if !b.External {
			wantSec[b.Name] = b.Hash
		}
	}
	for _, b := range plan.Configs {
		if !b.External {
			wantCfg[b.Name] = b.Hash
		}
	}
	netPlan := stackPlanObjects(wantNet, prior(netLabels), exists(netLabels))
	secPlan := stackPlanObjects(wantSec, prior(secLabels), exists(secLabels))
	cfgPlan := stackPlanObjects(wantCfg, prior(cfgLabels), exists(cfgLabels))

	replaced := map[string]bool{}
	for _, n := range netPlan.Replace {
		replaced[ex.networks[n].ID] = true
	}
	for _, n := range secPlan.Replace {
		replaced[ex.secrets[n].ID] = true
	}
	for _, n := range cfgPlan.Replace {
		replaced[ex.configs[n].ID] = true
	}
	desiredMode := map[string]string{}
	for _, s := range plan.Services {
		desiredMode[s.Name] = s.Mode
	}
	exSpecs := map[string]swarm.ServiceSpec{}
	for k, s := range ex.services {
		exSpecs[k] = s.Spec
	}
	svcPlan := stackPlanServices(desiredMode, exSpecs, replaced)

	var rmIDs []string
	for _, n := range svcPlan.Remove {
		if _, wanted := desiredMode[n]; !wanted && plan.KeepOrphans {
			continue // remove_orphans = false
		}
		rmIDs = append(rmIDs, ex.services[n].ID)
	}
	if err := stackRemoveServices(ctx, c, rmIDs); err != nil {
		return fmt.Errorf("removing services: %w", err)
	}
	for _, n := range append(append([]string{}, cfgPlan.Replace...), cfgPlan.Remove...) {
		id := ex.configs[n].ID
		if err := stackRemoveRetry(ctx, "config "+n, func(ctx context.Context) error {
			_, err := c.ConfigRemove(ctx, id, client.ConfigRemoveOptions{})
			return err
		}); err != nil {
			return err
		}
	}
	for _, n := range append(append([]string{}, secPlan.Replace...), secPlan.Remove...) {
		id := ex.secrets[n].ID
		if err := stackRemoveRetry(ctx, "secret "+n, func(ctx context.Context) error {
			_, err := c.SecretRemove(ctx, id, client.SecretRemoveOptions{})
			return err
		}); err != nil {
			return err
		}
	}
	for _, n := range append(append([]string{}, netPlan.Replace...), netPlan.Remove...) {
		id := ex.networks[n].ID
		if err := stackRemoveRetry(ctx, "network "+n, func(ctx context.Context) error {
			_, err := c.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
			return err
		}); err != nil {
			return err
		}
	}

	toCreate := func(p stackObjPlan) map[string]bool {
		m := map[string]bool{}
		for _, n := range append(append([]string{}, p.Create...), p.Replace...) {
			m[n] = true
		}
		return m
	}
	createN, createS, createC := toCreate(netPlan), toCreate(secPlan), toCreate(cfgPlan)
	for _, n := range plan.Networks {
		if !createN[n.Name] {
			continue
		}
		if _, err := c.NetworkCreate(ctx, n.Name, n.Create); err != nil {
			return fmt.Errorf("creating network %s: %w", n.Name, err)
		}
	}
	for _, b := range plan.Secrets {
		if !createS[b.Name] {
			continue
		}
		if _, err := c.SecretCreate(ctx, client.SecretCreateOptions{Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: b.Name, Labels: b.Labels}, Data: b.Data}}); err != nil {
			return fmt.Errorf("creating secret %s: %w", b.Name, err)
		}
	}
	for _, b := range plan.Configs {
		if !createC[b.Name] {
			continue
		}
		if _, err := c.ConfigCreate(ctx, client.ConfigCreateOptions{Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: b.Name, Labels: b.Labels}, Data: b.Data}}); err != nil {
			return fmt.Errorf("creating config %s: %w", b.Name, err)
		}
	}

	lk, err := svcLoadLookup(ctx, c, "")
	if err != nil {
		return err
	}
	create := map[string]bool{}
	for _, n := range svcPlan.Create {
		create[n] = true
	}
	var touched []string
	for _, s := range plan.Services {
		spec := s.Spec
		if err := composeSwarmResolve(&spec, lk); err != nil {
			return fmt.Errorf("service %s: %w", s.Name, err)
		}
		got, exists := ex.services[s.Name]
		if exists && !create[s.Name] {
			if composeSwarmServiceDrift(s, got) == "" {
				continue
			}
			if err := svcUpdate(ctx, pd, got.ID, spec); err != nil {
				return fmt.Errorf("updating service %s: %w", s.Name, err)
			}
			touched = append(touched, got.ID)
			continue
		}
		auth, err := pd.registryAuthFor(spec.TaskTemplate.ContainerSpec.Image)
		if err != nil {
			return fmt.Errorf("service %s: %w", s.Name, err)
		}
		resp, err := c.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: spec, EncodedRegistryAuth: auth, QueryRegistry: true})
		if err != nil {
			return fmt.Errorf("creating service %s: %w", s.Name, err)
		}
		for _, w := range resp.Warnings {
			warn("Service create warning", w)
		}
		touched = append(touched, resp.ID)
	}
	if wait {
		cfg := &svcConvergeModel{Timeout: types.StringValue(timeout.String()), Delay: types.StringValue("2s")}
		for _, id := range touched {
			if err := svcWaitConverged(ctx, c, id, cfg); err != nil {
				return err
			}
		}
	}
	return nil
}

// composeSwarmDestroy removes the stack (docker_stack's order) and, unless
// keepVolumes, the namespace's volumes on the manager's own daemon.
func composeSwarmDestroy(ctx context.Context, pd *providerData, ns string, keepVolumes bool, warn func(string, string)) error {
	d := stackDestroy(ctx, pd.Client, ns)
	if d.HasError() {
		var msgs []string
		for _, e := range d.Errors() {
			msgs = append(msgs, e.Summary()+": "+e.Detail())
		}
		return fmt.Errorf("%s", strings.Join(msgs, "; "))
	}
	if keepVolumes {
		return nil
	}
	vsRes, err := pd.Client.VolumeList(ctx, client.VolumeListOptions{Filters: labelFilter(map[string]string{labelStackNamespace: ns})})
	if err != nil {
		warn("Cannot list stack volumes", err.Error())
		return nil
	}
	vs := vsRes.Items
	for _, v := range vs {
		if _, err := pd.Client.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: false}); err != nil && !isNotFound(err) {
			warn("Cannot remove stack volume", fmt.Sprintf("%s: %v (volumes on other nodes are not removed)", v.Name, err))
		}
	}
	return nil
}

// composeSwarmIDs reports the computed maps; plan (may be nil) maps full
// names back to compose keys.
func composeSwarmIDs(plan *composeSwarmPlan, ns string, ex composeSwarmByName) (map[string][]string, map[string]string, map[string]string) {
	svcKey, netKey := map[string]string{}, map[string]string{}
	vols := map[string]string{}
	if plan != nil {
		for _, s := range plan.Services {
			svcKey[s.Name] = s.Key
		}
		for _, n := range plan.Networks {
			netKey[n.Name] = n.Key
		}
	}
	key := func(m map[string]string, name string) string {
		if k, ok := m[name]; ok {
			return k
		}
		return stackShortName(ns, name)
	}
	svcs := map[string][]string{}
	for name, s := range ex.services {
		svcs[key(svcKey, name)] = []string{s.ID}
		if cs := s.Spec.TaskTemplate.ContainerSpec; cs != nil {
			for _, m := range cs.Mounts {
				if m.Type == mount.TypeVolume && m.Source != "" {
					vols[stackShortName(ns, m.Source)] = m.Source
				}
			}
		}
	}
	nets := map[string]string{}
	for name, n := range ex.networks {
		nets[key(netKey, name)] = n.ID
	}
	return svcs, nets, vols
}
