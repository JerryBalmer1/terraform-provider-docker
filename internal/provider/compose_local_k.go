package provider

// docker_compose, mode = "local" (Pass 5.3 Lane K): the service keys the
// conversion used to warn about and ignore, mapped onto the Engine API the
// way docker compose v5.7.1 does (pkg/compose/create.go, apiSocket.go,
// service_containers.go, pull.go):
//
//   - blkio_config -> HostConfig.Blkio* (create.go:925-939 setBlkio);
//   - links / external_links -> HostConfig.Links and EndpointSettings.Links
//     (service_containers.go:309-362 getLinks);
//   - mac_address -> the primary endpoint's MacAddress (create.go:496-516
//     prepareContainerMACAddress, :698-703);
//   - use_api_socket -> a bind mount of /var/run/docker.sock, DOCKER_CONFIG
//     and a config.json with the registry credentials (apiSocket.go:45-132;
//     compose v5.7.1 differs: it adds DOCKER_CONFIG and the config only when
//     the host has credentials and the service does not set DOCKER_CONFIG,
//     with mode 0600);
//   - volumes[].volume.labels -> Mount.VolumeOptions.Labels
//     (create.go:1410-1437 buildMountOptions);
//   - pull_policy refresh / daily / weekly / every_<d> -> pull when the
//     image's LastTagTime is older than the period (pull.go:536-563
//     mustPull, internal/images/images.go:191 / :256 Metadata.LastTagTime);
//   - umask -> HostConfig.Umask (Engine API 1.56; compose v5.7.1 sends it
//     too, refusing an Engine below 1.56, create.go:739-751 umaskOption);
//   - credential_spec -> HostConfig.SecurityOpt "credentialspec=..." (the
//     Windows daemon form `docker run --security-opt` takes; compose v5.7.1
//     does not send it in local mode and reports it as unsupported,
//     internal/projectloader/unsupported_attributes.go:59; docker stack
//     deploy does send it in swarm mode).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/blkiodev"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// composeBlkio is compose's setBlkio (create.go:925-939).
func composeBlkio(b *ctypes.BlkioConfig, r *container.Resources) {
	if b == nil {
		return
	}
	r.BlkioWeight = b.Weight
	for _, d := range b.WeightDevice {
		r.BlkioWeightDevice = append(r.BlkioWeightDevice, &blkiodev.WeightDevice{Path: d.Path, Weight: d.Weight})
	}
	throttle := func(in []ctypes.ThrottleDevice) []*blkiodev.ThrottleDevice {
		var out []*blkiodev.ThrottleDevice
		for _, d := range in {
			out = append(out, &blkiodev.ThrottleDevice{Path: d.Path, Rate: uint64(d.Rate)})
		}
		return out
	}
	r.BlkioDeviceReadBps = throttle(b.DeviceReadBps)
	r.BlkioDeviceReadIOps = throttle(b.DeviceReadIOps)
	r.BlkioDeviceWriteBps = throttle(b.DeviceWriteBps)
	r.BlkioDeviceWriteIOps = throttle(b.DeviceWriteIOps)
}

// composeCredentialSpecOpt is the security option a Windows daemon reads a
// credential spec from (`docker run --security-opt credentialspec=...`):
// file://<file>, registry://<value>. A config:// spec names a Swarm config,
// which only a Swarm task can mount, so it is an error in local mode.
func composeCredentialSpecOpt(cs *ctypes.CredentialSpecConfig) (string, error) {
	if cs == nil {
		return "", nil
	}
	switch {
	case cs.Config != "":
		return "", fmt.Errorf("credential_spec.config %q names a Swarm config: use mode = \"swarm\" or credential_spec.file / registry", cs.Config)
	case cs.File != "":
		return "credentialspec=file://" + cs.File, nil
	case cs.Registry != "":
		return "credentialspec=registry://" + cs.Registry, nil
	}
	return "", nil
}

