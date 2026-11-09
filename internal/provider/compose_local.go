package provider

// docker_compose, mode = "local": a compose project expanded into
// containers, networks and volumes on one daemon, named and labelled the way
// `docker compose` does (<project>-<service>-<n>, <project>_<network>,
// com.docker.compose.* labels). This file holds the pure expansion; the
// daemon side is in compose_local_apply.go.

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Labels docker compose sets; docker_compose sets the same ones so
// `docker compose -p <project> ps` lists what it created.
const (
	composeLabelProject    = "com.docker.compose.project"
	composeLabelService    = "com.docker.compose.service"
	composeLabelNumber     = "com.docker.compose.container-number"
	composeLabelOneoff     = "com.docker.compose.oneoff"
	composeLabelNetwork    = "com.docker.compose.network"
	composeLabelVolume     = "com.docker.compose.volume"
	composeLabelConfigHash = "com.docker.compose.config-hash"
	composeLabelWorkingDir = "com.docker.compose.project.working_dir"
	// composeLabelSpecHash holds the provider's own digest of the create
	// options, which drives recreation; com.docker.compose.config-hash holds
	// the value `docker compose` computes (compose_seam.go) so the CLI and the
	// provider agree on what is up to date. Objects created before Pass 5
	// carry the provider digest in config-hash and no spec-hash label;
	// composeHashMatches accepts that form so they are not recreated.
	composeLabelSpecHash = "io.terraform.provider.compose-spec-hash"
	// composeLabelConfigFiles lists the compose files (comma-separated);
	// docker compose ls reads it. Set after the spec hash is computed so
	// containers created before Pass 5 keep their hash.
	composeLabelConfigFiles = "com.docker.compose.project.config_files"
)

// composeHashMatches reports whether an object's labels record want as the
// provider digest (spec-hash label, or the pre-Pass 5 config-hash form).
func composeHashMatches(labels map[string]string, want string) bool {
	if h, ok := labels[composeLabelSpecHash]; ok {
		return h == want
	}
	return labels[composeLabelConfigHash] == want
}

// composeEndpoint is one network a container joins.
type composeEndpoint struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	IPv4    string   `json:"ipv4,omitempty"`
	IPv6    string   `json:"ipv6,omitempty"`
}

// composeCopy is a file written into a container before it starts (secrets
// and configs whose source is content or an environment variable).
type composeCopy struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Mode int64  `json:"mode"`
}

type composeLocalNetwork struct {
	Key      string
	Name     string
	External bool
	Create   client.NetworkCreateOptions
	Hash     string
}

type composeLocalVolume struct {
	Key      string
	Name     string
	External bool
	Create   client.VolumeCreateOptions
	Hash     string
}

type composeLocalContainer struct {
	Service  string
	Number   int
	Name     string
	Config   *container.Config
	Host     *container.HostConfig
	Networks []composeEndpoint
	Copies   []composeCopy
	Platform string
	Pull     string
	// Build is the service's build section (nil: pull the image). The
	// image is built through the BuildKit seam when it is missing or
	// pull_policy is "build".
	Build *composeLocalBuild `json:"-"`
	// DependsOn are the enabled services this one depends on.
	DependsOn map[string]ctypes.ServiceDependency
	Hash      string
}

// composeLocalBuild is what a container needs to build its image.
type composeLocalBuild struct {
	Project *ctypes.Project
	Service ctypes.ServiceConfig
}

// composeLocalPlan is the whole desired project for one daemon.
type composeLocalPlan struct {
	Project    string
	Networks   []composeLocalNetwork
	Volumes    []composeLocalVolume
	Containers []composeLocalContainer // dependency order
	Warnings   []string
	// KeepOrphans is set when remove_orphans is false: containers of the
	// project whose service is no longer configured are left alone.
	KeepOrphans bool
}

