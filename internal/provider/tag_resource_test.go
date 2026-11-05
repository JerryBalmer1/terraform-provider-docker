package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// tagFakeEngine serves image inspect, tag and remove.
type tagFakeEngine struct {
	mu      sync.Mutex
	tags    map[string]string // familiar ref -> image ID
	removed []string
}

func (f *tagFakeEngine) refsOf(id string) []string {
	var out []string
	for r, i := range f.tags {
		if i == id {
			out = append(out, r)
		}
	}
	return out
}

func (f *tagFakeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if i := strings.Index(p, "/images/"); i >= 0 {
		p = p[i+len("/images/"):]
	}
	notFound := func(ref string) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"message":"No such image: %s"}`, ref)
	}
	lookup := func(ref string) (string, bool) {
		if strings.HasPrefix(ref, "sha256:") {
			for _, id := range f.tags {
				if id == ref {
					return id, true
				}
			}
			return "", false
		}
		id, ok := f.tags[pluginFamiliar(ref)]
		return id, ok
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/json"):
		ref := strings.TrimSuffix(p, "/json")
		id, ok := lookup(ref)
		if !ok {
			notFound(ref)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "RepoTags": f.refsOf(id)})
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/tag"):
		src := strings.TrimSuffix(p, "/tag")
		id, ok := lookup(src)
		if !ok {
			notFound(src)
			return
		}
		f.tags[pluginFamiliar(r.URL.Query().Get("repo")+":"+r.URL.Query().Get("tag"))] = id
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete:
		ref := pluginFamiliar(p)
		if _, ok := f.tags[ref]; !ok {
			notFound(p)
			return
		}
		f.removed = append(f.removed, ref)
		delete(f.tags, ref)
		_, _ = w.Write([]byte(`[{"Untagged":"x"}]`))
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func tagFake(t *testing.T, tags map[string]string) (*tagFakeEngine, *providerData) {
	t.Helper()
	f := &tagFakeEngine{tags: tags}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	return f, &providerData{Client: c}
}

func tfState(t *testing.T, s resource.SchemaResponse, v any) tfsdk.State {
	t.Helper()
	st := tfsdk.State{Schema: s.Schema, Raw: tftypes.NewValue(s.Schema.Type().TerraformType(context.Background()), nil)}
	if v != nil {
		if d := st.Set(context.Background(), v); d.HasError() {
			t.Fatal(d)
		}
	}
	return st
}

func tfPlan(t *testing.T, s resource.SchemaResponse, v any) tfsdk.Plan {
	t.Helper()
	st := tfState(t, s, v)
	return tfsdk.Plan{Schema: st.Schema, Raw: st.Raw}
}

func TestTagResourceFakeEngine(t *testing.T) {
	ctx := context.Background()
	f, pd := tagFake(t, map[string]string{"app:1": "sha256:a", "other:1": "sha256:b"})
	r := &tagResource{pd: pd}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)

	plan := tagResourceModel{ID: types.StringUnknown(), SourceImage: types.StringValue("app:1"),
		TargetImage: types.StringValue("registry.local:5000/app:prod"), SourceImageID: types.StringUnknown()}
	cresp := resource.CreateResponse{State: tfState(t, sr, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: tfPlan(t, sr, plan)}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	var st tagResourceModel
	cresp.State.Get(ctx, &st)
	if st.SourceImageID.ValueString() != "sha256:a" || st.ID.ValueString() != "sha256:a.registry.local:5000/app:prod" ||
		f.tags["registry.local:5000/app:prod"] != "sha256:a" {
		t.Fatalf("after create: state=%+v tags=%v", st, f.tags)
	}

	// Refresh with the tag in place keeps the resource.
	rresp := resource.ReadResponse{State: tfState(t, sr, &st)}
	r.Read(ctx, resource.ReadRequest{State: tfState(t, sr, &st)}, &rresp)
	if rresp.Diagnostics.HasError() || rresp.State.Raw.IsNull() {
		t.Fatalf("read: %v null=%v", rresp.Diagnostics, rresp.State.Raw.IsNull())
	}

	// Tag moved to another image: refresh drops it so the next apply re-tags.
	f.tags["registry.local:5000/app:prod"] = "sha256:b"
	rresp = resource.ReadResponse{State: tfState(t, sr, &st)}
	r.Read(ctx, resource.ReadRequest{State: tfState(t, sr, &st)}, &rresp)
	if !rresp.State.Raw.IsNull() {
		t.Error("moved tag should be removed from state")
	}
	// Destroy does not remove a tag that is no longer ours.
	dresp := resource.DeleteResponse{State: tfState(t, sr, &st)}
	r.Delete(ctx, resource.DeleteRequest{State: tfState(t, sr, &st)}, &dresp)
	if dresp.Diagnostics.HasError() || len(f.removed) != 0 {
		t.Errorf("delete of a moved tag: %v removed=%v", dresp.Diagnostics, f.removed)
	}

	// Destroy untags the target only; the source keeps its tag and ID.
	f.tags["registry.local:5000/app:prod"] = "sha256:a"
	dresp = resource.DeleteResponse{State: tfState(t, sr, &st)}
	r.Delete(ctx, resource.DeleteRequest{State: tfState(t, sr, &st)}, &dresp)
	if dresp.Diagnostics.HasError() || strings.Join(f.removed, ",") != "registry.local:5000/app:prod" || f.tags["app:1"] != "sha256:a" {
		t.Errorf("delete: %v removed=%v tags=%v", dresp.Diagnostics, f.removed, f.tags)
	}

	// Source given by ID and the target is the image's only tag: kept with a warning.
	f.removed = nil
	only := tagResourceModel{ID: types.StringValue("x"), SourceImage: types.StringValue("sha256:c"),
		TargetImage: types.StringValue("solo:1"), SourceImageID: types.StringValue("sha256:c")}
	f.tags["solo:1"] = "sha256:c"
	dresp = resource.DeleteResponse{State: tfState(t, sr, &only)}
	r.Delete(ctx, resource.DeleteRequest{State: tfState(t, sr, &only)}, &dresp)
	if dresp.Diagnostics.HasError() || dresp.Diagnostics.WarningsCount() != 1 || len(f.removed) != 0 {
		t.Errorf("only-tag delete: %v removed=%v", dresp.Diagnostics, f.removed)
	}
}

func TestTagNormalizeTarget(t *testing.T) {
	for in, want := range map[string]string{"app": "app:latest", "docker.io/library/app:1": "app:1", "ghcr.io/o/a:2": "ghcr.io/o/a:2"} {
		if got, err := tagNormalizeTarget(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"app@sha256:" + strings.Repeat("a", 64), "UPPER:1", ""} {
		if _, err := tagNormalizeTarget(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	if !tagHasOtherTag([]string{"a:1", "b:1"}, "b") {
		t.Error("tagHasOtherTag")
	}
	if tagHasOtherTag([]string{"b:latest"}, "b") {
		t.Error("b and b:latest are the same tag")
	}
}

func TestTagValidatePlan(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{{
			PlanOnly: true,
			Config: `
