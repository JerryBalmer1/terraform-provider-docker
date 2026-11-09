package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/swarm"
)

func TestP3SwarmListDataSources(t *testing.T) {
	f, pd := newP3eFake(t)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	three := uint64(3)
	f.json("GET /services", []swarm.Service{
		{ID: "s2", Meta: swarm.Meta{CreatedAt: ts}, Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1.27", Env: []string{"PW=x"}}},
			Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &three}}}},
		{ID: "s1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "agent"}, Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}}}},
	})
	f.json("GET /secrets", []swarm.Secret{{ID: "x1", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "pw", Labels: map[string]string{"a": "b"}}}}})
	f.json("GET /configs", []swarm.Config{{ID: "c1", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "conf"}, Data: []byte("k=v")}}})
	ctx := context.Background()

	state, diags := swarmBReadDS(t, NewServicesDataSource(), pd, map[string]tftypes.Value{"mode": p3eStr("replicated"), "name": p3eStr("w")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var ids, names []string
	state.GetAttribute(ctx, path.Root("ids"), &ids)
	state.GetAttribute(ctx, path.Root("names"), &names)
	if len(ids) != 2 || ids[0] != "s1" || names[1] != "web" {
		t.Errorf("ids=%v names=%v", ids, names)
	}
	var img, created types.String
	var replicas types.Number
	state.GetAttribute(ctx, path.Root("services").AtListIndex(1).AtName("spec").AtName("task_template").AtName("container_spec").AtName("image"), &img)
	state.GetAttribute(ctx, path.Root("services").AtListIndex(1).AtName("created_at"), &created)
	state.GetAttribute(ctx, path.Root("services").AtListIndex(1).AtName("spec").AtName("mode").AtName("replicated").AtName("replicas"), &replicas)
	if img.ValueString() != "nginx:1.27" || created.ValueString() != "2026-01-02T03:04:05Z" || replicas.String() != "3" {
		t.Errorf("services[1]: image=%s created=%s replicas=%s", img, created, replicas)
	}
	var raw, sum types.String
	state.GetAttribute(ctx, path.Root("services_json"), &raw)
	state.GetAttribute(ctx, path.Root("content_sha256"), &sum)
	if !strings.Contains(raw.ValueString(), `"ID":"s1"`) || len(sum.ValueString()) != 64 {
		t.Errorf("raw=%s sha=%s", raw, sum)
	}
	if args := p3eFilterArgs(t, f, "GET /services"); !filterExactMatch(args, "mode", "replicated") || !filterExactMatch(args, "name", "w") {
		t.Errorf("filters = %v", args)
	}
	var mode types.String
	state.GetAttribute(ctx, path.Root("mode"), &mode)
	if mode.ValueString() != "replicated" {
		t.Errorf("mode input not kept in state: %s", mode)
	}

	state, diags = swarmBReadDS(t, NewSecretsDataSource(), pd, map[string]tftypes.Value{"labels": p3eStrMap(map[string]string{"a": "b"})})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var secName types.String
	var secLabels map[string]string
	state.GetAttribute(ctx, path.Root("secrets").AtListIndex(0).AtName("spec").AtName("name"), &secName)
	state.GetAttribute(ctx, path.Root("secrets").AtListIndex(0).AtName("spec").AtName("labels"), &secLabels)
	if secName.ValueString() != "pw" || secLabels["a"] != "b" {
		t.Errorf("secrets[0] = %s %v", secName, secLabels)
	}
	if args := p3eFilterArgs(t, f, "GET /secrets"); !filterExactMatch(args, "label", "a=b") {
		t.Errorf("secret filters = %v", args)
	}

	state, diags = swarmBReadDS(t, NewConfigsDataSource(), pd, nil)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var data types.String
	state.GetAttribute(ctx, path.Root("configs").AtListIndex(0).AtName("spec").AtName("data"), &data)
	if data.ValueString() != "az12" {
		t.Errorf("config data = %s", data)
	}
}

func TestAccP3SwarmListDataSources(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	hAccTest(t, resource.TestCase{
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
				resource.TestCheckResourceAttr("data.docker_secrets.s", "names.0", "tfacc-secrets-ds"),
				resource.TestCheckResourceAttr("data.docker_secrets.s", "secrets.0.spec.name", "tfacc-secrets-ds"),
				resource.TestCheckResourceAttr("data.docker_configs.c", "configs.0.spec.data", "az12"),
				resource.TestCheckResourceAttrSet("data.docker_configs.c", "configs_json"),
				resource.TestCheckResourceAttrSet("data.docker_services.all", "services.#"),
				resource.TestCheckResourceAttrSet("data.docker_services.all", "engine_api_version"),
			),
		}},
	})
}
