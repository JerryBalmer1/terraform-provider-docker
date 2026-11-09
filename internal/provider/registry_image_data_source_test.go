package provider

import (
	"context"
	"testing"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/image"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// imgTestInspect builds an inspect response for unit tests.
func imgTestInspect(id string, tags, digests []string, labels map[string]string) image.InspectResponse {
	return image.InspectResponse{
		ID: id, RepoTags: tags, RepoDigests: digests,
		Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: labels}},
	}
}

func TestRegistryImageDataSourceSchema(t *testing.T) {
	var resp datasource.SchemaResponse
	NewRegistryImageDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	for _, a := range []string{"name", "id", "digest", "image_digest_ref", "platforms", "inspect_json"} {
		if _, ok := resp.Schema.Attributes[a]; !ok {
			t.Errorf("missing attribute %s", a)
		}
	}
}

// TestAccRegistryImageDataSource asks Docker Hub for a public tag's digest.
func TestAccRegistryImageDataSource(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "docker_registry_image" "alpine" {
  name = "alpine:3.20"
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestMatchResourceAttr("data.docker_registry_image.alpine", "digest", imgTestSHA),
			),
		}},
	})
}
