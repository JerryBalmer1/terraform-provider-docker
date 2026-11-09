package provider

// Pass 5 Lane A (WP cop): acceptance tests for the container data sources
// and actions. They skip without TF_ACC; the actions also need Terraform
// >= 1.14.

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/moby/moby/client"
)

func TestAccCopContainerDataSources(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-cop-ds")
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name    = %[1]q
  image   = %[2]q
  command = ["sh", "-c", "printf hello > /tmp/greeting; sleep 3600"]
  labels  = { tfacc_cop = "1" }
}

data "docker_container" "by_name" {
  name       = docker_container.c.name
  depends_on = [docker_container.c]
}

data "docker_container_changes" "c" {
  container  = docker_container.c.id
  depends_on = [docker_container.c]
}

data "docker_container_top" "c" {
  container  = docker_container.c.name
  depends_on = [docker_container.c]
}

data "docker_container_stats" "c" {
  container  = docker_container.c.name
  depends_on = [docker_container.c]
}

data "docker_container_file" "greeting" {
  container  = docker_container.c.name
  path       = "/tmp/greeting"
  depends_on = [docker_container.c]
}
`, name, actAccImage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.docker_container.by_name", "id", "docker_container.c", "id"),
				resource.TestCheckResourceAttr("data.docker_container.by_name", "labels.tfacc_cop", "1"),
				resource.TestCheckResourceAttr("data.docker_container.by_name", "inspect.state.running", "true"),
				resource.TestCheckResourceAttrSet("data.docker_container.by_name", "content_sha256"),
				resource.TestCheckTypeSetElemNestedAttrs("data.docker_container_changes.c", "changes.*", map[string]string{"path": "/tmp/greeting"}),
				resource.TestCheckResourceAttrSet("data.docker_container_top.c", "top.titles.0"),
				resource.TestCheckResourceAttrPair("data.docker_container_stats.c", "id", "docker_container.c", "id"),
				resource.TestCheckResourceAttrSet("data.docker_container_stats.c", "stats_json"),
				resource.TestCheckResourceAttr("data.docker_container_file.greeting", "content_base64", "aGVsbG8="),
				resource.TestCheckResourceAttr("data.docker_container_file.greeting", "stat.size", "5"),
			),
		}},
	})
}

// copAccAction is a running container plus one action triggered after it is
// created.
func copAccAction(name, actionHCL, actionRef string) string {
	return actAccContainer(name, fmt.Sprintf(`
%s

resource "terraform_data" "trigger" {
  input = docker_container.c.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [%s]
    }
  }
}
`, actionHCL, actionRef))
}

// copAccState checks the container's state after the actions ran.
func copAccState(t *testing.T, name string, want func(running, paused bool) bool, what string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		res, err := testAccDockerClient(t).ContainerInspect(context.Background(), name, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		st := res.Container.State
		if st == nil || !want(st.Running, st.Paused) {
			return fmt.Errorf("container %s: want %s, state %+v", name, what, st)
		}
		return nil
	}
}

func TestAccCopContainerVerbs(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-cop-verbs")
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: copAccAction(name, `
action "docker_container_pause" "p" {
  config {
    container_id = docker_container.c.id
  }
}`, "action.docker_container_pause.p"),
				Check: copAccState(t, name, func(_, paused bool) bool { return paused }, "paused"),
			},
		},
	})
	// A second, independent case: renaming docker_container.c would update it
	// in place (same container, still paused), and terraform_data.trigger
	// would then only be updated, so its after_create actions would not run.
	// A fresh configuration gets a fresh container and a fresh trigger.
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: copAccAction(name+"-2", `
action "docker_container_stop" "s" {
  config {
    container_id = docker_container.c.id
    timeout      = 1
  }
}

action "docker_container_start" "s" {
  config {
    container_id = docker_container.c.id
  }
}

action "docker_container_restart" "r" {
  config {
    container_id = docker_container.c.id
    timeout      = 1
  }
}

action "docker_container_kill" "k" {
  config {
    container_id = docker_container.c.id
    signal       = "SIGKILL"
  }
}

action "docker_container_unpause" "u" {
  config {
    container_id = docker_container.c.id
  }
}`, "action.docker_container_stop.s, action.docker_container_start.s, action.docker_container_restart.r, action.docker_container_kill.k"),
				Check: copAccState(t, name+"-2", func(running, _ bool) bool { return !running }, "stopped (killed)"),
				// docker_container.c has must_run = true (the default): after
				// the out-of-band kill, the refresh must plan it to be started
				// again in place.
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_container.c", plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue("docker_container.c", tfjsonpath.New("running"), knownvalue.Bool(true)),
				}},
			},
		},
	})
}

func TestAccCopContainerUnpauseError(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-cop-unpause")
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: copAccAction(name, `
action "docker_container_unpause" "u" {
  config {
    container_id = docker_container.c.id
  }
}`, "action.docker_container_unpause.u"),
			// Terraform wraps diagnostic text, so any whitespace may be a newline.
			ExpectError: regexp.MustCompile(`(?i)not\s+paused`),
		}},
	})
}

func TestAccCopContainerWait(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-cop-wait")
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: copAccAction(name, `
action "docker_container_kill" "k" {
  config {
    container_id = docker_container.c.id
  }
}

action "docker_container_wait" "w" {
  config {
    container_id     = docker_container.c.id
    timeout          = "1m"
    expect_exit_code = 0
  }
}`, "action.docker_container_kill.k, action.docker_container_wait.w"),
			// SIGKILL exits 137.
			ExpectError: regexp.MustCompile(`exited\s+with\s+code\s+137,\s+expected\s+0`),
		}},
	})
}

func TestAccCopContainerCp(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-cop-cp")
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: copAccAction(name, `
action "docker_container_cp" "x" {
  config {
    container_id = docker_container.c.id
    path         = "/tmp/copied.txt"
    content      = "copied!"
    mode         = "0600"
  }
}`, "action.docker_container_cp.x"),
			Check: func(*terraform.State) error {
				res, err := testAccDockerClient(t).ContainerStatPath(context.Background(), name, client.ContainerStatPathOptions{Path: "/tmp/copied.txt"})
				if err != nil {
					return fmt.Errorf("docker_container_cp did not create /tmp/copied.txt: %w", err)
				}
				if res.Stat.Size != 7 || res.Stat.Mode.Perm() != 0o600 {
					return fmt.Errorf("/tmp/copied.txt: size %d mode %v", res.Stat.Size, res.Stat.Mode)
				}
				return nil
			},
		}},
	})
}
