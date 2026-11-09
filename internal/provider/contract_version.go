package provider

// docker_server_version for the outputs contract: GET /version, read once
// per provider instance and cached on providerData.serverVersion.

import (
	"context"
	"fmt"
	"sync"

	"github.com/moby/moby/client"
)

// contractServerVersionCache caches the daemon's Version string. Failures
// are not cached, so a later call retries.
type contractServerVersionCache struct {
	mu sync.Mutex
	v  string
}

// dockerServerVersion returns the daemon's version (GET /version Version),
// cached after the first successful call.
func (pd *providerData) dockerServerVersion(ctx context.Context) (string, error) {
	if pd == nil || pd.Client == nil {
		return "", fmt.Errorf("provider not configured")
	}
	pd.serverVersion.mu.Lock()
	defer pd.serverVersion.mu.Unlock()
	if pd.serverVersion.v != "" {
		return pd.serverVersion.v, nil
	}
	res, err := pd.Client.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return "", fmt.Errorf("reading the daemon version from %s: %w", pd.Endpoint.Host, err)
	}
	pd.serverVersion.v = res.Version
	return res.Version, nil
}