// composeLinks is compose's getLinks (service_containers.go:309-362) on the
// planned container names: each linked service's containers under the link
// alias, "<service>-<number>" and "<project>-<service>-<number>" (number is
// the linking container's own number, as in compose), then external_links
// as "<container>:<alias>".
func composeLinks(p *ctypes.Project, svc ctypes.ServiceConfig, number int) ([]string, error) {
	var links []string
	for _, raw := range svc.Links {
		name, alias, ok := strings.Cut(raw, ":")
		if !ok {
			alias = name
		}
		dep, found := p.Services[name]
		if !found {
			return nil, fmt.Errorf("links %q: service %s is not enabled", raw, name)
		}
		scale := dep.GetScale()
		if dep.Deploy != nil && (dep.Deploy.Mode == "global" || dep.Deploy.Mode == "global-job") {
			scale = 1
		}
		for i := 1; i <= scale; i++ {
			c := composeContainerName(p.Name, dep, i)
			links = append(links,
				c+":"+alias,
				c+":"+name+"-"+strconv.Itoa(number),
				c+":"+p.Name+"-"+name+"-"+strconv.Itoa(number))
		}
	}
	for _, raw := range svc.ExternalLinks {
		name, alias, ok := strings.Cut(raw, ":")
		if !ok {
			alias = name
		}
		links = append(links, name+":"+alias)
	}
	return links, nil
}

// composeMAC validates a MAC address.
func composeMAC(s string) (network.HardwareAddr, error) {
	if s == "" {
		return nil, nil
	}
	hw, err := net.ParseMAC(s)
	if err != nil {
		return nil, fmt.Errorf("mac_address %q: %w", s, err)
	}
	return network.HardwareAddr(hw), nil
}

// composeAPISocketConfigPath is where use_api_socket puts the credentials
// (apiSocket.go:110-128: DOCKER_CONFIG=/run/secrets/docker and a config
// "#apisocket" mounted at /run/secrets/docker/config.json).
const (
	composeAPISocketDir    = "/run/secrets/docker"
	composeAPISocketConfig = composeAPISocketDir + "/config.json"
	composeAPISocketPath   = "/var/run/docker.sock"
	composeAPISocketSource = "#apisocket"
)

// composeWithAPISocket is compose's useAPISocket transformation of one
// service (apiSocket.go:45-132), applied before anything is hashed so the
// config-hash matches the CLI's: a bind mount of the Engine socket, the
// DOCKER_CONFIG variable (unless the service sets it) and the "#apisocket"
// config (compose v5.7.1 differs: no DOCKER_CONFIG or config when the host
// has no credentials or the service sets DOCKER_CONFIG, and mode 0600). The service's slices and maps are copied, never changed.
func composeWithAPISocket(svc ctypes.ServiceConfig) ctypes.ServiceConfig {
	if !svc.UseAPISocket {
		return svc
	}
	svc.Volumes = append(append([]ctypes.ServiceVolumeConfig(nil), svc.Volumes...), ctypes.ServiceVolumeConfig{
		Type: ctypes.VolumeTypeBind, Source: composeAPISocketPath, Target: composeAPISocketPath,
	})
	env := ctypes.MappingWithEquals{}
	for k, v := range svc.Environment {
		env[k] = v
	}
	if _, ok := env["DOCKER_CONFIG"]; !ok {
		dir := composeAPISocketDir
		env["DOCKER_CONFIG"] = &dir
	}
	svc.Environment = env
	svc.Configs = append(append([]ctypes.ServiceConfigObjConfig(nil), svc.Configs...), ctypes.ServiceConfigObjConfig{
		Source: composeAPISocketSource, Target: composeAPISocketConfig,
	})
	return svc
}

