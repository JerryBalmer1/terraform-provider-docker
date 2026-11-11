package provider

// docker_compose, mode = "local": reconciling the expanded project against
// one daemon, refreshing it and removing it.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"path"
	"sort"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// composeLocalExisting is what the daemon holds for a project.
type composeLocalExisting struct {
	containers map[string]container.Summary // by name (no leading /)
	networks   map[string]network.Summary   // by name
	volumes    map[string]*volume.Volume    // by name
}

func composeLocalList(ctx context.Context, pd *providerData, project string) (composeLocalExisting, error) {
	c := pd.Client
	f := labelFilter(map[string]string{composeLabelProject: project})
	ex := composeLocalExisting{containers: map[string]container.Summary{}, networks: map[string]network.Summary{}, volumes: map[string]*volume.Volume{}}
	csRes, err := c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return ex, fmt.Errorf("listing containers: %w", err)
	}
	cs := csRes.Items
	for _, s := range cs {
		if strings.EqualFold(s.Labels[composeLabelOneoff], "true") {
			continue // `docker compose run` containers are not part of the model
		}
		name := s.ID
		if len(s.Names) > 0 {
			name = strings.TrimPrefix(s.Names[0], "/")
		}
		ex.containers[name] = s
	}
	nsRes, err := c.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return ex, fmt.Errorf("listing networks: %w", err)
	}
	ns := nsRes.Items
	for _, n := range ns {
		ex.networks[n.Name] = n
	}
	vsRes, err := c.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if err != nil {
		return ex, fmt.Errorf("listing volumes: %w", err)
	}
	vs := vsRes.Items
	for _, v := range vs {
		ex.volumes[v.Name] = &v
	}
	return ex, nil
}

func (e composeLocalExisting) empty() bool {
	return len(e.containers)+len(e.networks)+len(e.volumes) == 0
}

// composeLocalStateDrift says why a kept container is not in the desired
// state, or "" when it is fine. Exited containers are drift when they failed
// or should have been restarted; a one-shot container (restart "no") that
// exited 0 is done, not drifted.
func composeLocalStateDrift(s container.Summary, restart container.RestartPolicyMode) string {
	switch s.State {
	case container.StateRunning, container.StateRestarting, container.StatePaused:
		return ""
	case container.StateExited:
		code := composeExitCode(s.Status)
		if code != 0 {
			return fmt.Sprintf("exited with code %d", code)
		}
		if restart == container.RestartPolicyAlways || restart == container.RestartPolicyUnlessStopped {
			return "is stopped"
		}
		return ""
	default:
		return "is " + string(s.State)
	}
}

// composeExitCode reads the code from a Summary status like "Exited (1) 2 minutes ago".
func composeExitCode(status string) int {
	_, rest, ok := strings.Cut(status, "(")
	if !ok {
		return 0
	}
	num, _, _ := strings.Cut(rest, ")")
	var n int
	_, _ = fmt.Sscanf(num, "%d", &n)
	return n
}

// composeLocalDrift lists differences between the daemon and the plan.
func composeLocalDrift(plan *composeLocalPlan, ex composeLocalExisting) []string {
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
		case !composeHashMatches(got.Labels, n.Hash):
			out = append(out, fmt.Sprintf("network %s differs from the configuration", n.Name))
		}
	}
	for _, name := range composeSortedKeys(ex.networks) {
		if !want[name] {
			out = append(out, fmt.Sprintf("network %s is not in the configuration", name))
		}
	}
	want = map[string]bool{}
	for _, v := range plan.Volumes {
		if v.External {
			continue
		}
		want[v.Name] = true
		if _, ok := ex.volumes[v.Name]; !ok {
			out = append(out, fmt.Sprintf("volume %s is missing", v.Name))
		}
	}
	for _, name := range composeSortedKeys(ex.volumes) {
		if !want[name] {
			out = append(out, fmt.Sprintf("volume %s is not in the configuration", name))
		}
	}
	want = map[string]bool{}
	for _, c := range plan.Containers {
		want[c.Name] = true
		got, ok := ex.containers[c.Name]
		if !ok {
			out = append(out, fmt.Sprintf("container %s is missing", c.Name))
			continue
		}
		if !composeHashMatches(got.Labels, c.Hash) {
			out = append(out, fmt.Sprintf("container %s differs from the configuration", c.Name))
			continue
		}
		if c.Refs != nil && !composeLocalRefsCurrent(c, got.Labels, ex, nil) {
			if _, err := c.Refs.resolve(composeLocalExistingID(ex)); err == nil {
				out = append(out, fmt.Sprintf("container %s was created for a container of %s that has been replaced", c.Name, strings.Join(c.Refs.names(), ", ")))
				continue
			}
		}
		if c.Refs == nil && !composeLocalConfigHashCurrent(c, got.Labels, ex, nil) {
			out = append(out, fmt.Sprintf("container %s differs from the configuration (com.docker.compose.config-hash)", c.Name))
			continue
		}
		if d := composeLocalStateDrift(got, c.Host.RestartPolicy.Name); d != "" {
			out = append(out, fmt.Sprintf("container %s %s", c.Name, d))
		}
	}
	for _, name := range composeSortedKeys(ex.containers) {
		if !want[name] && !plan.KeepOrphans {
			out = append(out, fmt.Sprintf("container %s is not in the configuration", name))
		}
	}
	return out
}

