package provider

// Acceptance tests for the single-host actions (Terraform >= 1.14). Each
// test triggers the action from a terraform_data resource's
// lifecycle.action_trigger: terraform-plugin-testing v1.16 has no direct
// "invoke action" step, so an after_create trigger is the way to run one.

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const actAccImage = "busybox:1.36"

// actAccVersionChecks skips action tests on Terraform < 1.14, which has no
// action blocks.
var actAccVersionChecks = []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)}

// actAccPull makes sure img is on the daemon (for containers created by
// the test itself rather than by Terraform).
func actAccPull(t *testing.T, c *client.Client, img string) {
	t.Helper()
	rc, err := c.ImagePull(context.Background(), img, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pulling %s: %v", img, err)
	}
	defer rc.Close()
	if _, err := imgDecodeStream(rc, nil); err != nil {
		t.Fatalf("pulling %s: %v", img, err)
	}
}

// actAccContainer is a running busybox container plus the given extra HCL.
func actAccContainer(name, extra string) string {
	return fmt.Sprintf(`
resource "docker_container" "c" {
  name    = %q
  image   = %q
  command = ["sh", "-c", "sleep 3600"]
}
%s`, name, actAccImage, extra)
}

func TestAccActionExec_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-exec")
	c := testAccDockerClient(t)
	ok := `
action "docker_exec" "marker" {
  config {
    container = docker_container.c.name
    command   = ["sh", "-c", "echo out; echo err >&2; echo $GREETING > /tmp/marker"]
    env       = { GREETING = "hello" }
    workdir   = "/tmp"
  }
}

resource "terraform_data" "marker" {
  input = docker_container.c.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_exec.marker]
    }
  }
}
`
	fail := ok + `
action "docker_exec" "fail" {
  config {
    container = docker_container.c.name
    command   = ["sh", "-c", "echo about to fail; exit 3"]
  }
}

resource "terraform_data" "fail" {
  input = docker_container.c.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_exec.fail]
    }
  }
}
`
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: actAccContainer(name, ok),
				Check: func(*terraform.State) error {
					res, err := c.ContainerStatPath(context.Background(), name, client.ContainerStatPathOptions{Path: "/tmp/marker"})
					if err != nil {
						return fmt.Errorf("docker_exec did not create /tmp/marker: %w", err)
					}
					if res.Stat.Size != int64(len("hello\n")) {
						return fmt.Errorf("/tmp/marker has %d bytes, want 6 (env not passed?)", res.Stat.Size)
					}
					return nil
				},
			},
			{
				Config:      actAccContainer(name, fail),
				ExpectError: regexp.MustCompile(`exited with code 3`),
			},
		},
	})
}

func TestAccActionContainerExport_basic(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-export")
	out := filepath.Join(t.TempDir(), "export", "c.tar")
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: actAccContainer(name, fmt.Sprintf(`
action "docker_container_export" "x" {
  config {
    container = docker_container.c.id
    path      = %q
  }
}

resource "terraform_data" "x" {
  input = docker_container.c.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_container_export.x]
    }
  }
}
`, filepath.ToSlash(out))),
			Check: func(*terraform.State) error { return actAccTarHas(out, "bin/busybox") },
		}},
	})
}

// actAccTarHas checks that the tar at p contains an entry named want.
func actAccTarHas(p, want string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s has no %s entry", p, want)
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}
		if strings.TrimPrefix(h.Name, "./") == want {
			return nil
		}
	}
}

func TestAccActionImageSaveLoad_basic(t *testing.T) {
	testAccPreCheck(t)
	out := filepath.ToSlash(filepath.Join(t.TempDir(), "images.tar"))
	cfg := fmt.Sprintf(`
resource "docker_image" "b" {
  name         = %[1]q
  keep_locally = true
}

action "docker_image_save" "s" {
  config {
    images = [docker_image.b.name]
    path   = %[2]q
  }
}

action "docker_image_load" "l" {
  config {
    path = %[2]q
  }
}

resource "terraform_data" "save" {
  input = docker_image.b.image_id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_save.s]
    }
  }
}
`, actAccImage, out)
	// Terraform 1.14 does not hold a depends_on dependent back until the
	// upstream resource's after_create actions finish, so the load runs in a
	// second step, once the save has written the archive.
	load := `
resource "terraform_data" "load" {
  input      = docker_image.b.image_id
  depends_on = [terraform_data.save]
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_load.l]
    }
  }
}
`
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: func(*terraform.State) error {
					if err := actAccTarHas(out, "manifest.json"); err != nil {
						return fmt.Errorf("docker_image_save: %w", err)
					}
					return nil
				},
			},
			{Config: cfg + load},
		},
	})
}

