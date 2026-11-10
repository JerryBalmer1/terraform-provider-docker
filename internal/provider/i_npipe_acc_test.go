package provider

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/client"
)

// iNpipeHost is the Docker Desktop engine named pipe on Windows.
const iNpipeHost = "npipe:////./pipe/docker_engine"

// iNpipePrefix names every object TestAccNpipeSmoke creates. The test never
// touches, lists for removal or inspects anything outside this prefix: Docker
// Desktop on a developer machine runs unrelated containers.
const iNpipePrefix = "tfacc-npipe-"

// iNpipeImageEnv overrides the image of the smoke container. The image must
// already be present on Docker Desktop: the test skips instead of pulling, so
// it never leaves a pulled image behind.
const iNpipeImageEnv = "TF_ACC_NPIPE_IMAGE"

// TestAccNpipeSmoke is the one acceptance test that talks to Docker Desktop
// directly (CLAUDE.md: acceptance runs go through the dind harness; this test
// is the documented exception). It proves the npipe:// transport end to end:
// a network, a volume and a container on both, all named tfacc-npipe-*, then
// destroy, CheckDestroy, and a final API listing that no tfacc-npipe-* object
// remains.
//
// Gated by TF_ACC=1 and TF_ACC_NPIPE=1; skips on non-Windows hosts. Run:
//
//	TF_ACC=1 TF_ACC_NPIPE=1 go test -run TestAccNpipeSmoke ./internal/provider -timeout 15m -count=1 -v
func TestAccNpipeSmoke(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance test")
	}
	if os.Getenv("TF_ACC_NPIPE") != "1" {
		t.Skip("TF_ACC_NPIPE=1 not set; skipping the Docker Desktop npipe smoke test")
	}
	if runtime.GOOS != "windows" {
		t.Skip("npipe:// is a Windows transport; skipping on " + runtime.GOOS)
	}
	// The provider reads DOCKER_HOST and the TLS variables when host is unset;
	// pin all of them so a harness DOCKER_HOST in the shell cannot redirect the test.
	t.Setenv("DOCKER_HOST", iNpipeHost)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_CONTEXT", "")

	c, err := newDockerClient(endpointConfig{Host: iNpipeHost})
	if err != nil {
		t.Fatalf("npipe client: %v", err)
	}
	ctx := context.Background()
	if _, err := c.Ping(ctx, client.PingOptions{}); err != nil {
		t.Skipf("Docker Desktop is not reachable on %s: %v", iNpipeHost, err)
	}
	image := os.Getenv(iNpipeImageEnv)
	if image == "" {
		image = "busybox:1.36"
	}
	if _, err := c.ImageInspect(ctx, image); err != nil {
		t.Skipf("image %s is not present on Docker Desktop (%v); pull it once or set %s to a local image. The test does not pull.", image, err, iNpipeImageEnv)
	}

	name := acctest.RandomWithPrefix(iNpipePrefix + "smoke")
	if !strings.HasPrefix(name, iNpipePrefix) {
		t.Fatalf("generated name %q lacks the %s prefix", name, iNpipePrefix)
	}

	// Registered first so it runs last, after resource.Test's own destroy, even
	// when a step fails: remove only this run's objects (exact names), then
	// fail if any tfacc-npipe-* object is left on Docker Desktop.
	t.Cleanup(func() {
		iNpipeRemoveExact(t, c, name)
		if left := iNpipeLeftovers(t, c); len(left) > 0 {
			t.Errorf("tfacc-npipe-* objects left on Docker Desktop: %s", strings.Join(left, ", "))
		}
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			for _, rs := range s.RootModule().Resources {
				var err error
				switch rs.Type {
				case "docker_container":
					_, err = c.ContainerInspect(ctx, rs.Primary.ID, client.ContainerInspectOptions{})
				case "docker_network":
					_, err = c.NetworkInspect(ctx, rs.Primary.ID, client.NetworkInspectOptions{})
				case "docker_volume":
					_, err = c.VolumeInspect(ctx, rs.Primary.ID, client.VolumeInspectOptions{})
				default:
					continue
				}
				if !isNotFound(err) {
					return fmt.Errorf("%s %s still exists on Docker Desktop (%v)", rs.Type, rs.Primary.ID, err)
				}
			}
			if left := iNpipeLeftovers(t, c); len(left) > 0 {
				return fmt.Errorf("tfacc-npipe-* objects left after destroy: %s", strings.Join(left, ", "))
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "docker" {
  host = %[1]q
}

resource "docker_network" "n" {
  name = %[2]q
}

resource "docker_volume" "v" {
  name = %[2]q
}

resource "docker_container" "c" {
  name    = %[2]q
  image   = %[3]q
  command = ["sh", "-c", "sleep 600"]
  # stop_timeout deliberately unset: Docker Desktop 29.5 fills
  # Config.StopTimeout = 1 on create, and the provider must not report that
  # daemon default as drift (fixed in Pass 5.3 Lane A).

  mounts {
    type   = "volume"
    source = docker_volume.v.name
    target = "/data"
  }

  networks = {
    (docker_network.n.name) = {}
  }
}
`, iNpipeHost, name, image),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("docker_network.n", "id"),
				resource.TestCheckResourceAttr("docker_volume.v", "name", name),
				resource.TestCheckResourceAttr("docker_container.c", "name", name),
				func(*terraform.State) error {
					res, err := c.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
					if err != nil {
						return fmt.Errorf("inspect %s over npipe: %w", name, err)
					}
					ct := res.Container
					if ct.State == nil || !ct.State.Running {
						return fmt.Errorf("container %s is not running", name)
					}
					if ct.NetworkSettings == nil || ct.NetworkSettings.Networks[name] == nil {
						return fmt.Errorf("container %s is not attached to network %s", name, name)
					}
					for _, m := range ct.Mounts {
						if m.Name == name && m.Destination == "/data" {
							return nil
						}
					}
					return fmt.Errorf("container %s has no volume %s at /data", name, name)
				},
			),
		}},
	})
}

// iNpipeRemoveExact force-removes the container, network and volume named
// exactly name, if they still exist (a failed step can skip Terraform's
// destroy). It never removes anything else.
func iNpipeRemoveExact(t *testing.T, c *client.Client, name string) {
	t.Helper()
	ctx := context.Background()
	if !strings.HasPrefix(name, iNpipePrefix) {
		t.Errorf("refusing to remove %q: not a %s name", name, iNpipePrefix)
		return
	}
	if _, err := c.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNotFound(err) {
		t.Logf("cleanup container %s: %v", name, err)
	}
	if _, err := c.NetworkRemove(ctx, name, client.NetworkRemoveOptions{}); err != nil && !isNotFound(err) {
		t.Logf("cleanup network %s: %v", name, err)
	}
	if _, err := c.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
		t.Logf("cleanup volume %s: %v", name, err)
	}
}

// iNpipeLeftovers lists every container, network and volume on the daemon
// whose name starts with tfacc-npipe-. The daemon's name filter is a substring
// match, so the prefix is re-checked here.
func iNpipeLeftovers(t *testing.T, c *client.Client) []string {
	t.Helper()
	ctx := context.Background()
	var left []string
	f := client.Filters{}.Add("name", iNpipePrefix)
	if res, err := c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f}); err != nil {
		left = append(left, "container list failed: "+err.Error())
	} else {
		for _, ct := range res.Items {
			for _, n := range ct.Names {
				if strings.HasPrefix(strings.TrimPrefix(n, "/"), iNpipePrefix) {
					left = append(left, "container "+strings.TrimPrefix(n, "/"))
				}
			}
		}
	}
	if res, err := c.NetworkList(ctx, client.NetworkListOptions{Filters: f}); err != nil {
		left = append(left, "network list failed: "+err.Error())
	} else {
		for _, n := range res.Items {
			if strings.HasPrefix(n.Name, iNpipePrefix) {
				left = append(left, "network "+n.Name)
			}
		}
	}
	if res, err := c.VolumeList(ctx, client.VolumeListOptions{Filters: f}); err != nil {
		left = append(left, "volume list failed: "+err.Error())
	} else {
		for _, v := range res.Items {
			if strings.HasPrefix(v.Name, iNpipePrefix) {
				left = append(left, "volume "+v.Name)
			}
		}
	}
	return left
}

// TestINpipeSmokeSkips: without TF_ACC_NPIPE the smoke test must skip, never
// dial Docker Desktop. Runs in the unit suite.
func TestINpipeSmokeSkips(t *testing.T) {
	t.Setenv("TF_ACC_NPIPE", "")
	t.Setenv("TF_ACC", "1")
	skipped := false
	ran := t.Run("inner", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		TestAccNpipeSmoke(t)
	})
	if !ran || !skipped {
		t.Fatalf("smoke test without TF_ACC_NPIPE: passed=%v skipped=%v, want a skip", ran, skipped)
	}
}
