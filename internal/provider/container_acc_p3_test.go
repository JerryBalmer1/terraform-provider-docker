package provider

// Pass 3 acceptance tests for docker_container and docker_volume (single
// daemon; run in the plain harness pass, swarm state does not matter).

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func ctrP3CheckDestroy(t *testing.T) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c := testAccDockerClient(t)
		for _, rs := range s.RootModule().Resources {
			switch rs.Type {
			case "docker_container":
				if _, err := c.ContainerInspect(context.Background(), rs.Primary.ID, client.ContainerInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("container %s still exists (%v)", rs.Primary.ID, err)
				}
			case "docker_network":
				if _, err := c.NetworkInspect(context.Background(), rs.Primary.ID, client.NetworkInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("network %s still exists (%v)", rs.Primary.ID, err)
				}
			}
		}
		return nil
	}
}

func ctrP3Inspect(t *testing.T, s *terraform.State, addr string) (container.InspectResponse, error) {
	id, err := p3Attr(s, addr, "id")
	if err != nil {
		return container.InspectResponse{}, err
	}
	res, err := testAccDockerClient(t).ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
	return res.Container, err
}

var ctrP3Ignore = []string{"wait", "wait_timeout", "must_run"}

// TestAccContainerP3_capabilities: cap add/drop written in mixed spellings
// (lower case, CAP_ prefix) round-trip without drift and import verifies
// (Pass 2 map: L2 capability name case).
func TestAccContainerP3_capabilities(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-caps")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name    = %q
  image   = "busybox:1.36"
  command = ["sleep", "3600"]

  capabilities {
    add  = ["NET_ADMIN", "cap_sys_time"]
    drop = ["CAP_MKNOD"]
  }
}
`, name),
				Check: func(s *terraform.State) error {
					insp, err := ctrP3Inspect(t, s, "docker_container.c")
					if err != nil {
						return err
					}
					norm := func(in []string) string {
						out := make([]string, 0, len(in))
						for _, c := range in {
							out = append(out, strings.TrimPrefix(strings.ToUpper(c), "CAP_"))
						}
						sort.Strings(out)
						return strings.Join(out, ",")
					}
					if got := norm(insp.HostConfig.CapAdd); got != "NET_ADMIN,SYS_TIME" {
						return fmt.Errorf("CapAdd %v", insp.HostConfig.CapAdd)
					}
					if got := norm(insp.HostConfig.CapDrop); got != "MKNOD" {
						return fmt.Errorf("CapDrop %v", insp.HostConfig.CapDrop)
					}
					t.Logf("daemon stores CapAdd=%v CapDrop=%v", insp.HostConfig.CapAdd, insp.HostConfig.CapDrop)
					return nil
				},
			},
			{
				// Import has no prior spelling, so it reports the daemon canonical
				// CAP_* names; compare those normalised instead of verbatim.
				ResourceName:            "docker_container.c",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: append([]string{"capabilities.add"}, ctrP3Ignore...),
				ImportStateCheck: func(is []*terraform.InstanceState) error {
					if len(is) != 1 {
						return fmt.Errorf("%d instances imported", len(is))
					}
					var got []string
					for k, v := range is[0].Attributes {
						if strings.HasPrefix(k, "capabilities.add.") && k != "capabilities.add.#" {
							got = append(got, strings.TrimPrefix(strings.ToUpper(v), "CAP_"))
						}
					}
					sort.Strings(got)
					if strings.Join(got, ",") != "NET_ADMIN,SYS_TIME" {
						return fmt.Errorf("imported capabilities.add %v", got)
					}
					return nil
				},
			},
		},
	})
}

// TestAccContainerP3_removeLimitsReplaces: memory, cpu_shares and nano_cpus
// update in place, but removing any of them forces replacement (ContainerUpdate
// treats 0 as "unchanged") (Pass 2 map: L2 removing memory/cpu forces
// replacement).
func TestAccContainerP3_removeLimitsReplaces(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-lim")
	cfg := func(limits string) string {
		return fmt.Sprintf(`
