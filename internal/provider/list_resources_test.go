package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/api/types/volume"
	"github.com/hashicorp/terraform-plugin-framework/list"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/querycheck"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

type p3eListRun struct {
	results []list.ListResult
}

func (r p3eListRun) ids(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, res := range r.results {
		for _, d := range res.Diagnostics {
			if d.Severity().String() == "Error" {
				t.Fatalf("result error: %s: %s", d.Summary(), d.Detail())
			}
		}
		var id types.String
		if d := res.Identity.GetAttribute(context.Background(), path.Root("id"), &id); d.HasError() {
			t.Fatalf("identity: %v", d)
		}
		out = append(out, id.ValueString()+"|"+res.DisplayName)
	}
	return out
}

// p3eRunList configures l with pd and runs List with cfg (list config
// attribute values), the managed resource schema of newRes and the {id}
// identity schema, as the framework server would.
func p3eRunList(t *testing.T, l list.ListResource, newRes func() resource.Resource, pd *providerData, cfg map[string]tftypes.Value, include bool, limit int64) p3eListRun {
	t.Helper()
	ctx := context.Background()
	if lc, ok := l.(list.ListResourceWithConfigure); ok {
		var cr resource.ConfigureResponse
		lc.Configure(ctx, resource.ConfigureRequest{ProviderData: pd}, &cr)
		if cr.Diagnostics.HasError() {
			t.Fatal(cr.Diagnostics)
		}
	}
	var sr list.ListResourceSchemaResponse
	l.ListResourceConfigSchema(ctx, list.ListResourceSchemaRequest{}, &sr)
	if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("list schema: %v", d)
	}
	var rs resource.SchemaResponse
	newRes().Schema(ctx, resource.SchemaRequest{}, &rs)
	req := list.ListRequest{
		Config:                 tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(sr.Schema.Type().TerraformType(ctx), cfg)},
		IncludeResource:        include,
		Limit:                  limit,
		ResourceSchema:         rs.Schema,
		ResourceIdentitySchema: listIDIdentitySchema(),
	}
	var stream list.ListResultsStream
	l.List(ctx, req, &stream)
	var run p3eListRun
	for res := range stream.Results {
		run.results = append(run.results, res)
	}
	return run
}

func p3eFilterArgs(t *testing.T, f *p3eFake, prefix string) filters.Args {
	t.Helper()
	for _, s := range f.requests() {
		if strings.HasPrefix(s, prefix+"?") {
			q := s[len(prefix)+1:]
			r, _ := http.NewRequest(http.MethodGet, "http://x/?"+q, nil)
			args, err := filters.FromJSON(r.URL.Query().Get("filters"))
			if err != nil {
				t.Fatal(err)
			}
			return args
		}
	}
	t.Fatalf("no request %s in %v", prefix, f.requests())
	return filters.Args{}
}

func TestListResourcesMetadataAndSchemas(t *testing.T) {
	ctx := context.Background()
	want := map[string]bool{"docker_container": true, "docker_image": true, "docker_network": true, "docker_volume": true,
		"docker_swarm_node": true, "docker_service": true, "docker_secret": true, "docker_config": true}
	for _, nf := range laneListResources() {
		l := nf()
		var mr resource.MetadataResponse
		l.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "docker"}, &mr)
		if !want[mr.TypeName] {
			t.Errorf("unexpected list type %q", mr.TypeName)
		}
		delete(want, mr.TypeName)
		var sr list.ListResourceSchemaResponse
		l.ListResourceConfigSchema(ctx, list.ListResourceSchemaRequest{}, &sr)
		if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s: %v", mr.TypeName, d)
		}
		if !strings.Contains(sr.Schema.MarkdownDescription, ". ") {
			t.Errorf("%s: description should have a standalone first sentence", mr.TypeName)
		}
	}
	for k := range want {
		t.Errorf("missing list resource %s", k)
	}
}

