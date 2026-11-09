package provider

import (
	"context"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/echoprovider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/api/types/swarm"
)

// p3eOpenEphemeral configures e with pd and opens it with cfg.
func p3eOpenEphemeral(t *testing.T, e ephemeral.EphemeralResource, pd *providerData, cfg map[string]tftypes.Value) (tfsdk.EphemeralResultData, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	if ec, ok := e.(ephemeral.EphemeralResourceWithConfigure); ok {
		var cr ephemeral.ConfigureResponse
		ec.Configure(ctx, ephemeral.ConfigureRequest{ProviderData: pd}, &cr)
		if cr.Diagnostics.HasError() {
			t.Fatal(cr.Diagnostics)
		}
	}
	var sr ephemeral.SchemaResponse
	e.Schema(ctx, ephemeral.SchemaRequest{}, &sr)
	if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema: %v", d)
	}
	typ := sr.Schema.Type().TerraformType(ctx)
	resp := ephemeral.OpenResponse{Result: tfsdk.EphemeralResultData{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	e.Open(ctx, ephemeral.OpenRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(typ, cfg)}}, &resp)
	return resp.Result, resp.Diagnostics
}

// testAccEphemeralFactories serves docker plus the echo provider, which
// copies an ephemeral value into state so a test can check it.
func testAccEphemeralFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"docker": testAccProtoV6ProviderFactories["docker"],
		"echo":   echoprovider.NewProviderServer(),
	}
}

func TestSwarmTokensEphemeralOpen(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /swarm", swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "clu1"}, JoinTokens: swarm.JoinTokens{Worker: "SWMTKN-w", Manager: "SWMTKN-m"}})
	res, diags := p3eOpenEphemeral(t, NewSwarmTokensEphemeralResource(), pd, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m swarmTokensModel
	res.Get(context.Background(), &m)
	if m.ID.ValueString() != "clu1" || m.Worker.ValueString() != "SWMTKN-w" || m.Manager.ValueString() != "SWMTKN-m" {
		t.Errorf("got %+v", m)
	}

	f.on("GET /swarm", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "This node is not a swarm manager."})
	})
	if _, diags := p3eOpenEphemeral(t, NewSwarmTokensEphemeralResource(), pd, nil); !diags.HasError() {
		t.Error("expected an error on a non-manager")
	}
}

func TestAccSwarmTokensEphemeral(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccEphemeralFactories(),
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{{
			Config: `
ephemeral "docker_swarm_tokens" "t" {}

provider "echo" {
  data = ephemeral.docker_swarm_tokens.t.worker
}

resource "echo" "t" {}
`,
			Check: resource.TestCheckResourceAttrSet("echo.t", "data"),
		}},
	})
}