resource "docker_container" "c" {
  name    = %q
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
%s
}
`, name, limits)
	}
	all := "  memory     = 67108864\n  cpu_shares = 512\n  nano_cpus  = 500000000\n"
	var lastID string
	captureID := func(changed bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			id, _ := p3Attr(s, "docker_container.c", "id")
			if lastID != "" && (id != lastID) != changed {
				return fmt.Errorf("container id %s -> %s, replacement expected=%v", lastID, id, changed)
			}
			lastID = id
			return nil
		}
	}
	replace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("docker_container.c", plancheck.ResourceActionReplace),
	}}
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("docker_container.c", plancheck.ResourceActionUpdate),
	}}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{Config: cfg(all), Check: captureID(false)},
			{Config: cfg("  memory     = 134217728\n  cpu_shares = 256\n  nano_cpus  = 250000000\n"), ConfigPlanChecks: update, Check: captureID(false)},
			{Config: cfg("  cpu_shares = 256\n  nano_cpus  = 250000000\n"), ConfigPlanChecks: replace, Check: captureID(true)},
			{Config: cfg("  nano_cpus  = 250000000\n"), ConfigPlanChecks: replace, Check: captureID(true)},
			{
				Config: cfg(""), ConfigPlanChecks: replace,
				Check: resource.ComposeAggregateTestCheckFunc(captureID(true), func(s *terraform.State) error {
					insp, err := ctrP3Inspect(t, s, "docker_container.c")
					if err != nil {
						return err
					}
					if r := insp.HostConfig.Resources; r.Memory != 0 || r.NanoCPUs != 0 || r.CPUShares != 0 {
						return fmt.Errorf("limits still set after removal: memory=%d nano_cpus=%d cpu_shares=%d", r.Memory, r.NanoCPUs, r.CPUShares)
					}
					return nil
				}),
			},
		},
	})
}

// TestAccContainerP3_mustRunRestart: a container stopped out of band is
// planned for start (running -> true) and started in place by the next
// apply (Pass 2 map: L2 must_run restarts a stopped container).
func TestAccContainerP3_mustRunRestart(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-mustrun")
	cfg := fmt.Sprintf(`
