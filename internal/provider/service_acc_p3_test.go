package provider

// Pass 3 acceptance tests for docker_service and docker_config on a live
// Swarm manager (the -SwarmInit harness pass; names start with TestAccService
// / TestAccDockerConfig so its regex selects them).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func svcP3CheckDestroy(t *testing.T) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c := testAccDockerClient(t)
		for _, rs := range s.RootModule().Resources {
			switch rs.Type {
			case "docker_service":
				if _, err := c.ServiceInspect(context.Background(), rs.Primary.ID, client.ServiceInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("service %s still exists (err=%v)", rs.Primary.ID, err)
				}
			case "docker_config":
				if _, err := c.ConfigInspect(context.Background(), rs.Primary.ID, client.ConfigInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("config %s still exists (err=%v)", rs.Primary.ID, err)
				}
			case "docker_secret":
				if _, err := c.SecretInspect(context.Background(), rs.Primary.ID, client.SecretInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("secret %s still exists (err=%v)", rs.Primary.ID, err)
				}
			}
		}
		return nil
	}
}

// svcP3Inspect returns the live spec of the docker_service at addr.
func svcP3Inspect(t *testing.T, s *terraform.State, addr string) (swarm.Service, error) {
	id, err := p3Attr(s, addr, "id")
	if err != nil {
		return swarm.Service{}, err
	}
	svcRes, err := testAccDockerClient(t).ServiceInspect(context.Background(), id, client.ServiceInspectOptions{})
	svc := svcRes.Service
	return svc, err
}

// svcP3TaskStates counts the service's tasks by state.
func svcP3TaskStates(t *testing.T, s *terraform.State, addr string) (map[swarm.TaskState]int, error) {
	id, err := p3Attr(s, addr, "id")
	if err != nil {
		return nil, err
	}
	tasks, err := p3ServiceTasks(context.Background(), testAccDockerClient(t), id)
	if err != nil {
		return nil, err
	}
	out := map[swarm.TaskState]int{}
	for _, tk := range tasks {
		out[tk.Status.State]++
	}
	return out, nil
}

// TestAccServiceP3_globalMode: global mode converges with one running task
// per eligible node; a label-only update converges too (the previous
// UpdateStatus is stale); import verifies (Pass 2 map: L4 global converge /
// stale UpdateStatus).
func TestAccServiceP3_globalMode(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-glob")
	cfg := func(label string) string {
		return fmt.Sprintf(`
resource "docker_service" "g" {
  name    = %q
  image   = "busybox:1.36"
  mode    = "global"
  command = ["sleep", "3600"]
  labels  = { rev = %q }

  converge_config {
    timeout = "3m"
  }
}
`, name, label)
	}
	checkPerNode := func(s *terraform.State) error {
		ctx := context.Background()
		c := testAccDockerClient(t)
		nodesRes, err := c.NodeList(ctx, client.NodeListOptions{})
		if err != nil {
			return err
		}
		nodes := nodesRes.Items
		eligible := 0
		for _, n := range nodes {
			if n.Status.State == swarm.NodeStateReady && n.Spec.Availability == swarm.NodeAvailabilityActive {
				eligible++
			}
		}
		st, err := svcP3TaskStates(t, s, "docker_service.g")
		if err != nil {
			return err
		}
		if st[swarm.TaskStateRunning] < eligible || eligible == 0 {
			return fmt.Errorf("global service: %d running tasks for %d eligible nodes (%v)", st[swarm.TaskStateRunning], eligible, st)
		}
		return nil
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             svcP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{Config: cfg("1"), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("docker_service.g", "mode", "global"), checkPerNode)},
			{
				Config: cfg("2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.g", plancheck.ResourceActionUpdate),
				}},
				Check: checkPerNode,
			},
			{ResourceName: "docker_service.g", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"converge_config", "image"}},
		},
	})
}