// composeAPISocketAuth is the config.json use_api_socket copies into the
// container: the provider's registry_auth credentials in the docker CLI
// config file format (compose writes configfile.ConfigFile{AuthConfigs}
// with every credential the CLI knows, apiSocket.go:68-86).
func composeAPISocketAuth(pd *providerData) []byte {
	type authEntry struct {
		Auth          string `json:"auth,omitempty"`
		IdentityToken string `json:"identitytoken,omitempty"`
	}
	auths := map[string]authEntry{}
	if pd != nil {
		for _, c := range pd.RegistryAuth {
			e := authEntry{IdentityToken: c.IdentityToken}
			if c.Username != "" || c.Password != "" {
				e.Auth = base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password))
			}
			auths[c.Address] = e
		}
	}
	b, _ := json.MarshalIndent(map[string]any{"auths": auths}, "", "\t")
	return b
}

// composePullRefresh returns the refresh period of a service's pull
// policy: daily, weekly and every_<duration> (compose-go
// ServiceConfig.GetPullPolicy), or refresh with pull_refresh_after (the
// compose-spec "Time after which to refresh the image. Used with
// pull_policy=refresh."; compose-go v2.16.2 types.go:103). 0 = not a
// refresh policy. refresh without pull_refresh_after pulls every time.
func composePullRefresh(svc ctypes.ServiceConfig) (time.Duration, bool, error) {
	policy, d, err := svc.GetPullPolicy()
	if err != nil {
		return 0, false, err
	}
	if policy != ctypes.PullPolicyRefresh {
		return 0, false, nil
	}
	if svc.PullPolicy == ctypes.PullPolicyRefresh && svc.PullRefreshAfter != "" {
		// Same duration syntax as every_<duration> (w, d, h, m, s).
		probe := ctypes.ServiceConfig{ContainerSpec: ctypes.ContainerSpec{PullPolicy: "every_" + svc.PullRefreshAfter}}
		_, d, err = probe.GetPullPolicy()
		if err != nil {
			return 0, false, fmt.Errorf("pull_refresh_after %q: %w", svc.PullRefreshAfter, err)
		}
	}
	return d, true, nil
}

// composeRefreshDue is compose's mustPull for the refresh policies
// (pull.go:553-558): pull when the image is missing or its last tag time
// (set by every pull and tag) is older than the period.
func composeRefreshDue(ctx context.Context, pd *providerData, ref string, every time.Duration, now time.Time) (bool, error) {
	insp, err := pd.Client.ImageInspect(ctx, ref)
	if err != nil {
		if isNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return now.After(insp.Metadata.LastTagTime.Add(every)), nil
}

// composeUmaskAPI is the Engine API version that added HostConfig.Umask
// (reference/upstream/engine-api/api/docs/CHANGELOG.md, v1.56).
const composeUmaskAPI = "1.56"

// composeCheckAPI fails when a container sets a field the daemon's Engine
// API version does not have.
func composeCheckAPI(ctx context.Context, pd *providerData, dc composeLocalContainer) error {
	if dc.Host == nil || dc.Host.Umask == nil {
		return nil
	}
	ok, v, err := pd.APIVersionAtLeast(ctx, composeUmaskAPI)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("service %s: umask needs Engine API %s or later, the daemon speaks %s", dc.Service, composeUmaskAPI, v)
	}
	return nil
}

// composeCLIOnlyKeys reports whether svc sets a key that changes
// com.docker.compose.config-hash (compose ServiceHash) without changing
// the container create request the provider's spec hash digests: an
// environment entry with no value (sent as a bare KEY outside the frozen
// hash), lifecycle hooks, env_file / label_file paths, extends, develop,
// attach, models or provider. For such services docker_compose's spec_hash
// includes the config-hash, so a change there plans an update and the
// container is recreated like `docker compose up` would (Pass 5.3 D5).
func composeCLIOnlyKeys(svc ctypes.ServiceConfig) bool {
	return len(composeEnvUnset(svc.Environment)) > 0 || len(svc.PreStart) > 0 || len(svc.PostStart) > 0 ||
		len(svc.PreStop) > 0 || len(svc.EnvFiles) > 0 || len(svc.LabelFiles) > 0 || svc.Extends != nil ||
		svc.Develop != nil || svc.Attach != nil || len(svc.Models) > 0 || svc.Provider != nil
}
