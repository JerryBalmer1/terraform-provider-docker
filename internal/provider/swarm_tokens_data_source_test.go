package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestSwarmTokensSchemaSensitive(t *testing.T) {
	var resp datasource.SchemaResponse
	NewSwarmTokensDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	for _, k := range []string{"worker", "manager"} {
		a, ok := resp.Schema.Attributes[k]
		if !ok || !a.IsSensitive() || !a.IsComputed() {
			t.Errorf("%s must be computed and sensitive", k)
		}
	}
}

func TestAccSwarmTokens_basic(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	hAccTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `data "docker_swarm_tokens" "t" {}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.docker_swarm_tokens.t", "id"),
				resource.TestCheckResourceAttrSet("data.docker_swarm_tokens.t", "worker"),
				resource.TestCheckResourceAttrSet("data.docker_swarm_tokens.t", "manager"),
			),
		}},
	})
}
