package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestContainerBuildCreate(t *testing.T) {
	ctx := context.Background()
	m := containerResourceModel{
		Name:       types.StringValue("web"),
		Image:      types.StringValue("nginx:1.27"),
		Command:    stringListValue([]string{"nginx", "-g", "daemon off;"}, false),
		Entrypoint: types.ListNull(types.StringType),
		Env:        stringMapValue(map[string]string{"A": "1"}, false),
		Labels:     stringMapValue(map[string]string{"team": "web"}, false),
		Ports:      []containerPortModel{{Internal: types.Int64Value(80), External: types.Int64Value(8080), Protocol: types.StringValue("tcp")}},
		NetworksAdvanced: []containerNetworkModel{
			{Name: types.StringValue("front"), Aliases: stringSetValue([]string{"www"}, false), IPv4Address: types.StringValue("10.0.0.9")},
			{Name: types.StringValue("back"), Aliases: types.SetNull(types.StringType), IPv4Address: types.StringNull()},
		},
		Restart:       types.StringValue("on-failure"),
		MaxRetryCount: types.Int64Value(4),
		Healthcheck: &containerHealthcheckModel{
			Test: stringListValue([]string{"CMD", "true"}, false), Interval: types.StringValue("10s"),
			Timeout: types.StringNull(), StartPeriod: types.StringNull(), Retries: types.Int64Null(),
		},
		Capabilities: &containerCapabilitiesModel{Add: stringSetValue([]string{"NET_ADMIN"}, false), Drop: types.SetNull(types.StringType)},
		Privileged:   types.BoolValue(false),
		User:         types.StringNull(),
		WorkingDir:   types.StringValue("/app"),
		LogDriver:    types.StringUnknown(),
		LogOpts:      types.MapUnknown(types.StringType),
		Memory:       types.Int64Value(64 << 20),
		MemorySwap:   types.Int64Unknown(),
		CPUShares:    types.Int64Null(),
		NanoCPUs:     types.Int64Value(500000000),
	}
	var diags diag.Diagnostics
	cfg, hc, netCfg, extra := containerBuildCreate(ctx, m, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if cfg.Image != "nginx:1.27" || len(cfg.Cmd) != 3 || cfg.Entrypoint != nil || cfg.Env[0] != "A=1" || cfg.WorkingDir != "/app" {
		t.Errorf("config: %+v", cfg)
	}
	if cfg.Labels[labelManagedBy] != labelManagedByValue || cfg.Labels["team"] != "web" {
		t.Errorf("labels: %v", cfg.Labels)
	}
	if cfg.Healthcheck == nil || cfg.Healthcheck.Interval.String() != "10s" {
		t.Errorf("healthcheck: %+v", cfg.Healthcheck)
	}
	if hc.RestartPolicy.Name != container.RestartPolicyOnFailure || hc.RestartPolicy.MaximumRetryCount != 4 {
		t.Errorf("restart: %+v", hc.RestartPolicy)
	}
	if hc.Memory != 64<<20 || hc.MemorySwap != 0 || hc.NanoCPUs != 500000000 || hc.LogConfig.Type != "" {
		t.Errorf("resources/log: mem=%d swap=%d nano=%d log=%q", hc.Memory, hc.MemorySwap, hc.NanoCPUs, hc.LogConfig.Type)
	}
	if string(hc.NetworkMode) != "front" || netCfg.EndpointsConfig["front"].IPAMConfig.IPv4Address != "10.0.0.9" ||
		netCfg.EndpointsConfig["front"].Aliases[0] != "www" || len(extra) != 1 || extra[0].Name.ValueString() != "back" {
		t.Errorf("networks: mode=%s cfg=%+v extra=%+v", hc.NetworkMode, netCfg, extra)
	}
	if len(hc.CapAdd) != 1 || len(hc.PortBindings) != 1 || len(cfg.ExposedPorts) != 1 {
		t.Errorf("caps/ports: %v %v %v", hc.CapAdd, hc.PortBindings, cfg.ExposedPorts)
	}

	m.Healthcheck.Test = types.ListNull(types.StringType)
	diags = nil
	containerBuildCreate(ctx, m, &diags)
	if !diags.HasError() {
		t.Error("healthcheck without test should fail")
	}
}

func TestAccContainer_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-ctr")
	c := testAccDockerClient(t)
	cfg := func(restart string, mem int) string {
		return fmt.Sprintf(`
resource "docker_network" "n" {
  name = %[1]q
}

resource "docker_volume" "v" {
  name = %[1]q
}

resource "docker_container" "test" {
  name    = %[1]q
  image   = "busybox:1.36"
  command = ["sh", "-c", "sleep 3600"]
  env     = { APP = "1" }
  labels  = { team = "acc" }
  restart = %[2]q
  memory  = %[3]d

  ports {
    internal = 8080
  }

  mounts {
    type   = "volume"
    source = docker_volume.v.name
    target = "/data"
  }

  mounts {
    type       = "tmpfs"
    target     = "/scratch"
    tmpfs_size = 1048576
  }

  networks_advanced {
    name    = docker_network.n.name
    aliases = ["app"]
  }

  healthcheck {
    test     = ["CMD", "true"]
    interval = "2s"
    retries  = 2
  }

  ulimits {
    name = "nofile"
    soft = 1024
    hard = 2048
  }

  wait         = true
  wait_timeout = "60s"
}
`, name, restart, mem)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "docker_container" {
					continue
				}
				if _, err := c.ContainerInspect(context.Background(), rs.Primary.ID); !isNotFound(err) {
					return fmt.Errorf("container %s still exists (%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg("no", 67108864),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_container.test", "id"),
					resource.TestCheckResourceAttr("docker_container.test", "running", "true"),
					resource.TestCheckResourceAttr("docker_container.test", "env.%", "1"),
					resource.TestCheckResourceAttr("docker_container.test", "labels.%", "1"),
					resource.TestCheckNoResourceAttr("docker_container.test", "entrypoint"),
				),
			},
			{
				// In-place update: restart policy and memory.
				Config: cfg("unless-stopped", 134217728),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_container.test", "restart", "unless-stopped"),
					resource.TestCheckResourceAttr("docker_container.test", "memory", "134217728"),
				),
			},
			{
				ResourceName:            "docker_container.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"wait", "wait_timeout", "healthcheck", "ulimits"},
			},
		},
	})
}

