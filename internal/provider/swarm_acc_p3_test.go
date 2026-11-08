package provider

// Pass 3 acceptance tests for docker_swarm_init (autolock, locked manager)
// and docker_swarm_node (import then apply, adopting a joined node, GPU-shaped
// placement). docker_swarm_init tests need a daemon outside any swarm (plain
// harness pass); docker_swarm_node tests are named TestAccSwarmNodeP3_* so the
// -SwarmInit pass regex (…|SwarmNode) picks them up.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func swarmP3CheckInactive(t *testing.T, ep endpointConfig) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, err := newDockerClient(ep)
		if err != nil {
			return err
		}
		defer c.Close()
		infoRes, err := c.Info(context.Background(), client.InfoOptions{})
		if err != nil {
			return err
		}
		info := infoRes.Info
		if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
			return fmt.Errorf("%s still in swarm: %s", ep.Host, info.Swarm.LocalNodeState)
		}
		return nil
	}
}

// swarmP3AutolockCheck: daemon AutoLockManagers == want and unlock_key in
// state equals SwarmGetUnlockKey (null when off).
func swarmP3AutolockCheck(t *testing.T, want bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c := testAccDockerClient(t)
		swRes, err := c.SwarmInspect(context.Background(), client.SwarmInspectOptions{})
		if err != nil {
			return err
		}
		sw := swRes.Swarm
		if sw.Spec.EncryptionConfig.AutoLockManagers != want {
			return fmt.Errorf("daemon AutoLockManagers=%v, want %v", sw.Spec.EncryptionConfig.AutoLockManagers, want)
		}
		key, _ := p3Attr(s, "docker_swarm_init.this", "unlock_key")
		if !want {
			if key != "" {
				return fmt.Errorf("unlock_key should be null with autolock off, got a value")
			}
			return nil
		}
		uk, err := c.SwarmGetUnlockKey(context.Background())
		if err != nil {
			return err
		}
		if key == "" || key != uk.Key {
			return fmt.Errorf("unlock_key in state does not match SwarmGetUnlockKey")
		}
		return nil
	}
}

func swarmP3AutolockConfig(lock bool) string {
	return fmt.Sprintf(`
resource "docker_swarm_init" "this" {
  advertise_addr = "127.0.0.1"
  autolock       = %t
}
`, lock)
}

// TestAccSwarmInitP3_autolockAtCreate: autolock = true at creation must stick
// (InitRequest plus Spec.EncryptionConfig; see the [P3-F] TODO: dockerd's
// initClusterSpec overwrites AutoLockManagers from the request Spec).
func TestAccSwarmInitP3_autolockAtCreate(t *testing.T) {
	testAccPreCheck(t)
	p3RequireInactive(t)
	t.Cleanup(func() { p3ForceLeave(os.Getenv("DOCKER_HOST")) })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             swarmP3CheckInactive(t, endpointConfig{Host: os.Getenv("DOCKER_HOST")}),
		Steps: []resource.TestStep{{
			Config: swarmP3AutolockConfig(true),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_swarm_init.this", "autolock", "true"),
				resource.TestCheckResourceAttrSet("docker_swarm_init.this", "unlock_key"),
				swarmP3AutolockCheck(t, true),
			),
		}},
	})
}

