package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestDockerConfigFakeDaemonReplace checks that changing data replaces the
// config (Swarm configs are immutable) and import by name fills data.
func TestDockerConfigFakeDaemonReplace(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	cfg := func(data string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_config" "c" {
  name = "app.conf"
  data = base64encode(%q)
}
`, data))
	}
	var firstID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("a=1"),
				Check: func(s *terraform.State) error {
					firstID = s.RootModule().Resources["docker_config.c"].Primary.ID
					f.mu.Lock()
					defer f.mu.Unlock()
					if string(f.configs[firstID].Spec.Data) != "a=1" || f.configs[firstID].Spec.Labels[labelManagedBy] != labelManagedByValue {
						return fmt.Errorf("stored config = %+v", f.configs[firstID].Spec)
					}
					return nil
				},
			},
			{
				Config: cfg("a=2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_config.c", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: func(s *terraform.State) error {
					if s.RootModule().Resources["docker_config.c"].Primary.ID == firstID {
						return fmt.Errorf("config was not replaced")
					}
					return nil
				},
			},
			{ResourceName: "docker_config.c", ImportState: true, ImportStateId: "app.conf", ImportStateVerify: true},
		},
	})
}

func TestAccDockerConfigBasic(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "docker_config" "test" {
  name = "tfacc-config"
  data = base64encode("key=value")
}
`,
				Check: resource.TestCheckResourceAttrSet("docker_config.test", "id"),
			},
			{ResourceName: "docker_config.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"data"}},
		},
		CheckDestroy: func(s *terraform.State) error {
			c := testAccDockerClient(t)
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "docker_config" {
					continue
				}
				if _, _, err := c.ConfigInspectWithRaw(context.Background(), rs.Primary.ID); !isNotFound(err) {
					return fmt.Errorf("config %s still exists (err=%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
	})
}
