package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/client"
)

// rSeedTLSRegistry pushes a tiny single-platform OCI image (one config blob,
// one empty layer) to repo:tag on the harness's self-signed TLS registry
// through the Registry HTTP API v2, and returns the manifest digest. The
// test itself skips verification; the provider under test must not.
func rSeedTLSRegistry(t *testing.T, reg, repo, tag string) string {
	t.Helper()
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // test seeding of a throwaway registry
	base := "https://" + reg
	upload := func(b []byte) string {
		dg := fmt.Sprintf("sha256:%x", sha256.Sum256(b))
		resp, err := hc.Post(base+"/v2/"+repo+"/blobs/uploads/", "application/octet-stream", nil)
		if err != nil {
			t.Fatalf("start upload: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("start upload: %s", resp.Status)
		}
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(base)
		loc = u.ResolveReference(loc)
		q := loc.Query()
		q.Set("digest", dg)
		loc.RawQuery = q.Encode()
		req, _ := http.NewRequest(http.MethodPut, loc.String(), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err = hc.Do(req)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %s: %s", dg, resp.Status)
		}
		return dg
	}
	// An empty gzip-compressed tar (two zero blocks).
	var layer bytes.Buffer
	gzEmptyTar(t, &layer)
	cfg := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:` +
		fmt.Sprintf("%x", sha256.Sum256(make([]byte, 1024))) + `"]},"config":{}}`)
	cfgDg, layerDg := upload(cfg), upload(layer.Bytes())
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},`+
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":%d}]}`,
		cfgDg, len(cfg), layerDg, layer.Len()))
	req, _ := http.NewRequest(http.MethodPut, base+"/v2/"+repo+"/manifests/"+tag, bytes.NewReader(manifest))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put manifest: %s", resp.Status)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
}

// TestAccRegistryImageDataInsecureSkipVerify reads an image from the
// harness's self-signed TLS registry (TF_ACC_TLS_REGISTRY): with
// insecure_skip_verify = false the read fails with a certificate error, with
// true it succeeds and reports the seeded manifest's digest.
func TestAccRegistryImageDataInsecureSkipVerify(t *testing.T) {
	testAccPreCheck(t)
	reg := os.Getenv("TF_ACC_TLS_REGISTRY")
	if reg == "" {
		t.Skip("TF_ACC_TLS_REGISTRY not set (test/harness tlsregistry); skipping")
	}
	want := rSeedTLSRegistry(t, reg, "tfacc/tlsprobe", "v1")
	cfg := func(insecure bool) string {
		return fmt.Sprintf(`
data "docker_registry_image" "tls" {
  name                 = "%s/tfacc/tlsprobe:v1"
  insecure_skip_verify = %t
}
`, reg, insecure)
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg(false), ExpectError: regexp.MustCompile(`(?i)certificate`)},
			{
				Config: cfg(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.docker_registry_image.tls", "digest", want),
					resource.TestCheckResourceAttr("data.docker_registry_image.tls", "platforms.0", "linux/amd64"),
				),
			},
		},
	})
}

// TestAccImageCommitActionConfigR commits a container with a `config`
// body and checks the new image's configuration on the daemon.
func TestAccImageCommitActionConfigR(t *testing.T) {
	testAccPreCheck(t)
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name    = "tpd-acc-commit-cfg"
  image   = %[1]q
  command = ["sleep", "3600"]
}
action "docker_image_commit" "snap" {
  config {
    container_id = docker_container.c.id
    reference    = "tpd-acc/commit-cfg:1"
    config = {
      env           = ["TPD_ACC=1"]
      cmd           = ["echo", "committed"]
      labels        = { "tpd.acc" = "commit" }
      exposed_ports = ["8080/tcp"]
      working_dir   = "/srv"
      healthcheck   = { test = ["CMD", "true"], interval = "30s" }
    }
  }
}
resource "terraform_data" "snap" {
  input = docker_container.c.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_commit.snap]
    }
  }
}
`, actAccImage),
			Check: func(*terraform.State) error {
				insp, err := testAccDockerClient(t).ImageInspect(context.Background(), "tpd-acc/commit-cfg:1")
				if err != nil {
					return err
				}
				c := insp.Config
				if c == nil || !strings.Contains(strings.Join(c.Env, ","), "TPD_ACC=1") || strings.Join(c.Cmd, " ") != "echo committed" ||
					c.Labels["tpd.acc"] != "commit" || c.WorkingDir != "/srv" || c.Healthcheck == nil || c.Healthcheck.Interval != 30*time.Second {
					return fmt.Errorf("committed config %+v", c)
				}
				if _, ok := c.ExposedPorts["8080/tcp"]; !ok {
					return fmt.Errorf("exposed ports %v", c.ExposedPorts)
				}
				_, _ = testAccDockerClient(t).ImageRemove(context.Background(), "tpd-acc/commit-cfg:1", client.ImageRemoveOptions{Force: true})
				return nil
			},
		}},
	})
}

// TestAccImageLoadQuietR saves an image and loads it back with quiet = false.
func TestAccImageLoadQuietR(t *testing.T) {
	testAccPreCheck(t)
	out := filepath.ToSlash(filepath.Join(t.TempDir(), "images.tar"))
	res := filepath.ToSlash(filepath.Join(t.TempDir(), "load.json"))
	cfg := fmt.Sprintf(`
resource "docker_image" "b" {
  name         = %[1]q
  keep_locally = true
}
action "docker_image_save" "s" {
  config {
    images = [docker_image.b.name]
    output = %[2]q
  }
}
action "docker_image_load" "l" {
  config {
    input       = %[2]q
    quiet       = false
    result_path = %[3]q
  }
}
resource "terraform_data" "save" {
  input = docker_image.b.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_save.s]
    }
  }
}
`, actAccImage, out, res)
	load := `
resource "terraform_data" "load" {
  input      = docker_image.b.id
  depends_on = [terraform_data.save]
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_load.l]
    }
  }
}
`
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				Config: cfg + load,
				Check: func(*terraform.State) error {
					b, err := os.ReadFile(res)
					if err != nil {
						return err
					}
					if !strings.Contains(string(b), "loaded_images") {
						return fmt.Errorf("load result %s", b)
					}
					return nil
				},
			},
		},
	})
}

// TestAccRegistryImagePlatformR pushes one platform of an image (the
// daemon's own) with docker_registry_image.platform.
func TestAccRegistryImagePlatformR(t *testing.T) {
	testAccPreCheck(t)
	reg := os.Getenv("TF_ACC_REGISTRY")
	if reg == "" {
		t.Skip("TF_ACC_REGISTRY not set; skipping registry push test")
	}
	info, err := testAccDockerClient(t).Info(context.Background(), client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[info.Info.Architecture]
	if arch == "" {
		arch = info.Info.Architecture
	}
	platform := info.Info.OSType + "/" + arch
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_image" "src" {
  name     = %[1]q
  platform = %[3]q
}
resource "docker_tag" "t" {
  source_image = docker_image.src.name
  target_image = "%[2]s/tfacc/platform:1"
}
resource "docker_registry_image" "p" {
  name          = docker_tag.t.target_image
  platform      = %[3]q
  keep_remotely = false
  push_retries  = 1
}
`, actAccImage, reg, platform),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestMatchResourceAttr("docker_registry_image.p", "digest", imgTestSHA),
				resource.TestCheckResourceAttr("docker_registry_image.p", "platform", platform),
			),
		}},
	})
}

// gzEmptyTar writes a gzip-compressed empty tar archive (1024 zero bytes).
func gzEmptyTar(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	zw := gzip.NewWriter(w)
	if err := tar.NewWriter(zw).Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