func (p *composeLocalPlan) warnf(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// composeLocalExpand turns a loaded project into the objects to create.
func composeLocalExpand(p *ctypes.Project) (*composeLocalPlan, error) {
	plan := &composeLocalPlan{Project: p.Name}
	projLabels := map[string]string{composeLabelProject: p.Name, labelManagedBy: labelManagedByValue}

	for _, key := range composeSortedKeys(p.Networks) {
		n := p.Networks[key]
		ln := composeLocalNetwork{Key: key, Name: n.Name, External: bool(n.External)}
		if !ln.External {
			opts := client.NetworkCreateOptions{
				Driver:     n.Driver,
				Internal:   n.Internal,
				Attachable: n.Attachable,
				EnableIPv4: n.EnableIPv4,
				EnableIPv6: n.EnableIPv6,
				Options:    map[string]string(n.DriverOpts),
				Labels:     mergeLabels(n.Labels, projLabels, map[string]string{composeLabelNetwork: key}),
			}
			if n.Ipam.Driver != "" || len(n.Ipam.Config) > 0 || len(n.Ipam.Options) > 0 {
				ipam := &network.IPAM{Driver: n.Ipam.Driver, Options: map[string]string(n.Ipam.Options)}
				for _, c := range n.Ipam.Config {
					if c == nil {
						continue
					}
					pool, err := ipamConfigFrom(c.Subnet, c.IPRange, c.Gateway, map[string]string(c.AuxiliaryAddresses))
					if err != nil {
						return nil, fmt.Errorf("network %s ipam: %w", key, err)
					}
					ipam.Config = append(ipam.Config, pool)
				}
				opts.IPAM = ipam
			}
			ln.Hash = composeNetworkHash(opts)
			opts.Labels[composeLabelConfigHash] = composeNetworkConfigHash(n)
			opts.Labels[composeLabelSpecHash] = ln.Hash
			ln.Create = opts
		}
		plan.Networks = append(plan.Networks, ln)
	}

	for _, key := range composeSortedKeys(p.Volumes) {
		v := p.Volumes[key]
		lv := composeLocalVolume{Key: key, Name: v.Name, External: bool(v.External)}
		if !lv.External {
			opts := client.VolumeCreateOptions{
				Name:       v.Name,
				Driver:     v.Driver,
				DriverOpts: map[string]string(v.DriverOpts),
				Labels:     mergeLabels(v.Labels, projLabels, map[string]string{composeLabelVolume: key}),
			}
			lv.Hash = composeVolumeHash(opts)
			opts.Labels[composeLabelConfigHash] = composeVolumeConfigHash(v)
			opts.Labels[composeLabelSpecHash] = lv.Hash
			lv.Create = opts
		}
		plan.Volumes = append(plan.Volumes, lv)
	}

	order, err := composeServiceOrder(p)
	if err != nil {
		return nil, err
	}
	for _, name := range order {
		svc := p.Services[name]
		cs, err := composeLocalService(p, plan, svc)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		plan.Containers = append(plan.Containers, cs...)
	}
	names := map[string]string{}
	for _, c := range plan.Containers {
		if other, dup := names[c.Name]; dup {
			return nil, fmt.Errorf("services %s and %s both use container name %q", other, c.Service, c.Name)
		}
		names[c.Name] = c.Service
	}
	return plan, nil
}

// composeContainerName is <project>-<service>-<n> unless container_name is set.
func composeContainerName(project string, svc ctypes.ServiceConfig, n int) string {
	if svc.ContainerName != "" {
		return svc.ContainerName
	}
	return fmt.Sprintf("%s-%s-%d", project, svc.Name, n)
}

// composeServiceRef rewrites "service:<name>" to "container:<first container>".
func composeServiceRef(p *ctypes.Project, ref string) (string, error) {
	s, ok := strings.CutPrefix(ref, ctypes.ServicePrefix)
	if !ok {
		return ref, nil
	}
	dep, found := p.Services[s]
	if !found {
		return "", fmt.Errorf("%q refers to a service that is not enabled", ref)
	}
	return ctypes.ContainerPrefix + composeContainerName(p.Name, dep, 1), nil
}

func composeLocalUnsupported(plan *composeLocalPlan, svc ctypes.ServiceConfig) {
	w := func(attr string) {
		plan.warnf("service %s: %s is not supported by docker_compose and is ignored", svc.Name, attr)
	}
	if svc.Develop != nil {
		w("develop")
	}
	if len(svc.PostStart) > 0 || len(svc.PreStop) > 0 || len(svc.PreStart) > 0 {
		w("lifecycle hooks (pre_start/post_start/pre_stop)")
	}
	if len(svc.Models) > 0 {
		w("models")
	}
	if svc.Provider != nil {
		w("provider")
	}
	if svc.CredentialSpec != nil {
		w("credential_spec")
	}
	if svc.BlkioConfig != nil {
		w("blkio_config")
	}
	if svc.UseAPISocket {
		w("use_api_socket")
	}
	if len(svc.ExternalLinks) > 0 {
		w("external_links")
	}
	if svc.Attach != nil {
		w("attach")
	}
}

func composeLocalService(p *ctypes.Project, plan *composeLocalPlan, svc ctypes.ServiceConfig) ([]composeLocalContainer, error) {
	if svc.Image == "" && svc.Build == nil {
		return nil, fmt.Errorf("no image and no build section: set image or build")
	}
	if svc.PullPolicy == ctypes.PullPolicyBuild && svc.Build == nil {
		return nil, fmt.Errorf("pull_policy \"build\" needs a build section")
	}
	image := composeImageName(p.Name, svc)
	composeLocalUnsupported(plan, svc)
	scale := svc.GetScale()
	if svc.Deploy != nil && (svc.Deploy.Mode == "global" || svc.Deploy.Mode == "global-job") {
		scale = 1
	}
	if svc.ContainerName != "" && scale > 1 {
		return nil, fmt.Errorf("container_name %q cannot be used with %d replicas", svc.ContainerName, scale)
	}

	cfg := &container.Config{
		Image:        image,
		Hostname:     svc.Hostname,
		Domainname:   svc.DomainName,
		User:         svc.User,
		WorkingDir:   svc.WorkingDir,
		Tty:          svc.Tty,
		OpenStdin:    svc.StdinOpen,
		StopSignal:   svc.StopSignal,
		Env:          composeEnvList(svc.Environment),
		ExposedPorts: network.PortSet{},
	}
	if svc.Command != nil {
		cfg.Cmd = []string(svc.Command)
	}
	if svc.Entrypoint != nil {
		cfg.Entrypoint = []string(svc.Entrypoint)
	}
	if svc.StopGracePeriod != nil {
		secs := int(time.Duration(*svc.StopGracePeriod).Seconds())
		cfg.StopTimeout = &secs
	}
	if hc := composeHealthcheck(svc.HealthCheck); hc != nil {
		cfg.Healthcheck = hc
	}

	dns, err := parseAddrs("dns", []string(svc.DNS))
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", svc.Name, err)
	}
	hc := &container.HostConfig{
		Privileged:     svc.Privileged,
		ReadonlyRootfs: svc.ReadOnly,
		CapAdd:         svc.CapAdd,
		CapDrop:        svc.CapDrop,
		DNS:            dns,
		DNSSearch:      []string(svc.DNSSearch),
		DNSOptions:     svc.DNSOpts,
		GroupAdd:       svc.GroupAdd,
		SecurityOpt:    svc.SecurityOpt,
		ShmSize:        int64(svc.ShmSize),
		Sysctls:        map[string]string(svc.Sysctls),
		Runtime:        svc.Runtime,
		OomScoreAdj:    int(svc.OomScoreAdj),
		CgroupParent:   svc.CgroupParent,
		CgroupnsMode:   container.CgroupnsMode(svc.Cgroup),
		Isolation:      container.Isolation(svc.Isolation),
		UsernsMode:     container.UsernsMode(svc.UserNSMode),
		UTSMode:        container.UTSMode(svc.Uts),
		StorageOpt:     svc.StorageOpt,
		Annotations:    map[string]string(svc.Annotations),
		Init:           svc.Init,
		PortBindings:   network.PortMap{},
	}
	if len(svc.ExtraHosts) > 0 {
		hc.ExtraHosts = svc.ExtraHosts.AsList(":")
		sort.Strings(hc.ExtraHosts)
	}
	for _, ref := range []struct {
		raw string
		set func(string)
	}{
		{svc.Ipc, func(s string) { hc.IpcMode = container.IpcMode(s) }},
		{svc.Pid, func(s string) { hc.PidMode = container.PidMode(s) }},
	} {
		v, err := composeServiceRef(p, ref.raw)
		if err != nil {
			return nil, err
		}
		ref.set(v)
	}
	if svc.Logging != nil {
		hc.LogConfig = container.LogConfig{Type: svc.Logging.Driver, Config: map[string]string(svc.Logging.Options)}
	} else if svc.LogDriver != "" || len(svc.LogOpt) > 0 {
		hc.LogConfig = container.LogConfig{Type: svc.LogDriver, Config: svc.LogOpt}
	}
	if err := composeRestart(svc, hc); err != nil {
		return nil, err
	}
	composeResources(svc, hc)
	for _, d := range svc.Devices {
		perms := d.Permissions
		if perms == "" {
			perms = containerDefaultDevicePerms
		}
		target := d.Target
		if target == "" {
			target = d.Source
		}
		hc.Devices = append(hc.Devices, container.DeviceMapping{PathOnHost: d.Source, PathInContainer: target, CgroupPermissions: perms})
	}
	hc.DeviceCgroupRules = svc.DeviceCgroupRules
	for _, g := range svc.Gpus {
		hc.DeviceRequests = append(hc.DeviceRequests, composeDeviceRequest(g))
	}
	if svc.Deploy != nil && svc.Deploy.Resources.Reservations != nil {
		for _, g := range svc.Deploy.Resources.Reservations.Devices {
			hc.DeviceRequests = append(hc.DeviceRequests, composeDeviceRequest(g))
		}
	}
	for _, name := range composeSortedKeys(svc.Ulimits) {
		u := svc.Ulimits[name]
		soft, hard := int64(u.Soft), int64(u.Hard)
		if u.Single != 0 {
			soft, hard = int64(u.Single), int64(u.Single)
		}
		hc.Ulimits = append(hc.Ulimits, &container.Ulimit{Name: name, Soft: soft, Hard: hard})
	}
	for _, t := range svc.Tmpfs {
		target, opts, _ := strings.Cut(t, ":")
		if hc.Tmpfs == nil {
			hc.Tmpfs = map[string]string{}
		}
		hc.Tmpfs[target] = opts
	}
	for _, vf := range svc.VolumesFrom {
		src, mode, _ := strings.Cut(vf, ":")
		if c, ok := strings.CutPrefix(src, ctypes.ContainerPrefix); ok {
			src = c
			// "container:<name>[:mode]"
			if name, m, ok := strings.Cut(c, ":"); ok {
				src, mode = name, m
			}
		} else {
			dep, ok := p.Services[src]
			if !ok {
				return nil, fmt.Errorf("volumes_from %q: service not enabled", vf)
			}
			src = composeContainerName(p.Name, dep, 1)
		}
		if mode != "" {
			src += ":" + mode
		}
		hc.VolumesFrom = append(hc.VolumesFrom, src)
	}

	// Ports
	for _, e := range svc.Expose {
		ports, err := exposedPorts(e)
		if err != nil {
			return nil, fmt.Errorf("expose %q: %w", e, err)
		}
		for _, port := range ports {
			cfg.ExposedPorts[port] = struct{}{}
		}
	}
	for _, pc := range svc.Ports {
		port, ok := network.PortFrom(uint16(pc.Target), network.IPProtocol(composeProto(pc.Protocol)))
		if !ok || pc.Target > 65535 {
			return nil, fmt.Errorf("port %d: not a valid %s port", pc.Target, composeProto(pc.Protocol))
		}
		hostIP, err := parseOptAddr("host_ip", pc.HostIP)
		if err != nil {
			return nil, fmt.Errorf("port %d: %w", pc.Target, err)
		}
		cfg.ExposedPorts[port] = struct{}{}
		hc.PortBindings[port] = append(hc.PortBindings[port], network.PortBinding{HostIP: hostIP, HostPort: pc.Published})
	}

	// Mounts
	for _, v := range svc.Volumes {
		if err := composeLocalMount(p, svc, v, hc); err != nil {
			return nil, err
		}
	}
	var copies []composeCopy
	for _, ref := range svc.Secrets {
		obj, ok := p.Secrets[ref.Source]
		if !ok {
			return nil, fmt.Errorf("secret %q is not defined", ref.Source)
		}
		target := ref.Target
		if target == "" {
			target = "/run/secrets/" + ref.Source
		} else if !path.IsAbs(target) {
			target = "/run/secrets/" + target
		}
		c, err := composeLocalFileRef(p, "secret", ref.Source, ctypes.FileObjectConfig(obj), ctypes.FileReferenceConfig(ref), target, 0o444, hc)
		if err != nil {
			return nil, err
		}
		copies = append(copies, c...)
	}
	for _, ref := range svc.Configs {
		obj, ok := p.Configs[ref.Source]
		if !ok {
			return nil, fmt.Errorf("config %q is not defined", ref.Source)
		}
		target := ref.Target
		if target == "" {
			target = "/" + ref.Source
		}
		c, err := composeLocalFileRef(p, "config", ref.Source, ctypes.FileObjectConfig(obj), ctypes.FileReferenceConfig(ref), target, 0o444, hc)
		if err != nil {
			return nil, err
		}
		copies = append(copies, c...)
	}

	// Networks
	var eps []composeEndpoint
	if svc.NetworkMode != "" {
		nm, err := composeServiceRef(p, svc.NetworkMode)
		if err != nil {
			return nil, err
		}
		hc.NetworkMode = container.NetworkMode(nm)
	} else {
		for _, key := range svc.NetworksByPriority() {
			n, ok := p.Networks[key]
			if !ok {
				return nil, fmt.Errorf("network %q is not defined", key)
			}
			ep := composeEndpoint{Key: key, Name: n.Name, Aliases: []string{svc.Name}}
			if nc := svc.Networks[key]; nc != nil {
				ep.Aliases = append(ep.Aliases, nc.Aliases...)
				ep.IPv4, ep.IPv6 = nc.Ipv4Address, nc.Ipv6Address
			}
			eps = append(eps, ep)
		}
		if len(eps) > 0 {
			hc.NetworkMode = container.NetworkMode(eps[0].Name)
		}
	}

	deps := map[string]ctypes.ServiceDependency{}
	for d, dep := range svc.DependsOn {
		if _, ok := p.Services[d]; ok {
			deps[d] = dep
		}
	}

	cliHash, err := composeServiceConfigHash(svc)
	if err != nil {
		return nil, fmt.Errorf("config hash: %w", err)
	}
	var out []composeLocalContainer
	for n := 1; n <= scale; n++ {
		c := *cfg
		h := *hc
		c.Labels = mergeLabels(svc.Labels, svc.CustomLabels, map[string]string{
			composeLabelProject:    p.Name,
			composeLabelService:    svc.Name,
			composeLabelNumber:     strconv.Itoa(n),
			composeLabelOneoff:     "False",
			composeLabelWorkingDir: p.WorkingDir,
			labelManagedBy:         labelManagedByValue,
		})
		lc := composeLocalContainer{
			Service: svc.Name, Number: n, Name: composeContainerName(p.Name, svc, n),
			Config: &c, Host: &h, Networks: eps, Copies: copies, Platform: svc.Platform,
			Pull: svc.PullPolicy, DependsOn: deps,
		}
		if svc.Build != nil {
			lc.Build = &composeLocalBuild{Project: p, Service: svc}
		}
		lc.Hash = composeHash(struct {
			Config   composeHashConfig
			Host     *container.HostConfig
			Networks []composeEndpoint
			Copies   []composeCopy
			Platform string
		}{composeHashConfig{lc.Config}, lc.Host, lc.Networks, lc.Copies, lc.Platform})
		c.Labels[composeLabelConfigHash] = cliHash
		if len(p.ComposeFiles) > 0 {
			c.Labels[composeLabelConfigFiles] = strings.Join(p.ComposeFiles, ",")
		}
		c.Labels[composeLabelSpecHash] = lc.Hash
		out = append(out, lc)
	}
	return out, nil
}

