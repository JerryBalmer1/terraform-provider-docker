package provider

// Pass 3 acceptance tests for docker_image builds (relative context,
// Dockerfile outside the context, unreadable context) and
// docker_registry_image against the harness registry (TF_ACC_REGISTRY).

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// imgP3RelDir creates a directory under the provider process's working
// directory (the package dir during go test; Terraform's working directory
// for a real provider binary) and returns its RELATIVE path.
func imgP3RelDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.ToSlash(filepath.Clean(dir))
}

// TestAccImageP3_relativeContext: build.context relative to the working
// directory, a Dockerfile OUTSIDE the context given relative to the context,
// and a second build with an absolute out-of-context Dockerfile; labels from
// those Dockerfiles prove they were used; plans stay empty (Pass 2 map: L1
// relative build.context / dockerfile outside context).
func TestAccImageP3_relativeContext(t *testing.T) {
	testAccPreCheck(t)
	ctxRel := imgP3RelDir(t, ".tfacc-ctx-")
	dfRel := imgP3RelDir(t, ".tfacc-df-")
	imgTestWrite(t, ctxRel, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nLABEL from=incontext\n",
		"hello.txt":  "hi",
	})
	imgTestWrite(t, dfRel, map[string]string{
		"Dockerfile.out": "FROM alpine:3.20\nCOPY hello.txt /hello.txt\nLABEL from=outside\n",
	})
	absDF, err := filepath.Abs(filepath.Join(dfRel, "Dockerfile.out"))
	if err != nil {
		t.Fatal(err)
	}
	sfx := strings.ToLower(acctest.RandString(6))
	cfg := fmt.Sprintf(`
resource "docker_image" "rel" {
  name = "tfacc-p3-rel-%[1]s:latest"
  build {
    context = %[2]q
  }
}

resource "docker_image" "outrel" {
  name = "tfacc-p3-outrel-%[1]s:latest"
  build {
    context    = %[2]q
    dockerfile = %[3]q
  }
}

resource "docker_image" "outabs" {
  name = "tfacc-p3-outabs-%[1]s:latest"
  build {
    context    = %[2]q
    dockerfile = %[4]q
  }
}

data "docker_image" "rel" {
  name = docker_image.rel.id
}

data "docker_image" "outrel" {
  name = docker_image.outrel.id
}

data "docker_image" "outabs" {
  name = docker_image.outabs.id
}
`, sfx, ctxRel, "../"+filepath.Base(dfRel)+"/Dockerfile.out", p3HCLPath(absDF))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccImageCheckDestroy(t),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("docker_image.rel", "context_digest", imgTestSHA),
					resource.TestCheckResourceAttr("data.docker_image.rel", "labels.from", "incontext"),
					resource.TestCheckResourceAttr("data.docker_image.outrel", "labels.from", "outside"),
					resource.TestCheckResourceAttr("data.docker_image.outabs", "labels.from", "outside"),
					resource.TestCheckResourceAttr("docker_image.rel", "build.context", ctxRel),
				),
			},
			{
				// Editing the out-of-context Dockerfile changes context_digest -> rebuild.
				PreConfig: func() {
					imgTestWrite(t, dfRel, map[string]string{"Dockerfile.out": "FROM alpine:3.20\nCOPY hello.txt /hello.txt\nLABEL from=outside2\n"})
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_image.outrel", plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction("docker_image.outabs", plancheck.ResourceActionReplace),
					plancheck.ExpectResourceAction("docker_image.rel", plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr("data.docker_image.outabs", "labels.from", "outside2"),
			},
		},
	})
}

// TestAccImageP3_unreadableContext: when the context cannot be read at plan
// time, context_digest plans unknown and an existing image is planned for
// replacement; once it is back the plan is empty; a NEW image whose context
// does not exist fails at apply with "Cannot read build context" (Pass 2 map:
// L1 context unreadable at plan time).
func TestAccImageP3_unreadableContext(t *testing.T) {
	testAccPreCheck(t)
	dir := t.TempDir()
	ctxDir := filepath.Join(dir, "ctx")
	if err := os.Mkdir(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgTestWrite(t, ctxDir, map[string]string{"Dockerfile": "FROM alpine:3.20\nLABEL p3=unreadable\n"})
	sfx := strings.ToLower(acctest.RandString(6))
	base := fmt.Sprintf(`
resource "docker_image" "u" {
  name = "tfacc-p3-unread-%s:latest"
  build {
    context = %q
  }
}
`, sfx, p3HCLPath(ctxDir))
	missing := base + fmt.Sprintf(`
resource "docker_image" "missing" {
  name = "tfacc-p3-missing-%s:latest"
  build {
    context = %q
  }
}
`, sfx, p3HCLPath(filepath.Join(dir, "does-not-exist")))
	moved := filepath.Join(dir, "ctx-moved")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccImageCheckDestroy(t),
		Steps: []resource.TestStep{
			{Config: base},
			{
				PreConfig: func() {
					if err := os.Rename(ctxDir, moved); err != nil {
						t.Fatal(err)
					}
				},
				Config:             base,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPreRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_image.u", plancheck.ResourceActionReplace),
					plancheck.ExpectUnknownValue("docker_image.u", tfjsonpath.New("context_digest")),
				}},
			},
			{
				PreConfig: func() {
					if err := os.Rename(moved, ctxDir); err != nil {
						t.Fatal(err)
					}
				},
				Config:   base,
				PlanOnly: true,
			},
			{
				Config:      missing,
				ExpectError: regexp.MustCompile(`Cannot read build context`),
			},
		},
	})
}

