package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

func TestImageResourceSchemas(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]fwresource.Resource{"docker_image": NewImageResource(), "docker_registry_image": NewRegistryImageResource()} {
		var resp fwresource.SchemaResponse
		r.Schema(ctx, fwresource.SchemaRequest{}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s: %v", name, diags)
		}
		for _, a := range []string{"id", "name", "triggers"} {
			if _, ok := resp.Schema.Attributes[a]; !ok {
				t.Errorf("%s: missing %s", name, a)
			}
		}
	}
}

func TestImageBuildOptions(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	plan := &imageResourceModel{
		Name:     types.StringValue("app:1"),
		Platform: types.StringValue("linux/amd64"),
		Build: &imageBuildModel{
			Context:  types.StringValue("."),
			Args:     stringMapValue(map[string]string{"A": "1", "B": ""}, true),
			Labels:   stringMapValue(map[string]string{"team": "x"}, true),
			Target:   types.StringValue("prod"),
			Platform: types.StringNull(),
			NoCache:  types.BoolValue(true),
			Pull:     types.BoolNull(),
		},
	}
	o := imgBuildOptions(ctx, plan, "Dockerfile.prod", &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if len(o.Tags) != 1 || o.Tags[0] != "app:1" || o.Dockerfile != "Dockerfile.prod" || o.Target != "prod" {
		t.Errorf("basic fields: %+v", o)
	}
	if platformsString(o.Platforms) != "linux/amd64" {
		t.Errorf("platform should fall back to top-level: %q", platformsString(o.Platforms))
	}
	if o.BuildArgs["A"] == nil || *o.BuildArgs["A"] != "1" || o.BuildArgs["B"] == nil || *o.BuildArgs["B"] != "" {
		t.Errorf("build args: %v", o.BuildArgs)
	}
	if o.Labels["team"] != "x" || o.Labels[labelManagedBy] != labelManagedByValue {
		t.Errorf("labels: %v", o.Labels)
	}
	if !o.NoCache || o.PullParent || !o.Remove {
		t.Errorf("flags: %+v", o)
	}
	plan.Build.Platform = types.StringValue("linux/arm64")
	if o := imgBuildOptions(ctx, plan, "Dockerfile", &diags); platformsString(o.Platforms) != "linux/arm64" {
		t.Errorf("build.platform should win: %q", platformsString(o.Platforms))
	}
}

func TestImageAuthConfigs(t *testing.T) {
	m := imgAuthConfigs([]registryCredential{
		{Address: "registry-1.docker.io", Username: "hub", Password: "p"},
		{Address: "https://ghcr.io", Username: "gh", Password: "q"},
		{Address: "myacr.azurecr.io", IdentityToken: "refresh-tok"},
	})
	if m["myacr.azurecr.io"].IdentityToken != "refresh-tok" {
		t.Errorf("identity token not passed through: %+v", m["myacr.azurecr.io"])
	}
	if m[imgDockerHubIndexServer].Username != "hub" || m["docker.io"].Username != "hub" {
		t.Errorf("hub keys: %v", m)
	}
	if m["ghcr.io"].Username != "gh" || m["https://ghcr.io"].Password != "q" {
		t.Errorf("ghcr keys: %v", m)
	}
	if len(imgAuthConfigs(nil)) != 0 {
		t.Error("nil creds should give empty map")
	}
}

func TestImagePlanContextDigest(t *testing.T) {
	if d := imgPlanContextDigest(nil); !d.IsNull() {
		t.Errorf("no build: %v", d)
	}
	if d := imgPlanContextDigest(&imageBuildModel{Context: types.StringUnknown(), Dockerfile: types.StringNull()}); !d.IsUnknown() {
		t.Errorf("unknown context: %v", d)
	}
	if d := imgPlanContextDigest(&imageBuildModel{Context: types.StringValue(filepath.Join(t.TempDir(), "missing")), Dockerfile: types.StringNull()}); !d.IsUnknown() {
		t.Errorf("missing context: %v", d)
	}
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": "FROM scratch"})
	d := imgPlanContextDigest(&imageBuildModel{Context: types.StringValue(dir), Dockerfile: types.StringNull()})
	if d.IsUnknown() || d.IsNull() || !strings.HasPrefix(d.ValueString(), "sha256:") {
		t.Errorf("digest: %v", d)
	}
}

func TestImageApplyInspect(t *testing.T) {
	m := imageResourceModel{Name: types.StringNull(), KeepLocally: types.BoolNull(), ForceRemove: types.BoolNull()}
	imgApplyInspect(&m, image.InspectResponse{ID: "sha256:1", RepoTags: []string{"nginx:1"}, RepoDigests: []string{"nginx@sha256:2"}})
	if m.ID.ValueString() != "sha256:1" || m.ImageID.ValueString() != "sha256:1" || m.Name.ValueString() != "nginx:1" ||
		m.RepoDigest.ValueString() != "nginx@sha256:2" || m.KeepLocally.ValueBool() || m.KeepLocally.IsNull() {
		t.Errorf("model: %+v", m)
	}
	m2 := imageResourceModel{Name: types.StringValue("app:dev")}
	imgApplyInspect(&m2, image.InspectResponse{ID: "sha256:9"})
	if m2.Name.ValueString() != "app:dev" || !m2.RepoDigest.IsNull() {
		t.Errorf("built image: %+v", m2)
	}
}

func TestImageDataSourceApply(t *testing.T) {
	m := imageDataSourceModel{Name: types.StringValue("nginx")}
	imgDataSourceApply(&m, image.InspectResponse{ID: "sha256:1", RepoTags: []string{"nginx:latest"}})
	if m.ID.ValueString() != "sha256:1" || len(m.RepoTags.Elements()) != 1 || m.Labels.IsNull() || !m.RepoDigest.IsNull() {
		t.Errorf("model: %+v", m)
	}
}

// imgFakeEngine serves just enough of the Engine API for imgRemove.
type imgFakeEngine struct {
	mu      sync.Mutex
	images  map[string]string // ref -> ID
	removed []string
}

func (f *imgFakeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if i := strings.Index(p, "/images/"); i >= 0 {
		p = p[i+len("/images/"):]
	}
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/json"):
		ref := strings.TrimSuffix(p, "/json")
		id, ok := f.images[ref]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"message":"No such image: %s"}`, ref)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id})
	case r.Method == http.MethodDelete:
		if _, ok := f.images[p]; !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"message":"No such image: %s"}`, p)
			return
		}
		if r.URL.Query().Get("force") != "1" && r.URL.Query().Get("force") != "true" && p == "busy:1" {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"image is being used by stopped container"}`)
			return
		}
		f.removed = append(f.removed, p)
		delete(f.images, p)
		_, _ = w.Write([]byte(`[{"Untagged":"x"}]`))
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func imgFakeClient(t *testing.T, f *imgFakeEngine) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestImageRemove(t *testing.T) {
	ctx := context.Background()
	f := &imgFakeEngine{images: map[string]string{"app:1": "sha256:a", "sha256:a": "sha256:a", "sha256:old": "sha256:old", "busy:1": "sha256:b"}}
	c := imgFakeClient(t, f)

	// Tag still points at our image: untag by name.
	if err := imgRemove(ctx, c, "app:1", "sha256:a", false); err != nil {
		t.Fatal(err)
	}
	// Tag moved to another image: remove ours by ID.
	f.images["app:2"] = "sha256:new"
	if err := imgRemove(ctx, c, "app:2", "sha256:old", false); err != nil {
		t.Fatal(err)
	}
	// Already gone: no error.
	if err := imgRemove(ctx, c, "gone:1", "sha256:gone", false); err != nil {
		t.Errorf("missing image should not fail: %v", err)
	}
	if strings.Join(f.removed, ",") != "app:1,sha256:old" {
		t.Errorf("removed %v", f.removed)
	}
	// Conflict surfaces without force, succeeds with it.
	if err := imgRemove(ctx, c, "busy:1", "sha256:b", false); err == nil {
		t.Error("conflict should fail without force")
	}
	if err := imgRemove(ctx, c, "busy:1", "sha256:b", true); err != nil {
		t.Errorf("force: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Acceptance

// testAccImageCheckDestroy verifies every docker_image tag is gone.
func testAccImageCheckDestroy(t *testing.T) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c := testAccDockerClient(t)
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "docker_image" {
				continue
			}
			name := rs.Primary.Attributes["name"]
			if insp, err := c.ImageInspect(context.Background(), name); err == nil && insp.ID == rs.Primary.Attributes["image_id"] {
				return fmt.Errorf("image %s still present", name)
			} else if err != nil && !isNotFound(err) {
				return err
			}
		}
		return nil
	}
}

func TestAccImagePull(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccImageCheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: `
resource "docker_image" "alpine" {
  name = "alpine:3.20"
}
data "docker_image" "alpine" {
  name = docker_image.alpine.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("docker_image.alpine", "image_id", imgTestSHA),
					resource.TestCheckResourceAttrPair("docker_image.alpine", "id", "docker_image.alpine", "image_id"),
					resource.TestCheckResourceAttrSet("docker_image.alpine", "repo_digest"),
					resource.TestCheckNoResourceAttr("docker_image.alpine", "context_digest"),
					resource.TestCheckResourceAttrPair("data.docker_image.alpine", "image_id", "docker_image.alpine", "image_id"),
				),
			},
			{
				ResourceName:            "docker_image.alpine",
				ImportState:             true,
				ImportStateId:           "alpine:3.20",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"keep_locally", "force_remove"},
			},
		},
	})
}

