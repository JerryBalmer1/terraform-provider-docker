package provider

// Shared helpers for the Pass 3 acceptance tests (*_acc_p3_test.go). They
// follow the existing conventions: testAccPreCheck (TF_ACC + DOCKER_HOST),
// swarmAccManagerAddr (skip unless DOCKER_HOST is a swarm manager), and the
// variables test/harness/scripts/harness-up.{ps1,sh} write to .env.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// p3Env returns the values of names, skipping the test (with the variable
// that is missing) unless all are set.
func p3Env(t *testing.T, names ...string) []string {
	t.Helper()
	out := make([]string, len(names))
	for i, n := range names {
		v := os.Getenv(n)
		if v == "" {
			t.Skipf("%s not set; skipping (bring up test/harness, which exports it)", n)
		}
		out[i] = v
	}
	return out
}

// p3RequireInactive skips unless DOCKER_HOST is outside any swarm (tests
// that run docker_swarm_init themselves; the plain harness pass).
func p3RequireInactive(t *testing.T) {
	t.Helper()
	info, err := testAccDockerClient(t).Info(context.Background())
	if err != nil {
		t.Skipf("cannot reach DOCKER_HOST: %v", err)
	}
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skip("DOCKER_HOST is already part of a swarm; this test runs docker_swarm_init itself (plain harness pass)")
	}
}

// p3RequireNodeInactive skips unless the daemon at host is outside any swarm.
func p3RequireNodeInactive(t *testing.T, host string) {
	t.Helper()
	c := p3Client(t, endpointConfig{Host: host})
	info, err := c.Info(context.Background())
	if err != nil {
		t.Skipf("cannot reach %s: %v", host, err)
	}
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skipf("%s is already part of a swarm (%s); a previous test did not clean up", host, info.Swarm.LocalNodeState)
	}
}

