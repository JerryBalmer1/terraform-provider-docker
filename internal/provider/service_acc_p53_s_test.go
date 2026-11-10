package provider

// Pass 5.3 Lane S acceptance tests. The service, secret, config and node
// tests need the -SwarmInit harness pass (names match $SwarmAccRegex);
// TestAccSwarmInitP53_spec needs a daemon outside any swarm (plain pass).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// TestAccServiceP53_containerFields creates a service with every Pass 5.3
// container / task attribute the harness Engine supports (API 1.51 when written; 1.56 since Pass 6a) and
// checks the daemon kept them; the plan after apply must be empty.
func TestAccServiceP53_containerFields(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-svc53")
	cfg := fmt.Sprintf(`
resource "docker_service" "s" {
  name          = %[1]q
  image         = "busybox:1.36"
  command       = ["sleep", "3600"]
  tty           = true
  open_stdin    = true
  oom_score_adj = 200

  privileges {
    no_new_privileges = true
    seccomp {
      mode = "unconfined"
    }
    app_armor {
      mode = "disabled"
    }
  }

  mounts {
    type   = "tmpfs"
    target = "/scratch"
    tmpfs_options {
      size_bytes = 1048576
      options    = [["exec"], ["nr_inodes", "1000"]]
    }
  }

  mounts {
    type   = "bind"
    source = "/tmp"
    target = "/host-tmp"
    bind_options {
      non_recursive = true
    }
  }

  healthcheck {
    test           = ["CMD", "true"]
    interval       = "30s"
    start_period   = "10s"
    start_interval = "2s"
  }

  resources {
    limits {
      pids = 64
    }
  }
}
`, name)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_service.s", "tty", "true"),
				resource.TestCheckResourceAttr("docker_service.s", "mounts.0.tmpfs_options.options.1.1", "1000"),
				func(*terraform.State) error {
					res, err := testAccDockerClient(t).ServiceInspect(context.Background(), name, client.ServiceInspectOptions{})
					if err != nil {
						return err
					}
					cs := res.Service.Spec.TaskTemplate.ContainerSpec
					switch {
					case !cs.TTY || !cs.OpenStdin || cs.OomScoreAdj != 200:
						return fmt.Errorf("tty/open_stdin/oom_score_adj = %v/%v/%v", cs.TTY, cs.OpenStdin, cs.OomScoreAdj)
					case cs.Privileges == nil || !cs.Privileges.NoNewPrivileges || cs.Privileges.Seccomp == nil ||
						cs.Privileges.Seccomp.Mode != swarm.SeccompModeUnconfined || cs.Privileges.AppArmor == nil ||
						cs.Privileges.AppArmor.Mode != swarm.AppArmorModeDisabled:
						return fmt.Errorf("privileges = %+v", cs.Privileges)
					case len(cs.Mounts) != 2 || len(cs.Mounts[0].TmpfsOptions.Options) != 2 || !cs.Mounts[1].BindOptions.NonRecursive:
						return fmt.Errorf("mounts = %+v", cs.Mounts)
					case cs.Healthcheck == nil || cs.Healthcheck.StartInterval != 2*time.Second:
						return fmt.Errorf("healthcheck = %+v", cs.Healthcheck)
					case res.Service.Spec.TaskTemplate.Resources.Limits.Pids != 64:
						return fmt.Errorf("pids = %+v", res.Service.Spec.TaskTemplate.Resources.Limits)
					}
					return nil
				},
			),
		}, {
			ResourceName: "docker_service.s", ImportState: true, ImportStateVerify: true,
			ImportStateVerifyIgnore: []string{"converge_config", "image", "version_index", "privileges", "mounts", "healthcheck", "resources",
				"tty", "open_stdin", "oom_score_adj", "inspect", "deploy"},
		}},
	})
}

// TestAccServiceP53_daemonDropsMountFields pins the reasons of the field
// coverage skips in reference/_gen/fieldmap.d/S.json: the harness Engine
// drops BindOptions.CreateMountpoint / ReadOnlyNonRecursive /
// ReadOnlyForceRecursive and Mount.Consistency from a service spec, and
// rejects mounts of type image. When this test fails, the Engine started
// honoring them: implement the fields. Re-run in Pass 6a on Engine 29.9.3
// (API 1.56): still dropped / rejected.
func TestAccServiceP53_daemonDropsMountFields(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	ctx := context.Background()
	name := acctest.RandomWithPrefix("tfacc-svc53drop")
	spec := swarm.ServiceSpec{Annotations: swarm.Annotations{Name: name},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: hAccImage("busybox:1.36"), Command: []string{"sleep", "3600"},
			Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/tmp", Target: "/t", ReadOnly: true, Consistency: mount.ConsistencyCached,
				BindOptions: &mount.BindOptions{CreateMountpoint: true, ReadOnlyNonRecursive: true, ReadOnlyForceRecursive: true}}}}}}
	out, err := c.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: spec})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = c.ServiceRemove(context.Background(), out.ID, client.ServiceRemoveOptions{}) })
	res, err := c.ServiceInspect(ctx, out.ID, client.ServiceInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Service.Spec.TaskTemplate.ContainerSpec.Mounts[0]
	if b := m.BindOptions; b != nil && (b.CreateMountpoint || b.ReadOnlyNonRecursive || b.ReadOnlyForceRecursive) {
		t.Errorf("the Engine now keeps bind options %+v: implement them in docker_service", b)
	}
	if m.Consistency != "" && m.Consistency != mount.ConsistencyDefault {
		t.Errorf("the Engine now keeps Consistency %q: implement it in docker_service", m.Consistency)
	}
	img := spec
	img.Name = name + "-img"
	img.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{Image: hAccImage("busybox:1.36"),
		Mounts: []mount.Mount{{Type: mount.TypeImage, Source: hAccImage("busybox:1.36"), Target: "/img", ImageOptions: &mount.ImageOptions{Subpath: "bin"}}}}
	if o, err := c.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: img}); err == nil {
		_, _ = c.ServiceRemove(ctx, o.ID, client.ServiceRemoveOptions{})
		t.Errorf("the Engine now accepts image mounts in services: implement mounts.image_options in docker_service")
	} else if !strings.Contains(strings.ToLower(err.Error()), "mount") {
		t.Errorf("image mount rejected for another reason: %v", err)
	}
}

