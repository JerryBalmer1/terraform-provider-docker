package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestComposeResourceContractC checks the Pass 5 outputs of docker_compose
// (contract compose:name, identity maps), the renamed name /
// project_directory attributes, the CLI config-hash labels and
// remove_orphans = false.
func TestComposeResourceContractC(t *testing.T) {
	f, host := newComposeFakeEngine(t)
	dir := t.TempDir()
	cfg := func(extra, orphans string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_compose" "c" {
  name              = "contract"
  project_directory = %q
  remove_orphans    = %s
  content           = <<-EOT
services:
  web:
    image: nginx:1.27
    environment: {SECRET: hunter2}
    models: [llm]
%s
networks:
  default: {}
models:
  llm: {model: ai/smollm2}
jobs:
  migrate:
    image: busybox
    triggers: {manual: true}
EOT
}
`, filepath.ToSlash(dir), orphans, extra))
	}
	worker := "  worker:\n    image: busybox:1.36\n    deploy: {replicas: 2}"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(worker, "false"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_compose.c", "id", "contract"),
					resource.TestCheckResourceAttr("docker_compose.c", "name", "contract"),
					resource.TestCheckResourceAttr("docker_compose.c", "labels.com.docker.compose.project", "contract"),
					resource.TestCheckResourceAttr("docker_compose.c", "image_refs.web", "nginx:1.27"),
					resource.TestCheckResourceAttr("docker_compose.c", "container_names.worker.1", "contract-worker-2"),
					resource.TestCheckResourceAttr("docker_compose.c", "network_names.default", "contract_default"),
					resource.TestCheckResourceAttr("docker_compose.c", "models.llm", "ai/smollm2"),
					resource.TestCheckResourceAttr("docker_compose.c", "jobs.migrate.#", "0"),
					resource.TestCheckResourceAttr("docker_compose.c", "config_files.#", "1"),
					resource.TestCheckResourceAttr("docker_compose.c", "project.services.web.image", "nginx:1.27"),
					resource.TestCheckResourceAttr("docker_compose.c", "project.services.web.environment.SECRET", "hunter2"),
					resource.TestCheckResourceAttr("docker_compose.c", "project.jobs.migrate.image", "busybox"),
					resource.TestCheckResourceAttr("docker_compose.c", "compose_go_version", contractComposeGoVersion),
					resource.TestCheckResourceAttr("docker_compose.c", "compose_spec_version", contractComposeSpecVersion),
					resource.TestCheckResourceAttr("docker_compose.c", "engine_api_version", "1.47"),
					resource.TestCheckResourceAttr("docker_compose.c", "docker_server_version", "28.5.0"),
					resource.TestMatchResourceAttr("docker_compose.c", "compose_yaml", regexp.MustCompile(`image: nginx:1.27`)),
					resource.TestMatchResourceAttr("docker_compose.c", "content_sha256", regexp.MustCompile(`^[0-9a-f]{64}$`)),
					func(s *terraform.State) error {
						a := s.RootModule().Resources["docker_compose.c"].Primary.Attributes
						if a["content_sha256"] != composeSHA256Hex([]byte(a["compose_yaml"])) {
							return fmt.Errorf("content_sha256 is not the hash of compose_yaml")
						}
						f.mu.Lock()
						defer f.mu.Unlock()
						web := f.byName("contract-web-1")
						if web == nil {
							return fmt.Errorf("no web container")
						}
						if len(web.Config.Labels[composeLabelConfigHash]) != 64 || len(web.Config.Labels[composeLabelSpecHash]) != 64 ||
							web.Config.Labels[composeLabelConfigHash] == web.Config.Labels[composeLabelSpecHash] {
							return fmt.Errorf("hash labels %v", web.Config.Labels)
						}
						if !strings.HasSuffix(filepath.ToSlash(web.Config.Labels[composeLabelConfigFiles]), "/docker-compose.yml") {
							return fmt.Errorf("config_files label %q", web.Config.Labels[composeLabelConfigFiles])
						}
						return nil
					},
				),
			},
			{
				// remove_orphans = false: the worker containers stay and are not drift.
				Config: cfg("", "false"),
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if f.byName("contract-worker-1") == nil || f.byName("contract-worker-2") == nil {
						return fmt.Errorf("orphans removed with remove_orphans = false")
					}
					return nil
				},
			},
			{
				Config:   cfg("", "false"),
				PlanOnly: true,
			},
			{
				// remove_orphans = true removes them.
				Config: cfg("", "true"),
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if f.byName("contract-worker-1") != nil {
						return fmt.Errorf("orphan kept with remove_orphans = true")
					}
					return nil
				},
			},
		},
	})
}

func TestComposeJobsWarningC(t *testing.T) {
	if composeJobsWarning(&ctypes.Project{Name: "p"}) != "" {
		t.Error("warning without jobs")
	}
	p, err := seamComposeImpl.Load(context.Background(), seamComposeLoadRequest{WorkingDir: t.TempDir(), ProjectName: "p",
		Content: "services:\n  a: {image: x}\njobs:\n  migrate:\n    image: busybox\n    triggers: {manual: true}\n"})
	if err != nil {
		t.Fatal(err)
	}
	if w := composeJobsWarning(p); !strings.Contains(w, "migrate") || !strings.Contains(w, "docker_compose_run") {
		t.Errorf("warning %q", w)
	}
}

// TestComposeHashMatchesC covers the pre-Pass 5 label form.
func TestComposeHashMatchesC(t *testing.T) {
	if !composeHashMatches(map[string]string{composeLabelConfigHash: "old"}, "old") {
		t.Error("legacy config-hash form not accepted")
	}
	if composeHashMatches(map[string]string{composeLabelConfigHash: "old", composeLabelSpecHash: "new"}, "old") {
		t.Error("spec-hash label must win")
	}
	if !composeHashMatches(map[string]string{composeLabelConfigHash: "cli", composeLabelSpecHash: "new"}, "new") {
		t.Error("spec-hash match")
	}
}

func TestComposeStateUpgradeV0C(t *testing.T) {
	ctx := context.Background()
	r := NewComposeResource().(*composeResource)
	var sr fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &sr)
	if sr.Schema.Version != 1 {
		t.Fatalf("schema version %d", sr.Schema.Version)
	}
	v0 := `{"id":"shop","project_name":"shop","working_dir":"/srv/shop","content":"services: {}","mode":"local","wait":true,` +
		`"wait_timeout":"2m","keep_volumes":false,"spec_hash":"abc","services":{"web":["c1"]},"networks":{},"volumes":{}}`
	resp := fwresource.UpgradeStateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.UpgradeState(ctx)[0].StateUpgrader(ctx, fwresource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: []byte(v0)}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m composeResourceModel
	if d := resp.State.Get(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	if m.Name.ValueString() != "shop" || m.ProjectDirectory.ValueString() != "/srv/shop" || m.SpecHash.ValueString() != "abc" ||
		!m.RemoveOrphans.IsNull() || !m.Project.IsNull() {
		t.Errorf("upgraded %+v", m)
	}
	resp = fwresource.UpgradeStateResponse{State: tfsdk.State{Schema: sr.Schema}}
	r.UpgradeState(ctx)[0].StateUpgrader(ctx, fwresource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: []byte("[")}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("invalid JSON: no error")
	}
}