// composeLocalApplyOpts carries the resource's behaviour settings.
type composeLocalApplyOpts struct {
	Wait        bool
	WaitTimeout time.Duration
	KeepVolumes bool
}

// composeLocalApply creates missing objects, recreates changed ones and
// removes orphans. Warnings go to diags-like warn.
func composeLocalApply(ctx context.Context, pd *providerData, plan *composeLocalPlan, o composeLocalApplyOpts, warn func(string, string)) error {
	c := pd.Client
	ex, err := composeLocalList(ctx, pd, plan.Project)
	if err != nil {
		return err
	}

	// Networks: external ones must exist; changed ones are replaced.
	replacedNet := map[string]bool{}
	wantNet := map[string]bool{}
	for _, n := range plan.Networks {
		if n.External {
			if _, err := c.NetworkInspect(ctx, n.Name, client.NetworkInspectOptions{}); err != nil {
				return fmt.Errorf("external network %s: %w", n.Name, err)
			}
			continue
		}
		wantNet[n.Name] = true
		if got, ok := ex.networks[n.Name]; ok && !composeHashMatches(got.Labels, n.Hash) {
			replacedNet[n.Name] = true
		}
	}
	for _, v := range plan.Volumes {
		if !v.External {
			continue
		}
		if _, err := c.VolumeInspect(ctx, v.Name, client.VolumeInspectOptions{}); err != nil {
			return fmt.Errorf("external volume %s: %w", v.Name, err)
		}
	}

	// Containers to remove: orphans, changed ones, ones on a replaced
	// network, and dependents (restart: true) of recreated ones.
	desired := map[string]*composeLocalContainer{}
	for i := range plan.Containers {
		desired[plan.Containers[i].Name] = &plan.Containers[i]
	}
	remove := map[string]bool{}
	for name := range ex.containers {
		if desired[name] == nil && !plan.KeepOrphans {
			remove[name] = true
		}
	}
	recreatedSvc := map[string]bool{}
	replaced := map[string]bool{} // container names created or recreated
	refreshed, err := composeLocalPullRefresh(ctx, pd, plan, ex, time.Now())
	if err != nil {
		return err
	}
	for _, dc := range plan.Containers {
		got, ok := ex.containers[dc.Name]
		if !ok {
			recreatedSvc[dc.Service] = true
			replaced[dc.Name] = true
			continue
		}
		recreate := !composeHashMatches(got.Labels, dc.Hash) || refreshed[dc.Name]
		if got.NetworkSettings != nil {
			for netName := range got.NetworkSettings.Networks {
				if replacedNet[netName] {
					recreate = true
				}
			}
		}
		for dep, d := range dc.DependsOn {
			if d.Restart && recreatedSvc[dep] {
				recreate = true
			}
		}
		// com.docker.compose.config-hash, like compose mustRecreate (Pass
		// 5.3 D5); with service:<x> / volumes_from references, a new target
		// container or a hash resolved against another one (c_compose_refs.go).
		if !recreate && !composeLocalConfigHashCurrent(dc, got.Labels, ex, replaced) {
			recreate = true
		}
		if recreate {
			remove[dc.Name] = true
			recreatedSvc[dc.Service] = true
			replaced[dc.Name] = true
		}
	}
	for _, name := range composeSortedKeys(remove) {
		// pre_stop hooks of the service (none for orphans, whose service
		// is gone: compose passes no service for them either).
		if dc := desired[name]; dc != nil {
			if err := composeRunPreStop(ctx, pd, ex.containers[name], dc.PreStop); err != nil {
				return err
			}
		}
		if _, err := c.ContainerRemove(ctx, ex.containers[name].ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNotFound(err) {
			return fmt.Errorf("removing container %s: %w", name, err)
		}
	}

	// Networks: remove replaced and orphan ones, then create what is missing.
	for _, name := range composeSortedKeys(ex.networks) {
		if !replacedNet[name] && wantNet[name] {
			continue
		}
		id := ex.networks[name].ID
		if err := stackRemoveRetry(ctx, "network "+name, func(ctx context.Context) error {
			_, err := c.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
			return err
		}); err != nil {
			return err
		}
	}
	netIDs := map[string]string{}
	for name, n := range ex.networks {
		if !replacedNet[name] {
			netIDs[name] = n.ID
		}
	}
	for _, n := range plan.Networks {
		if n.External {
			netIDs[n.Name] = n.Name
			continue
		}
		if _, ok := netIDs[n.Name]; ok {
			continue
		}
		if err := composeOwnerCheckNetwork(ctx, pd, n.Name); err != nil {
			return err
		}
		opts := n.Create
		opts.Labels = pd.ownerInject(opts.Labels)
		resp, err := c.NetworkCreate(ctx, n.Name, opts)
		if err != nil {
			return fmt.Errorf("creating network %s: %w", n.Name, err)
		}
		netIDs[n.Name] = resp.ID
	}

	// Volumes: create missing; a changed definition is kept (never drop data).
	for _, v := range plan.Volumes {
		if v.External {
			continue
		}
		if got, ok := ex.volumes[v.Name]; ok {
			if !composeHashMatches(got.Labels, v.Hash) {
				warn("Compose volume kept", fmt.Sprintf("volume %s exists with a different driver, options or labels; it is kept as is (remove it to recreate it)", v.Name))
			}
			continue
		}
		if err := composeOwnerCheckVolume(ctx, pd, v.Name); err != nil {
			return err
		}
		opts := v.Create
		opts.Labels = pd.ownerInject(opts.Labels)
		if _, err := c.VolumeCreate(ctx, opts); err != nil {
			return fmt.Errorf("creating volume %s: %w", v.Name, err)
		}
	}

	// Containers in dependency order. A service's shared pre_start steps
	// run before the first container created for it, unless one of its
	// containers is kept (compose_hooks.go).
	kept := map[string]bool{}
	for _, dc := range plan.Containers {
		if _, ok := ex.containers[dc.Name]; ok && !remove[dc.Name] {
			kept[dc.Service] = true
		}
	}
	sharedDone := map[string]bool{}
	ids := map[string][]string{} // service -> container IDs
	for _, dc := range plan.Containers {
		for dep, d := range dc.DependsOn {
			if err := composeWaitDependency(ctx, pd, dep, ids[dep], d.Condition, o.WaitTimeout); err != nil {
				if !d.Required {
					warn("Optional dependency not ready", fmt.Sprintf("%s: %v", dc.Name, err))
					continue
				}
				return fmt.Errorf("service %s: %w", dc.Service, err)
			}
		}
		got, exists := ex.containers[dc.Name]
		if exists && !remove[dc.Name] {
			ids[dc.Service] = append(ids[dc.Service], got.ID)
			if got.State == container.StateCreated || (got.State == container.StateExited && composeLocalStateDrift(got, dc.Host.RestartPolicy.Name) != "") {
				if _, err := c.ContainerStart(ctx, got.ID, client.ContainerStartOptions{}); err != nil {
					return fmt.Errorf("starting container %s: %w", dc.Name, err)
				}
				// compose runs post_start for every container it starts
				// (convergence.go:905-931 startService).
				if err := composeRunExecHooks(ctx, pd, got.ID, dc.Name, "post_start", dc.PostStart); err != nil {
					return err
				}
			}
			continue
		}
		if !kept[dc.Service] && !sharedDone[dc.Service] {
			dc.SharedPreStart = true
			sharedDone[dc.Service] = true
		}
		id, err := composeLocalCreate(ctx, pd, dc, netIDs)
		if err != nil {
			return err
		}
		ids[dc.Service] = append(ids[dc.Service], id)
	}

	// Orphan volumes go last, once their containers are gone.
	if !o.KeepVolumes {
		want := map[string]bool{}
		for _, v := range plan.Volumes {
			want[v.Name] = true
		}
		for _, name := range composeSortedKeys(ex.volumes) {
			if want[name] {
				continue
			}
			if _, err := c.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: false}); err != nil && !isNotFound(err) {
				warn("Cannot remove orphan volume", fmt.Sprintf("%s: %v", name, err))
			}
		}
	}

	if o.Wait {
		for _, dc := range plan.Containers {
			if err := composeWaitDependency(ctx, pd, dc.Service, ids[dc.Service], "ready", o.WaitTimeout); err != nil {
				return fmt.Errorf("service %s: %w", dc.Service, err)
			}
		}
	}
	return nil
}

