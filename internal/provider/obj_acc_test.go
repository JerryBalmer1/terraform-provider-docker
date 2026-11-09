package provider

// Acceptance tests for the WP obj types. They skip without TF_ACC and
// DOCKER_HOST (testAccPreCheck); actions also skip below Terraform 1.14.

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
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// objAccResultFile returns a check that result_path p holds key.
func objAccResultFile(p, key string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			return err
		}
		if _, ok := out[key]; !ok {
			return fmt.Errorf("%s has no %q: %s", p, key, b)
		}
		return nil
	}
}

func TestAccObjSystemDataSources(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "docker_info" "i" {}
data "docker_version" "v" {}
data "docker_system_df" "df" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.docker_info.i", "id"),
				resource.TestCheckResourceAttrSet("data.docker_info.i", "info_json"),
				resource.TestCheckResourceAttrSet("data.docker_info.i", "content_sha256"),
				resource.TestCheckResourceAttrPair("data.docker_version.v", "id", "data.docker_info.i", "id"),
				resource.TestCheckResourceAttrSet("data.docker_version.v", "version.api_version"),
				resource.TestCheckResourceAttrSet("data.docker_system_df.df", "df.image_usage.total_count"),
			),
		}},
	})
}

func TestAccObjNetworksVolumesDataSources(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-objlist")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_network" "n" {
  name        = %[1]q
  enable_ipv6 = false
  labels      = { tfacc = %[1]q }
}

resource "docker_volume" "v" {
  name   = %[1]q
  labels = { tfacc = %[1]q }
}

data "docker_networks" "n" {
  labels     = { tfacc = %[1]q }
  depends_on = [docker_network.n]
}

data "docker_volumes" "v" {
  labels     = { tfacc = %[1]q }
  depends_on = [docker_volume.v]
}

data "docker_network" "one" {
  name = docker_network.n.id
}
`, name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.docker_networks.n", "names.#", "1"),
				resource.TestCheckResourceAttr("data.docker_networks.n", "names.0", name),
				resource.TestCheckResourceAttrPair("data.docker_networks.n", "ids.0", "docker_network.n", "id"),
				resource.TestCheckResourceAttr("data.docker_volumes.v", "names.#", "1"),
				resource.TestCheckResourceAttr("data.docker_volumes.v", "volumes.volumes.0.name", name),
				resource.TestCheckResourceAttrSet("docker_network.n", "inspect_json"),
				resource.TestCheckResourceAttrSet("docker_volume.v", "content_sha256"),
				resource.TestCheckResourceAttr("data.docker_network.one", "enable_ipv6", "false"),
			),
		}},
	})
}

func TestAccObjPluginsDataSource(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `data "docker_plugins" "all" {}`,
			Check:  resource.TestCheckResourceAttrSet("data.docker_plugins.all", "plugins_json"),
		}},
	})
}

// TestAccObjPruneActions runs the container, network, volume and builder
// prune actions, each narrowed by a label nothing else on the daemon carries.
func TestAccObjPruneActions(t *testing.T) {
	testAccPreCheck(t)
	label := acctest.RandomWithPrefix("tfacc-objprune")
	c := testAccDockerClient(t)
	actAccPull(t, c, actAccImage)
	created, err := c.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Config: &container.Config{Image: actAccImage, Cmd: []string{"true"}, Labels: map[string]string{"tfacc-objprune": label}}, Name: label})
	if err != nil {
		t.Fatalf("seeding container: %v", err)
	}
	t.Cleanup(func() {
		_, _ = c.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := c.NetworkCreate(context.Background(), label, client.NetworkCreateOptions{Labels: map[string]string{"tfacc-objprune": label}}); err != nil {
		t.Fatalf("seeding network: %v", err)
	}
	t.Cleanup(func() { _, _ = c.NetworkRemove(context.Background(), label, client.NetworkRemoveOptions{}) })
	dir := t.TempDir()
	rp := func(n string) string { return filepath.ToSlash(filepath.Join(dir, n+".json")) }
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_container_prune" "c" {
  config {
    filters     = { label = "tfacc-objprune=%[1]s" }
    result_path = %[2]q
  }
}

action "docker_network_prune" "n" {
  config {
    filters     = { label = "tfacc-objprune=%[1]s" }
    result_path = %[3]q
  }
}

action "docker_volume_prune" "v" {
  config {
    filters     = { label = "tfacc-objprune=%[1]s" }
    result_path = %[4]q
  }
}

action "docker_builder_prune" "b" {
  config {
    filters     = { until = "87600h" }
    result_path = %[5]q
  }
}

resource "terraform_data" "p" {
  input = "prune"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_container_prune.c, action.docker_network_prune.n, action.docker_volume_prune.v, action.docker_builder_prune.b]
    }
  }
}
`, label, rp("c"), rp("n"), rp("v"), rp("b")),
			Check: resource.ComposeAggregateTestCheckFunc(
				func(*terraform.State) error {
					if _, err := c.ContainerInspect(context.Background(), created.ID, client.ContainerInspectOptions{}); !isNotFound(err) {
						return fmt.Errorf("container %s should be pruned, inspect: %v", label, err)
					}
					if _, err := c.NetworkInspect(context.Background(), label, client.NetworkInspectOptions{}); !isNotFound(err) {
						return fmt.Errorf("network %s should be pruned, inspect: %v", label, err)
					}
					return nil
				},
				objAccResultFile(rp("c"), "containers_deleted"),
				objAccResultFile(rp("n"), "networks_deleted"),
				objAccResultFile(rp("v"), "volumes_deleted"),
				objAccResultFile(rp("b"), "caches_deleted"),
			),
		}},
	})
}

// TestAccObjPluginCreatePush creates a minimal plugin from a directory
// (TF_ACC_PLUGIN_DIR: config.json + rootfs/) and pushes it to the harness
// registry (TF_ACC_REGISTRY). Skips unless both are set.
func TestAccObjPluginCreatePush(t *testing.T) {
	testAccPreCheck(t)
	env := p3Env(t, "TF_ACC_PLUGIN_DIR", "TF_ACC_REGISTRY")
	name := strings.TrimSuffix(env[1], "/") + "/" + acctest.RandomWithPrefix("tfacc-plugin") + ":1"
	c := testAccDockerClient(t)
	t.Cleanup(func() { _, _ = c.PluginRemove(context.Background(), name, client.PluginRemoveOptions{Force: true}) })
	out := filepath.ToSlash(filepath.Join(t.TempDir(), "push.json"))
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_plugin_create" "p" {
  config {
    name        = %[1]q
    context_dir = %[2]q
  }
}

action "docker_plugin_push" "p" {
  config {
    name        = %[1]q
    result_path = %[3]q
  }
}

resource "terraform_data" "p" {
  input = "plugin"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_plugin_create.p, action.docker_plugin_push.p]
    }
  }
}
`, name, filepath.ToSlash(env[0]), out),
			Check: objAccResultFile(out, "digest"),
		}},
	})
}