resource "docker_container" "c" {
  name     = %q
  image    = "busybox:1.36"
  command  = ["sleep", "3600"]
  must_run = true
}
`, name)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_container.c", "running", "true"),
					func(s *terraform.State) error { id, _ = p3Attr(s, "docker_container.c", "id"); return nil },
				),
			},
			{
				PreConfig: func() {
					timeout := 1
					if _, err := testAccDockerClient(t).ContainerStop(context.Background(), id, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
						t.Fatalf("stopping container out of band: %v", err)
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_container.c", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_container.c", "running", "true"),
					resource.TestCheckResourceAttrPtr("docker_container.c", "id", &id),
					func(s *terraform.State) error {
						insp, err := ctrP3Inspect(t, s, "docker_container.c")
						if err != nil {
							return err
						}
						if !insp.State.Running {
							return fmt.Errorf("container not running after apply (status %s)", insp.State.Status)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccContainerP3_laneCAttributes: the Pass 2 Lane C container attributes
// on a live daemon: hostname, dns, dns_search, extra_hosts, devices, tty,
// stdin_open, stop_signal, stop_timeout, sysctls, security_opt, read_only;
// checked on ContainerInspect, empty plan, import verify (sysctls not
// ignored: daemon-added netns sysctls would show up here).
func TestAccContainerP3_laneCAttributes(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-ctrc")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name         = %q
  image        = "busybox:1.36"
  command      = ["sh", "-i"]
  hostname     = "tfacc-ctr"
  dns          = ["10.0.0.53"]
  dns_search   = ["example.internal"]
  extra_hosts  = ["db.local:10.0.0.5", "host.docker.internal:host-gateway"]
  tty          = true
  stdin_open   = true
  stop_signal  = "SIGUSR1"
  stop_timeout = 5
  sysctls      = { "net.core.somaxconn" = "1024" }
  security_opt = ["no-new-privileges:true"]
  read_only    = true

  devices {
    host_path      = "/dev/null"
    container_path = "/dev/tfacc-null"
    permissions    = "rw"
  }

  devices {
    host_path = "/dev/zero"
  }

  mounts {
    type   = "tmpfs"
    target = "/tmp"
  }
}
`, name),
				Check: func(s *terraform.State) error {
					insp, err := ctrP3Inspect(t, s, "docker_container.c")
					if err != nil {
						return err
					}
					cfg, hc := insp.Config, insp.HostConfig
					var errs []string
					if cfg.Hostname != "tfacc-ctr" || !cfg.Tty || !cfg.OpenStdin || cfg.StopSignal != "SIGUSR1" || cfg.StopTimeout == nil || *cfg.StopTimeout != 5 {
						errs = append(errs, fmt.Sprintf("Config hostname=%q tty=%v stdin=%v stopsignal=%q stoptimeout=%v", cfg.Hostname, cfg.Tty, cfg.OpenStdin, cfg.StopSignal, cfg.StopTimeout))
					}
					if strings.Join(addrStrings(hc.DNS), ",") != "10.0.0.53" || strings.Join(hc.DNSSearch, ",") != "example.internal" {
						errs = append(errs, fmt.Sprintf("DNS %v DNSSearch %v", hc.DNS, hc.DNSSearch))
					}
					if strings.Join(hc.ExtraHosts, ",") != "db.local:10.0.0.5,host.docker.internal:host-gateway" {
						errs = append(errs, fmt.Sprintf("ExtraHosts %v", hc.ExtraHosts))
					}
					if hc.Sysctls["net.core.somaxconn"] != "1024" || !hc.ReadonlyRootfs || strings.Join(hc.SecurityOpt, ",") != "no-new-privileges:true" {
						errs = append(errs, fmt.Sprintf("Sysctls %v ReadonlyRootfs %v SecurityOpt %v", hc.Sysctls, hc.ReadonlyRootfs, hc.SecurityOpt))
					}
					devs := map[string]string{}
					for _, d := range hc.Devices {
						devs[d.PathOnHost] = d.PathInContainer + ":" + d.CgroupPermissions
					}
					if devs["/dev/null"] != "/dev/tfacc-null:rw" || devs["/dev/zero"] != "/dev/zero:rwm" {
						errs = append(errs, fmt.Sprintf("Devices %+v", hc.Devices))
					}
					if !insp.State.Running {
						errs = append(errs, "not running: "+string(insp.State.Status))
					}
					if len(errs) > 0 {
						return fmt.Errorf("live container mismatch: %s", strings.Join(errs, "; "))
					}
					return nil
				},
			},
			{ResourceName: "docker_container.c", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: ctrP3Ignore},
		},
	})
}

// TestAccContainerP3_imageDefaults: with hostname and stop_signal unset, the
// daemon's 12-char ID hostname and the image STOPSIGNAL (nginx: SIGQUIT) are
// not reported, so plan and import stay clean (Pass 2 P2-C hostname default,
// STOPSIGNAL merge).
func TestAccContainerP3_imageDefaults(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-imgdef")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name  = %q
  image = "nginx:1.27-alpine"
}
`, name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("docker_container.c", "hostname"),
					resource.TestCheckNoResourceAttr("docker_container.c", "stop_signal"),
					resource.TestCheckNoResourceAttr("docker_container.c", "stop_timeout"),
					func(s *terraform.State) error {
						insp, err := ctrP3Inspect(t, s, "docker_container.c")
						if err != nil {
							return err
						}
						if insp.Config.StopSignal != "SIGQUIT" {
							return fmt.Errorf("image STOPSIGNAL not applied by the daemon: %q", insp.Config.StopSignal)
						}
						if insp.Config.Hostname != insp.ID[:12] {
							return fmt.Errorf("hostname %q, want ID prefix %q", insp.Config.Hostname, insp.ID[:12])
						}
						return nil
					},
				),
			},
			{ResourceName: "docker_container.c", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: ctrP3Ignore},
		},
	})
}

// TestAccContainerP3_gpusNoDriver: gpus = "all" reaches the daemon as an
// nvidia DeviceRequest; the harness has no NVIDIA runtime, so the daemon
// must refuse it (proves the request is sent; a real GPU host is out of
// scope).
func TestAccContainerP3_gpusNoDriver(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-gpu")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name    = %q
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
  gpus    = "all"
}
`, name),
			ExpectError: regexp.MustCompile(`(?i)(nvidia|device driver|capabilities: \[\[gpu\]\])`),
		}},
	})
}