// TestAccServiceP3_jobModes: replicated_job converges when total_completions
// tasks completed; changing total_completions re-runs it in place; switching
// to global_job replaces the service (Pass 2 P2-C job modes, live).
func TestAccServiceP3_jobModes(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-job")
	replicated := func(total int) string {
		return fmt.Sprintf(`
resource "docker_service" "j" {
  name              = %q
  image             = "busybox:1.36"
  mode              = "replicated_job"
  total_completions = %d
  max_concurrent    = 1
  command           = ["sh", "-c", "sleep 1"]

  restart_policy {
    condition = "on-failure"
  }

  converge_config {
    timeout = "3m"
  }
}
`, name, total)
	}
	global := fmt.Sprintf(`
resource "docker_service" "j" {
  name    = %q
  image   = "busybox:1.36"
  mode    = "global_job"
  command = ["sh", "-c", "sleep 1"]

  restart_policy {
    condition = "on-failure"
  }

  converge_config {
    timeout = "3m"
  }
}
`, name)
	completed := func(min int) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			st, err := svcP3TaskStates(t, s, "docker_service.j")
			if err != nil {
				return err
			}
			if st[swarm.TaskStateComplete] < min {
				return fmt.Errorf("want >= %d complete tasks, got %v", min, st)
			}
			return nil
		}
	}
	var firstID string
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             svcP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: replicated(2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.j", "total_completions", "2"),
					resource.TestCheckResourceAttr("docker_service.j", "max_concurrent", "1"),
					completed(2),
					func(s *terraform.State) error { firstID, _ = p3Attr(s, "docker_service.j", "id"); return nil },
				),
			},
			{
				Config: replicated(3),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.j", plancheck.ResourceActionUpdate),
				}},
				Check: completed(3),
			},
			{
				Config: global,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.j", plancheck.ResourceActionReplace),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.j", "mode", "global_job"),
					completed(1),
					func(s *terraform.State) error {
						if id, _ := p3Attr(s, "docker_service.j", "id"); id == firstID {
							return fmt.Errorf("mode change did not replace the service")
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccServiceP3_forceUpdate: an out-of-band `docker service update
// --force` (ForceUpdate++) causes no drift while force_update is unset;
// setting it sends the value; removing it later keeps the daemon's counter
// (Pass 2 P2-C force_update preservation).
func TestAccServiceP3_forceUpdate(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-force")
	cfg := func(extra string) string {
		return fmt.Sprintf(`
resource "docker_service" "f" {
  name    = %q
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
%s
  converge_config {
    timeout = "3m"
  }
}
`, name, extra)
	}
	forceIs := func(want uint64) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			svc, err := svcP3Inspect(t, s, "docker_service.f")
			if err != nil {
				return err
			}
			if got := svc.Spec.TaskTemplate.ForceUpdate; got != want {
				return fmt.Errorf("daemon ForceUpdate=%d, want %d", got, want)
			}
			return nil
		}
	}
	var svcID string
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             svcP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("docker_service.f", "force_update"),
					func(s *terraform.State) error { svcID, _ = p3Attr(s, "docker_service.f", "id"); return nil },
				),
			},
			{
				// Like `docker service update --force`.
				PreConfig: func() {
					ctx := context.Background()
					c := testAccDockerClient(t)
					svcRes, err := c.ServiceInspect(ctx, svcID, client.ServiceInspectOptions{})
					if err != nil {
						t.Fatal(err)
					}
					svc := svcRes.Service
					spec := svc.Spec
					spec.TaskTemplate.ForceUpdate++
					if _, err := c.ServiceUpdate(ctx, svcID, client.ServiceUpdateOptions{Version: svc.Version, Spec: spec}); err != nil {
						t.Fatalf("out-of-band force update: %v", err)
					}
				},
				Config:   cfg(""),
				PlanOnly: true,
			},
			{
				Config: cfg("  force_update = 7\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.f", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.f", "force_update", "7"),
					forceIs(7),
				),
			},
			{
				// force_update removed together with another change: the
				// daemon's counter must be preserved, not reset to 0.
				Config: cfg("  env = { REV = \"2\" }\n"),
				Check:  forceIs(7),
			},
		},
	})
}

