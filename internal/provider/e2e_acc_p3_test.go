package provider

// Pass 3: overlay data plane, repo_digest as a service image, job and global
// mode converge across real workers, and destroy with a worker that already
// left or is unreachable. Both tests run docker_swarm_init themselves, so
// they need the plain harness pass (manager outside any swarm).

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// e2eDataPlaneConfig: nginx pinned to worker1 (image = repo_digest), a
// replicated_job on worker2 that must fetch http://<web service>/ over the
// overlay (VIP, VXLAN between the two dind nodes), and a global service on
// every worker.
func e2eDataPlaneConfig(p e2eParams) string {
	return fmt.Sprintf(`
resource "docker_image" "web" {
  name         = %[2]q
  keep_locally = true
}

resource "docker_swarm_init" "dp" {
  advertise_addr = %[3]q
}

resource "docker_network" "dp" {
  name       = "dp-net-%[1]s"
  driver     = "overlay"
  attachable = true

  depends_on = [docker_swarm_init.dp]
}

resource "docker_swarm_node" "w1" {
  host            = %[5]q
  join_token      = docker_swarm_init.dp.worker_join_token
  manager_address = %[4]q
  advertise_addr  = %[3]q
}

resource "docker_swarm_node" "w2" {
  host            = %[6]q
  join_token      = docker_swarm_init.dp.worker_join_token
  manager_address = %[4]q
  advertise_addr  = %[3]q
}

resource "docker_service" "web" {
  name     = "dp-web-%[1]s"
  image    = docker_image.web.image_digest_ref
  replicas = 1
  networks = { (docker_network.dp.name) = {} }

  placement {
    constraints = ["node.hostname==worker1"]
  }

  converge_config {
    timeout = "5m"
  }

  depends_on = [docker_swarm_node.w1, docker_swarm_node.w2]
}

resource "docker_service" "probe" {
  name              = "dp-probe-%[1]s"
  image             = "busybox:1.36"
  mode              = "replicated_job"
  total_completions = 1
  networks          = { (docker_network.dp.name) = {} }
  command           = ["sh", "-c", "for i in $(seq 1 45); do wget -q -T 3 -O /dev/null http://dp-web-%[1]s/ && echo DATAPLANE-OK && exit 0; sleep 2; done; exit 1"]

  restart_policy {
    condition = "none"
  }

  placement {
    constraints = ["node.hostname==worker2"]
  }

  converge_config {
    timeout = "4m"
  }

  depends_on = [docker_service.web]
}

resource "docker_service" "agent" {
  name    = "dp-agent-%[1]s"
  image   = "busybox:1.36"
  mode    = "global"
  command = ["sleep", "3600"]

  placement {
    constraints = ["node.role==worker"]
  }

  converge_config {
    timeout = "5m"
  }

  depends_on = [docker_swarm_node.w1, docker_swarm_node.w2]
}
`, p.Suffix, e2eImage, p.AdvertiseAddr, p.ManagerAddr, p.Worker1Host, p.Worker2Host)
}

func e2eParamsFromEnv(t *testing.T) e2eParams {
	t.Helper()
	p := e2eParams{
		Suffix:        acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum),
		AdvertiseAddr: os.Getenv("E2E_ADVERTISE_ADDR"),
		ManagerAddr:   os.Getenv("E2E_MANAGER_ADDR"),
		Worker1Host:   os.Getenv("DOCKER_WORKER1_HOST"),
		Worker2Host:   os.Getenv("DOCKER_WORKER2_HOST"),
	}
	if p.Worker1Host == "" || p.Worker2Host == "" || p.ManagerAddr == "" {
		t.Skip("DOCKER_WORKER1_HOST, DOCKER_WORKER2_HOST and E2E_MANAGER_ADDR must be set (source test/harness/.env)")
	}
	if p.AdvertiseAddr == "" {
		p.AdvertiseAddr = "eth0"
	}
	return p
}

// e2eNodeHostname returns the hostname of the node a task ran on.
func e2eNodeHostname(ctx context.Context, t *testing.T, nodeID string) string {
	nRes, err := testAccDockerClient(t).NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
	if err != nil {
		return "?" + nodeID
	}
	n := nRes.Node
	return n.Description.Hostname
}

