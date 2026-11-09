package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// p3eReadDS configures d with pd and reads it with cfg.
func p3eReadDS(t *testing.T, d datasource.DataSource, pd *providerData, cfg map[string]tftypes.Value) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	if dc, ok := d.(datasource.DataSourceWithConfigure); ok {
		dc.Configure(ctx, datasource.ConfigureRequest{ProviderData: pd}, &datasource.ConfigureResponse{})
	}
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	if diags := sr.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema: %v", diags)
	}
	typ := sr.Schema.Type().TerraformType(ctx)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(typ, cfg)}}, &resp)
	return resp.State, resp.Diagnostics
}

func TestP3ContainersDataSource(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /containers/json", []container.Summary{
		{ID: "c2", Names: []string{"/web"}, Image: "nginx:1.27", State: "running", Labels: map[string]string{"app": "shop"},
			Ports:           []container.PortSummary{{IP: mustAddr("0.0.0.0"), PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{"front": {}, "back": {}}}},
		{ID: "c1", Names: []string{"/api"}, Image: "api:2", State: "exited", Created: 1700000000},
	})
	state, diags := p3eReadDS(t, NewContainersDataSource(), pd, map[string]tftypes.Value{
		"labels": p3eStrMap(map[string]string{"app": "shop"}),
		"all":    tftypes.NewValue(tftypes.Bool, false),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	ctx := context.Background()
	var ids, names []string
	state.GetAttribute(ctx, path.Root("ids"), &ids)
	state.GetAttribute(ctx, path.Root("names"), &names)
	if strings.Join(ids, ",") != "c1,c2" || strings.Join(names, ",") != "api,web" {
		t.Errorf("ids = %v, names = %v", ids, names)
	}
	var raw, sum string
	state.GetAttribute(ctx, path.Root("containers_json"), &raw)
	state.GetAttribute(ctx, path.Root("content_sha256"), &sum)
	var back []container.Summary
	if err := json.Unmarshal([]byte(raw), &back); err != nil || len(back) != 2 || back[1].Ports[0].PublicPort != 8080 {
		t.Errorf("containers_json = %s (%v)", raw, err)
	}
	if len(sum) != 64 {
		t.Errorf("content_sha256 = %q", sum)
	}
	var id0, img1, state1 string
	var created0 float64
	state.GetAttribute(ctx, path.Root("containers").AtListIndex(0).AtName("id"), &id0)
	state.GetAttribute(ctx, path.Root("containers").AtListIndex(0).AtName("created"), &created0)
	state.GetAttribute(ctx, path.Root("containers").AtListIndex(1).AtName("image"), &img1)
	state.GetAttribute(ctx, path.Root("containers").AtListIndex(1).AtName("state"), &state1)
	if id0 != "c1" || created0 != 1700000000 || img1 != "nginx:1.27" || state1 != "running" {
		t.Errorf("containers[0].id=%q created=%v containers[1].image=%q state=%q", id0, created0, img1, state1)
	}
	args := p3eFilterArgs(t, f, "GET /containers/json")
	if !filterExactMatch(args, "label", "app=shop") {
		t.Errorf("filters = %v", args)
	}
	for _, s := range f.requests() {
		if strings.Contains(s, "all=1") {
			t.Errorf("all=false still listed all: %s", s)
		}
	}
}

func TestCopContainersListError(t *testing.T) {
	_, pd := newP3eFake(t)
	_, diags := p3eReadDS(t, NewContainersDataSource(), pd, map[string]tftypes.Value{})
	if !diags.HasError() {
		t.Fatal("expected an error when the list fails")
	}
}

func TestAccP3ContainersDataSource(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_container" "c" {
  name    = "tfacc-containers-ds"
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
  labels  = { tfacc_containers = "1" }
}

data "docker_containers" "c" {
  labels     = { tfacc_containers = "1" }
  depends_on = [docker_container.c]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.docker_containers.c", "containers.#", "1"),
				resource.TestCheckResourceAttr("data.docker_containers.c", "names.0", "tfacc-containers-ds"),
				resource.TestCheckResourceAttr("data.docker_containers.c", "containers.0.names.0", "/tfacc-containers-ds"),
				resource.TestCheckResourceAttrPair("data.docker_containers.c", "ids.0", "docker_container.c", "id"),
			),
		}},
	})
}