// TestAccContainerP3_ipv6Address: networks_advanced.ipv6_address on an
// IPv6-enabled bridge network (Pass 2 P2-C IPv6 static address). Skips when
// the daemon cannot create an IPv6 network.
func TestAccContainerP3_ipv6Address(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	c := testAccDockerClient(t)
	probe := acctest.RandomWithPrefix("tfacc-v6probe")
	enable := true
	resp, err := c.NetworkCreate(ctx, probe, client.NetworkCreateOptions{EnableIPv6: &enable, IPAM: &network.IPAM{Config: []network.IPAMConfig{{Subnet: mustPrefix("fd00:7f::/64")}}}})
	if err != nil {
		t.Skipf("daemon cannot create an IPv6 network: %v", err)
	}
	_, _ = c.NetworkRemove(ctx, resp.ID, client.NetworkRemoveOptions{})
	name := acctest.RandomWithPrefix("tfacc-v6")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "docker_network" "v6" {
  name = "%[1]s-net"
  ipv6 = true

  ipam_config {
    subnet = "10.77.0.0/24"
  }

  ipam_config {
    subnet = "fd00:77::/64"
  }
}

resource "docker_container" "c" {
  name    = %[1]q
  image   = "busybox:1.36"
  command = ["sleep", "3600"]

  networks_advanced {
    name         = docker_network.v6.name
    ipv4_address = "10.77.0.10"
    ipv6_address = "fd00:77::10"
  }
}
`, name),
				Check: func(s *terraform.State) error {
					insp, err := ctrP3Inspect(t, s, "docker_container.c")
					if err != nil {
						return err
					}
					for _, ep := range insp.NetworkSettings.Networks {
						if addrString(ep.GlobalIPv6Address) == "fd00:77::10" && addrString(ep.IPAddress) == "10.77.0.10" {
							return nil
						}
					}
					return fmt.Errorf("static addresses not applied: %+v", insp.NetworkSettings.Networks)
				},
			},
			{ResourceName: "docker_container.c", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: ctrP3Ignore},
		},
	})
}

// TestAccVolumeP3_refusesExistingName: docker_volume Create refuses a name
// that already exists (VolumeCreate would silently adopt it) and points at
// import; the pre-existing volume is left alone (Pass 2 map: L2).
func TestAccVolumeP3_refusesExistingName(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-volex")
	c := testAccDockerClient(t)
	if _, err := c.VolumeCreate(context.Background(), client.VolumeCreateOptions{Name: name, Labels: map[string]string{"owner": "out-of-band"}}); err != nil {
		t.Fatalf("pre-creating volume: %v", err)
	}
	t.Cleanup(func() { _, _ = c.VolumeRemove(context.Background(), name, client.VolumeRemoveOptions{Force: true}) })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			vRes, err := c.VolumeInspect(context.Background(), name, client.VolumeInspectOptions{})
			if err != nil {
				return fmt.Errorf("pre-existing volume was removed: %v", err)
			}
			v := vRes.Volume
			if v.Labels["owner"] != "out-of-band" {
				return fmt.Errorf("pre-existing volume changed: %v", v.Labels)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_volume" "v" {
  name = %q
}
`, name),
			ExpectError: regexp.MustCompile(`(?i)already exists`),
		}},
	})
}