func composeParsePlatform(s string) *ocispec.Platform {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "/")
	p := &ocispec.Platform{OS: parts[0]}
	if len(parts) > 1 {
		p.Architecture = parts[1]
	}
	if len(parts) > 2 {
		p.Variant = parts[2]
	}
	return p
}

// composePull pulls ref unconditionally (pull_policy always).
func composePull(ctx context.Context, pd *providerData, ref, platform string) error {
	auth, err := pd.registryAuthFor(ref)
	if err != nil {
		return err
	}
	rc, err := pd.Client.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: auth, Platforms: platformsFromString(platform)})
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
	}
}

// composeLocalCreate creates and starts one container like `docker compose
// up`: create (composeLocalCreateOnly), the pre_start steps, start, then
// the post_start hooks (compose_hooks.go).
func composeLocalCreate(ctx context.Context, pd *providerData, dc composeLocalContainer, netIDs map[string]string) (string, error) {
	id, err := composeLocalCreateOnly(ctx, pd, dc, netIDs)
	if err != nil {
		return id, err
	}
	if err := composeRunPreStart(ctx, pd, dc, id, netIDs); err != nil {
		return id, err
	}
	if _, err := pd.Client.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return id, fmt.Errorf("starting container %s: %w", dc.Name, err)
	}
	if err := composeRunExecHooks(ctx, pd, id, dc.Name, "post_start", dc.PostStart); err != nil {
		return id, err
	}
	return id, nil
}

