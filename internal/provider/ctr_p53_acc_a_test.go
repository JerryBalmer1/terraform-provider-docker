package provider

// Pass 5.3 Lane A acceptance tests (dind harness, test/harness/Invoke-E2E.ps1):
// docker_container state "exited" / "created" (D1, with the #211 class
// check: a stopped container with port bindings plans with no diff), the
// stop-then-inspect regression (D3, Moby #50133), start (D3) and the
// docker_container_run action (D2). They skip without TF_ACC / DOCKER_HOST;
// the action test also needs Terraform >= 1.14.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/client"
)

// TestAccContainerP53_exited: a one-shot run kept as an exited container;
// a second plan (the container is stopped, its port binding has no runtime
// NetworkSettings.Ports) is empty.
func TestAccContainerP53_exited(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-p53-exited")
	cfg := fmt.Sprintf(`
resource "docker_container" "c" {
  name          = %q
  image         = %q
  command       = ["sh", "-c", "echo run-ok; echo to-stderr >&2; exit 0"]
  state         = "exited"
  logs_tail     = 50
  pids_limit    = 64
  oom_score_adj = 10
  annotations   = { "org.example.test" = "p53" }
  ports {
    container_port = 8080
  }
}
`, name, actAccImage)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_container.c", "state", "exited"),
					resource.TestCheckResourceAttr("docker_container.c", "running", "false"),
					resource.TestCheckResourceAttr("docker_container.c", "exit_code", "0"),
					resource.TestCheckResourceAttrSet("docker_container.c", "finished_at"),
					resource.TestCheckResourceAttr("docker_container.c", "log_entries.#", "2"),
					resource.TestCheckResourceAttr("docker_container.c", "pids_limit", "64"),
					resource.TestCheckResourceAttr("docker_container.c", "ports.0.container_port", "8080"),
					func(s *terraform.State) error {
						logs, _ := p3Attr(s, "docker_container.c", "logs")
						if !strings.Contains(logs, "stdout") || !strings.Contains(logs, "run-ok") || !strings.Contains(logs, "to-stderr") {
							return fmt.Errorf("logs = %q", logs)
						}
						return nil
					},
				),
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
		},
	})
}

// TestAccContainerP53_createdNotStarted: state = "created" never starts
// the container, and the next plan is empty.
func TestAccContainerP53_createdNotStarted(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-p53-created")
	cfg := fmt.Sprintf(`
resource "docker_container" "c" {
  name    = %q
  image   = %q
  command = ["sleep", "3600"]
  state   = "created"
  ports {
    container_port = 80
    host_port      = 18089
  }
}
`, name, actAccImage)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_container.c", "running", "false"),
					func(s *terraform.State) error {
						insp, err := ctrP3Inspect(t, s, "docker_container.c")
						if err != nil {
							return err
						}
						if insp.State.Status != "created" {
							return fmt.Errorf("status = %s, want created", insp.State.Status)
						}
						return nil
					},
				),
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
		},
	})
}

// TestAccContainerP53_startStopInspect (D3): a container starts (the start
// request carries no body), and an inspect right after ContainerStop returns
// sees "exited" (Moby #50133, fixed by moby PR #50136). The stop is the
// docker_container_stop action's call sequence (ContainerStop, then
// ContainerInspect) made with the provider's own client settings.
func TestAccContainerP53_startStopInspect(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-p53-stop")
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name    = %q
  image   = %q
  command = ["sleep", "3600"]
}
`, name, actAccImage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_container.c", "running", "true"),
				func(s *terraform.State) error {
					c := testAccDockerClient(t)
					id, _ := p3Attr(s, "docker_container.c", "id")
					for i := 0; i < 5; i++ {
						if i > 0 {
							if _, err := c.ContainerStart(context.Background(), id, client.ContainerStartOptions{}); err != nil {
								return fmt.Errorf("restart %d: %v", i, err)
							}
						}
						timeout := 1
						if _, err := c.ContainerStop(context.Background(), id, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
							return fmt.Errorf("stop %d: %v", i, err)
						}
						res, err := c.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
						if err != nil {
							return err
						}
						if st := res.Container.State; st == nil || st.Running || st.Status != "exited" {
							return fmt.Errorf("round %d: inspect right after stop: %+v (Moby #50133)", i, st)
						}
					}
					return nil
				},
			),
			// The out-of-band stop leaves running = false: the refresh plans a start.
			ExpectNonEmptyPlan: true,
		}},
	})
}

// TestAccContainerRun_basic (D2): docker_container_run runs a container to
// completion, writes exit_code / stdout / stderr to result_path and removes
// the container.
func TestAccContainerRun_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-run")
	out := filepath.Join(t.TempDir(), "run.json")
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_container_run" "r" {
  config {
    name        = %[1]q
    image       = %[2]q
    command     = ["sh", "-c", "echo hello-run; echo err-run >&2; exit 0"]
    labels      = { tfacc = "run" }
    result_path = %[3]q
  }
}

resource "terraform_data" "trigger" {
  input = "x"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_container_run.r]
    }
  }
}
`, name, actAccImage, filepath.ToSlash(out)),
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(out)
				if err != nil {
					return err
				}
				var res struct {
					ContainerID string `json:"container_id"`
					Result      struct {
						ExitCode float64 `json:"exit_code"`
						Stdout   string  `json:"stdout"`
						Stderr   string  `json:"stderr"`
					} `json:"result"`
				}
				if err := json.Unmarshal(b, &res); err != nil {
					return err
				}
				if res.Result.ExitCode != 0 || res.Result.Stdout != "hello-run\n" || res.Result.Stderr != "err-run\n" || res.ContainerID == "" {
					return fmt.Errorf("result = %s", b)
				}
				if _, err := testAccDockerClient(t).ContainerInspect(context.Background(), res.ContainerID, client.ContainerInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("container %s not removed: %v", res.ContainerID, err)
				}
				return nil
			},
		}},
	})
}
