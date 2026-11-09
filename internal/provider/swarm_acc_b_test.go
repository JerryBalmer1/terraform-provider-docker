package provider

// Pass 5 Lane B acceptance tests. Every test here needs DOCKER_HOST to be a
// swarm manager (the harness's -SwarmInit pass) and skips otherwise; the
// action tests also need Terraform >= 1.14.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var swarmBActionVersionChecks = []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)}

// TestAccNodeResource_basic manages labels and availability of an already
// joined node with docker_node, imports it by hostname, and checks that
// destroy leaves the node in the swarm.
func TestAccNodeResource_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	node := actAccPickNode(t, c)
	t.Cleanup(func() {
		active := swarm.NodeAvailabilityActive
		res, err := c.NodeInspect(context.Background(), node.ID, client.NodeInspectOptions{})
		if err != nil {
			return
		}
		labels := res.Node.Spec.Labels
		delete(labels, "tfacc_node")
		_ = swarmUpdateNode(context.Background(), c, node.ID, swarmNodeSpecChange{Availability: &active, Labels: &labels})
	})
	cfg := func(v, avail string) string {
		return fmt.Sprintf(`
resource "docker_node" "n" {
  node_id      = %q
  availability = %q
  labels       = { tfacc_node = %q }
}
`, node.Description.Hostname, avail, v)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("1", "pause"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_node.n", "id", node.ID),
					resource.TestCheckResourceAttr("docker_node.n", "inspect.spec.labels.tfacc_node", "1"),
					actAccNodeAvailability(c, node.ID, swarm.NodeAvailabilityPause),
				),
			},
			{
				Config: cfg("2", "active"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_node.n", "labels.tfacc_node", "2"),
					actAccNodeAvailability(c, node.ID, swarm.NodeAvailabilityActive),
				),
			},
			{ResourceName: "docker_node.n", ImportState: true, ImportStateId: node.Description.Hostname, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"version_index"}},
		},
		CheckDestroy: func(*terraform.State) error {
			if _, err := c.NodeInspect(context.Background(), node.ID, client.NodeInspectOptions{}); err != nil {
				return fmt.Errorf("destroy must keep the node in the swarm: %v", err)
			}
			return nil
		},
	})
}

// TestAccActionNodePromoteDemote_basic promotes a worker to manager and
// demotes it again.
func TestAccActionNodePromoteDemote_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	node := actAccPickNode(t, c)
	if node.Spec.Role != swarm.NodeRoleWorker {
		t.Skip("needs a ready worker node")
	}
	t.Cleanup(func() {
		worker := swarm.NodeRoleWorker
		_ = swarmUpdateNode(context.Background(), c, node.ID, swarmNodeSpecChange{Role: &worker})
	})
	out := filepath.Join(t.TempDir(), "promote.json")
	promote := actAccTrigger("docker_node_promote", "p", fmt.Sprintf("    node_id     = %q\n    result_path = %q", node.Description.Hostname, filepath.ToSlash(out)))
	demote := actAccTrigger("docker_node_demote", "d", fmt.Sprintf("    node_id = %q", node.ID))
	role := func(want swarm.NodeRole) resource.TestCheckFunc {
		return func(*terraform.State) error {
			res, err := c.NodeInspect(context.Background(), node.ID, client.NodeInspectOptions{})
			if err != nil {
				return err
			}
			if res.Node.Spec.Role != want {
				return fmt.Errorf("role %s, want %s", res.Node.Spec.Role, want)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   swarmBActionVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: promote, Check: resource.ComposeAggregateTestCheckFunc(role(swarm.NodeRoleManager), func(*terraform.State) error {
				b, err := os.ReadFile(out)
				if err != nil || !strings.Contains(string(b), `"role": "manager"`) {
					return fmt.Errorf("result file: %s %v", b, err)
				}
				return nil
			})},
			{Config: promote + demote, Check: role(swarm.NodeRoleWorker)},
		},
	})
}

// TestAccActionSwarmRotateCA_basic rotates the root CA and waits for it.
func TestAccActionSwarmRotateCA_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	before, err := c.SwarmInspect(context.Background(), client.SwarmInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   swarmBActionVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: actAccTrigger("docker_swarm_rotate_ca", "ca", `    timeout = "10m"`),
			Check: func(*terraform.State) error {
				after, err := c.SwarmInspect(context.Background(), client.SwarmInspectOptions{})
				if err != nil {
					return err
				}
				if after.Swarm.Spec.CAConfig.ForceRotate != before.Swarm.Spec.CAConfig.ForceRotate+1 || after.Swarm.RootRotationInProgress {
					return fmt.Errorf("ForceRotate %d -> %d, in progress %v", before.Swarm.Spec.CAConfig.ForceRotate,
						after.Swarm.Spec.CAConfig.ForceRotate, after.Swarm.RootRotationInProgress)
				}
				return nil
			},
		}},
	})
}

// TestAccActionSwarmUnlockKey_basic turns autolock on, rotates the unlock
// key, runs docker_swarm_unlock against the (unlocked) manager, and turns
// autolock off again.
func TestAccActionSwarmUnlockKey_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	ctx := context.Background()
	setAutolock := func(on bool) error {
		return actSwarmUpdate(ctx, c, client.SwarmUpdateOptions{}, func(s *swarm.Spec) error {
			s.EncryptionConfig.AutoLockManagers = on
			return nil
		})
	}
	if err := setAutolock(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = setAutolock(false) })
	oldKey, err := c.SwarmGetUnlockKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "key.json")
	rotate := actAccTrigger("docker_swarm_rotate_unlock_key", "k", fmt.Sprintf("    result_path = %q", filepath.ToSlash(out)))
	unlock := actAccTrigger("docker_swarm_unlock", "u", `    unlock_key = "SWMKEY-1-unused-the-manager-is-not-locked"`)
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   swarmBActionVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: rotate + unlock,
			Check: func(*terraform.State) error {
				newKey, err := c.SwarmGetUnlockKey(ctx)
				if err != nil {
					return err
				}
				b, err := os.ReadFile(out)
				if err != nil {
					return err
				}
				if newKey.Key == oldKey.Key || !strings.Contains(string(b), newKey.Key) {
					return fmt.Errorf("unlock key not rotated or not in the result file")
				}
				return nil
			},
		}},
	})
}