// composeLocalCreateOnly gets dc's image (build, pull per pull_policy),
// creates the container, connects its extra networks and copies its
// secrets and configs in. It does not start it.
func composeLocalCreateOnly(ctx context.Context, pd *providerData, dc composeLocalContainer, netIDs map[string]string) (string, error) {
	c := pd.Client
	if err := composeCheckAPI(ctx, pd, dc); err != nil {
		return "", err
	}
	built := false
	if dc.Build != nil {
		exists, err := composeImageExists(ctx, pd, dc.Config.Image)
		if err != nil {
			return "", fmt.Errorf("inspecting image %s: %w", dc.Config.Image, err)
		}
		if !exists || dc.Pull == ctypes.PullPolicyBuild {
			if _, err := composeBuild(ctx, pd, dc.Build.Project, dc.Build.Service, nil); err != nil {
				return "", err
			}
		}
		built = true
	}
	switch {
	case built:
	case dc.Pull == ctypes.PullPolicyNever:
	case dc.Pull == ctypes.PullPolicyAlways:
		if err := composePull(ctx, pd, dc.Config.Image, dc.Platform); err != nil {
			return "", fmt.Errorf("pulling %s: %w", dc.Config.Image, err)
		}
	case dc.PullRefresh:
		due, err := composeRefreshDue(ctx, pd, dc.Config.Image, dc.PullEvery, time.Now())
		if err != nil {
			return "", fmt.Errorf("inspecting image %s: %w", dc.Config.Image, err)
		}
		if due {
			if err := composePull(ctx, pd, dc.Config.Image, dc.Platform); err != nil {
				return "", fmt.Errorf("pulling %s: %w", dc.Config.Image, err)
			}
		}
	default:
		if err := containerEnsureImage(ctx, pd, dc.Config.Image); err != nil {
			return "", fmt.Errorf("pulling %s: %w", dc.Config.Image, err)
		}
	}
	if dc.Refs != nil {
		var err error
		if dc, err = composeLocalResolveRefs(ctx, pd, dc); err != nil {
			return "", err
		}
	}
	dc = composeLocalWire(ctx, pd, dc)
	// One-off containers (docker_compose_run, pre_start steps) are
	// owner-labelled inject-only: an action owns nothing in state.
	if !strings.EqualFold(dc.Config.Labels[composeLabelOneoff], "true") && composeOwnerChecks(ctx) {
		if err := composeOwnerCheckContainer(ctx, pd, dc.Name); err != nil {
			return "", err
		}
	}
	var netCfg *network.NetworkingConfig
	if len(dc.Networks) > 0 {
		ep := dc.Networks[0]
		es, err := composeEndpointSettings(ep, dc.Host.Links)
		if err != nil {
			return "", fmt.Errorf("container %s network %s: %w", dc.Name, ep.Name, err)
		}
		netCfg = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{ep.Name: es}}
	}
	resp, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Config: dc.Config, HostConfig: dc.Host, NetworkingConfig: netCfg, Platform: composeParsePlatform(dc.Platform), Name: dc.Name})
	if err != nil {
		return "", fmt.Errorf("creating container %s: %w", dc.Name, err)
	}
	for _, ep := range dc.Networks[min(1, len(dc.Networks)):] {
		id := netIDs[ep.Name]
		if id == "" {
			id = ep.Name
		}
		es, err := composeEndpointSettings(ep, dc.Host.Links)
		if err != nil {
			return resp.ID, fmt.Errorf("container %s network %s: %w", dc.Name, ep.Name, err)
		}
		if _, err := c.NetworkConnect(ctx, id, client.NetworkConnectOptions{Container: resp.ID, EndpointConfig: es}); err != nil {
			return resp.ID, fmt.Errorf("connecting %s to network %s: %w", dc.Name, ep.Name, err)
		}
	}
	if len(dc.Copies) > 0 {
		copies := make([]composeCopy, len(dc.Copies))
		for i, cp := range dc.Copies {
			if cp.APIAuth {
				cp.Data = composeAPISocketAuth(pd)
			}
			copies[i] = cp
		}
		tr, err := composeTar(copies)
		if err != nil {
			return resp.ID, err
		}
		if _, err := c.CopyToContainer(ctx, resp.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: tr}); err != nil {
			return resp.ID, fmt.Errorf("copying secrets/configs into %s: %w", dc.Name, err)
		}
	}
	return resp.ID, nil
}

