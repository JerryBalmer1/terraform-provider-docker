package provider

// Harness image redirection for the acceptance tests (Pass 5.1 Lane H).
//
// test/harness/scripts/harness-up.{ps1,sh} copy the Docker Hub images listed
// in test/harness/seed-images.txt into the harness registry (registry:2 in
// the manager's network namespace) and export:
//
//	TF_ACC_REGISTRY_NODES  the registry as every dind daemon reaches it
//	                       (manager:12380, an --insecure-registry on all nodes)
//	TF_ACC_SEEDED_IMAGES   the comma-separated Hub references it seeded
//
// hAccImage and hAccConfig turn a seeded Hub reference into its harness copy
// (busybox:1.36 -> manager:12380/busybox:1.36), so the tests pull from the
// local registry instead of Docker Hub, on the manager and on every worker
// a Swarm task may land on. Without the harness (or for an image that was not
// seeded) the reference is returned unchanged.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// hAccSeeded returns the seeded Hub references, longest first (so that a
// regexp alternation prefers nginx:1.27-alpine over a shorter prefix).
func hAccSeeded() []string {
	reg := strings.TrimSuffix(os.Getenv("TF_ACC_REGISTRY_NODES"), "/")
	if reg == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(os.Getenv("TF_ACC_SEEDED_IMAGES"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// hAccImage returns the harness registry copy of the Docker Hub image ref
// ("busybox:1.36" -> "manager:12380/busybox:1.36") when harness-up seeded
// it, else ref unchanged.
func hAccImage(ref string) string {
	for _, s := range hAccSeeded() {
		if s == ref {
			return strings.TrimSuffix(os.Getenv("TF_ACC_REGISTRY_NODES"), "/") + "/" + ref
		}
	}
	return ref
}

// hAccHostImage is hAccImage for a reference the provider resolves from the
// host (registry data sources, docker_registry_image): the same harness copy,
// named through TF_ACC_REGISTRY (127.0.0.1:12380), which the host can reach.
func hAccHostImage(ref string) string {
	reg := strings.TrimSuffix(os.Getenv("TF_ACC_REGISTRY"), "/")
	if reg == "" || hAccImage(ref) == ref {
		return ref
	}
	return reg + "/" + ref
}

// hAccRepo returns the repository part of hAccImage(ref) (no tag), e.g. for
// matching image_digest_ref values ("<repo>@sha256:...").
func hAccRepo(ref string) string {
	img := hAccImage(ref)
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		return img[:i]
	}
	return img
}

// hAccConfig rewrites every seeded Docker Hub reference that appears as a
// whole word in text (Terraform configuration, compose YAML, a Dockerfile)
// to its harness copy. A reference that is already qualified
// (docker.io/library/busybox:1.36, x/busybox:1.36) is left alone.
func hAccConfig(text string) string {
	seeded := hAccSeeded()
	if len(seeded) == 0 {
		return text
	}
	alts := make([]string, len(seeded))
	for i, s := range seeded {
		alts[i] = regexp.QuoteMeta(s)
	}
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_./:@-])(` + strings.Join(alts, "|") + `)([^A-Za-z0-9_.:@-]|$)`)
	// Matches cannot overlap their delimiters, so loop until stable (two
	// adjacent references separated by one character).
	for {
		next := re.ReplaceAllStringFunc(text, func(m string) string {
			sm := re.FindStringSubmatch(m)
			return sm[1] + hAccImage(sm[2]) + sm[3]
		})
		if next == text {
			return next
		}
		text = next
	}
}

// hAccTest is resource.Test with hAccConfig applied to every step's Config,
// so the acceptance tests pull seeded images from the harness registry. Go
// code in a test (checks, Dockerfiles written to disk, direct API calls)
// maps its own references with hAccImage / hAccConfig.
func hAccTest(t *testing.T, tc resource.TestCase) {
	t.Helper()
	for i := range tc.Steps {
		tc.Steps[i].Config = hAccConfig(tc.Steps[i].Config)
	}
	resource.Test(t, tc)
}

func TestHAccImageRewrite(t *testing.T) {
	t.Setenv("TF_ACC_REGISTRY_NODES", "")
	if got := hAccConfig(`image = "busybox:1.36"`); got != `image = "busybox:1.36"` {
		t.Fatalf("without the harness: %q", got)
	}
	t.Setenv("TF_ACC_REGISTRY_NODES", "manager:12380")
	t.Setenv("TF_ACC_SEEDED_IMAGES", "busybox:1.36,nginx:1.27-alpine,nginx:alpine")
	cases := map[string]string{
		`image = "busybox:1.36"`:                `image = "manager:12380/busybox:1.36"`,
		"FROM busybox:1.36\nRUN true\n":         "FROM manager:12380/busybox:1.36\nRUN true\n",
		"  base: {image: busybox:1.36}\n":       "  base: {image: manager:12380/busybox:1.36}\n",
		`"nginx:1.27-alpine","nginx:alpine"`:    `"manager:12380/nginx:1.27-alpine","manager:12380/nginx:alpine"`,
		`docker.io/library/busybox:1.36`:        `docker.io/library/busybox:1.36`,
		`busybox:1.36.1 busybox:1.36@sha256:ab`: `busybox:1.36.1 busybox:1.36@sha256:ab`,
		`nginx:1.27 alpine:3.20`:                `nginx:1.27 alpine:3.20`,
		`manager:12380/busybox:1.36`:            `manager:12380/busybox:1.36`,
	}
	for in, want := range cases {
		if got := hAccConfig(in); got != want {
			t.Errorf("hAccConfig(%q) = %q, want %q", in, got, want)
		}
	}
	if got := hAccImage("alpine:3.20"); got != "alpine:3.20" {
		t.Errorf("unseeded image rewritten: %q", got)
	}
	if got := hAccRepo("nginx:1.27-alpine"); got != "manager:12380/nginx" {
		t.Errorf("hAccRepo = %q", got)
	}
}