// TestAccServiceP3_laneCAttributes: every Pass 2 Lane C docker_service
// attribute against a live Swarm: hostname, user, dir, hosts, dns,
// cap_add/cap_drop, sysctls, ulimits, stop_grace_period, networks map
// aliases; created, checked on the daemon spec, updated in place, imported.
func TestAccServiceP3_laneCAttributes(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-svcc")
	cfg := func(host, grace string, hosts, caps, aliases string) string {
		return fmt.Sprintf(`
resource "docker_network" "o" {
  name   = "%[1]s-net"
  driver = "overlay"
}

resource "docker_service" "c" {
  name              = %[1]q
  image             = "busybox:1.36"
  command           = ["sleep", "3600"]
  hostname          = %[2]q
  user              = "nobody"
  dir               = "/tmp"
  hosts             = %[4]s
  cap_add           = %[5]s
  cap_drop          = ["MKNOD"]
  sysctls           = { "net.core.somaxconn" = "1024" }
  stop_grace_period = %[3]q

  dns_config {
    nameservers = ["10.0.0.53"]
    search      = ["example.internal"]
    options     = ["ndots:2"]
  }

  ulimits {
    name = "nofile"
    soft = 1024
    hard = 2048
  }

  networks = {
    (docker_network.o.id) = { aliases = %[6]s }
  }

  converge_config {
    timeout = "3m"
  }
}
`, name, host, grace, hosts, caps, aliases)
	}
	specCheck := func(hostname string, grace time.Duration, wantHosts []string, wantCaps []string, wantAliases []string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			svc, err := svcP3Inspect(t, s, "docker_service.c")
			if err != nil {
				return err
			}
			cs := svc.Spec.TaskTemplate.ContainerSpec
			var errs []string
			if cs.Hostname != hostname {
				errs = append(errs, fmt.Sprintf("Hostname %q", cs.Hostname))
			}
			if cs.User != "nobody" || cs.Dir != "/tmp" {
				errs = append(errs, fmt.Sprintf("User %q Dir %q", cs.User, cs.Dir))
			}
			if strings.Join(cs.Hosts, ",") != strings.Join(wantHosts, ",") {
				errs = append(errs, fmt.Sprintf("Hosts %q, want %q (swarmkit \"ip host\" form)", cs.Hosts, wantHosts))
			}
			if cs.DNSConfig == nil || strings.Join(addrStrings(cs.DNSConfig.Nameservers), ",") != "10.0.0.53" ||
				strings.Join(cs.DNSConfig.Search, ",") != "example.internal" || strings.Join(cs.DNSConfig.Options, ",") != "ndots:2" {
				errs = append(errs, fmt.Sprintf("DNSConfig %+v", cs.DNSConfig))
			}
			gotCaps := append([]string(nil), cs.CapabilityAdd...)
			for i := range gotCaps {
				gotCaps[i] = strings.TrimPrefix(strings.ToUpper(gotCaps[i]), "CAP_")
			}
			sort.Strings(gotCaps)
			if strings.Join(gotCaps, ",") != strings.Join(wantCaps, ",") || len(cs.CapabilityDrop) != 1 {
				errs = append(errs, fmt.Sprintf("CapabilityAdd %v (want %v) CapabilityDrop %v", cs.CapabilityAdd, wantCaps, cs.CapabilityDrop))
			}
			if cs.Sysctls["net.core.somaxconn"] != "1024" {
				errs = append(errs, fmt.Sprintf("Sysctls %v", cs.Sysctls))
			}
			if len(cs.Ulimits) != 1 || cs.Ulimits[0].Name != "nofile" || cs.Ulimits[0].Soft != 1024 || cs.Ulimits[0].Hard != 2048 {
				errs = append(errs, fmt.Sprintf("Ulimits %+v", cs.Ulimits))
			}
			if cs.StopGracePeriod == nil || *cs.StopGracePeriod != grace {
				errs = append(errs, fmt.Sprintf("StopGracePeriod %v, want %v", cs.StopGracePeriod, grace))
			}
			nets := svc.Spec.TaskTemplate.Networks
			if len(nets) != 1 {
				errs = append(errs, fmt.Sprintf("Networks %+v", nets))
			} else {
				a := append([]string(nil), nets[0].Aliases...)
				sort.Strings(a)
				if strings.Join(a, ",") != strings.Join(wantAliases, ",") {
					errs = append(errs, fmt.Sprintf("Aliases %v, want %v", a, wantAliases))
				}
			}
			if len(errs) > 0 {
				return fmt.Errorf("live spec mismatch: %s", strings.Join(errs, "; "))
			}
			return nil
		}
	}
	var firstID string
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             svcP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: cfg("tfacc-host", "20s", `["db.local:10.0.0.5", "cache:10.0.0.6"]`, `["NET_ADMIN"]`, `["svc-alias"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.c", "hosts.0", "db.local:10.0.0.5"),
					resource.TestCheckResourceAttr("docker_service.c", "stop_grace_period", "20s"),
					specCheck("tfacc-host", 20*time.Second, []string{"10.0.0.5 db.local", "10.0.0.6 cache"}, []string{"NET_ADMIN"}, []string{"svc-alias"}),
					func(s *terraform.State) error { firstID, _ = p3Attr(s, "docker_service.c", "id"); return nil },
				),
			},
			{
				// In place: hostname, hosts, caps (CAP_ prefix, lower case), grace, aliases.
				Config: cfg("tfacc-host2", "30s", `["db.local:10.0.0.5", "cache:10.0.0.6", "extra:10.0.0.7"]`, `["NET_ADMIN", "cap_sys_time"]`, `["svc-alias", "svc-alias2"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.c", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					specCheck("tfacc-host2", 30*time.Second, []string{"10.0.0.5 db.local", "10.0.0.6 cache", "10.0.0.7 extra"}, []string{"NET_ADMIN", "SYS_TIME"}, []string{"svc-alias", "svc-alias2"}),
					func(s *terraform.State) error {
						if id, _ := p3Attr(s, "docker_service.c", "id"); id != firstID {
							return fmt.Errorf("service replaced (%s -> %s), want in-place update", firstID, id)
						}
						return nil
					},
				),
			},
			{ResourceName: "docker_service.c", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"converge_config", "image"}},
		},
	})
}