// actAccRootfsTar writes a one-file filesystem tarball for docker import.
func actAccRootfsTar(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rootfs.tar")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body := []byte("imported by terraform\n")
	if err := tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAccActionImageImport_basic(t *testing.T) {
	testAccPreCheck(t)
	repo := acctest.RandomWithPrefix("tfacc-import")
	src := filepath.ToSlash(actAccRootfsTar(t))
	c := testAccDockerClient(t)
	t.Cleanup(func() { _, _ = c.ImageRemove(context.Background(), repo+":v1", client.ImageRemoveOptions{Force: true}) })
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_image_import" "i" {
  config {
    source     = %q
    repository = %q
    tag        = "v1"
    changes    = ["LABEL source=tfacc", "CMD [\"/hello.txt\"]"]
  }
}

resource "terraform_data" "i" {
  input = "import"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_import.i]
    }
  }
}
`, src, repo),
			Check: func(*terraform.State) error {
				insp, err := c.ImageInspect(context.Background(), repo+":v1")
				if err != nil {
					return fmt.Errorf("imported image missing: %w", err)
				}
				if insp.Config == nil || insp.Config.Labels["source"] != "tfacc" {
					return fmt.Errorf("changes not applied: %+v", insp.Config)
				}
				return nil
			},
		}},
	})
}

func TestAccActionImagePrune_basic(t *testing.T) {
	testAccPreCheck(t)
	label := acctest.RandomWithPrefix("tfacc-prune")
	c := testAccDockerClient(t)
	// An unused image carrying the test's label: import one from a tarball.
	ref := label + ":v1"
	f, err := os.Open(actAccRootfsTar(t))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := c.ImageImport(context.Background(), client.ImageImportSource{Source: f, SourceName: "-"}, label, client.ImageImportOptions{Tag: "v1", Changes: []string{"LABEL tfacc-prune=" + label}})
	if err == nil {
		_, err = imgDecodeStream(rc, nil)
		rc.Close()
	}
	f.Close()
	if err != nil {
		t.Fatalf("seeding image: %v", err)
	}
	t.Cleanup(func() { _, _ = c.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{Force: true}) })
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_image_prune" "p" {
  config {
    dangling = false
    filters  = { label = "tfacc-prune=%s" }
  }
}

resource "terraform_data" "p" {
  input = "prune"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_prune.p]
    }
  }
}
`, label),
			Check: func(*terraform.State) error {
				if _, err := c.ImageInspect(context.Background(), ref); err == nil {
					return fmt.Errorf("image %s still present after docker_image_prune", ref)
				}
				return nil
			},
		}},
	})
}

func TestAccActionSystemPrune_basic(t *testing.T) {
	testAccPreCheck(t)
	label := acctest.RandomWithPrefix("tfacc-sysprune")
	c := testAccDockerClient(t)
	actAccPull(t, c, actAccImage)
	// A stopped container carrying the test's label. The filters keep the
	// prune away from everything else on the daemon.
	created, err := c.ContainerCreate(context.Background(), client.ContainerCreateOptions{Config: &container.Config{Image: actAccImage, Cmd: []string{"true"}, Labels: map[string]string{"tfacc-prune": label}}, HostConfig: nil, NetworkingConfig: nil, Platform: nil, Name: label})
	if err != nil {
		t.Fatalf("seeding container: %v", err)
	}
	t.Cleanup(func() {
		_, _ = c.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	resource.Test(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_system_prune" "p" {
  config {
    containers  = true
    images      = false
    networks    = false
    volumes     = false
    build_cache = false
    filters     = { label = "tfacc-prune=%s" }
  }
}

resource "terraform_data" "p" {
  input = "prune"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_system_prune.p]
    }
  }
}
`, label),
			Check: func(*terraform.State) error {
				if _, err := c.ContainerInspect(context.Background(), created.ID, client.ContainerInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("container %s should be pruned, inspect: %v", label, err)
				}
				return nil
			},
		}},
	})
}
