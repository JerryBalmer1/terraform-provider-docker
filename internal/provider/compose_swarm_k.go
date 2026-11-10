package provider

// docker_compose, mode = "swarm" (Pass 5.3 Lane K): credential_spec, as
// docker stack deploy converts it (docker/cli v29.9.3
// cli/compose/convert/service.go:104-110, :288-345 convertServiceConfigObjs,
// :680-715 convertCredentialSpec).

import (
	"fmt"
	"sort"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/swarm"
)

// composeSwarmCredentialSpec sets cs.Privileges.CredentialSpec. A config
// credential spec also gets a Runtime config reference (no file target);
// composeSwarmResolve turns its name into the config ID.
func composeSwarmCredentialSpec(p *ctypes.Project, svc ctypes.ServiceConfig, cs *swarm.ContainerSpec) error {
	spec := svc.CredentialSpec
	if spec == nil {
		return nil
	}
	n := 0
	for _, v := range []string{spec.Config, spec.File, spec.Registry} {
		if v != "" {
			n++
		}
	}
	switch {
	case n == 0:
		return nil
	case n > 1:
		return fmt.Errorf("invalid credential spec: set only one of config, file and registry")
	}
	cred := &swarm.CredentialSpec{File: spec.File, Registry: spec.Registry}
	if spec.Config != "" {
		obj, ok := p.Configs[spec.Config]
		if !ok {
			return fmt.Errorf("credential_spec.config %q is not defined", spec.Config)
		}
		cred.Config = obj.Name
		cs.Configs = append(cs.Configs, &swarm.ConfigReference{ConfigName: obj.Name, Runtime: &swarm.ConfigReferenceRuntimeTarget{}})
	}
	cs.Privileges = &swarm.Privileges{CredentialSpec: cred}
	return nil
}

// composeSortedEnv returns a sorted copy of an environment list.
func composeSortedEnv(env []string) []string {
	out := append([]string(nil), env...)
	sort.Strings(out)
	return out
}