func composeProto(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}

// composeEnvList renders environment as sorted KEY=VALUE; unresolved
// variables (nil values) are dropped like `docker compose` does.
func composeEnvList(env ctypes.MappingWithEquals) []string {
	var out []string
	for _, k := range composeSortedKeys(env) {
		if v := env[k]; v != nil {
			out = append(out, k+"="+*v)
		}
	}
	return out
}

func composeHealthcheck(h *ctypes.HealthCheckConfig) *container.HealthConfig {
	if h == nil {
		return nil
	}
	if h.Disable {
		return &container.HealthConfig{Test: []string{"NONE"}}
	}
	out := &container.HealthConfig{Test: []string(h.Test)}
	d := func(v *ctypes.Duration) time.Duration {
		if v == nil {
			return 0
		}
		return time.Duration(*v)
	}
	out.Interval, out.Timeout, out.StartPeriod, out.StartInterval = d(h.Interval), d(h.Timeout), d(h.StartPeriod), d(h.StartInterval)
	if h.Retries != nil {
		out.Retries = int(*h.Retries)
	}
	return out
}

// composeRestart maps restart (or deploy.restart_policy) to a restart policy.
func composeRestart(svc ctypes.ServiceConfig, hc *container.HostConfig) error {
	r := svc.Restart
	if r == "" && svc.Deploy != nil && svc.Deploy.RestartPolicy != nil {
		rp := svc.Deploy.RestartPolicy
		switch rp.Condition {
		case "none":
			r = "no"
		case "on-failure":
			r = "on-failure"
			if rp.MaxAttempts != nil {
				r += ":" + strconv.FormatUint(*rp.MaxAttempts, 10)
			}
		case "", "any":
			r = "always"
		default:
			return fmt.Errorf("deploy.restart_policy.condition %q is not valid", rp.Condition)
		}
	}
	if r == "" {
		return nil
	}
	name, retries, _ := strings.Cut(r, ":")
	switch container.RestartPolicyMode(name) {
	case container.RestartPolicyDisabled, container.RestartPolicyAlways, container.RestartPolicyUnlessStopped, container.RestartPolicyOnFailure:
	default:
		return fmt.Errorf("restart %q is not valid", r)
	}
	hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(name)}
	if retries != "" {
		n, err := strconv.Atoi(retries)
		if err != nil {
			return fmt.Errorf("restart %q: %w", r, err)
		}
		hc.RestartPolicy.MaximumRetryCount = n
	}
	return nil
}