// TestAccE2EOverlayDataPlane: a task on worker2 reaches a service on worker1
// through the overlay VIP (Pass 2 map: L6 overlay data plane; L5 repo_digest
// as docker_service.image; L4 global converge; P2-C replicated_job converge).
func TestAccE2EOverlayDataPlane(t *testing.T) {
	testAccPreCheck(t)
	p := e2eParamsFromEnv(t)
	p3RequireInactive(t)
	p3RequireNodeInactive(t, p.Worker1Host)
	p3RequireNodeInactive(t, p.Worker2Host)
	t.Cleanup(func() { p3ForceLeave(p.Worker1Host); p3ForceLeave(p.Worker2Host) })
	cfg := e2eDataPlaneConfig(p)
	var none1, none2 string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             e2eCheckDestroy(t, &none1, &none2),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("docker_image.web", "image_digest_ref", regexp.MustCompile(`^nginx@sha256:[0-9a-f]{64}$`)),
					resource.TestCheckResourceAttrPair("docker_service.web", "image", "docker_image.web", "image_digest_ref"),
					resource.TestCheckResourceAttr("docker_service.probe", "mode", "replicated_job"),
					resource.TestCheckResourceAttr("docker_service.agent", "mode", "global"),
					func(s *terraform.State) error {
						ctx := context.Background()
						cli := testAccDockerClient(t)
						webID, _ := p3Attr(s, "docker_service.web", "id")
						probeID, _ := p3Attr(s, "docker_service.probe", "id")
						agentID, _ := p3Attr(s, "docker_service.agent", "id")

						svcRes, err := cli.ServiceInspect(ctx, webID, client.ServiceInspectOptions{})
						if err != nil {
							return err
						}
						svc := svcRes.Service
						if img := svc.Spec.TaskTemplate.ContainerSpec.Image; !strings.Contains(img, "@sha256:") {
							return fmt.Errorf("web service image %q is not digest-pinned", img)
						}
						webTasks, err := p3ServiceTasks(ctx, cli, webID)
						if err != nil {
							return err
						}
						var webNode string
						for _, tk := range webTasks {
							if tk.Status.State == swarm.TaskStateRunning {
								webNode = e2eNodeHostname(ctx, t, tk.NodeID)
							}
						}
						probeTasks, err := p3ServiceTasks(ctx, cli, probeID)
						if err != nil {
							return err
						}
						var probeDone, probeNode string
						for _, tk := range probeTasks {
							probeNode = e2eNodeHostname(ctx, t, tk.NodeID)
							if tk.Status.State == swarm.TaskStateComplete {
								probeDone = probeNode
							}
						}
						if probeDone == "" {
							return fmt.Errorf("probe job never completed (tasks: %d, last on %s)", len(probeTasks), probeNode)
						}
						if webNode != "worker1" || probeDone != "worker2" {
							return fmt.Errorf("data plane not cross-node: web on %q, probe completed on %q", webNode, probeDone)
						}
						t.Logf("overlay data plane: probe on %s fetched dp-web-%s (task on %s) through the VIP", probeDone, p.Suffix, webNode)
						return e2eCheckTasksSpread(t, &agentID, 2, 2)(s)
					},
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

// TestAccE2ESwarmLeaveStoppedWorker: before destroy, worker1 leaves the
// swarm on its own and worker2's daemon is stopped. Destroy must still
// succeed: SwarmLeave on a non-member is success, an unreachable node is
// skipped and force-removed once the manager reports it down (Pass 2 map:
// L3 SwarmLeave non-member / unreachable node at destroy).
func TestAccE2ESwarmLeaveStoppedWorker(t *testing.T) {
	testAccPreCheck(t)
	p := e2eParamsFromEnv(t)
	p3RequireCompose(t)
	p3RequireInactive(t)
	p3RequireNodeInactive(t, p.Worker1Host)
	p3RequireNodeInactive(t, p.Worker2Host)
	stopped := false
	t.Cleanup(func() {
		if stopped {
			if err := p3Compose("start", "worker2"); err != nil {
				t.Errorf("restarting worker2: %v", err)
			}
			if err := p3WaitDaemon(endpointConfig{Host: p.Worker2Host}, 2*time.Minute, nil); err != nil {
				t.Errorf("worker2 did not come back: %v", err)
			}
		}
		p3ForceLeave(p.Worker1Host)
		p3ForceLeave(p.Worker2Host)
		if err := p3WaitDaemon(endpointConfig{Host: p.Worker2Host}, time.Minute, func(s swarm.LocalNodeState) bool { return s == swarm.LocalNodeStateInactive }); err != nil {
			t.Errorf("worker2 still in a swarm after cleanup: %v", err)
		}
	})
	cfg := fmt.Sprintf(`
resource "docker_swarm_init" "lv" {
  advertise_addr = %[1]q
}

resource "docker_swarm_node" "w1" {
  host            = %[3]q
  join_token      = docker_swarm_init.lv.worker_join_token
  manager_address = %[2]q
  advertise_addr  = %[1]q
}

resource "docker_swarm_node" "w2" {
  host            = %[4]q
  join_token      = docker_swarm_init.lv.worker_join_token
  manager_address = %[2]q
  advertise_addr  = %[1]q
}
`, p.AdvertiseAddr, p.ManagerAddr, p.Worker1Host, p.Worker2Host)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			// worker2 is still stopped here; ask the manager and worker1.
			for _, h := range []string{os.Getenv("DOCKER_HOST"), p.Worker1Host} {
				c, err := newDockerClient(endpointConfig{Host: h})
				if err != nil {
					return err
				}
				infoRes, err := c.Info(context.Background(), client.InfoOptions{})
				info := infoRes.Info
				c.Close()
				if err != nil {
					return fmt.Errorf("%s: %w", h, err)
				}
				if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
					return fmt.Errorf("%s swarm state %q after destroy, want inactive", h, info.Swarm.LocalNodeState)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_swarm_node.w1", "node_id"),
					resource.TestCheckResourceAttrSet("docker_swarm_node.w2", "node_id"),
				),
			},
			{
				// Out of band: worker1 leaves by itself, worker2's daemon stops.
				// The manager still lists both nodes, so the plan stays empty;
				// the interesting part is the destroy that follows.
				PreConfig: func() {
					c, err := newDockerClient(endpointConfig{Host: p.Worker1Host})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					if _, err := c.SwarmLeave(context.Background(), client.SwarmLeaveOptions{Force: true}); err != nil {
						t.Fatalf("worker1 leave: %v", err)
					}
					if err := p3Compose("stop", "worker2"); err != nil {
						t.Fatalf("stopping worker2: %v", err)
					}
					stopped = true
				},
				Config:   cfg,
				PlanOnly: true,
			},
		},
	})
}
