package provider

// The compose load/normalize/config-hash seam (seams.go, seamComposeImpl)
// and the compose config-hash algorithms.
//
// The config-hash functions reproduce docker/compose internal/confighash
// (Apache License 2.0, Copyright 2020 Docker Compose CLI authors) so the
// com.docker.compose.config-hash labels docker_compose stamps are the values
// `docker compose up` stamps for the same project: the CLI then sees the
// provider's objects as up to date and vice versa (TestAccComposeParity).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

func init() { seamComposeImpl = composeSeam{} }

// composeSeam implements seamCompose on top of composeLoad.
type composeSeam struct{}

func (composeSeam) Load(ctx context.Context, req seamComposeLoadRequest) (*ctypes.Project, error) {
	return composeLoad(ctx, composeInputs{
		Content:     req.Content,
		Files:       req.Paths,
		ProjectName: req.ProjectName,
		Profiles:    req.Profiles,
		Env:         req.Env,
		EnvFiles:    req.EnvFiles,
		WorkingDir:  req.WorkingDir,
	})
}

// Normalize prints the project the way `docker compose config` does.
func (composeSeam) Normalize(p *ctypes.Project) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("no project")
	}
	return p.MarshalYAML()
}

// ConfigHash is the service's com.docker.compose.config-hash.
func (composeSeam) ConfigHash(p *ctypes.Project, service string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("no project")
	}
	svc, ok := p.Services[service]
	if !ok {
		return "", fmt.Errorf("service %q is not in project %s (or not enabled)", service, p.Name)
	}
	return composeServiceConfigHash(svc)
}

// composeServiceHashKeyOrder freezes the top-level key order of the service
// hash (docker/compose confighash.serviceHashKeyOrder).
var composeServiceHashKeyOrder = []string{
	"profiles", "annotations", "attach", "build", "develop", "blkio_config", "cap_add", "cap_drop",
	"cgroup_parent", "cgroup", "cpu_count", "cpu_percent", "cpu_period", "cpu_quota", "cpu_rt_period",
	"cpu_rt_runtime", "cpus", "cpuset", "cpu_shares", "command", "configs", "container_name",
	"credential_spec", "depends_on", "deploy", "device_cgroup_rules", "devices", "dns", "dns_opt",
	"dns_search", "dockerfile", "domainname", "entrypoint", "provider", "environment", "env_file",
	"expose", "extends", "external_links", "extra_hosts", "group_add", "gpus", "hostname", "healthcheck",
	"image", "init", "ipc", "isolation", "labels", "label_file", "links", "logging", "log_driver",
	"log_opt", "mem_limit", "mem_reservation", "memswap_limit", "mem_swappiness", "mac_address",
	"models", "net", "network_mode", "networks", "oom_kill_disable", "oom_score_adj", "pid",
	"pids_limit", "platform", "ports", "privileged", "pull_policy", "read_only", "restart", "runtime",
	"scale", "secrets", "security_opt", "shm_size", "stdin_open", "stop_grace_period", "stop_signal",
	"storage_opt", "sysctls", "tmpfs", "tty", "ulimits", "use_api_socket", "user", "userns_mode", "uts",
	"volume_driver", "volumes", "volumes_from", "working_dir", "pre_start", "post_start", "pre_stop",
	"pull_refresh_after", "umask",
}

// composeServiceConfigHash is docker/compose confighash.ServiceHash: the
// sha256 of the service JSON without build, pull_policy, scale,
// deploy.replicas, depends_on and profiles, top-level keys in the frozen
// order.
func composeServiceConfigHash(o ctypes.ServiceConfig) (string, error) {
	o.Build = nil
	o.PullPolicy = ""
	o.Scale = nil
	if o.Deploy != nil {
		d := *o.Deploy
		d.Replicas = nil
		o.Deploy = &d
	}
	o.DependsOn = nil
	o.Profiles = nil
	raw, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	pinned, err := composePinRootKeyOrder(raw, composeServiceHashKeyOrder)
	if err != nil {
		return "", err
	}
	return composeSHA256Hex(pinned), nil
}

// composeNetworkConfigHash is docker/compose confighash.NetworkHash.
func composeNetworkConfigHash(o ctypes.NetworkConfig) string {
	raw, err := json.Marshal(&o)
	if err != nil {
		return ""
	}
	return composeSHA256Hex(raw)
}

// composeVolumeConfigHash is docker/compose confighash.VolumeHash.
func composeVolumeConfigHash(o ctypes.VolumeConfig) string {
	if o.Driver == "" {
		o.Driver = "local"
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return ""
	}
	return composeSHA256Hex(raw)
}

func composeSHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// composePinRootKeyOrder re-emits a JSON object with its top-level keys in
// order (values verbatim), unlisted keys appended in sorted order.
func composePinRootKeyOrder(raw []byte, order []string) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	write := func(key string, val json.RawMessage) {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		name, _ := json.Marshal(key)
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(val)
	}
	for _, key := range order {
		if val, ok := root[key]; ok {
			write(key, val)
			delete(root, key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(root)) {
		write(key, root[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
