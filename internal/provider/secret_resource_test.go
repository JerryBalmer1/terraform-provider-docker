package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestSecretBase64Validation(t *testing.T) {
	if err := secretValidateBase64("aGVsbG8="); err != nil {
		t.Errorf("valid: %v", err)
	}
	if err := secretValidateBase64("not base64!"); err == nil {
		t.Error("invalid accepted")
	}
	if err := secretValidateBase64(""); err != nil {
		t.Errorf("empty: %v", err)
	}
}

func TestSecretFakeDaemonInvalidData(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcFakeConfig(host, `
resource "docker_secret" "bad" {
  name = "bad"
  data = "plain text"
}
`),
			ExpectError: regexp.MustCompile(`Invalid base64`),
		}},
	})
}

func TestAccSecretBasic(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "docker_secret" "test" {
  name   = "tfacc-secret"
  data   = base64encode("s3cret")
  labels = { purpose = "test" }
}
`,
				Check: resource.TestCheckResourceAttrSet("docker_secret.test", "id"),
			},
			{ResourceName: "docker_secret.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"data"}},
		},
		CheckDestroy: func(s *terraform.State) error {
			c := testAccDockerClient(t)
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "docker_secret" {
					continue
				}
				if _, _, err := c.SecretInspectWithRaw(context.Background(), rs.Primary.ID); !isNotFound(err) {
					return fmt.Errorf("secret %s still exists (err=%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
	})
}