// TestContainerPlanOnly plans a full docker_container/network/volume config
// with the real terraform binary but no daemon (nothing is applied), which
// exercises schema defaults inside blocks and plan modifiers.
func TestContainerPlanOnly(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
			Config: `
resource "docker_network" "n" {
  name   = "plan-net"
  driver = "overlay"
  ipam_config {
    subnet = "10.5.0.0/24"
  }
}

resource "docker_volume" "v" {}

resource "docker_container" "c" {
  name  = "plan"
  image = "nginx:1.27"
  ports {
    internal = 80
  }
  mounts {
    type   = "volume"
    source = docker_volume.v.name
    target = "/data"
  }
  networks_advanced {
    name = docker_network.n.name
  }
  healthcheck {
    test = ["CMD", "true"]
  }
  capabilities {
    add = ["NET_ADMIN"]
  }
  ulimits {
    name = "nofile"
    soft = 1
    hard = 2
  }
  devices {
    host_path = "/dev/fuse"
  }
  hostname     = "plan"
  dns          = ["1.1.1.1"]
  extra_hosts  = ["db:10.0.0.5"]
  tty          = true
  stdin_open   = true
  stop_signal  = "SIGQUIT"
  stop_timeout = 20
  sysctls      = { "net.core.somaxconn" = "1024" }
  security_opt = ["no-new-privileges:true"]
  read_only    = true
  gpus         = "all"
}
`,
		}},
	})
}