// TestAccSwarmInitP3_autolock: autolock toggled in place through SwarmUpdate:
// on -> unlock_key matches SwarmGetUnlockKey; off -> unlock_key null; on
// again; import (Pass 2 map: L3 autolock SwarmUpdate / unlock_key).
func TestAccSwarmInitP3_autolock(t *testing.T) {
	testAccPreCheck(t)
	p3RequireInactive(t)
	t.Cleanup(func() { p3ForceLeave(os.Getenv("DOCKER_HOST")) })
	toggle := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("docker_swarm_init.this", plancheck.ResourceActionUpdate),
	}}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             swarmP3CheckInactive(t, endpointConfig{Host: os.Getenv("DOCKER_HOST")}),
		Steps: []resource.TestStep{
			{
				Config: swarmP3AutolockConfig(false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_swarm_init.this", "autolock", "false"),
					resource.TestCheckNoResourceAttr("docker_swarm_init.this", "unlock_key"),
					swarmP3AutolockCheck(t, false),
				),
			},
			{
				Config:           swarmP3AutolockConfig(true),
				ConfigPlanChecks: toggle,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_swarm_init.this", "autolock", "true"),
					resource.TestCheckResourceAttrSet("docker_swarm_init.this", "unlock_key"),
					swarmP3AutolockCheck(t, true),
				),
			},
			{
				ResourceName:            "docker_swarm_init.this",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"advertise_addr"},
			},
			{
				Config:           swarmP3AutolockConfig(false),
				ConfigPlanChecks: toggle,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("docker_swarm_init.this", "unlock_key"),
					swarmP3AutolockCheck(t, false),
				),
			},
		},
	})
}

// TestAccSwarmInitP3_lockedManagerTLS: on the harness TLS daemon (so the
// provider's tls block is exercised too), autolock, then restart the daemon
// so the swarm comes back locked: refresh must keep the resource (warning,
// empty plan); after SwarmUnlock with the key from state, destroy leaves
// (Pass 2 map: L3 locked manager; L0 tls).
func TestAccSwarmInitP3_lockedManagerTLS(t *testing.T) {
	testAccPreCheck(t)
	ep := p3TLSEndpoint(t)
	p3RequireCompose(t)
	p3RequireNodeInactiveEP(t, ep)
	cfgFor := func(lock bool) string {
		return fmt.Sprintf(`
provider "docker" {
  host = %q
  tls {
    ca   = %q
    cert = %q
    key  = %q
  }
}

resource "docker_swarm_init" "locked" {
  advertise_addr = "eth0"
  autolock       = %t
}
`, ep.Host, p3HCLPath(ep.CA), p3HCLPath(ep.Cert), p3HCLPath(ep.Key), lock)
	}
	// Autolock is enabled through the in-place update (SwarmUpdate) because
	// autolock at creation is broken (see TestAccSwarmInitP3_autolockAtCreate).
	cfg := cfgFor(true)
	var unlockKey string
	t.Cleanup(func() {
		c, err := newDockerClient(ep)
		if err != nil {
			return
		}
		defer c.Close()
		ctx := context.Background()
		if res, err := c.Info(ctx, client.InfoOptions{}); err == nil && res.Info.Swarm.LocalNodeState == swarm.LocalNodeStateLocked && unlockKey != "" {
			_, _ = c.SwarmUnlock(ctx, client.SwarmUnlockOptions{Key: unlockKey})
		}
		if res, err := c.Info(ctx, client.InfoOptions{}); err == nil && res.Info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
			_, _ = c.SwarmLeave(ctx, client.SwarmLeaveOptions{Force: true})
		}
	})
	isState := func(want swarm.LocalNodeState) func(swarm.LocalNodeState) bool {
		return func(s swarm.LocalNodeState) bool { return s == want }
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             swarmP3CheckInactive(t, ep),
		Steps: []resource.TestStep{
			{Config: cfgFor(false)},
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_swarm_init.locked", "unlock_key"),
					func(s *terraform.State) error {
						unlockKey, _ = p3Attr(s, "docker_swarm_init.locked", "unlock_key")
						return nil
					},
				),
			},
			{
				PreConfig: func() {
					if err := p3Compose("restart", "tlsnode"); err != nil {
						t.Fatal(err)
					}
					if err := p3WaitDaemon(ep, 3*time.Minute, isState(swarm.LocalNodeStateLocked)); err != nil {
						t.Fatalf("tlsnode did not come back locked: %v", err)
					}
				},
				Config:   cfg,
				PlanOnly: true,
			},
			{
				PreConfig: func() {
					c, err := newDockerClient(ep)
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					if _, err := c.SwarmUnlock(context.Background(), client.SwarmUnlockOptions{Key: unlockKey}); err != nil {
						t.Fatalf("unlock with the key from state: %v", err)
					}
					if err := p3WaitDaemon(ep, 2*time.Minute, isState(swarm.LocalNodeStateActive)); err != nil {
						t.Fatal(err)
					}
				},
				Config:   cfg,
				PlanOnly: true,
			},
		},
	})
}