// regP3Manifest asks the registry for repo:ref (tag or digest) and returns
// the HTTP status and Docker-Content-Digest header.
func regP3Manifest(reg, repo, ref string) (int, string, error) {
	req, err := http.NewRequest(http.MethodHead, "http://"+reg+"/v2/"+repo+"/manifests/"+ref, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ", "))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, "", err
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Docker-Content-Digest"), nil
}

// TestAccRegistryImageP3_pushDeleteKeep: push to the harness registry with
// push_retries; digest equals what the registry serves for the tag;
// on destroy keep_remotely=false DELETEs the manifest (404 afterwards) and
// keep_remotely=true leaves it (Pass 2 map: L1 docker_registry_image push
// digest, retries, keep_remotely DELETE).
func TestAccRegistryImageP3_pushDeleteKeep(t *testing.T) {
	testAccPreCheck(t)
	reg := p3Env(t, "TF_ACC_REGISTRY")[0]
	if code, _, err := regP3Manifest(reg, "probe", "none"); err != nil || code == 0 {
		t.Skipf("registry %s not reachable from the test process: %v", reg, err)
	}
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": "FROM alpine:3.20\nRUN echo p3 > /p3\n"})
	sfx := strings.ToLower(acctest.RandString(6))
	delRepo, keepRepo := "tfacc/p3del-"+sfx, "tfacc/p3keep-"+sfx
	cfg := fmt.Sprintf(`
resource "docker_image" "del" {
  name = "%[1]s/%[2]s:1"
  build {
    context = %[4]q
  }
}

resource "docker_image" "keep" {
  name = "%[1]s/%[3]s:1"
  build {
    context = %[4]q
  }
}

resource "docker_registry_image" "del" {
  name          = docker_image.del.name
  keep_remotely = false
  push_retries  = 2
  triggers      = { image = docker_image.del.id }
}

resource "docker_registry_image" "keep" {
  name          = docker_image.keep.name
  keep_remotely = true
  push_retries  = 0
}
`, reg, delRepo, keepRepo, p3HCLPath(dir))
	digests := map[string]string{}
	t.Cleanup(func() {
		if d := digests[keepRepo]; d != "" {
			req, _ := http.NewRequest(http.MethodDelete, "http://"+reg+"/v2/"+keepRepo+"/manifests/"+d, nil)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	})
	checkDigest := func(addr, repo string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			d, _ := p3Attr(s, addr, "digest")
			code, served, err := regP3Manifest(reg, repo, "1")
			if err != nil {
				return err
			}
			if code != http.StatusOK || served != d {
				return fmt.Errorf("%s: registry answers %d with digest %q, state has %q", repo, code, served, d)
			}
			digests[repo] = d
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			code, _, err := regP3Manifest(reg, delRepo, digests[delRepo])
			if err != nil {
				return err
			}
			if code != http.StatusNotFound {
				return fmt.Errorf("keep_remotely=false: manifest %s@%s still served (HTTP %d) after destroy", delRepo, digests[delRepo], code)
			}
			code, _, err = regP3Manifest(reg, keepRepo, digests[keepRepo])
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("keep_remotely=true: manifest %s@%s gone (HTTP %d) after destroy", keepRepo, digests[keepRepo], code)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("docker_registry_image.del", "digest", imgTestSHA),
					checkDigest("docker_registry_image.del", delRepo),
					checkDigest("docker_registry_image.keep", keepRepo),
					func(*terraform.State) error {
						if digests[delRepo] == "" || digests[keepRepo] == "" {
							return fmt.Errorf("digests not captured: %v", digests)
						}
						return nil
					},
				),
			},
			{
				// keep_remotely / push_retries update in place (no re-push).
				Config: strings.Replace(cfg, "push_retries  = 0", "push_retries  = 3", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_registry_image.keep", plancheck.ResourceActionUpdate),
				}},
			},
		},
	})
}
