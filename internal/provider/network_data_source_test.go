package provider

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/network"
)

func TestNetworkDataFromInspect(t *testing.T) {
	n := network.Inspect{
		ID: "nid", Name: "bridge", Driver: "bridge", Scope: "local",
		IPAM: network.IPAM{Driver: "default", Config: []network.IPAMConfig{{Subnet: mustPrefix("172.17.0.0/16"), Gateway: mustAddr("172.17.0.1")}}},
	}
	got := networkDataFromInspect(n, types.StringValue("bridge"))
	if got.ID.ValueString() != "nid" || len(got.IPAMConfig.Elements()) != 1 || got.Options.IsNull() || got.Labels.IsNull() {
		t.Errorf("model: %+v", got)
	}
}

func TestAccNetworkDataSource_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-netds")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_network" "n" {
  name   = %q
  labels = { team = "acc" }
}

data "docker_network" "by_name" {
  name = docker_network.n.name
}

data "docker_network" "bridge" {
  name = "bridge"
}
`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.docker_network.by_name", "id", "docker_network.n", "id"),
				resource.TestCheckResourceAttr("data.docker_network.by_name", "driver", "bridge"),
				resource.TestCheckResourceAttr("data.docker_network.by_name", "labels.team", "acc"),
				resource.TestCheckResourceAttr("data.docker_network.bridge", "scope", "local"),
			),
		}},
	})
}