resource "docker_tag" "t" {
  source_image = "app:1"
  target_image = "app:1"
}`,
			ExpectError: mustRegexpT(t, `target_image equals source_image`),
		}},
	})
}

// TestTagExamplePlan plans examples/resources/docker_tag with no daemon.
func TestTagExamplePlan(t *testing.T) {
	testExamplePlan(t, filepath.Join("resources", "docker_tag", "resource.tf"))
}

// testExamplePlan plans an example file with no daemon so it stays valid
// against the schema.
func testExamplePlan(t *testing.T, rel string) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", rel))
	if err != nil {
		t.Fatal(err)
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    []tfresource.TestStep{{Config: string(b), PlanOnly: true, ExpectNonEmptyPlan: true}},
	})
}

func TestAccTag_basic(t *testing.T) {
	testAccPreCheck(t)
	c := testAccDockerClient(t)
	target := "tfacc-tag/busybox:acc"
	tfresource.Test(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if _, err := c.ImageInspect(context.Background(), target); !isNotFound(err) {
				return fmt.Errorf("tag %s still exists (%v)", target, err)
			}
			if _, err := c.ImageInspect(context.Background(), "busybox:1.36"); err != nil {
				return fmt.Errorf("source image was removed: %v", err)
			}
			return nil
		},
		Steps: []tfresource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_image" "src" {
  name         = "busybox:1.36"
  keep_locally = true
}

resource "docker_tag" "t" {
  source_image = docker_image.src.name
  target_image = %q
}
`, target),
			Check: tfresource.ComposeAggregateTestCheckFunc(
				tfresource.TestCheckResourceAttrPair("docker_tag.t", "source_image_id", "docker_image.src", "image_id"),
			),
		}},
	})
}
