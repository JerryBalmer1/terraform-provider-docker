package provider

// Acceptance tests for the swarm actions (Terraform >= 1.14). They skip
// unless DOCKER_HOST is a swarm manager (the harness's -SwarmInit pass).
// Services are created through the client, not docker_service, so the
// actions' changes do not show up as drift in the post-apply plan.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// actAccTrigger is an action block plus a terraform_data that runs it once
// on create.
func actAccTrigger(kind, name, body string) string {
	return fmt.Sprintf(`
action %[1]q %[2]q {
  config {
%[3]s
  }
}

resource "terraform_data" %[2]q {
  input = %[2]q
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.%[1]s.%[2]s]
    }
  }
}
`, kind, name, body)
}

func TestAccActionSwarmRotateTokens_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	beforeRes, err := c.SwarmInspect(context.Background(), client.SwarmInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := beforeRes.Swarm
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: actAccTrigger("docker_swarm_rotate_tokens", "r", `    worker  = true
    manager = false`),
			Check: func(*terraform.State) error {
				afterRes, err := c.SwarmInspect(context.Background(), client.SwarmInspectOptions{})
				if err != nil {
					return err
				}
				after := afterRes.Swarm
				if after.JoinTokens.Worker == before.JoinTokens.Worker {
					return fmt.Errorf("worker token not rotated")
				}
				if after.JoinTokens.Manager != before.JoinTokens.Manager {
					return fmt.Errorf("manager token rotated although manager = false")
				}
				return nil
			},
		}},
	})
}

// actAccPickNode prefers a worker so the manager keeps running tasks.
func actAccPickNode(t *testing.T, c *client.Client) swarm.Node {
	t.Helper()
	nodesRes, err := c.NodeList(context.Background(), client.NodeListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nodes := nodesRes.Items
	for _, n := range nodes {
		if n.Spec.Role == swarm.NodeRoleWorker && n.Status.State == swarm.NodeStateReady {
			return n
		}
	}
	return nodes[0]
}

func actAccNodeAvailability(c *client.Client, id string, want swarm.NodeAvailability) resource.TestCheckFunc {
	return func(*terraform.State) error {
		nRes, err := c.NodeInspect(context.Background(), id, client.NodeInspectOptions{})
		if err != nil {
			return err
		}
		n := nRes.Node
		if n.Spec.Availability != want {
			return fmt.Errorf("node %s availability %s, want %s", n.Description.Hostname, n.Spec.Availability, want)
		}
		return nil
	}
}

func TestAccActionNodeDrainActivate_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	node := actAccPickNode(t, c)
	// Whatever happens, leave the node schedulable for the next tests.
	t.Cleanup(func() {
		active := swarm.NodeAvailabilityActive
		_ = swarmUpdateNode(context.Background(), c, node.ID, swarmNodeSpecChange{Availability: &active})
	})
	drain := actAccTrigger("docker_node_drain", "d", fmt.Sprintf(`    node_id = %q
    timeout = "2m"`, node.Description.Hostname))
	activate := actAccTrigger("docker_node_activate", "a", fmt.Sprintf(`    node_id = %q`, node.ID))
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: drain,
				Check: resource.ComposeAggregateTestCheckFunc(
					actAccNodeAvailability(c, node.ID, swarm.NodeAvailabilityDrain),
					func(*terraform.State) error {
						left, err := actNodeActiveTasks(context.Background(), c, node.ID)
						if err != nil {
							return err
						}
						if len(left) > 0 {
							return fmt.Errorf("tasks still active on drained node: %v", left)
						}
						return nil
					},
				),
			},
			{
				Config: drain + activate,
				Check:  actAccNodeAvailability(c, node.ID, swarm.NodeAvailabilityActive),
			},
		},
	})
}

func TestAccActionServiceScaleForceUpdateRollback_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	name := acctest.RandomWithPrefix("tfacc-svcact")
	one := uint64(1)
	created, err := c.ServiceCreate(context.Background(), client.ServiceCreateOptions{Spec: swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
			Image: actAccImage, Command: []string{"sh", "-c", "sleep 3600"},
		}},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &one}},
	}})
	if err != nil {
		t.Fatalf("creating service: %v", err)
	}
	t.Cleanup(func() { _ = svcRemoveAndWait(context.Background(), c, created.ID, 2*time.Minute) })
	if err := actServiceWait(context.Background(), c, created.ID, 3*time.Minute); err != nil {
		t.Fatalf("seed service did not converge: %v", err)
	}
	inspect := func() (swarm.Service, error) {
		sRes, err := c.ServiceInspect(context.Background(), created.ID, client.ServiceInspectOptions{})
		s := sRes.Service
		return s, err
	}
	scale := actAccTrigger("docker_service_scale", "s", fmt.Sprintf(`    service_id = %q
    replicas = 2
    timeout  = "3m"`, name))
	force := actAccTrigger("docker_service_force_update", "f", fmt.Sprintf(`    service_id = %q
    timeout = "3m"`, name))
	rollback := actAccTrigger("docker_service_rollback", "r", fmt.Sprintf(`    service_id = %q
    timeout = "3m"`, created.ID))
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: scale,
				Check: func(*terraform.State) error {
					s, err := inspect()
					if err != nil {
						return err
					}
					if r := s.Spec.Mode.Replicated.Replicas; r == nil || *r != 2 {
						return fmt.Errorf("replicas not scaled to 2: %v", r)
					}
					return nil
				},
			},
			{
				Config: scale + force,
				Check: func(*terraform.State) error {
					s, err := inspect()
					if err != nil {
						return err
					}
					if s.Spec.TaskTemplate.ForceUpdate != 1 {
						return fmt.Errorf("ForceUpdate = %d, want 1", s.Spec.TaskTemplate.ForceUpdate)
					}
					return nil
				},
			},
			{
				// The previous spec is the one before the force update.
				Config: scale + force + rollback,
				Check: func(*terraform.State) error {
					s, err := inspect()
					if err != nil {
						return err
					}
					if s.Spec.TaskTemplate.ForceUpdate != 0 {
						return fmt.Errorf("ForceUpdate = %d after rollback, want 0", s.Spec.TaskTemplate.ForceUpdate)
					}
					if s.UpdateStatus == nil || s.UpdateStatus.State != swarm.UpdateStateRollbackCompleted {
						return fmt.Errorf("update status %+v, want rollback_completed", s.UpdateStatus)
					}
					return nil
				},
			},
		},
	})
}