// composeEndpointSettings is one endpoint of a compose container: aliases,
// static addresses, mac_address and the legacy links (compose sends the
// links on every endpoint, create.go:441-454 createEndpointSettings).
func composeEndpointSettings(ep composeEndpoint, links []string) (*network.EndpointSettings, error) {
	es, err := containerEndpointSettings(ep.Aliases, ep.IPv4, ep.IPv6)
	if err != nil {
		return nil, err
	}
	if es.MacAddress, err = composeMAC(ep.MacAddress); err != nil {
		return nil, err
	}
	es.Links = links
	es.GwPriority = ep.GwPriority
	if len(ep.DriverOpts) > 0 {
		es.DriverOpts = maps.Clone(ep.DriverOpts)
	}
	for _, ll := range ep.LinkLocalIPs {
		if ll == "" {
			continue
		}
		a, err := netip.ParseAddr(ll)
		if err != nil {
			return nil, fmt.Errorf("invalid link-local IP %q: %w", ll, err)
		}
		if es.IPAMConfig == nil {
			es.IPAMConfig = &network.EndpointIPAMConfig{}
		}
		es.IPAMConfig.LinkLocalIPs = append(es.IPAMConfig.LinkLocalIPs, a)
	}
	return es, nil
}

// composeLabelImage is the image ID label `docker compose` puts on every
// container it creates (api.ImageDigestLabel).
const composeLabelImage = "com.docker.compose.image"

// composeLabelDependsOn lists a container's dependencies as
// "<service>:<condition>:<restart>", comma-separated, like `docker compose`
// (api.DependenciesLabel). compose v5.7.0 writes them in Go map order
// (pkg/compose/depends_on_label.go:30-36 encodeDependsOnLabel ranges over
// service.DependsOn, called from create.go:319), which differs from run to run; docker_compose sorts
// them so the label is deterministic.
const composeLabelDependsOn = "com.docker.compose.depends_on"

