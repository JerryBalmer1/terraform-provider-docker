package provider

import (
	"fmt"
	"os"
	"testing"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccBuildxBuilderCloud_live creates a docker_buildx_builder with
// driver = "cloud" against the real Docker Build Cloud service: login at
// auth.docker.io with Docker Hub credentials, then ListWorkers over
// build-cloud.docker.com:443. It does not run on the dind harness (listed in
// reference/_gen/deferred.d/acceptance.json).
//
// Gated by TF_ACC=1 and TF_ACC_BUILD_CLOUD=1. Needs:
//
//	TF_ACC_BUILD_CLOUD_BUILDER  <org>/<builder> of a Build Cloud builder the account may use
//	DOCKER_REGISTRY_USER        Docker Hub username
//	DOCKER_REGISTRY_PASS        Docker Hub password or personal access token
//
// Run: TF_ACC=1 TF_ACC_BUILD_CLOUD=1 go test -run TestAccBuildxBuilderCloud_live ./internal/provider -count=1 -v
func TestAccBuildxBuilderCloud_live(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance test")
	}
	if os.Getenv("TF_ACC_BUILD_CLOUD") != "1" {
		t.Skip("TF_ACC_BUILD_CLOUD=1 not set; skipping the live Docker Build Cloud test")
	}
	builder := os.Getenv("TF_ACC_BUILD_CLOUD_BUILDER")
	if builder == "" || os.Getenv("DOCKER_REGISTRY_USER") == "" || os.Getenv("DOCKER_REGISTRY_PASS") == "" {
		t.Skip("TF_ACC_BUILD_CLOUD_BUILDER, DOCKER_REGISTRY_USER and DOCKER_REGISTRY_PASS are needed for the live Docker Build Cloud test")
	}
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{{
			Config: fmt.Sprintf(`
provider "docker" {
  registry_auth {
    address = "docker.io"
  }
}

resource "docker_buildx_builder" "cloud" {
  name     = "tfacc-cloud"
  driver   = "cloud"
  endpoint = %q
}`, builder),
			Check: tfresource.ComposeAggregateTestCheckFunc(
				tfresource.TestCheckResourceAttr("docker_buildx_builder.cloud", "id", bkCloudEndpointPrefix+builder),
				tfresource.TestCheckResourceAttrSet("docker_buildx_builder.cloud", "platforms.0"),
				tfresource.TestCheckResourceAttrSet("docker_buildx_builder.cloud", "buildkit_version"),
			),
		}},
	})
}
