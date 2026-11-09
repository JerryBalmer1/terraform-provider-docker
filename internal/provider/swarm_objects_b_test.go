package provider

// Pass 5 Lane B: in-place label updates of docker_secret / docker_config and
// the outputs-contract attributes of the swarm object resources, against
// the fake swarm manager (newSvcFakeDaemon).

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// swarmBStateAttr returns attribute key of the resource addr in s.
func swarmBStateAttr(s *terraform.State, addr, key string) (string, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return "", fmt.Errorf("%s not in state", addr)
	}
	v, ok := rs.Primary.Attributes[key]
	if !ok {
		return "", fmt.Errorf("%s.%s not in state", addr, key)
	}
	return v, nil
}

func TestSecretConfigLabelsUpdateInPlace(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	cfg := func(team string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_secret" "s" {
  name   = "s1"
  data   = base64encode("pw")
  labels = { team = %[1]q }
}

resource "docker_config" "c" {
  name   = "c1"
  data   = base64encode("listen 80;")
  labels = { team = %[1]q }
}
`, team))
	}
	var firstIndex string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_secret.s", "inspect_json"),
					resource.TestCheckResourceAttrSet("docker_secret.s", "content_sha256"),
					resource.TestCheckResourceAttr("docker_secret.s", "inspect.spec.name", "s1"),
					resource.TestCheckResourceAttr("docker_secret.s", "docker_server_version", "29.0.0-fake"),
					resource.TestCheckResourceAttr("docker_config.c", "inspect.spec.labels.team", "a"),
					func(s *terraform.State) error {
						v, err := swarmBStateAttr(s, "docker_secret.s", "version_index")
						firstIndex = v
						if err != nil || v == "" || v == "0" {
							return fmt.Errorf("version_index = %q (%v)", v, err)
						}
						raw, err := swarmBStateAttr(s, "docker_config.c", "inspect_json")
						if err != nil {
							return err
						}
						var m map[string]any
						if err := json.Unmarshal([]byte(raw), &m); err != nil || m["ID"] == nil {
							return fmt.Errorf("inspect_json is not the Engine body: %v %v", m, err)
						}
						return nil
					},
				),
			},
			{
				Config: cfg("b"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_secret.s", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("docker_config.c", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_secret.s", "labels.team", "b"),
					resource.TestCheckResourceAttr("docker_config.c", "inspect.spec.labels.team", "b"),
					func(s *terraform.State) error {
						f.mu.Lock()
						defer f.mu.Unlock()
						for _, sec := range f.secrets {
							if sec.Spec.Labels["team"] != "b" || sec.Spec.Labels[labelManagedBy] != labelManagedByValue {
								return fmt.Errorf("secret labels = %v", sec.Spec.Labels)
							}
						}
						for _, c := range f.configs {
							if c.Spec.Labels["team"] != "b" || string(c.Spec.Data) != "listen 80;" {
								return fmt.Errorf("config = %+v", c.Spec)
							}
						}
						v, _ := swarmBStateAttr(s, "docker_secret.s", "version_index")
						if v == firstIndex {
							return fmt.Errorf("version_index did not change: %s", v)
						}
						return nil
					},
				),
			},
			{
				// Content still forces replacement.
				Config: svcFakeConfig(host, `
resource "docker_secret" "s" {
  name   = "s1"
  data   = base64encode("pw2")
  labels = { team = "b" }
}

resource "docker_config" "c" {
  name   = "c1"
  data   = base64encode("listen 80;")
  labels = { team = "b" }
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_secret.s", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
	})
}

func TestServiceContractOutputs(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcFakeConfig(host, `
resource "docker_service" "web" {
  name  = "web"
  image = "nginx:1.27"
  networks = { ext_net = { aliases = ["www"] } }
  endpoint_spec {
    ports {
      name           = "http"
      target_port    = 80
      published_port = 8080
    }
  }
}
`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("docker_service.web", "inspect_json"),
				resource.TestCheckResourceAttrSet("docker_service.web", "content_sha256"),
				resource.TestCheckResourceAttrSet("docker_service.web", "version_index"),
				resource.TestCheckResourceAttr("docker_service.web", "inspect.spec.name", "web"),
				resource.TestCheckResourceAttr("docker_service.web", "published_ports.0.published_port", "8080"),
				resource.TestCheckResourceAttr("docker_service.web", "published_ports.0.name", "http"),
				resource.TestCheckResourceAttr("docker_service.web", "image_digest_ref", "nginx@sha256:"+repeatA(64)),
				resource.TestCheckResourceAttr("docker_service.web", "networks.ext_net.aliases.0", "www"),
				resource.TestCheckResourceAttr("docker_service.web", "inspect.spec.task_template.networks.0.aliases.0", "www"),
				resource.TestCheckResourceAttr("docker_service.web", "deploy.mode", "replicated"),
				resource.TestCheckResourceAttr("docker_service.web", "deploy.replicas", "1"),
				resource.TestCheckResourceAttr("docker_service.web", "deploy.endpoint_mode", "vip"),
				resource.TestCheckResourceAttrSet("docker_service.web", "compose_spec_version"),
				resource.TestCheckResourceAttrSet("docker_service.web", "compose_go_version"),
			),
		}},
	})
}

func repeatA(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestSvcImageDigestRef(t *testing.T) {
	cases := map[string]string{
		"nginx:1.27@sha256:" + repeatA(64):         "nginx@sha256:" + repeatA(64),
		"ghcr.io/org/app:v1@sha256:" + repeatA(64): "ghcr.io/org/app@sha256:" + repeatA(64),
		"nginx:1.27":                             "",
		"not a ref":                              "",
		"localhost:5000/x@sha256:" + repeatA(64): "localhost:5000/x@sha256:" + repeatA(64),
		"docker.io/library/redis:7@sha256:" + repeatA(64): "redis@sha256:" + repeatA(64),
	}
	for in, want := range cases {
		if got := svcImageDigestRef(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
