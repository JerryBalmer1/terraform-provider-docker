package provider

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestP3SwarmListDataSources(t *testing.T) {
	f, pd := newP3eFake(t)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	three := uint64(3)
	f.json("GET /services", []swarm.Service{
		{ID: "s2", Meta: swarm.Meta{CreatedAt: ts}, Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1.27"}},
			Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &three}}}},
		{ID: "s1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "agent"}, Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}}}},
	})
	f.json("GET /secrets", []swarm.Secret{{ID: "x1", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "pw", Labels: map[string]string{"a": "b"}}}}})
	f.json("GET /configs", []swarm.Config{{ID: "c1", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "conf"}, Data: []byte("k=v")}}})
	ctx := context.Background()

	state, diags := p3eReadDS(t, NewP3ServicesDataSource(), pd, map[string]tftypes.Value{"mode": p3eStr("replicated"), "name": p3eStr("w")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var svcs []struct {
		ID        string      `tfsdk:"id"`
		Name      string      `tfsdk:"name"`
		Labels    types.Map   `tfsdk:"labels"`
		CreatedAt string      `tfsdk:"created_at"`
		UpdatedAt string      `tfsdk:"updated_at"`
		Image     string      `tfsdk:"image"`
		Mode      string      `tfsdk:"mode"`
		Replicas  types.Int64 `tfsdk:"replicas"`
	}
	if d := state.GetAttribute(ctx, path.Root("services"), &svcs); d.HasError() {
		t.Fatal(d)
	}
	if len(svcs) != 2 || svcs[0].Name != "agent" || svcs[0].Mode != "global" || !svcs[0].Replicas.IsNull() ||
		svcs[1].Replicas.ValueInt64() != 3 || svcs[1].Image != "nginx:1.27" || svcs[1].CreatedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("services = %+v", svcs)
	}
	if args := p3eFilterArgs(t, f, "GET /services"); !args.ExactMatch("mode", "replicated") || !args.ExactMatch("name", "w") {
		t.Errorf("filters = %v", args)
	}
	var mode types.String
	state.GetAttribute(ctx, path.Root("mode"), &mode)
	if mode.ValueString() != "replicated" {
		t.Errorf("mode input not kept in state: %s", mode)
	}

	state, diags = p3eReadDS(t, NewP3SecretsDataSource(), pd, map[string]tftypes.Value{"labels": p3eStrMap(map[string]string{"a": "b"})})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var secs []struct {
		ID        string            `tfsdk:"id"`
		Name      string            `tfsdk:"name"`
		Labels    map[string]string `tfsdk:"labels"`
		CreatedAt string            `tfsdk:"created_at"`
		UpdatedAt string            `tfsdk:"updated_at"`
	}
	if d := state.GetAttribute(ctx, path.Root("secrets"), &secs); d.HasError() {
		t.Fatal(d)
	}
	if len(secs) != 1 || secs[0].Name != "pw" || secs[0].Labels["a"] != "b" || secs[0].CreatedAt != "" {
		t.Errorf("secrets = %+v", secs)
	}

	state, diags = p3eReadDS(t, NewP3ConfigsDataSource(), pd, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var data types.String
	state.GetAttribute(ctx, path.Root("configs").AtListIndex(0).AtName("data"), &data)
	if data.ValueString() != "az12" {
		t.Errorf("config data = %s", data)
	}
}

func TestAccP3SwarmListDataSources(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_secret" "s" {
  name   = "tfacc-secrets-ds"
  data   = base64encode("x")
  labels = { tfacc_lists = "1" }
}

resource "docker_config" "c" {
  name   = "tfacc-configs-ds"
  data   = base64encode("k=v")
  labels = { tfacc_lists = "1" }
}

data "docker_secrets" "s" {
  labels     = { tfacc_lists = "1" }
  depends_on = [docker_secret.s]
}

data "docker_configs" "c" {
  labels     = { tfacc_lists = "1" }
  depends_on = [docker_config.c]
}

data "docker_services" "all" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.docker_secrets.s", "secrets.#", "1"),
				resource.TestCheckResourceAttr("data.docker_secrets.s", "secrets.0.name", "tfacc-secrets-ds"),
				resource.TestCheckResourceAttr("data.docker_configs.c", "configs.0.data", "az12"),
				resource.TestCheckResourceAttrSet("data.docker_services.all", "services.#"),
			),
		}},
	})
}