// composeResources applies the service-level limits, with deploy.resources
// filling what is unset (the same precedence docker compose uses).
func composeResources(svc ctypes.ServiceConfig, hc *container.HostConfig) {
	r := &hc.Resources
	r.Memory = int64(svc.MemLimit)
	r.MemoryReservation = int64(svc.MemReservation)
	r.MemorySwap = int64(svc.MemSwapLimit)
	if svc.MemSwappiness != 0 {
		s := int64(svc.MemSwappiness)
		r.MemorySwappiness = &s
	}
	r.NanoCPUs = int64(float64(svc.CPUS) * 1e9)
	r.CPUShares = svc.CPUShares
	r.CPUQuota = svc.CPUQuota
	r.CPUPeriod = svc.CPUPeriod
	r.CPURealtimePeriod = svc.CPURTPeriod
	r.CPURealtimeRuntime = svc.CPURTRuntime
	r.CpusetCpus = svc.CPUSet
	r.CPUCount = svc.CPUCount
	r.CPUPercent = int64(svc.CPUPercent)
	if svc.PidsLimit != 0 {
		pl := svc.PidsLimit
		r.PidsLimit = &pl
	}
	if svc.OomKillDisable {
		t := true
		r.OomKillDisable = &t
	}
	if svc.Deploy == nil {
		return
	}
	if l := svc.Deploy.Resources.Limits; l != nil {
		if r.Memory == 0 {
			r.Memory = int64(l.MemoryBytes)
		}
		if r.NanoCPUs == 0 {
			r.NanoCPUs = int64(float64(l.NanoCPUs) * 1e9)
		}
		if r.PidsLimit == nil && l.Pids != 0 {
			pl := l.Pids
			r.PidsLimit = &pl
		}
	}
	if res := svc.Deploy.Resources.Reservations; res != nil && r.MemoryReservation == 0 {
		r.MemoryReservation = int64(res.MemoryBytes)
	}
}

