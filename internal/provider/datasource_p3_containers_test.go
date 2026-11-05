package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
	f.json("GET /containers/json", []container.Summary{
		{ID: "c2", Names: []string{"/web"}, Image: "nginx:1.27", State: "running", Labels: map[string]string{"app": "shop"},
			Ports:           []container.Port{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{"front": {}, "back": {}}}},
		{ID: "c1", Names: []string{"/api"}, Image: "api:2", State: "exited", Created: 1700000000},
	})
	state, diags := p3eReadDS(t, NewP3ContainersDataSource(), pd, map[string]tftypes.Value{
		"labels": p3eStrMap(map[string]string{"app": "shop"}),
		"all":    tftypes.NewValue(tftypes.Bool, false),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m struct {
		Containers []struct {
			ID       string            `tfsdk:"id"`
			Name     string            `tfsdk:"name"`
			Names    []string          `tfsdk:"names"`
			Image    string            `tfsdk:"image"`
			ImageID  string            `tfsdk:"image_id"`
			Command  string            `tfsdk:"command"`
			Created  string            `tfsdk:"created"`
			State    string            `tfsdk:"state"`
			Status   string            `tfsdk:"status"`
			Labels   map[string]string `tfsdk:"labels"`
			Networks []string          `tfsdk:"networks"`
			Ports    []struct {
				IP          string `tfsdk:"ip"`
				PrivatePort int64  `tfsdk:"private_port"`
				PublicPort  int64  `tfsdk:"public_port"`
				Type        string `tfsdk:"type"`
			} `tfsdk:"ports"`
		} `tfsdk:"containers"`
	}
	if d := state.GetAttribute(context.Background(), path.Root("containers"), &m.Containers); d.HasError() {
		t.Fatal(d)
	}
	cs := m.Containers
	if len(cs) != 2 || cs[0].Name != "api" || cs[1].Name != "web" || cs[0].Created != "2023-11-14T22:13:20Z" {
		t.Fatalf("containers = %+v", cs)
	}
	if strings.Join(cs[1].Networks, ",") != "back,front" || cs[1].Ports[0].PublicPort != 8080 || cs[1].Labels["app"] != "shop" {
		t.Errorf("web = %+v", cs[1])
	}
	args := p3eFilterArgs(t, f, "GET /containers/json")
	if !args.ExactMatch("label", "app=shop") {
		t.Errorf("filters = %v", args)
	}
	for _, s := range f.requests() {
		if strings.Contains(s, "all=1") {
			t.Errorf("all=false still listed all: %s", s)
		}
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
				resource.TestCheckResourceAttr("data.docker_containers.c", "containers.0.name", "tfacc-containers-ds"),
			),
		}},
	})
}