func TestAccImageBuild(t *testing.T) {
	testAccPreCheck(t)
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{
		"Dockerfile":    "FROM alpine:3.20\nARG GREETING\nCOPY hello.txt /hello.txt\nRUN echo \"$GREETING\" > /greeting\n",
		"hello.txt":     "v1",
		".dockerignore": "*.secret\n",
		"x.secret":      "nope",
	})
	cfg := fmt.Sprintf(`
resource "docker_image" "app" {
  name = "tfacc-image-build:latest"
  build {
    context = %q
    args    = { GREETING = "hi" }
    labels  = { "tfacc" = "1" }
  }
}
data "docker_image" "app" {
  name = docker_image.app.image_id
}
`, filepath.ToSlash(dir))
	var firstID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccImageCheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("docker_image.app", "image_id", imgTestSHA),
					resource.TestMatchResourceAttr("docker_image.app", "context_digest", imgTestSHA),
					resource.TestCheckResourceAttr("data.docker_image.app", "labels.tfacc", "1"),
					resource.TestCheckResourceAttr("data.docker_image.app", "labels."+labelManagedBy, labelManagedByValue),
					func(s *terraform.State) error {
						firstID = s.RootModule().Resources["docker_image.app"].Primary.Attributes["image_id"]
						return nil
					},
				),
			},
			{
				// Editing a file in the context forces a rebuild.
				PreConfig: func() { imgTestWrite(t, dir, map[string]string{"hello.txt": "v2"}) },
				Config:    cfg,
				Check: func(s *terraform.State) error {
					if id := s.RootModule().Resources["docker_image.app"].Primary.Attributes["image_id"]; id == firstID {
						return fmt.Errorf("image not rebuilt after context edit (%s)", id)
					}
					return nil
				},
			},
			{
				// Editing an ignored file does not.
				PreConfig: func() {
					if err := os.WriteFile(filepath.Join(dir, "x.secret"), []byte("changed"), 0o644); err != nil {
						t.Fatal(err)
					}
				},
				Config:   cfg,
				PlanOnly: true,
			},
		},
	})
}

var imgTestSHA = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