// composeLocalWire turns a planned container into what `docker compose up`
// sends to the Engine. The plan (and its pinned spec hash) keeps the
// provider's own form; only the create request differs:
//   - environment keys with no value are added as bare "KEY" entries;
//   - the label com.docker.compose.image holds the image ID, read once the
//     image is present, and com.docker.compose.depends_on the dependencies;
//   - every network endpoint gets the container name as its first alias
//     (compose getAliases: container name, service name, configured aliases);
//   - a named volume without nocopy / subpath / driver options is mounted
//     through HostConfig.Binds as "<volume>:<target>:rw|ro", like compose
//     (it prefers the Binds API unless a Mount-API-only option is used).
//
// dc's maps and slices are copied, never changed in place.
func composeLocalWire(ctx context.Context, pd *providerData, dc composeLocalContainer) composeLocalContainer {
	cfg := *dc.Config
	cfg.Env = composeEnvWithUnset(dc.Config.Env, dc.UnsetEnv)
	cfg.Labels = make(map[string]string, len(dc.Config.Labels)+1)
	for k, v := range dc.Config.Labels {
		cfg.Labels[k] = v
	}
	// The owner label is not part of the service, so neither
	// com.docker.compose.config-hash nor the spec hash sees it.
	cfg.Labels = pd.ownerInject(cfg.Labels)
	if insp, err := pd.Client.ImageInspect(ctx, dc.Config.Image); err == nil && insp.ID != "" {
		cfg.Labels[composeLabelImage] = insp.ID
	}
	deps := make([]string, 0, len(dc.DependsOn))
	for name, d := range dc.DependsOn {
		deps = append(deps, fmt.Sprintf("%s:%s:%t", name, d.Condition, d.Restart))
	}
	sort.Strings(deps)
	cfg.Labels[composeLabelDependsOn] = strings.Join(deps, ",")
	dc.Config = &cfg

	eps := make([]composeEndpoint, len(dc.Networks))
	for i, ep := range dc.Networks {
		al := []string{dc.Name}
		for _, a := range ep.Aliases {
			if a != dc.Name {
				al = append(al, a)
			}
		}
		ep.Aliases = al
		eps[i] = ep
	}
	dc.Networks = eps

	if dc.Host != nil && len(dc.Host.Mounts) > 0 {
		hc := *dc.Host
		hc.Binds = append([]string(nil), dc.Host.Binds...)
		hc.Mounts = nil
		for _, m := range dc.Host.Mounts {
			if m.Type == mount.TypeVolume && m.Source != "" && m.VolumeOptions == nil && m.Consistency == "" {
				access := "rw"
				if m.ReadOnly {
					access = "ro"
				}
				hc.Binds = append(hc.Binds, m.Source+":"+m.Target+":"+access)
				continue
			}
			hc.Mounts = append(hc.Mounts, m)
		}
		dc.Host = &hc
	}
	return dc
}