// TestAccServiceP3_fileNameDefault: secrets/configs refs without file_name:
// a secret referenced by NAME gets file_name = the name as written; a config
// referenced by ID gets the config's name, on the daemon spec; state keeps
// file_name null and the plan stays empty (Pass 2 map: L4 file_name default).
func TestAccServiceP3_fileNameDefault(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	sfx := acctest.RandString(6)
	secName, cfgName := "tfacc-sec-"+sfx, "tfacc-cfg-"+sfx
	cfg := fmt.Sprintf(`
resource "docker_secret" "s" {
  name = %[1]q
  data = base64encode("s3cr3t")
}

resource "docker_config" "c" {
  name = %[2]q
  data = base64encode("k=v")
}

resource "docker_service" "fn" {
  name    = "tfacc-fn-%[3]s"
  image   = "busybox:1.36"
  command = ["sh", "-c", "test -f /run/secrets/%[1]s && test -f /%[2]s && sleep 3600"]

  secrets {
    secret_id = docker_secret.s.name
  }

  configs {
    config_id = docker_config.c.id
  }

  converge_config {
    timeout = "3m"
  }
}
`, secName, cfgName, sfx)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             svcP3CheckDestroy(t),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				// file_name is Optional, not Computed: the default is applied
				// to the daemon spec only and stays null in state (no drift).
				resource.TestCheckNoResourceAttr("docker_service.fn", "secrets.0.file_name"),
				resource.TestCheckNoResourceAttr("docker_service.fn", "configs.0.file_name"),
				resource.TestCheckResourceAttr("docker_service.fn", "secrets.0.mode", "292"),
				resource.TestCheckResourceAttr("docker_service.fn", "configs.0.uid", "0"),
				func(s *terraform.State) error {
					svc, err := svcP3Inspect(t, s, "docker_service.fn")
					if err != nil {
						return err
					}
					cs := svc.Spec.TaskTemplate.ContainerSpec
					if len(cs.Secrets) != 1 || cs.Secrets[0].File == nil || cs.Secrets[0].File.Name != secName {
						return fmt.Errorf("secret ref on daemon: %+v", cs.Secrets)
					}
					if len(cs.Configs) != 1 || cs.Configs[0].File == nil || cs.Configs[0].File.Name != cfgName {
						return fmt.Errorf("config ref on daemon: %+v", cs.Configs)
					}
					return nil
				},
			),
		}},
	})
}

// TestAccDockerConfigP3_importFillsData: configs are readable, so import
// fills data from the daemon and ImportStateVerify passes WITHOUT ignoring
// data (Pass 2 map: L4 docker_config import fills data).
func TestAccDockerConfigP3_importFillsData(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-cfgimp")
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             svcP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "docker_config" "i" {
  name   = %q
  data   = base64encode("line1\nline2=x\n")
  labels = { app = "p3" }
}
`, name),
			},
			{ResourceName: "docker_config.i", ImportState: true, ImportStateVerify: true},
			{ResourceName: "docker_config.i", ImportState: true, ImportStateId: name, ImportStateVerify: true},
		},
	})
}
