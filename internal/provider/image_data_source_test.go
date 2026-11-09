package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestImageDataSourceRepoDigest(t *testing.T) {
	m := imageDataSourceModel{}
	m.Name = stringOrNull("ghcr.io/o/a:1")
	imgDataSourceApply(&m, imgTestInspect("sha256:1", []string{"ghcr.io/o/a:1"}, []string{"nginx@sha256:x", "ghcr.io/o/a@sha256:y"}, map[string]string{"k": "v"}))
	if m.ImageDigestRef.ValueString() != "ghcr.io/o/a@sha256:y" || m.Digest.ValueString() != "sha256:y" ||
		m.ImageRef.ValueString() != "ghcr.io/o/a:1" || m.ID.ValueString() != "sha256:1" {
		t.Errorf("identity: %+v", m)
	}
	if v, ok := m.Labels.Elements()["k"]; !ok || v.String() != `"v"` {
		t.Errorf("labels %v", m.Labels)
	}
}

// TestAccImageDataSource reads an image pulled by a docker_image resource.
func TestAccImageDataSource(t *testing.T) {
	testAccPreCheck(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_image" "busybox" {
  name = "busybox:1.36"
  # busybox:1.36 is shared by the whole acceptance suite (actAccImage):
  # destroying this test's resource must not delete it from under others.
  keep_locally = true
}
data "docker_image" "busybox" {
  name = docker_image.busybox.name
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.docker_image.busybox", "id", "docker_image.busybox", "id"),
				resource.TestCheckTypeSetElemAttr("data.docker_image.busybox", "repo_tags.*", hAccImage("busybox:1.36")),
				resource.TestCheckResourceAttrSet("data.docker_image.busybox", "image_digest_ref"),
			),
		}},
	})
}