// p3Client builds a client for ep and closes it when the test ends.
func p3Client(t *testing.T, ep endpointConfig) *client.Client {
	t.Helper()
	c, err := newDockerClient(ep)
	if err != nil {
		t.Fatalf("docker client for %s: %v", ep.Host, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// p3TLSEndpoint returns the harness TLS daemon endpoint (skips without it).
func p3TLSEndpoint(t *testing.T) endpointConfig {
	t.Helper()
	v := p3Env(t, "TF_ACC_TLS_HOST", "TF_ACC_TLS_CA", "TF_ACC_TLS_CERT", "TF_ACC_TLS_KEY")
	for _, f := range v[1:] {
		if _, err := os.Stat(f); err != nil {
			t.Skipf("TLS material %s not readable: %v", f, err)
		}
	}
	return endpointConfig{Host: v[0], CA: v[1], Cert: v[2], Key: v[3]}
}

// p3Compose runs `docker compose -f $TF_ACC_HARNESS_COMPOSE <args>` against
// the HOST daemon (the one running the harness containers), never the
// harness manager: DOCKER_HOST and TLS variables are removed from its
// environment. Only harness containers are touched (stop/start/restart).
func p3Compose(args ...string) error {
	f := os.Getenv("TF_ACC_HARNESS_COMPOSE")
	if f == "" {
		return fmt.Errorf("TF_ACC_HARNESS_COMPOSE not set")
	}
	cmd := exec.Command("docker", append([]string{"compose", "-f", filepath.FromSlash(f)}, args...)...)
	for _, kv := range os.Environ() {
		k := strings.SplitN(kv, "=", 2)[0]
		switch strings.ToUpper(k) {
		case "DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_CONTEXT":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// p3RequireCompose skips unless the harness compose file and the docker CLI
// are available (tests that stop/restart a harness container).
func p3RequireCompose(t *testing.T) {
	t.Helper()
	p3Env(t, "TF_ACC_HARNESS_COMPOSE")
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not on PATH; needed to stop/start a harness container")
	}
}

// p3WaitDaemon waits until the daemon at ep answers Info and cond(info) holds.
func p3WaitDaemon(ep endpointConfig, timeout time.Duration, cond func(swarm.LocalNodeState) bool) error {
	c, err := newDockerClient(ep)
	if err != nil {
		return err
	}
	defer c.Close()
	var last string
	err = waitFor(context.Background(), timeout, 2*time.Second, func(ctx context.Context) (bool, error) {
		// Bound each call: a published port can accept and then hang while
		// the daemon behind it is still starting.
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		info, err := c.Info(cctx)
		if err != nil {
			last = err.Error()
			return false, nil
		}
		last = string(info.Swarm.LocalNodeState)
		return cond == nil || cond(info.Swarm.LocalNodeState), nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w (last: %s)", ep.Host, err, last)
	}
	return nil
}

// p3ForceLeave makes the daemon at host leave any swarm (best effort; for
// cleanup after a failed test so later tests find the node inactive).
func p3ForceLeave(host string) {
	c, err := newDockerClient(endpointConfig{Host: host})
	if err != nil {
		return
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if info, err := c.Info(ctx); err == nil && info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		_ = c.SwarmLeave(ctx, true)
	}
}

// p3JoinOutOfBand joins the daemon at host to DOCKER_HOST's swarm as a
// worker without the provider (like `docker swarm join`), returning its node
// ID once the manager sees it.
func p3JoinOutOfBand(t *testing.T, host, mgrAddr string) string {
	t.Helper()
	ctx := context.Background()
	mgr := testAccDockerClient(t)
	sw, err := mgr.SwarmInspect(ctx)
	if err != nil {
		t.Fatalf("swarm inspect: %v", err)
	}
	nc := p3Client(t, endpointConfig{Host: host})
	adv := os.Getenv("E2E_ADVERTISE_ADDR")
	if adv == "" {
		adv = "eth0"
	}
	if err := nc.SwarmJoin(ctx, swarm.JoinRequest{
		ListenAddr: swarmDefaultListenAddr, AdvertiseAddr: adv,
		RemoteAddrs: []string{mgrAddr}, JoinToken: sw.JoinTokens.Worker,
	}); err != nil {
		t.Fatalf("out-of-band join of %s: %v", host, err)
	}
	t.Cleanup(func() { p3ForceLeave(host) })
	var nodeID string
	err = waitFor(ctx, 2*time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
		info, err := nc.Info(ctx)
		if err != nil || info.Swarm.LocalNodeState != swarm.LocalNodeStateActive || info.Swarm.NodeID == "" {
			return false, nil
		}
		nodeID = info.Swarm.NodeID
		_, _, err = mgr.NodeInspectWithRaw(ctx, nodeID)
		return err == nil, nil
	})
	if err != nil {
		t.Fatalf("out-of-band join of %s did not settle: %v", host, err)
	}
	t.Cleanup(func() {
		// If the provider did not remove it (failed test), drop it from the manager.
		c, err := newDockerClient(endpointConfig{Host: os.Getenv("DOCKER_HOST")})
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.NodeRemove(context.Background(), nodeID, swarm.NodeRemoveOptions{Force: true})
	})
	return nodeID
}

// p3Attr returns a primary attribute of addr from state.
func p3Attr(s *terraform.State, addr, key string) (string, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok || rs.Primary == nil {
		return "", fmt.Errorf("%s not in state", addr)
	}
	return rs.Primary.Attributes[key], nil
}

// p3ServiceTasks lists the tasks of service id (desired state any).
func p3ServiceTasks(ctx context.Context, c *client.Client, id string) ([]swarm.Task, error) {
	return c.TaskList(ctx, swarm.TaskListOptions{Filters: filters.NewArgs(filters.Arg("service", id))})
}

// p3HCLPath renders a filesystem path for HCL (forward slashes).
func p3HCLPath(p string) string { return filepath.ToSlash(p) }