// composeTar builds the archive CopyToContainer extracts at "/".
func composeTar(files []composeCopy) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		hdr := &tar.Header{
			Name:     strings.TrimPrefix(path.Clean(f.Path), "/"),
			Mode:     f.Mode,
			Size:     int64(len(f.Data)),
			Uid:      f.UID,
			Gid:      f.GID,
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(0, 0),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// composeWaitDependency waits for every container of a service to meet
// condition: service_started (no wait), service_healthy, or
// service_completed_successfully; "ready" means running (and healthy when
// it has a healthcheck) or exited 0.
func composeWaitDependency(ctx context.Context, pd *providerData, svc string, ids []string, condition string, timeout time.Duration) error {
	if condition == "" || condition == ctypes.ServiceConditionStarted || len(ids) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	for _, id := range ids {
		var last string
		err := waitFor(ctx, timeout, containerPollInterval, func(ctx context.Context) (bool, error) {
			inspRes, err := pd.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if err != nil {
				return false, err
			}
			insp := inspRes.Container
			st := insp.State
			if st == nil {
				return false, nil
			}
			last = string(st.Status)
			exited := !st.Running && !st.Restarting && (st.Status == "exited" || st.Status == "dead")
			healthy := st.Health == nil || st.Health.Status == container.NoHealthcheck || st.Health.Status == container.Healthy
			switch condition {
			case ctypes.ServiceConditionCompletedSuccessfully:
				if exited {
					if st.ExitCode != 0 {
						return false, fmt.Errorf("dependency %s exited with code %d", svc, st.ExitCode)
					}
					return true, nil
				}
				return false, nil
			case ctypes.ServiceConditionHealthy:
				if exited {
					return false, fmt.Errorf("dependency %s exited (code %d) before becoming healthy", svc, st.ExitCode)
				}
				if st.Health != nil && st.Health.Status == container.Unhealthy {
					return false, fmt.Errorf("dependency %s is unhealthy", svc)
				}
				return st.Running && st.Health != nil && st.Health.Status == container.Healthy, nil
			default: // ready
				if exited {
					if st.ExitCode != 0 {
						return false, fmt.Errorf("container %s exited with code %d", insp.Name, st.ExitCode)
					}
					return true, nil
				}
				return st.Running && healthy, nil
			}
		})
		if err != nil {
			return fmt.Errorf("waiting for %s (%s, last state %q): %w", svc, condition, last, err)
		}
	}
	return nil
}

// composeLocalDestroy removes every object labelled with the project.
// The pre_stop hooks of plan's services (nil: none, e.g. the configuration
// cannot be parsed) run before their containers are removed.
func composeLocalDestroy(ctx context.Context, pd *providerData, project string, keepVolumes bool, plan *composeLocalPlan) error {
	c := pd.Client
	ex, err := composeLocalList(ctx, pd, project)
	if err != nil {
		return err
	}
	var errs []error
	hooks := composeHooksByService(plan)
	for _, name := range composeSortedKeys(ex.containers) {
		s := ex.containers[name]
		if err := composeRunPreStop(ctx, pd, s, hooks[s.Labels[composeLabelService]]); err != nil {
			errs = append(errs, err)
		}
	}
	for _, name := range composeSortedKeys(ex.containers) {
		if _, err := c.ContainerRemove(ctx, ex.containers[name].ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("removing container %s: %w", name, err))
		}
	}
	for _, name := range composeSortedKeys(ex.networks) {
		id := ex.networks[name].ID
		if err := stackRemoveRetry(ctx, "network "+name, func(ctx context.Context) error {
			_, err := c.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
			return err
		}); err != nil {
			errs = append(errs, err)
		}
	}
	if !keepVolumes {
		for _, name := range composeSortedKeys(ex.volumes) {
			if _, err := c.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: false}); err != nil && !isNotFound(err) {
				errs = append(errs, fmt.Errorf("removing volume %s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// composeLocalIDs reports the computed services/networks/volumes maps.
func composeLocalIDs(ex composeLocalExisting) (map[string][]string, map[string]string, map[string]string) {
	svcs := map[string][]string{}
	type numbered struct {
		n  string
		id string
	}
	tmp := map[string][]numbered{}
	for _, s := range ex.containers {
		svc := s.Labels[composeLabelService]
		tmp[svc] = append(tmp[svc], numbered{s.Labels[composeLabelNumber], s.ID})
	}
	for svc, l := range tmp {
		sort.Slice(l, func(i, j int) bool {
			if len(l[i].n) != len(l[j].n) {
				return len(l[i].n) < len(l[j].n)
			}
			return l[i].n < l[j].n
		})
		for _, x := range l {
			svcs[svc] = append(svcs[svc], x.id)
		}
	}
	nets := map[string]string{}
	for _, n := range ex.networks {
		key := n.Labels[composeLabelNetwork]
		if key == "" {
			key = n.Name
		}
		nets[key] = n.ID
	}
	vols := map[string]string{}
	for _, v := range ex.volumes {
		key := v.Labels[composeLabelVolume]
		if key == "" {
			key = v.Name
		}
		vols[key] = v.Name
	}
	return svcs, nets, vols
}

// composeLocalResolveRefs points dc's service references at the live
// containers: the create request gets their IDs and the config-hash label
// the hash of the resolved service, as docker compose computes it
// (c_compose_refs.go). dc's maps and slices are copied, never changed.
func composeLocalResolveRefs(ctx context.Context, pd *providerData, dc composeLocalContainer) (composeLocalContainer, error) {
	ids := map[string]string{}
	for _, name := range dc.Refs.names() {
		insp, err := pd.Client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil {
			return dc, fmt.Errorf("container %s refers to container %s: %w", dc.Name, name, err)
		}
		ids[name] = insp.Container.ID
	}
	hash, err := dc.Refs.configHash(func(n string) (string, bool) { id, ok := ids[n]; return id, ok })
	if err != nil {
		return dc, fmt.Errorf("container %s: %w", dc.Name, err)
	}
	cfg := *dc.Config
	cfg.Labels = make(map[string]string, len(dc.Config.Labels))
	for k, v := range dc.Config.Labels {
		cfg.Labels[k] = v
	}
	cfg.Labels[composeLabelConfigHash] = hash
	dc.Config = &cfg
	dc.Host = dc.Refs.host(dc.Host, ids)
	return dc, nil
}