func TestListContainerFiltersAndIdentity(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /containers/json", []container.Summary{
		{ID: strings.Repeat("b", 64), Names: []string{"/web"}},
		{ID: strings.Repeat("a", 64), Names: []string{"/api"}},
	})
	run := p3eRunList(t, NewContainerListResource(), NewContainerResource, pd, map[string]tftypes.Value{
		"status": p3eStr("running"),
		"labels": p3eStrMap(map[string]string{"app": "shop", "tier": ""}),
	}, false, 0)
	got := run.ids(t)
	if len(got) != 2 || got[0] != strings.Repeat("a", 64)+"|api" || got[1] != strings.Repeat("b", 64)+"|web" {
		t.Fatalf("results = %v", got)
	}
	args := p3eFilterArgs(t, f, "GET /containers/json")
	if !args.ExactMatch("status", "running") || !args.ExactMatch("label", "app=shop") || !args.ExactMatch("label", "tier") {
		t.Errorf("filters = %v", args)
	}
	var all bool
	for _, s := range f.requests() {
		all = all || strings.Contains(s, "all=1")
	}
	if !all {
		t.Errorf("all should default to true: %v", f.requests())
	}
}

func TestListLimit(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /networks", []network.Summary{{ID: "n1", Name: "a"}, {ID: "n2", Name: "b"}, {ID: "n3", Name: "c"}})
	run := p3eRunList(t, NewNetworkListResource(), NewNetworkResource, pd, nil, false, 2)
	if got := run.ids(t); len(got) != 2 || got[0] != "n1|a" {
		t.Fatalf("results = %v", got)
	}
}

func TestListImageUsesTag(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /images/json", []image.Summary{
		{ID: "sha256:" + strings.Repeat("c", 64), RepoTags: []string{"nginx:1.27", "nginx:latest"}},
		{ID: "sha256:" + strings.Repeat("d", 64), RepoTags: []string{"<none>:<none>"}},
	})
	run := p3eRunList(t, NewImageListResource(), NewImageResource, pd, map[string]tftypes.Value{
		"dangling": tftypes.NewValue(tftypes.Bool, false),
	}, false, 0)
	got := run.ids(t)
	if len(got) != 2 || got[0] != "sha256:"+strings.Repeat("d", 64)+"|dddddddddddd" || got[1] != "sha256:"+strings.Repeat("c", 64)+"|nginx:1.27" {
		t.Fatalf("results = %v", got)
	}
	if args := p3eFilterArgs(t, f, "GET /images/json"); !args.ExactMatch("dangling", "false") {
		t.Errorf("filters = %v", args)
	}
}

func TestListSwarmNodeLabelFilter(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /nodes", []swarm.Node{{ID: "node1", Description: swarm.NodeDescription{Hostname: "m1"}}})
	run := p3eRunList(t, NewSwarmNodeListResource(), NewSwarmNodeResource, pd, map[string]tftypes.Value{
		"role":   p3eStr("manager"),
		"labels": p3eStrMap(map[string]string{"zone": "a"}),
	}, false, 0)
	if got := run.ids(t); len(got) != 1 || got[0] != "node1|m1" {
		t.Fatalf("results = %v", got)
	}
	args := p3eFilterArgs(t, f, "GET /nodes")
	if !args.ExactMatch("node.label", "zone=a") || !args.ExactMatch("role", "manager") {
		t.Errorf("filters = %v", args)
	}
}

func TestListServiceAndVolume(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /services", []swarm.Service{{ID: "svc1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"}}}})
	f.json("GET /volumes", volume.ListResponse{Volumes: []*volume.Volume{{Name: "data"}, nil}})
	if got := p3eRunList(t, NewServiceListResource(), NewServiceResource, pd, map[string]tftypes.Value{"mode": p3eStr("global")}, false, 0).ids(t); len(got) != 1 || got[0] != "svc1|web" {
		t.Fatalf("services = %v", got)
	}
	if args := p3eFilterArgs(t, f, "GET /services"); !args.ExactMatch("mode", "global") {
		t.Errorf("filters = %v", args)
	}
	if got := p3eRunList(t, NewVolumeListResource(), NewVolumeResource, pd, nil, false, 0).ids(t); len(got) != 1 || got[0] != "data|data" {
		t.Fatalf("volumes = %v", got)
	}
}

