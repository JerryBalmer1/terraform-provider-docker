package provider

// Acceptance tests of the Pass 5 compose and stack types. They skip
// without TF_ACC (testAccPreCheck), actions below Terraform 1.14, the
// registry ones without TF_ACC_REGISTRY, the stack ones unless DOCKER_HOST
// is a Swarm manager.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const composeAccProject = `
resource "docker_compose" "acc" {
  name    = "tfacc-c5"
  content = <<-EOT
    services:
      web:
        image: nginx:alpine
        ports: ["18085:80"]
      job:
        image: busybox:1.36
        command: ["sh", "-c", "echo hello-from-job"]
        restart: "no"
        # A top-level volume no service uses is not created (compose drops
        # unused resources), so job mounts it.
        volumes: ["data:/data"]
    volumes:
      data: {}
  EOT
}
`

func TestAccComposeDataSources(t *testing.T) {
	testAccPreCheck(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: composeAccProject},
			{
				Config: composeAccProject + `
data "docker_compose_containers" "c" {
  name       = docker_compose.acc.name
  all        = true
}
data "docker_compose_projects" "p" {
  all        = true
  depends_on = [docker_compose.acc]
}
data "docker_compose_volumes" "v" {
  name       = docker_compose.acc.name
  depends_on = [docker_compose.acc]
}
data "docker_compose_images" "i" {
  name       = docker_compose.acc.name
  depends_on = [docker_compose.acc]
}
data "docker_compose_logs" "l" {
  name       = docker_compose.acc.name
  services   = ["job"]
  depends_on = [docker_compose.acc]
}
data "docker_compose_port" "web" {
  name         = docker_compose.acc.name
  service      = "web"
  private_port = 80
  depends_on   = [docker_compose.acc]
}
data "docker_compose_top" "t" {
  name       = docker_compose.acc.name
  services   = ["web"]
  depends_on = [docker_compose.acc]
}
data "docker_compose_config" "cfg" {
  name    = "tfacc-c5"
  content = docker_compose.acc.content
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.docker_compose_containers.c", "container_ids.web.#", "1"),
					resource.TestCheckTypeSetElemAttr("data.docker_compose_projects.p", "names.*", "tfacc-c5"),
					resource.TestCheckResourceAttr("data.docker_compose_volumes.v", "names.0", "tfacc-c5_data"),
					resource.TestCheckResourceAttrSet("data.docker_compose_images.i", "image_ids.web"),
					resource.TestMatchResourceAttr("data.docker_compose_logs.l", "logs", regexp.MustCompile(`job\.tfacc-c5-job-1 hello-from-job`)),
					resource.TestCheckResourceAttr("data.docker_compose_port.web", "published_port", "18085"),
					resource.TestCheckResourceAttrSet("data.docker_compose_top.t", "processes.tfacc-c5-web-1.titles.0"),
					resource.TestCheckResourceAttrPair("data.docker_compose_config.cfg", "content_sha256", "docker_compose.acc", "content_sha256"),
				),
			},
		},
	})
}

