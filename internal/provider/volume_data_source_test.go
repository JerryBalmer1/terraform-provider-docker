package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/volume"
)

func TestVolumeDataFromInspect(t *testing.T) {
	got := volumeDataFromInspect(volume.Volume{Name: "v", Driver: "local", Mountpoint: "/m", Labels: map[string]string{"a": "b"}})
	if got.ID.ValueString() != "v" || got.Driver.ValueString() != "local" || len(got.Labels.Elements()) != 1 || got.DriverOpts.IsNull() {
		t.Errorf("model: %+v", got)
	}
}

func TestAccVolumeDataSource_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-volds")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_volume" "v" {
  name   = %q
  labels = { team = "acc" }
}

data "docker_volume" "v" {
  name = docker_volume.v.name
}
`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.docker_volume.v", "mountpoint", "docker_volume.v", "mountpoint"),
				resource.TestCheckResourceAttr("data.docker_volume.v", "driver", "local"),
			),
		}},
	})
}
