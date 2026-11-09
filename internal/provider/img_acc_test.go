package provider

// Pass 5 Lane A (WP img) acceptance tests. They skip without TF_ACC and run
// only through test/harness/Invoke-E2E.ps1 (plain pass, no swarm needed).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// TestAccImageContractOutputs pulls an image and reads it back through the
// image data sources, checking the Pass 5 contract outputs.
func TestAccImageContractOutputs(t *testing.T) {
	testAccPreCheck(t)
	// The normalized reference: docker.io/library/busybox:1.36 for the Hub
	// image, manager:12380/busybox:1.36 (already fully qualified) for the
	// harness registry copy.
	wantRef := actAccImage
	if wantRef == "busybox:1.36" {
		wantRef = "docker.io/library/" + wantRef
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_image" "b" {
  name = %[1]q
  # busybox:1.36 is shared by the whole acceptance suite (actAccImage):
  # destroying this test's resource must not delete it from under others.
  keep_locally = true
}
data "docker_image" "b" {
  name = docker_image.b.name
}
data "docker_image_history" "b" {
  name = docker_image.b.name
}
data "docker_images" "b" {
  reference = docker_image.b.name
}
`, actAccImage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestMatchResourceAttr("docker_image.b", "id", regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)),
				resource.TestCheckResourceAttr("docker_image.b", "image_ref", wantRef),
				resource.TestCheckResourceAttrSet("docker_image.b", "inspect_json_sha256"),
				resource.TestCheckResourceAttrSet("docker_image.b", "engine_api_version"),
				resource.TestCheckResourceAttrPair("data.docker_image.b", "id", "docker_image.b", "id"),
				resource.TestCheckResourceAttrSet("data.docker_image.b", "created"),
				resource.TestCheckResourceAttrSet("data.docker_image.b", "content_sha256"),
				resource.TestCheckResourceAttrPair("data.docker_image_history.b", "id", "docker_image.b", "id"),
				resource.TestCheckResourceAttrSet("data.docker_image_history.b", "history.#"),
				resource.TestCheckTypeSetElemAttrPair("data.docker_images.b", "ids.*", "docker_image.b", "id"),
			),
		}},
	})
}

// TestAccImageBuildStages builds a two-stage Dockerfile with the classic
// builder and checks the stages / dockerfile outputs and an extra tag.
func TestAccImageBuildStages(t *testing.T) {
	testAccPreCheck(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+actAccImage+" AS base\nRUN echo hi > /hi\nFROM base\nCMD [\"cat\", \"/hi\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_image" "s" {
  name = "tpd-acc/stages:1"
  build {
    context  = %q
    tags     = ["tpd-acc/stages:extra"]
    shm_size = 67108864
  }
}
`, filepath.ToSlash(dir)),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_image.s", "stages.#", "2"),
				resource.TestCheckResourceAttr("docker_image.s", "stages.0.name", "base"),
				resource.TestCheckResourceAttr("docker_image.s", "stages.1.base_stage", "base"),
				resource.TestCheckResourceAttrSet("docker_image.s", "dockerfile_sha256"),
				resource.TestCheckNoResourceAttr("docker_image.s", "image_digest_ref"),
			),
		}},
	})
}

// TestAccImageSearch searches Docker Hub through the daemon (needs Internet
// or the harness Hub mirror).
func TestAccImageSearch(t *testing.T) {
	testAccPreCheck(t)
	if os.Getenv("TPD_ACC_INTERNET") == "" {
		t.Skip("TPD_ACC_INTERNET not set; docker_image_search needs Docker Hub")
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "docker_image_search" "s" {
  term        = "busybox"
  limit       = 5
  is_official = true
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckTypeSetElemAttr("data.docker_image_search.s", "names.*", "busybox"),
				resource.TestCheckResourceAttrSet("data.docker_image_search.s", "content_sha256"),
			),
		}},
	})
}

// TestAccImageAttestations reads attestations; it needs Engine API 1.55 and
// the containerd image store, otherwise it expects the gate error.
func TestAccImageAttestations(t *testing.T) {
	testAccPreCheck(t)
	if os.Getenv("TPD_ACC_API_155") == "" {
		hAccTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: fmt.Sprintf(`
resource "docker_image" "b" {
  name = %q
  # busybox:1.36 is shared by the whole acceptance suite (actAccImage):
  # destroying this test's resource must not delete it from under others.
  keep_locally = true
}
data "docker_image_attestations" "a" {
  name = docker_image.b.name
}
`, actAccImage),
				ExpectError: regexp.MustCompile(`1\.55`),
			}},
		})
		return
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_image" "b" {
  name = %q
  # busybox:1.36 is shared by the whole acceptance suite (actAccImage):
  # destroying this test's resource must not delete it from under others.
  keep_locally = true
}
data "docker_image_attestations" "a" {
  name = docker_image.b.name
}
`, actAccImage),
			Check: resource.TestCheckResourceAttrSet("data.docker_image_attestations.a", "content_sha256"),
		}},
	})
}

// TestAccImageCommitAction commits a running container and writes the result
// object to result_path.
func TestAccImageCommitAction(t *testing.T) {
	testAccPreCheck(t)
	out := filepath.ToSlash(filepath.Join(t.TempDir(), "commit.json"))
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name    = "tpd-acc-commit"
  image   = %[1]q
  command = ["sh", "-c", "echo x > /marker; sleep 3600"]
}
action "docker_image_commit" "snap" {
  config {
    container_id = docker_container.c.id
    reference    = "tpd-acc/commit:1"
    result_path  = %[2]q
  }
}
resource "terraform_data" "snap" {
  input = docker_container.c.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_commit.snap]
    }
  }
}
`, actAccImage, out),
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(out)
				if err != nil {
					return err
				}
				if !regexp.MustCompile(`"id": "sha256:[0-9a-f]{64}"`).Match(b) {
					return fmt.Errorf("result: %s", b)
				}
				return nil
			},
		}},
	})
}