func TestAccComposeActions(t *testing.T) {
	testAccPreCheck(t)
	dir := t.TempDir()
	res := filepath.ToSlash(filepath.Join(dir, "run.json"))
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   actAccVersionChecks,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_compose" "acc" {
  name    = "tfacc-c5a"
  content = <<-EOT
    services:
      web:
        image: nginx:alpine
      job:
        image: busybox:1.36
        command: ["sh", "-c", "exit 0"]
        restart: "no"
  EOT
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [
        action.docker_compose_wait.job,
        action.docker_compose_exec.web,
        action.docker_compose_run.job,
        action.docker_compose_pause.web,
        action.docker_compose_unpause.web,
        action.docker_compose_stop.web,
        action.docker_compose_start.web,
        action.docker_compose_restart.web,
        action.docker_compose_kill.web,
        action.docker_compose_wait.web_killed,
        action.docker_compose_scale.web,
        action.docker_compose_cp.web,
        action.docker_compose_export.web,
        action.docker_compose_commit.web,
      ]
    }
  }
}
action "docker_compose_wait" "job" {
  config {
    name     = "tfacc-c5a"
    services = ["job"]
  }
}
action "docker_compose_exec" "web" {
  config {
    name    = "tfacc-c5a"
    service = "web"
    command = ["nginx", "-t"]
  }
}
action "docker_compose_run" "job" {
  config {
    name        = "tfacc-c5a"
    content     = docker_compose.acc.content
    service     = "job"
    command     = ["echo", "ran"]
    result_path = %[1]q
  }
}
action "docker_compose_pause" "web" {
  config {
    name     = "tfacc-c5a"
    services = ["web"]
  }
}
action "docker_compose_unpause" "web" {
  config {
    name     = "tfacc-c5a"
    services = ["web"]
  }
}
action "docker_compose_stop" "web" {
  config {
    name     = "tfacc-c5a"
    services = ["web"]
    timeout  = 2
  }
}
action "docker_compose_start" "web" {
  config {
    name     = "tfacc-c5a"
    services = ["web"]
  }
}
action "docker_compose_restart" "web" {
  config {
    name     = "tfacc-c5a"
    services = ["web"]
  }
}
action "docker_compose_kill" "web" {
  config {
    name     = "tfacc-c5a"
    services = ["web"]
    signal   = "SIGKILL"
  }
}
# The kill API returns before the container has exited: wait for it, so
# scale sees web-1 exited (and starts it again) rather than still running.
action "docker_compose_wait" "web_killed" {
  config {
    name     = "tfacc-c5a"
    services = ["web"]
  }
}
action "docker_compose_scale" "web" {
  config {
    name     = "tfacc-c5a"
    content  = docker_compose.acc.content
    replicas = { web = 2 }
  }
}
action "docker_compose_cp" "web" {
  config {
    name           = "tfacc-c5a"
    service        = "web"
    direction      = "from_container"
    container_path = "/etc/nginx/conf.d"
    local_path     = %[2]q
  }
}
action "docker_compose_export" "web" {
  config {
    name    = "tfacc-c5a"
    service = "web"
    output  = %[3]q
  }
}
action "docker_compose_commit" "web" {
  config {
    name       = "tfacc-c5a"
    service    = "web"
    repository = "tfacc-c5a-web:snapshot"
  }
}
`, res, filepath.ToSlash(filepath.Join(dir, "conf")), filepath.ToSlash(filepath.Join(dir, "web.tar"))),
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(filepath.Join(dir, "run.json"))
				if err != nil {
					return err
				}
				var r map[string]any
				if err := json.Unmarshal(b, &r); err != nil {
					return err
				}
				if out := r["result"].(map[string]any)["stdout"]; out != "ran\n" {
					return fmt.Errorf("run stdout %q", out)
				}
				if _, err := os.Stat(filepath.Join(dir, "conf", "conf.d", "default.conf")); err != nil {
					return fmt.Errorf("cp: %v", err)
				}
				if fi, err := os.Stat(filepath.Join(dir, "web.tar")); err != nil || fi.Size() == 0 {
					return fmt.Errorf("export: %v", err)
				}
				// scale web=2 after kill: like `docker compose scale`, the
				// killed web-1 is started again and web-2 is created.
				cs, err := testAccDockerClient(t).ContainerList(context.Background(), client.ContainerListOptions{All: true,
					Filters: client.Filters{}.Add("label", composeLabelProject+"=tfacc-c5a").Add("label", composeLabelService+"=web")})
				if err != nil {
					return err
				}
				running := 0
				var states []string
				for _, c := range cs.Items {
					states = append(states, fmt.Sprintf("%v=%s", c.Names, c.State))
					if c.State == container.StateRunning {
						running++
					}
				}
				if len(cs.Items) != 2 || running != 2 {
					return fmt.Errorf("after scale web=2: web containers %v; want 2 running", states)
				}
				return nil
			},
			// The scale action leaves web-2, which is not in the
			// configuration: the refresh must report that drift and plan
			// docker_compose.acc for an in-place update (compose up).
			ExpectNonEmptyPlan: true,
			ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("docker_compose.acc", plancheck.ResourceActionUpdate),
			}},
		}},
	})
}

// TestAccComposeImageActions pulls, builds, pushes and publishes against
// the harness registry (TF_ACC_REGISTRY).
func TestAccComposeImageActions(t *testing.T) {
	testAccPreCheck(t)
	reg := os.Getenv("TF_ACC_REGISTRY")
	if reg == "" {
		t.Skip("TF_ACC_REGISTRY not set; skipping compose image actions")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "Dockerfile"), []byte(hAccConfig("FROM busybox:1.36\nRUN echo built > /built\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks:   actAccVersionChecks,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
locals {
  project = <<-EOT
    services:
      app:
        build: {context: ./app}
        image: %[1]s/tfacc/c5app:1
  EOT
}
resource "terraform_data" "go" {
  input = "x"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_compose_pull.base, action.docker_compose_build.app, action.docker_compose_push.app, action.docker_compose_publish.app]
    }
  }
}
action "docker_compose_pull" "base" {
  config {
    name    = "tfacc-c5b"
    content = "services:\n  base: {image: busybox:1.36}\n"
  }
}
action "docker_compose_build" "app" {
  config {
    name              = "tfacc-c5b"
    project_directory = %[2]q
    content           = local.project
  }
}
action "docker_compose_push" "app" {
  config {
    name              = "tfacc-c5b"
    project_directory = %[2]q
    content           = local.project
  }
}
action "docker_compose_publish" "app" {
  config {
    name              = "tfacc-c5b"
    project_directory = %[2]q
    content           = local.project
    repository        = "%[1]s/tfacc/c5app-compose:1"
  }
}
`, reg, filepath.ToSlash(dir)),
		}},
	})
}

// TestAccStackDataSources needs a Swarm manager (swarm pass).
func TestAccStackDataSources(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	cfg := `
resource "docker_stack" "acc" {
  name = "tfaccc5"
  service {
    name     = "web"
    image    = "nginx:alpine"
    replicas = 1
  }
}
`
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_stack.acc", "service_ids.web"),
					resource.TestMatchResourceAttr("docker_stack.acc", "stack_yaml", regexp.MustCompile(regexp.QuoteMeta(hAccImage("nginx:alpine")))),
				),
			},
			{
				Config: cfg + `
data "docker_stacks" "all" {
  depends_on = [docker_stack.acc]
}
data "docker_stack" "acc" {
  name       = docker_stack.acc.name
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckTypeSetElemAttr("data.docker_stacks.all", "names.*", "tfaccc5"),
					resource.TestCheckResourceAttrPair("data.docker_stack.acc", "service_ids.web", "docker_stack.acc", "service_ids.web"),
					resource.TestCheckResourceAttr("data.docker_stack.acc", "services.0.desired_replicas", "1"),
				),
			},
		},
	})
}
