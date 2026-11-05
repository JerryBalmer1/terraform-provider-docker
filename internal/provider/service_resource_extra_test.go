package provider

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// TestServiceFakeDaemonP2Attributes creates a service using the Pass 2
// container attributes and networks_advanced, updates some in place and
// imports it, against the fake Engine API.
func TestServiceFakeDaemonP2Attributes(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	cfg := func(force int, hostsEntry string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_service" "p2" {
  name              = "p2"
  image             = "busybox:1.36"
  args              = ["sleep", "infinity"]
  hostname          = "p2-{{.Task.Slot}}"
  user              = "nobody"
  dir               = "/tmp"
  hosts             = [%q]
  cap_add           = ["NET_ADMIN"]
  cap_drop          = ["MKNOD"]
  sysctls           = { "net.core.somaxconn" = "1024" }
  stop_grace_period = "1m"
  force_update      = %d

  networks_advanced {
    name    = "ext_net"
    aliases = ["p2-alias"]
  }

  dns {
    nameservers = ["10.0.0.2"]
    options     = ["ndots:2"]
  }

  ulimits {
    name = "nofile"
    soft = 1024
    hard = 2048
  }

  converge_config {
    timeout = "10s"
    delay   = "100ms"
  }
}
`, hostsEntry, force))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg(1, "db.local:10.0.0.5"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.p2", "hosts.0", "db.local:10.0.0.5"),
					resource.TestCheckResourceAttr("docker_service.p2", "networks_advanced.0.name", "ext_net"),
					resource.TestCheckResourceAttr("docker_service.p2", "stop_grace_period", "1m"),
					resource.TestCheckNoResourceAttr("docker_service.p2", "networks"),
					func(*terraform.State) error {
						f.mu.Lock()
						defer f.mu.Unlock()
						s := f.findService("p2")
						cs := s.Spec.TaskTemplate.ContainerSpec
						if !reflect.DeepEqual(cs.Hosts, []string{"10.0.0.5 db.local"}) || cs.Hostname != "p2-{{.Task.Slot}}" || cs.User != "nobody" || cs.Dir != "/tmp" {
							return fmt.Errorf("container spec = %+v", cs)
						}
						if cs.StopGracePeriod == nil || *cs.StopGracePeriod != time.Minute || s.Spec.TaskTemplate.ForceUpdate != 1 {
							return fmt.Errorf("grace/force = %v %d", cs.StopGracePeriod, s.Spec.TaskTemplate.ForceUpdate)
						}
						n := s.Spec.TaskTemplate.Networks
						if len(n) != 1 || n[0].Target != "extnet0000000001" || !reflect.DeepEqual(n[0].Aliases, []string{"p2-alias"}) {
							return fmt.Errorf("networks = %+v", n)
						}
						return nil
					},
				),
			},
			{
				Config: cfg(2, "db.local:10.0.0.6"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.p2", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					s := f.findService("p2")
					if s.Spec.TaskTemplate.ForceUpdate != 2 || s.Spec.TaskTemplate.ContainerSpec.Hosts[0] != "10.0.0.6 db.local" || f.creates["p2"] != 1 {
						return fmt.Errorf("in-place update not applied: force=%d hosts=%v creates=%d", s.Spec.TaskTemplate.ForceUpdate,
							s.Spec.TaskTemplate.ContainerSpec.Hosts, f.creates["p2"])
					}
					return nil
				},
			},
			{
				ResourceName:      "docker_service.p2",
				ImportState:       true,
				ImportStateVerify: true,
				// force_update is ignored on refresh unless configured; networks_advanced
				// names come back as IDs on import.
				ImportStateVerifyIgnore: []string{"converge_config", "image", "force_update", "networks_advanced.0.name", "labels.%"},
			},
		},
	})
}

// TestServiceFakeDaemonJobMode runs a replicated job (converges on
// completed tasks), updates it in place, then switches to a non-job mode,
// which must replace the service.
func TestServiceFakeDaemonJobMode(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	job := func(total int) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_service" "job" {
  name              = "job"
  image             = "busybox:1.36"
  args              = ["true"]
  mode              = "replicated_job"
  max_concurrent    = 2
  total_completions = %d

  restart_policy {
    condition = "none"
  }

  converge_config {
    timeout = "10s"
    delay   = "100ms"
  }
}
`, total))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: job(4),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.job", "mode", "replicated_job"),
					resource.TestCheckResourceAttr("docker_service.job", "total_completions", "4"),
				),
			},
			{
				Config: job(6),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.job", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if s := f.findService("job"); *s.Spec.Mode.ReplicatedJob.TotalCompletions != 6 {
						return fmt.Errorf("total_completions not updated")
					}
					return nil
				},
			},
			{
				Config: svcFakeConfig(host, `
resource "docker_service" "job" {
  name  = "job"
  image = "busybox:1.36"
  args  = ["true"]
  mode  = "global_job"
}
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.job", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("docker_service.job", "mode", "global_job"),
			},
		},
	})
}

// TestServiceNetworksConflict: networks and networks_advanced together fail
// at plan time.
func TestServiceNetworksConflict(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: svcFakeConfig(host, `
resource "docker_service" "c" {
  name     = "c"
  image    = "busybox"
  networks = ["ext_net"]
  networks_advanced {
    name = "ext_net"
  }
}
`),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`Conflicting network settings`),
		}},
	})
}