func composeDeviceRequest(g ctypes.DeviceRequest) container.DeviceRequest {
	caps := g.Capabilities
	if len(caps) == 0 {
		caps = []string{"gpu"}
	}
	dr := container.DeviceRequest{Driver: g.Driver, Count: int(g.Count), DeviceIDs: g.IDs, Capabilities: [][]string{caps}, Options: map[string]string(g.Options)}
	if dr.Count == 0 && len(dr.DeviceIDs) == 0 {
		dr.Count = -1
	}
	return dr
}

func composeLocalMount(p *ctypes.Project, svc ctypes.ServiceConfig, v ctypes.ServiceVolumeConfig, hc *container.HostConfig) error {
	switch v.Type {
	case ctypes.VolumeTypeBind:
		if v.Bind == nil || bool(v.Bind.CreateHostPath) {
			// Legacy bind syntax creates a missing host path like -v does.
			b := v.Source + ":" + v.Target
			var opts []string
			if v.ReadOnly {
				opts = append(opts, "ro")
			}
			if v.Bind != nil {
				if v.Bind.SELinux != "" {
					opts = append(opts, v.Bind.SELinux)
				}
				if v.Bind.Propagation != "" {
					opts = append(opts, v.Bind.Propagation)
				}
			}
			if v.Consistency != "" {
				opts = append(opts, v.Consistency)
			}
			if len(opts) > 0 {
				b += ":" + strings.Join(opts, ",")
			}
			hc.Binds = append(hc.Binds, b)
			return nil
		}
		m := mount.Mount{Type: mount.TypeBind, Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly, Consistency: mount.Consistency(v.Consistency)}
		if v.Bind.Propagation != "" || v.Bind.Recursive != "" {
			m.BindOptions = &mount.BindOptions{Propagation: mount.Propagation(v.Bind.Propagation)}
			switch v.Bind.Recursive {
			case "disabled":
				m.BindOptions.NonRecursive = true
			case "writable":
				m.BindOptions.ReadOnlyNonRecursive = true
			case "readonly":
				m.BindOptions.ReadOnlyForceRecursive = true
			}
		}
		hc.Mounts = append(hc.Mounts, m)
	case ctypes.VolumeTypeVolume, "":
		m := mount.Mount{Type: mount.TypeVolume, Target: v.Target, ReadOnly: v.ReadOnly}
		if v.Source != "" {
			vol, ok := p.Volumes[v.Source]
			if !ok {
				return fmt.Errorf("volume %q is not defined", v.Source)
			}
			m.Source = vol.Name
		} else if svc.VolumeDriver != "" {
			m.VolumeOptions = &mount.VolumeOptions{DriverConfig: &mount.Driver{Name: svc.VolumeDriver}}
		}
		if v.Volume != nil && (v.Volume.NoCopy || v.Volume.Subpath != "") {
			if m.VolumeOptions == nil {
				m.VolumeOptions = &mount.VolumeOptions{}
			}
			m.VolumeOptions.NoCopy = v.Volume.NoCopy
			m.VolumeOptions.Subpath = v.Volume.Subpath
		}
		hc.Mounts = append(hc.Mounts, m)
	case ctypes.VolumeTypeTmpfs:
		m := mount.Mount{Type: mount.TypeTmpfs, Target: v.Target, ReadOnly: v.ReadOnly}
		if v.Tmpfs != nil {
			m.TmpfsOptions = &mount.TmpfsOptions{SizeBytes: int64(v.Tmpfs.Size), Mode: os.FileMode(v.Tmpfs.Mode)}
		}
		hc.Mounts = append(hc.Mounts, m)
	case ctypes.VolumeTypeNamedPipe:
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeNamedPipe, Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly})
	case ctypes.VolumeTypeImage:
		m := mount.Mount{Type: mount.TypeImage, Source: v.Source, Target: v.Target, ReadOnly: v.ReadOnly}
		if v.Image != nil && v.Image.SubPath != "" {
			m.ImageOptions = &mount.ImageOptions{Subpath: v.Image.SubPath}
		}
		hc.Mounts = append(hc.Mounts, m)
	default:
		return fmt.Errorf("volume type %q is not supported", v.Type)
	}
	return nil
}

