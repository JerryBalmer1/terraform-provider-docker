package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
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
	hAccTest(t, resource.TestCase{
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
				if _, err := c.ConfigInspect(context.Background(), rs.Primary.ID, client.ConfigInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("config %s still exists (err=%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
	})
}

// TestDockerConfigCreateFromDataWO checks data_wo reaches the daemon, is not
// stored, and Read does not back-fill data (that would plan a replacement).
func TestDockerConfigCreateFromDataWO(t *testing.T) {
	f, pd := newP3eFake(t)
	var got swarm.ConfigSpec
	f.on("POST /configs/create", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		p3eWriteJSON(w, http.StatusCreated, map[string]string{"ID": "cfg1"})
	})
	f.on("GET /configs/cfg1", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, http.StatusOK, swarm.Config{ID: "cfg1", Spec: got})
	})
	state, _, diags := p3eSecretLikeCreate(t, NewConfigResource(), pd, map[string]tftypes.Value{
		"name":            p3eStr("app.conf"),
		"data_wo":         p3eStr("YT0x"),
		"data_wo_version": tftypes.NewValue(tftypes.Number, 1),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if string(got.Data) != "a=1" {
		t.Errorf("daemon got %q", got.Data)
	}
	var m dockerConfigResourceModel
	state.Get(context.Background(), &m)
	if !m.Data.IsNull() || !m.DataWO.IsNull() {
		t.Errorf("state data = %s, data_wo = %s; both must stay null", m.Data, m.DataWO)
	}
}

func TestAccDockerConfigWriteOnly(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	cfg := func(v int) string {
		return fmt.Sprintf(`
resource "docker_config" "wo" {
  name            = "tfacc-config-wo-%d"
  data_wo         = base64encode("k=%d")
  data_wo_version = %d
}
`, v, v, v)
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps: []resource.TestStep{
			{Config: cfg(1), Check: resource.TestCheckNoResourceAttr("docker_config.wo", "data")},
			{Config: cfg(2), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("docker_config.wo", plancheck.ResourceActionReplace),
			}}},
		},
	})
}