// TestListIncludeResource checks the full resource object comes from the
// managed resource's own ImportState + Read.
func TestListIncludeResource(t *testing.T) {
	f, pd := newP3eFake(t)
	sec := swarm.Secret{ID: "sec1", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "db-pw",
		Labels: map[string]string{"app": "shop", labelManagedBy: labelManagedByValue}}}}
	f.json("GET /secrets", []swarm.Secret{sec})
	f.json("GET /secrets/sec1", sec)
	cfg := swarm.Config{ID: "cfg1", Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: "app.conf"}, Data: []byte("a=1")}}
	f.json("GET /configs", []swarm.Config{cfg})
	f.json("GET /configs/cfg1", cfg)
	f.json("GET /nodes", []swarm.Node{{ID: "node1", Description: swarm.NodeDescription{Hostname: "m1"}}})
	f.json("GET /nodes/node1", swarm.Node{ID: "node1", Description: swarm.NodeDescription{Hostname: "m1"},
		Spec: swarm.NodeSpec{Role: swarm.NodeRoleManager, Availability: swarm.NodeAvailabilityActive}})
	ctx := context.Background()

	run := p3eRunList(t, NewSecretListResource(), NewSecretResource, pd, nil, true, 0)
	if got := run.ids(t); len(got) != 1 || got[0] != "sec1|db-pw" {
		t.Fatalf("secrets = %v", got)
	}
	var sm secretResourceModel
	if d := run.results[0].Resource.Get(ctx, &sm); d.HasError() {
		t.Fatal(d)
	}
	if sm.Name.ValueString() != "db-pw" || !sm.Data.IsNull() || len(sm.Labels.Elements()) != 1 {
		t.Errorf("secret object = %+v", sm)
	}

	run = p3eRunList(t, NewConfigListResource(), NewDockerConfigResource, pd, nil, true, 0)
	var cm dockerConfigResourceModel
	if d := run.results[0].Resource.Get(ctx, &cm); d.HasError() {
		t.Fatal(d)
	}
	if cm.ID.ValueString() != "cfg1" || cm.Data.ValueString() != "YT0x" {
		t.Errorf("config object = %+v", cm)
	}

	// docker_swarm_node writes private state in ImportState.
	run = p3eRunList(t, NewSwarmNodeListResource(), NewSwarmNodeResource, pd, nil, true, 0)
	if len(run.results) != 1 || run.results[0].Diagnostics.HasError() || run.results[0].Resource.Raw.IsNull() {
		t.Fatalf("node results = %+v", run.results)
	}
	var nodeID types.String
	run.results[0].Resource.GetAttribute(ctx, path.Root("node_id"), &nodeID)
	if nodeID.ValueString() != "node1" {
		t.Errorf("node_id = %s", nodeID)
	}
}

func TestListVanishedObjectSkipped(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /secrets", []swarm.Secret{{ID: "gone", Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: "x"}}}})
	f.on("GET /secrets/gone", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, http.StatusNotFound, map[string]string{"message": "no such secret"})
	})
	run := p3eRunList(t, NewSecretListResource(), NewSecretResource, pd, nil, true, 0)
	if len(run.results) != 0 {
		t.Fatalf("vanished secret listed: %+v", run.results)
	}
}

func TestListNoIdentitySchema(t *testing.T) {
	_, pd := newP3eFake(t)
	l := NewVolumeListResource()
	l.(list.ListResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: pd}, &resource.ConfigureResponse{})
	var stream list.ListResultsStream
	l.List(context.Background(), list.ListRequest{}, &stream)
	n := 0
	for res := range stream.Results {
		n++
		if !res.Diagnostics.HasError() {
			t.Error("expected an error without identity schema")
		}
	}
	if n != 1 {
		t.Errorf("results = %d", n)
	}
}

func TestListInitPrivate(t *testing.T) {
	var resp resource.ImportStateResponse
	listInitPrivate(&resp)
	if resp.Private == nil {
		t.Fatal("Private not initialised")
	}
	if d := resp.Private.SetKey(context.Background(), "k", []byte(`"v"`)); d.HasError() {
		t.Fatal(d)
	}
	listInitPrivate(nil) // no panic
}

// TestAccListSecretQuery runs `terraform query` against a swarm manager.
func TestAccListSecretQuery(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		Steps: []tfresource.TestStep{
			{Config: `
resource "docker_secret" "q" {
  name   = "tfacc-list-secret"
  data   = base64encode("x")
  labels = { tfacc_list = "1" }
}
`},
			{
				Query: true,
				Config: `
provider "docker" {}

list "docker_secret" "q" {
  provider = docker
  config {
    labels = { tfacc_list = "1" }
  }
}
`,
				QueryResultChecks: []querycheck.QueryResultCheck{
					querycheck.ExpectLength("docker_secret.q", 1),
				},
			},
		},
	})
}