// p3RequireNodeInactiveEP is p3RequireNodeInactive for an endpoint with TLS.
func p3RequireNodeInactiveEP(t *testing.T, ep endpointConfig) {
	t.Helper()
	res, err := p3Client(t, ep).Info(context.Background(), client.InfoOptions{})
	if err != nil {
		t.Skipf("cannot reach %s: %v", ep.Host, err)
	}
	info := res.Info
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skipf("%s is already part of a swarm (%s)", ep.Host, info.Swarm.LocalNodeState)
	}
}

func swarmP3NodeConfig(host, mgrAddr, extra string) string {
	return fmt.Sprintf(`
data "docker_swarm_tokens" "t" {}

resource "docker_swarm_node" "w1" {
  host            = %q
  join_token      = data.docker_swarm_tokens.t.worker
  manager_address = %q
  advertise_addr  = "eth0"
%s
}
`, host, mgrAddr, extra)
}

// TestAccSwarmNodeP3_importThenApply: a node joined outside Terraform is
// imported; the first apply with the full config fills host/join_token/...
// IN PLACE (private-state flag), not by replacement; a later plan is empty
// (Pass 2 map: L3 swarm_node post-import in-place apply).
func TestAccSwarmNodeP3_importThenApply(t *testing.T) {
	testAccPreCheck(t)
	worker := p3Env(t, "DOCKER_WORKER1_HOST")[0]
	mgrAddr := swarmAccManagerAddr(t)
	p3RequireNodeInactive(t, worker)
	var nodeID string
	cfg := swarmP3NodeConfig(worker, mgrAddr, `  labels          = { imported = "yes" }`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             resource.ComposeTestCheckFunc(swarmAccCheckNodesGone, swarmP3CheckInactive(t, endpointConfig{Host: worker})),
		Steps: []resource.TestStep{
			{
				PreConfig:          func() { nodeID = p3JoinOutOfBand(t, worker, mgrAddr) },
				Config:             cfg,
				ResourceName:       "docker_swarm_node.w1",
				ImportState:        true,
				ImportStatePersist: true,
				ImportStateIdFunc:  func(*terraform.State) (string, error) { return nodeID, nil },
				ImportStateCheck: func(is []*terraform.InstanceState) error {
					if len(is) != 1 || is[0].Attributes["role"] != "worker" || is[0].Attributes["host"] != "" {
						return fmt.Errorf("unexpected imported state: %+v", is)
					}
					return nil
				},
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_swarm_node.w1", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_swarm_node.w1", "host", worker),
					resource.TestCheckResourceAttr("docker_swarm_node.w1", "labels.imported", "yes"),
					func(s *terraform.State) error {
						if id, _ := p3Attr(s, "docker_swarm_node.w1", "id"); id != nodeID {
							return fmt.Errorf("node replaced: id %s, imported %s", id, nodeID)
						}
						return nil
					},
				),
			},
			{
				// The flag is cleared by Update: a host change now replaces.
				Config:             swarmP3NodeConfig(worker+"/", mgrAddr, `  labels          = { imported = "yes" }`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPreRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_swarm_node.w1", plancheck.ResourceActionReplace),
				}},
			},
		},
	})
}

