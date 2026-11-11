package provider

// Pass 6c Lane Z: compose round trip, like Lane S's item h for the Engine types. The kitchen_sink fixture's raw
// compose_yaml, loaded again through compose-go and JSON-encoded the way the provider encodes the project
// (composeProjectValue), equals the structured `project` output value for value, names mapped by the contract.
//
// Run: pwsh -NoProfile -File test/harness/Invoke-E2E.ps1 -Run 'TestAccComposeP6cZ' -Timeout 20m

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func testAccComposeP6cZRoundTrip(t *testing.T, addr, itemID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		attrs := rs.Primary.Attributes
		raw := attrs["compose_yaml"]
		if raw == "" {
			return fmt.Errorf("%s.compose_yaml is empty", addr)
		}
		p, err := composeLoad(context.Background(), composeInputs{Content: raw, WorkingDir: t.TempDir()})
		if err != nil {
			return fmt.Errorf("%s.compose_yaml does not load again: %v", addr, err)
		}
		v := composeProjectValue(p)
		it := contractMustItem(itemID)
		var node *contractNode
		for _, o := range it.Outputs {
			if o.Name == "project" {
				node = contractOutputNode(it, o)
			}
		}
		want := map[string]string{}
		var skipped, unknown []string
		p6cFlat(node, v, "project", want, &skipped)
		p6cUnknownKeys(node, v, "project", &unknown)
		var bad []string
		for k, w := range want {
			g, ok := attrs[k]
			if !ok {
				bad = append(bad, fmt.Sprintf("%s: missing in state, raw has %q", k, w))
				continue
			}
			if !p6cSameValue(w, g) {
				bad = append(bad, fmt.Sprintf("%s: state %q, raw %q", k, g, w))
			}
		}
		sort.Strings(bad)
		sort.Strings(unknown)
		t.Logf("%s: %d leaves compared, %d skipped %v, %d raw keys without a field %v", addr, len(want), len(skipped), skipped, len(unknown), unknown)
		if len(unknown) > 0 {
			bad = append(bad, fmt.Sprintf("raw keys without a contract field: %v", unknown))
		}
		if len(bad) > 0 {
			if len(bad) > 40 {
				bad = append(bad[:40], fmt.Sprintf("... %d more", len(bad)-40))
			}
			return fmt.Errorf("%s: project differs from compose_yaml:\n%s", addr, strings.Join(bad, "\n"))
		}
		if len(want) < 100 {
			return fmt.Errorf("%s: only %d leaves compared; the kitchen_sink project did not land", addr, len(want))
		}
		return nil
	}
}

// TestAccComposeP6cZProjectRoundTrip: data.docker_compose_config on the kitchen_sink fixture (every compose-spec
// area the hash fixtures cover: build, deploy, secrets, configs, ports with published ranges, byte sizes, durations,
// ulimits, extra_hosts) mirrors its compose_yaml; ports published is a string.
func TestAccComposeP6cZProjectRoundTrip(t *testing.T) {
	testAccPreCheck(t)
	fixture, err := filepath.Abs(filepath.Join("testdata", "compose_hash", "kitchen_sink.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`
data "docker_compose_config" "sink" {
  files = [%q]
}
`, filepath.ToSlash(fixture))
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				testAccComposeP6cZRoundTrip(t, "data.docker_compose_config.sink", "compose:cli-config"),
				resource.TestCheckResourceAttrSet("data.docker_compose_config.sink", "project.services.api.ports.0.published"),
			),
		}},
	})
}
