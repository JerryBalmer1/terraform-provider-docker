package provider

// docker_compose, mode = "local": references from one service to another
// service's container (network_mode / ipc / pid "service:<x>" and
// volumes_from "<x>[:mode]"), and environment entries with no value.
//
// docker compose v5.1.4 resolves the references at converge time, once the
// target's containers exist (pkg/compose/convergence.go:133 ensureService
// calls resolveServiceReferences, :271-320): volumes_from "<x>[:mode]"
// becomes the ID of <x>'s first container (sorted by name, mode dropped),
// "container:<c>[:mode]" becomes "<c>", and a "service:<x>" namespace
// becomes "container:<ID>". It then hashes that resolved service for
// com.docker.compose.config-hash (create.go:497-502 prepareLabels ->
// hash.go:27 ServiceHash) and compares the label with it to decide
// recreation (convergence.go:326-338 mustRecreate). So the label carries
// the target's container ID, and a dependant is recreated whenever its
// target gets a new container. docker_compose does the same here: the plan
// keeps the container names (its spec hash is unchanged), and the create
// request and the config-hash label are resolved against the live IDs.

import (
	"fmt"
	"sort"
	"strings"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
)

// composeLocalRefs is what a container needs to resolve its references.
type composeLocalRefs struct {
	// Service is the service as loaded (unresolved), for the CLI hash.
	Service ctypes.ServiceConfig
	// Targets maps each referenced service to the name of its first
	// container (the one docker compose picks: sorted by name).
	Targets map[string]string
}

// composeLocalRefsFor returns svc's references, or nil when it has none.
// A service with volumes_from always has some: even "container:<c>:ro" is
// rewritten (to "<c>") before docker compose hashes it.
func composeLocalRefsFor(p *ctypes.Project, svc ctypes.ServiceConfig) (*composeLocalRefs, error) {
	targets := map[string]string{}
	add := func(name string) error {
		dep, ok := p.Services[name]
		if !ok {
			return fmt.Errorf("service %s refers to service %s, which is not enabled", svc.Name, name)
		}
		targets[name] = composeContainerName(p.Name, dep, 1)
		return nil
	}
	for _, vf := range svc.VolumesFrom {
		spec := strings.Split(vf, ":")
		if spec[0] == "container" {
			continue
		}
		if err := add(spec[0]); err != nil {
			return nil, err
		}
	}
	for _, mode := range []string{svc.NetworkMode, svc.Ipc, svc.Pid} {
		if name, ok := strings.CutPrefix(mode, ctypes.ServicePrefix); ok {
			if err := add(name); err != nil {
				return nil, err
			}
		}
	}
	if len(svc.VolumesFrom) == 0 && len(targets) == 0 {
		return nil, nil
	}
	return &composeLocalRefs{Service: svc, Targets: targets}, nil
}