// composeLocalFileRef mounts a secret/config into a container: a file
// source becomes a read-only bind mount; content or environment sources are
// copied in before the container starts. External objects only exist in
// Swarm, so they are an error in local mode.
func composeLocalFileRef(p *ctypes.Project, kind, name string, obj ctypes.FileObjectConfig, ref ctypes.FileReferenceConfig, target string, defMode int64, hc *container.HostConfig) ([]composeCopy, error) {
	if obj.External {
		return nil, fmt.Errorf("%s %q is external: external secrets and configs exist only in Swarm; use mode = \"swarm\" or give it a file, content or environment source", kind, name)
	}
	if obj.File != "" {
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeBind, Source: obj.File, Target: target, ReadOnly: true})
		return nil, nil
	}
	data, _, err := composeBlobData(obj, p.Environment)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", kind, name, err)
	}
	c := composeCopy{Path: target, Data: data, Mode: defMode}
	if ref.Mode != nil {
		c.Mode = int64(*ref.Mode)
	}
	if ref.UID != "" {
		if c.UID, err = strconv.Atoi(ref.UID); err != nil {
			return nil, fmt.Errorf("%s %q uid %q: %w", kind, name, ref.UID, err)
		}
	}
	if ref.GID != "" {
		if c.GID, err = strconv.Atoi(ref.GID); err != nil {
			return nil, fmt.Errorf("%s %q gid %q: %w", kind, name, ref.GID, err)
		}
	}
	return []composeCopy{c}, nil
}
