package provider

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// regAccRegistry returns TF_ACC_REGISTRY (a registry:2 reachable by the
// daemon and the provider, e.g. "localhost:5000") or skips.
func regAccRegistry(t *testing.T) string {
	t.Helper()
	testAccPreCheck(t)
	reg := os.Getenv("TF_ACC_REGISTRY")
	if reg == "" {
		t.Skip("TF_ACC_REGISTRY not set; skipping registry test")
	}
	return reg
}

// TestAccRegistryManifestList pushes two single-platform images, combines
// them into one index with docker_registry_manifest_list, reads it back with
// docker_registry_image_manifests and imports it.
func TestAccRegistryManifestList(t *testing.T) {
	reg := regAccRegistry(t)
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": hAccConfig("FROM alpine:3.20\nARG V\nRUN echo $V > /v\n")})
	repo := "tfacc/ml-" + strings.ToLower(acctest.RandString(6))
	cfg := fmt.Sprintf(`
resource "docker_image" "a" {
  name = "%[1]s/%[2]s:a"
  build {
    context    = %[3]q
    args       = { V = "a" }
  }
}

resource "docker_image" "b" {
  name = "%[1]s/%[2]s:b"
  build {
    context    = %[3]q
    args       = { V = "b" }
  }
}

resource "docker_registry_image" "a" {
  name          = docker_image.a.name
  keep_remotely = true
}

resource "docker_registry_image" "b" {
  name          = docker_image.b.name
  keep_remotely = true
}

resource "docker_registry_manifest_list" "ml" {
  name = "%[1]s/%[2]s:multi"
  manifests = [
    { image = docker_registry_image.a.image_digest_ref },
    { image = docker_registry_image.b.image_digest_ref, architecture = "arm64", variant = "v8" },
  ]
}

data "docker_registry_image_manifests" "ml" {
  name = docker_registry_manifest_list.ml.image_digest_ref
}
`, reg, repo, p3HCLPath(dir))
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("docker_registry_manifest_list.ml", "digest", imgTestSHA),
					resource.TestCheckResourceAttr("docker_registry_manifest_list.ml", "platforms.#", "2"),
					resource.TestCheckResourceAttr("docker_registry_manifest_list.ml", "platforms.1", "linux/arm64/v8"),
					resource.TestMatchResourceAttr("docker_registry_manifest_list.ml", "media_type", regexp.MustCompile(`manifest\.list|image\.index`)),
					resource.TestCheckResourceAttrPair("data.docker_registry_image_manifests.ml", "digest", "docker_registry_manifest_list.ml", "digest"),
					resource.TestCheckResourceAttrPair("data.docker_registry_image_manifests.ml", "manifest_json_sha256", "docker_registry_manifest_list.ml", "content_sha256"),
					resource.TestCheckResourceAttrSet("data.docker_registry_image_manifests.ml", "image_config_json"),
				),
			},
			{
				ResourceName:            "docker_registry_manifest_list.ml",
				ImportState:             true,
				ImportStateId:           reg + "/" + repo + ":multi",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"manifests", "keep_remotely", "insecure_skip_verify"},
			},
		},
	})
}

// TestAccRegistryLogin logs in to TF_ACC_REGISTRY through the daemon. An
// open registry:2 accepts any credentials; set TF_ACC_REGISTRY_USERNAME and
// TF_ACC_REGISTRY_PASSWORD for one with auth.
func TestAccRegistryLogin(t *testing.T) {
	reg := regAccRegistry(t)
	user, pass := os.Getenv("TF_ACC_REGISTRY_USERNAME"), os.Getenv("TF_ACC_REGISTRY_PASSWORD")
	if user == "" {
		user, pass = "tfacc", "tfacc"
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccEphemeralFactories(),
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
ephemeral "docker_registry_login" "r" {
  server_address = %q
  username       = %q
  password       = %q
}

provider "echo" {
  data = ephemeral.docker_registry_login.r.auth.status
}

resource "echo" "r" {}
`, reg, user, pass),
			Check: resource.TestMatchResourceAttr("echo.r", "data", regexp.MustCompile(`(?i)login succeeded`)),
		}},
	})
}
