package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestImageDataSourceRepoDigest(t *testing.T) {
	m := imageDataSourceModel{}
	m.Name = stringOrNull("ghcr.io/o/a:1")
	imgDataSourceApply(&m, imgTestInspect("sha256:1", []string{"ghcr.io/o/a:1"}, []string{"nginx@sha256:x", "ghcr.io/o/a@sha256:y"}, map[string]string{"k": "v"}))
	if m.RepoDigest.ValueString() != "ghcr.io/o/a@sha256:y" {
		t.Errorf("repo digest %v", m.RepoDigest)
	}
	if v, ok := m.Labels.Elements()["k"]; !ok || v.String() != `"v"` {
		t.Errorf("labels %v", m.Labels)
	}
}

// TestAccImageDataSource reads an image pulled by a docker_image resource.
func TestAccImageDataSource(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_image" "busybox" {
  name = "busybox:1.36"
}
data "docker_image" "busybox" {
  name = docker_image.busybox.name
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.docker_image.busybox", "id", "docker_image.busybox", "image_id"),
				resource.TestCheckTypeSetElemAttr("data.docker_image.busybox", "repo_tags.*", "busybox:1.36"),
				resource.TestCheckResourceAttrSet("data.docker_image.busybox", "repo_digest"),
			),
		}},
	})
}
