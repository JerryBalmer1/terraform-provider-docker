package provider

// Docker CLI context resolution. Contexts live in <DOCKER_CONFIG or
// ~/.docker>/contexts and are read with docker/cli's context store; nothing
// runs the docker CLI.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	dctx "github.com/docker/cli/cli/context"
	"github.com/docker/cli/cli/context/store"
)

// dockerContextEndpoint is what a context contributes to the endpoint.
type dockerContextEndpoint struct {
	Name          string
	Host          string
	CA            []byte
	Cert          []byte
	Key           []byte
	SkipTLSVerify bool
}

// dockerConfigDir is DOCKER_CONFIG when set, else ~/.docker.
func dockerConfigDir(getenv func(string) string) string {
	if d := getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	return homePath(".docker")
}

// currentDockerContext reads currentContext from <dir>/config.json. A missing
// or unreadable file means no current context.
func currentDockerContext(dir string) string {
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return ""
	}
	return cfg.CurrentContext
}

// contextStoreConfig types the docker endpoint like the CLI does, without
// pulling in docker/cli's client-building code.
func contextStoreConfig() store.Config {
	return store.NewConfig(
		func() any { return &map[string]any{} },
		store.EndpointTypeGetter("docker", func() any { return &dctx.EndpointMetaBase{} }),
	)
}

// loadDockerContext resolves a named context from <configDir>/contexts.
// "default" (and "") is the implicit context and yields nil.
func loadDockerContext(configDir, name string) (*dockerContextEndpoint, error) {
	if name == "" || name == "default" {
		return nil, nil
	}
	if configDir == "" {
		return nil, fmt.Errorf("docker context %q: cannot locate the docker config directory; set DOCKER_CONFIG", name)
	}
	s := store.New(filepath.Join(configDir, "contexts"), contextStoreConfig())
	meta, err := s.GetMetadata(name)
	if err != nil {
		return nil, fmt.Errorf("docker context %q (store %s): %w", name, filepath.Join(configDir, "contexts"), err)
	}
	raw, ok := meta.Endpoints["docker"]
	if !ok {
		return nil, fmt.Errorf("docker context %q has no docker endpoint", name)
	}
	ep, ok := raw.(dctx.EndpointMetaBase)
	if !ok {
		return nil, fmt.Errorf("docker context %q: unexpected docker endpoint type %T", name, raw)
	}
	if ep.Host == "" {
		return nil, errors.New("docker context " + name + " has an empty docker host")
	}
	out := &dockerContextEndpoint{Name: name, Host: ep.Host, SkipTLSVerify: ep.SkipTLSVerify}
	tlsData, err := dctx.LoadTLSData(s, name, "docker")
	if err != nil {
		return nil, err
	}
	if tlsData != nil {
		out.CA, out.Cert, out.Key = tlsData.CA, tlsData.Cert, tlsData.Key
	}
	return out, nil
}