// TestAccSwarmNodeP3_adoptJoined: docker_swarm_node.Create on a daemon that
// is already an active member of this swarm adopts it (same node ID) instead
// of failing (Pass 2 map: L3 swarm_node adopts an already-joined node).
func TestAccSwarmNodeP3_adoptJoined(t *testing.T) {
	testAccPreCheck(t)
	worker := p3Env(t, "DOCKER_WORKER1_HOST")[0]
	mgrAddr := swarmAccManagerAddr(t)
	p3RequireNodeInactive(t, worker)
	var nodeID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             resource.ComposeTestCheckFunc(swarmAccCheckNodesGone, swarmP3CheckInactive(t, endpointConfig{Host: worker})),
		Steps: []resource.TestStep{
			{
				PreConfig: func() { nodeID = p3JoinOutOfBand(t, worker, mgrAddr) },
				Config:    swarmP3NodeConfig(worker, mgrAddr, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_swarm_node.w1", "role", "worker"),
					func(s *terraform.State) error {
						if id, _ := p3Attr(s, "docker_swarm_node.w1", "node_id"); id != nodeID {
							return fmt.Errorf("node_id %s, want the already-joined %s", id, nodeID)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccSwarmNodeP3_gpuPlacement: worker3 advertises engine label gpu=nvidia
// and generic resource NVIDIA-GPU=GPU-0 (no real GPU). Joined with a node
// label, a service constrained to it lands there (harness item: NVIDIA-shaped
// node for placement tests).
func TestAccSwarmNodeP3_gpuPlacement(t *testing.T) {
	testAccPreCheck(t)
	w3 := p3Env(t, "DOCKER_WORKER3_HOST")[0]
	mgrAddr := swarmAccManagerAddr(t)
	p3RequireNodeInactive(t, w3)
	t.Cleanup(func() { p3ForceLeave(w3) })
	name := acctest.RandomWithPrefix("tfacc-gpu")
	cfg := fmt.Sprintf(`
data "docker_swarm_tokens" "t" {}

resource "docker_swarm_node" "gpu" {
  host            = %[1]q
  join_token      = data.docker_swarm_tokens.t.worker
  manager_address = %[2]q
  advertise_addr  = "eth0"
  labels          = { gpu = "nvidia" }
}

resource "docker_service" "gpu" {
  name    = %[3]q
  image   = "busybox:1.36"
  command = ["sleep", "3600"]

  placement {
    constraints = ["engine.labels.gpu==nvidia", "node.labels.gpu==nvidia"]
  }

  converge_config {
    timeout = "3m"
  }

  depends_on = [docker_swarm_node.gpu]
}
`, w3, mgrAddr, name)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             resource.ComposeTestCheckFunc(swarmAccCheckNodesGone, swarmP3CheckInactive(t, endpointConfig{Host: w3})),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: func(s *terraform.State) error {
				ctx := context.Background()
				c := testAccDockerClient(t)
				nodeID, _ := p3Attr(s, "docker_swarm_node.gpu", "node_id")
				nRes, err := c.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
				if err != nil {
					return err
				}
				n := nRes.Node
				if n.Description.Engine.Labels["gpu"] != "nvidia" {
					return fmt.Errorf("worker3 engine labels %v lack gpu=nvidia", n.Description.Engine.Labels)
				}
				var gr []string
				for _, g := range n.Description.Resources.GenericResources {
					if g.DiscreteResourceSpec != nil {
						gr = append(gr, fmt.Sprintf("%s=%d", g.DiscreteResourceSpec.Kind, g.DiscreteResourceSpec.Value))
					}
					if g.NamedResourceSpec != nil {
						gr = append(gr, g.NamedResourceSpec.Kind+"="+g.NamedResourceSpec.Value)
					}
				}
				if !strings.Contains(strings.Join(gr, ","), "NVIDIA-GPU=GPU-0") {
					return fmt.Errorf("worker3 generic resources %v lack NVIDIA-GPU=GPU-0", gr)
				}
				svcID, _ := p3Attr(s, "docker_service.gpu", "id")
				tasks, err := p3ServiceTasks(ctx, c, svcID)
				if err != nil {
					return err
				}
				for _, tk := range tasks {
					if tk.Status.State == swarm.TaskStateRunning {
						if tk.NodeID != nodeID {
							return fmt.Errorf("task on node %s, want worker3 %s", tk.NodeID, nodeID)
						}
						return nil
					}
				}
				return fmt.Errorf("no running task (%d tasks)", len(tasks))
			},
		}},
	})
}
