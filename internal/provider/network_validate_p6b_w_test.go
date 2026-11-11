package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// pwConfig builds a resource config where every attribute is null except
// the given top-level values.
func pwConfig(t *testing.T, r resource.Resource, set map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	ot := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, at := range ot.AttributeTypes {
		if v, ok := set[name]; ok {
			vals[name] = v
		} else {
			vals[name] = tftypes.NewValue(at, nil)
		}
	}
	return tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(ot, vals)}
}

func pwIPAMPools(t *testing.T, subnets ...string) tftypes.Value {
	t.Helper()
	var sr resource.SchemaResponse
	(&networkResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	lt := sr.Schema.Type().TerraformType(context.Background()).(tftypes.Object).AttributeTypes["ipam_config"].(tftypes.List)
	et := lt.ElementType.(tftypes.Object)
	var elems []tftypes.Value
	for _, s := range subnets {
		m := map[string]tftypes.Value{}
		for k, at := range et.AttributeTypes {
			m[k] = tftypes.NewValue(at, nil)
		}
		m["subnet"] = tftypes.NewValue(tftypes.String, s)
		elems = append(elems, tftypes.NewValue(et, m))
	}
	return tftypes.NewValue(lt, elems)
}

func TestNetworkValidateIPv4PoolWithIPv4Disabled(t *testing.T) {
	r := &networkResource{}
	cases := []struct {
		name   string
		v4     any // nil = unset
		pools  []string
		errors int
	}{
		{"v4 false with a v4 pool", false, []string{"10.231.0.0/24"}, 1},
		{"v4 false with v4 and v6 pools", false, []string{"fd00:1::/64", "10.232.0.0/24"}, 1},
		{"v4 false with a v6 pool only", false, []string{"fd00:1::/64"}, 0},
		{"v4 true with a v4 pool", true, []string{"10.231.0.0/24"}, 0},
		{"v4 unset with a v4 pool", nil, []string{"10.231.0.0/24"}, 0},
		{"v4 false without pools", false, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := map[string]tftypes.Value{
				"name":        tftypes.NewValue(tftypes.String, "n"),
				"enable_ipv4": tftypes.NewValue(tftypes.Bool, tc.v4),
				"ipam_config": pwIPAMPools(t, tc.pools...),
			}
			var resp resource.ValidateConfigResponse
			r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: pwConfig(t, r, set)}, &resp)
			if got := resp.Diagnostics.ErrorsCount(); got != tc.errors {
				t.Fatalf("errors = %d, want %d: %v", got, tc.errors, resp.Diagnostics)
			}
			if tc.errors > 0 && !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "IPv4 pool on a network with IPv4 disabled") {
				t.Errorf("summary = %q", resp.Diagnostics.Errors()[0].Summary())
			}
		})
	}
	// Unknown values never fail validation.
	if got := networkIPv4PoolsWithIPv4Disabled(types.BoolUnknown(), []networkIPAMConfigModel{{Subnet: types.StringValue("10.0.0.0/24")}}); got != nil {
		t.Errorf("unknown enable_ipv4: %v", got)
	}
	if got := networkIPv4PoolsWithIPv4Disabled(types.BoolValue(false), []networkIPAMConfigModel{{Subnet: types.StringUnknown()}}); got != nil {
		t.Errorf("unknown subnet: %v", got)
	}
}

// TestAccNetworkP6bW_ipv4DisabledPool: the pairwise finding end to end. The
// IPv4 pool with enable_ipv4 = false is now a plan-time error; the IPv6-only
// network itself still applies with an empty second plan.
func TestAccNetworkP6bW_ipv4DisabledPool(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-p6bw-net")
	cfg := func(pool string) string {
		return fmt.Sprintf(`
resource "docker_network" "n" {
  name        = %q
  enable_ipv4 = false
  enable_ipv6 = true
%s
}
`, name, pool)
	}
	hAccTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config:      cfg("  ipam_config {\n    subnet = \"10.231.0.0/24\"\n  }"),
				ExpectError: regexp.MustCompile(`IPv4 pool on a network with IPv4 disabled`),
				PlanOnly:    true,
			},
			{
				Config: cfg(""),
				Check:  tfresource.TestCheckResourceAttr("docker_network.n", "enable_ipv4", "false"),
			},
		},
	})
}