// names returns the target container names, sorted and unique.
func (r *composeLocalRefs) names() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range r.Targets {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// resolve is docker compose's resolveServiceReferences on a copy of the
// service: id returns the container ID behind a target container name.
func (r *composeLocalRefs) resolve(id func(name string) (string, bool)) (ctypes.ServiceConfig, error) {
	s := r.Service
	s.VolumesFrom = append([]string(nil), r.Service.VolumesFrom...)
	// convergence.go:285-303 resolveVolumeFrom
	for i, vol := range s.VolumesFrom {
		spec := strings.Split(vol, ":")
		if spec[0] == "container" {
			if len(spec) > 1 {
				s.VolumesFrom[i] = spec[1]
			}
			continue
		}
		cid, ok := id(r.Targets[spec[0]])
		if !ok {
			return s, fmt.Errorf("cannot share volume with service %s: container missing", spec[0])
		}
		s.VolumesFrom[i] = cid
	}
	// convergence.go:305-320 resolveSharedNamespaces
	for _, f := range []struct {
		field *string
		noun  string
	}{{&s.NetworkMode, "network"}, {&s.Ipc, "IPC"}, {&s.Pid, "PID"}} {
		name, ok := strings.CutPrefix(*f.field, ctypes.ServicePrefix)
		if !ok {
			continue
		}
		cid, ok := id(r.Targets[name])
		if !ok {
			return s, fmt.Errorf("cannot share %s namespace with service %s: container missing", f.noun, name)
		}
		*f.field = ctypes.ContainerPrefix + cid
	}
	return s, nil
}

// configHash is com.docker.compose.config-hash of the resolved service.
func (r *composeLocalRefs) configHash(id func(name string) (string, bool)) (string, error) {
	s, err := r.resolve(id)
	if err != nil {
		return "", err
	}
	return composeServiceConfigHash(s)
}

// host returns a copy of h with the service references pointing at
// container IDs, as docker compose sends them (create.go:298-319 takes
// NetworkMode, IpcMode, PidMode and VolumesFrom from the resolved
// service). One difference is kept on purpose: a volumes_from mode
// ("web:ro") stays on the entry ("<ID>:ro"), where compose drops it and
// mounts the volumes read-write (reference/CONTRACT.md, Known differences
// from upstream).
func (r *composeLocalRefs) host(h *container.HostConfig, ids map[string]string) *container.HostConfig {
	out := *h
	svc := r.Service
	if name, ok := strings.CutPrefix(svc.NetworkMode, ctypes.ServicePrefix); ok {
		out.NetworkMode = container.NetworkMode(ctypes.ContainerPrefix + ids[r.Targets[name]])
	}
	if name, ok := strings.CutPrefix(svc.Ipc, ctypes.ServicePrefix); ok {
		out.IpcMode = container.IpcMode(ctypes.ContainerPrefix + ids[r.Targets[name]])
	}
	if name, ok := strings.CutPrefix(svc.Pid, ctypes.ServicePrefix); ok {
		out.PidMode = container.PidMode(ctypes.ContainerPrefix + ids[r.Targets[name]])
	}
	if len(h.VolumesFrom) == len(svc.VolumesFrom) {
		out.VolumesFrom = append([]string(nil), h.VolumesFrom...)
		for i, vf := range svc.VolumesFrom {
			src, mode, _ := strings.Cut(vf, ":")
			if src == "container" {
				continue // a named container: sent by name, as planned
			}
			out.VolumesFrom[i] = ids[r.Targets[src]]
			if mode != "" {
				out.VolumesFrom[i] += ":" + mode
			}
		}
	}
	return &out
}

// composeLocalRefsCurrent reports whether got, the live container for dc,
// is up to date with its references: no target container is being
// replaced in this apply (replaced, by name) and its config-hash label is
// the resolved hash against the live target IDs, like mustRecreate.
// Containers without the spec-hash label (made before Pass 5, or by the
// CLI) are judged by composeHashMatches alone.
func composeLocalRefsCurrent(dc composeLocalContainer, got map[string]string, ex composeLocalExisting, replaced map[string]bool) bool {
	if dc.Refs == nil {
		return true
	}
	for _, n := range dc.Refs.names() {
		if replaced[n] {
			return false
		}
	}
	if _, ok := got[composeLabelSpecHash]; !ok {
		return true
	}
	want, err := dc.Refs.configHash(composeLocalExistingID(ex))
	if err != nil {
		return false
	}
	return got[composeLabelConfigHash] == want
}

// composeLocalExistingID looks a container ID up by name in ex.
func composeLocalExistingID(ex composeLocalExisting) func(string) (string, bool) {
	return func(name string) (string, bool) {
		s, ok := ex.containers[name]
		if !ok || s.ID == "" {
			return "", false
		}
		return s.ID, true
	}
}

// composeEnvUnset lists the environment keys whose value is unresolved
// (`KEY: null`, or a bare `KEY` not set in env), sorted.
//
// The compose spec (reference/upstream/compose-spec/container_spec.md,
// "environment"): "If the value is not resolved, the variable is unset and
// is removed from the service container environment." docker compose
// v5.1.4 implements that by sending the bare "KEY" in Config.Env
// (pkg/compose/convert.go:30-40 ToMobyEnv, called from create.go:236); the
// Engine then removes KEY from the image's environment
// (moby container/env.go ReplaceOrAppendEnvValues: "Values w/o = means they
// want this env to be removed/unset"). docker_compose sends the same.
func composeEnvUnset(env ctypes.MappingWithEquals) []string {
	var out []string
	for _, k := range composeSortedKeys(env) {
		if env[k] == nil {
			out = append(out, k)
		}
	}
	return out
}

// composeEnvWithUnset adds the bare unset keys to env (KEY=VALUE entries),
// skipping keys env already sets, sorted.
func composeEnvWithUnset(env, unset []string) []string {
	if len(unset) == 0 {
		return env
	}
	set := map[string]bool{}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		set[k] = true
	}
	out := append([]string(nil), env...)
	for _, k := range unset {
		if !set[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
