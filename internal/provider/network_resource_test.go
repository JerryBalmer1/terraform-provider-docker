package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func TestNetworkExpandIPAM(t *testing.T) {
	ipam, err := networkExpandIPAM(context.Background(), "default", []networkIPAMConfigModel{{
		Subnet: types.StringValue("10.9.0.0/24"), IPRange: types.StringNull(), Gateway: types.StringValue("10.9.0.1"),
		AuxAddress: stringMapValue(map[string]string{"host": "10.9.0.2"}, false),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if ipam.Driver != "default" || len(ipam.Config) != 1 || addrString(ipam.Config[0].Gateway) != "10.9.0.1" || addrString(ipam.Config[0].AuxAddress["host"]) != "10.9.0.2" {
		t.Errorf("ipam: %+v", ipam)
	}
}

func TestNetworkFlattenIPAM(t *testing.T) {
	got := []network.IPAMConfig{
		{Subnet: mustPrefix("10.9.0.0/24"), Gateway: mustAddr("10.9.0.1")},
		{Subnet: mustPrefix("fd00::/64"), Gateway: mustAddr("fd00::1")},
	}
	prior := []networkIPAMConfigModel{{Subnet: types.StringValue("10.9.0.0/24"), IPRange: types.StringNull(), Gateway: types.StringNull(), AuxAddress: types.MapNull(types.StringType)}}
	out := networkFlattenIPAM(got, prior, false)
	if len(out) != 1 || !out[0].Gateway.IsNull() || out[0].Subnet.ValueString() != "10.9.0.0/24" {
		t.Errorf("daemon-filled gateway and auto v6 pool must not be reported: %+v", out)
	}
	if out := networkFlattenIPAM(got, nil, false); out == nil || len(out) != 0 {
		t.Errorf("daemon-assigned pools without prior: %+v", out)
	}
	if out := networkFlattenIPAM(got, nil, true); len(out) != 2 || out[1].Gateway.ValueString() != "fd00::1" {
		t.Errorf("import reports all pools: %+v", out)
	}
	if out := networkFlattenIPAM(nil, prior, false); len(out) != 0 {
		t.Errorf("a removed pool must disappear (drift): %+v", out)
	}
}

func TestNetworkFromInspect(t *testing.T) {
	n := network.Inspect{
		ID: "nid", Name: "app", Driver: "overlay", Scope: "swarm", Attachable: true,
		IPAM:    network.IPAM{Driver: "", Config: []network.IPAMConfig{{Subnet: mustPrefix("10.0.1.0/24"), Gateway: mustAddr("10.0.1.1")}}},
		Options: map[string]string{"com.docker.network.driver.overlay.vxlanid_list": "4097", "encrypted": ""},
		Labels:  map[string]string{"team": "a", labelManagedBy: labelManagedByValue},
	}
	prior := networkResourceModel{
		ID: types.StringValue("nid"), Name: types.StringValue("app"),
		Options: stringMapValue(map[string]string{"encrypted": ""}, false), Labels: stringMapValue(map[string]string{"team": "a"}, false),
	}
	got := networkFromInspect(n, prior)
	if got.Driver.ValueString() != "overlay" || !got.Attachable.ValueBool() || got.IPAMDriver.ValueString() != "default" || got.Scope.ValueString() != "swarm" {
		t.Errorf("basic: %+v", got)
	}
	if len(got.Options.Elements()) != 1 || len(got.Labels.Elements()) != 1 {
		t.Errorf("driver-added options / managed-by label leaked: %v %v", got.Options, got.Labels)
	}
	if got.IPAMConfig == nil || len(got.IPAMConfig) != 0 {
		t.Errorf("ipam without prior pools: %+v", got.IPAMConfig)
	}
	imp := networkFromInspect(n, networkResourceModel{ID: types.StringValue("app"), Name: types.StringNull(), Options: types.MapNull(types.StringType), Labels: types.MapNull(types.StringType)})
	if imp.Name.ValueString() != "app" || len(imp.Options.Elements()) != 2 || len(imp.IPAMConfig) != 1 {
		t.Errorf("import: %+v", imp)
	}
}

func TestAccNetwork_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-net")
	c := testAccDockerClient(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "docker_network" {
					continue
				}
				if _, err := c.NetworkInspect(context.Background(), rs.Primary.ID, client.NetworkInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("network %s still exists (%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "docker_network" "test" {
  name     = %q
  internal = true
  labels   = { team = "acc" }
  options  = { "com.docker.network.bridge.enable_icc" = "true" }

  ipam_config {
    subnet  = "10.231.0.0/24"
    gateway = "10.231.0.1"
  }
}
`, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_network.test", "id"),
					resource.TestCheckResourceAttr("docker_network.test", "driver", "bridge"),
					resource.TestCheckResourceAttr("docker_network.test", "scope", "local"),
					resource.TestCheckResourceAttr("docker_network.test", "ipam_config.0.subnet", "10.231.0.0/24"),
				),
			},
			{
				ResourceName:      "docker_network.test",
				ImportState:       true,
				ImportStateId:     name,
				ImportStateVerify: true,
				// options: the driver may add its own on import.
				ImportStateVerifyIgnore: []string{"options"},
			},
		},
	})
}

// TestNetworkIPAMRequiresReplace: a missing ipam_config block or an unset
// pool argument accepts the network's pools (a moved kreuzwerker state
// records the daemon-assigned pool and gateway); a configured value that
// differs forces replacement.
func TestNetworkIPAMRequiresReplace(t *testing.T) {
	ctx := context.Background()
	objType := types.ObjectType{AttrTypes: map[string]attr.Type{
		"subnet": types.StringType, "ip_range": types.StringType, "gateway": types.StringType, "aux_address": types.MapType{ElemType: types.StringType},
	}}
	pool := func(subnet, gateway string) attr.Value {
		s, g := types.StringNull(), types.StringNull()
		if subnet != "" {
			s = types.StringValue(subnet)
		}
		if gateway != "" {
			g = types.StringValue(gateway)
		}
		return types.ObjectValueMust(objType.AttrTypes, map[string]attr.Value{
			"subnet": s, "ip_range": types.StringNull(), "gateway": g, "aux_address": types.MapNull(types.StringType),
		})
	}
	list := func(vs ...attr.Value) types.List { return types.ListValueMust(objType, vs) }
	state := list(pool("172.20.0.0/16", "172.20.0.1"))
	for name, tc := range map[string]struct {
		cfg  types.List
		want bool
	}{
		"no block":      {list(), false},
		"null block":    {types.ListNull(objType), false},
		"subnet only":   {list(pool("172.20.0.0/16", "")), false},
		"same pool":     {list(pool("172.20.0.0/16", "172.20.0.1")), false},
		"other subnet":  {list(pool("10.1.0.0/24", "")), true},
		"other gateway": {list(pool("172.20.0.0/16", "172.20.0.9")), true},
		"one more pool": {list(pool("172.20.0.0/16", ""), pool("10.1.0.0/24", "")), true},
	} {
		var resp listplanmodifier.RequiresReplaceIfFuncResponse
		networkIPAMRequiresReplace(ctx, planmodifier.ListRequest{ConfigValue: tc.cfg, StateValue: state, PlanValue: tc.cfg}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		if resp.RequiresReplace != tc.want {
			t.Errorf("%s: RequiresReplace = %v, want %v", name, resp.RequiresReplace, tc.want)
		}
	}
}