// TestAccSecretP53_templating: secret and config templating drivers reach
// the daemon and read back without drift.
func TestAccSecretP53_templating(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-tpl53")
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_secret" "s" {
  name       = "%[1]s-s"
  data       = base64encode("svc={{ .Service.Name }}")
  templating = { name = "golang" }
}

resource "docker_config" "c" {
  name       = "%[1]s-c"
  data       = base64encode("node={{ .Node.Hostname }}")
  templating = { name = "golang" }
}
`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_secret.s", "templating.name", "golang"),
				resource.TestCheckResourceAttr("docker_config.c", "templating.name", "golang"),
				func(*terraform.State) error {
					c := testAccDockerClient(t)
					s, err := c.SecretInspect(context.Background(), name+"-s", client.SecretInspectOptions{})
					if err != nil {
						return err
					}
					if s.Secret.Spec.Templating == nil || s.Secret.Spec.Templating.Name != "golang" {
						return fmt.Errorf("secret templating = %+v", s.Secret.Spec.Templating)
					}
					cf, err := c.ConfigInspect(context.Background(), name+"-c", client.ConfigInspectOptions{})
					if err != nil {
						return err
					}
					if cf.Config.Spec.Templating == nil || cf.Config.Spec.Templating.Name != "golang" {
						return fmt.Errorf("config templating = %+v", cf.Config.Spec.Templating)
					}
					return nil
				},
			),
		}},
	})
}

// TestAccNodeResourceP53_name sets NodeSpec.Name through docker_node.
func TestAccNodeResourceP53_name(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	c := testAccDockerClient(t)
	node := actAccPickNode(t, c)
	t.Cleanup(func() {
		res, err := c.NodeInspect(context.Background(), node.ID, client.NodeInspectOptions{})
		if err != nil {
			return
		}
		spec := res.Node.Spec
		spec.Name = node.Spec.Name
		_, _ = c.NodeUpdate(context.Background(), node.ID, client.NodeUpdateOptions{Version: res.Node.Version, Spec: spec})
	})
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_node" "n" {
  node_id = %q
  name    = "tfacc-p53-node"
}
`, node.ID),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_node.n", "name", "tfacc-p53-node"),
				func(*terraform.State) error {
					res, err := c.NodeInspect(context.Background(), node.ID, client.NodeInspectOptions{})
					if err != nil {
						return err
					}
					if res.Node.Spec.Name != "tfacc-p53-node" {
						return fmt.Errorf("NodeSpec.Name = %q", res.Node.Spec.Name)
					}
					return nil
				},
			),
		}},
	})
}

// TestAccSwarmInitP53_spec: data_path_addr and an external CA at init, then
// an in-place update of the external CA list. Needs a daemon outside any
// swarm (plain harness pass).
func TestAccSwarmInitP53_spec(t *testing.T) {
	testAccPreCheck(t)
	infoRes, err := testAccDockerClient(t).Info(context.Background(), client.InfoOptions{})
	if err != nil {
		t.Skipf("cannot reach DOCKER_HOST: %v", err)
	}
	if infoRes.Info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skip("DOCKER_HOST is already part of a swarm; docker_swarm_init needs a daemon outside any swarm")
	}
	cfg := func(path string) string {
		return fmt.Sprintf(`
resource "docker_swarm_init" "this" {
  advertise_addr = "127.0.0.1"
  data_path_addr = "127.0.0.1"
  labels         = { env = "tfacc" }
  ca_config = {
    external_cas = [{ url = "https://127.0.0.1:8888/api/v1/cfssl/%s" }]
  }
}
`, path)
	}
	check := func(path string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			res, err := testAccDockerClient(t).SwarmInspect(context.Background(), client.SwarmInspectOptions{})
			if err != nil {
				return err
			}
			sp := res.Swarm.Spec
			if len(sp.CAConfig.ExternalCAs) != 1 || !strings.HasSuffix(sp.CAConfig.ExternalCAs[0].URL, "/"+path) {
				return fmt.Errorf("external CAs = %+v", sp.CAConfig.ExternalCAs)
			}
			if sp.Labels["env"] != "tfacc" {
				return fmt.Errorf("labels = %v", sp.Labels)
			}
			return nil
		}
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             swarmP3CheckInactive(t, endpointConfig{Host: os.Getenv("DOCKER_HOST")}),
		Steps: []resource.TestStep{
			{Config: cfg("sign"), Check: check("sign")},
			{Config: cfg("sign2"), Check: check("sign2")},
		},
	})
}
