package main

import (
	"bytes"
	"strings"
	"testing"
)

var testTargets = []string{"darwin_amd64", "darwin_arm64", "linux_386", "linux_amd64", "linux_arm", "linux_arm64", "windows_amd64", "windows_arm64"}

// The snapshot fixture uploads exactly the registry set: the 8 zips, the manifest (release.extra_files), the
// SHA256SUMS and the .sig the signs block adds (a snapshot skips signing).
func TestDistAssetsOK(t *testing.T) {
	got, want, err := DistAssets("testdata/dist-ok", "testdata/goreleaser.yml", testTargets)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 11 {
		t.Fatalf("want 11 expected assets, got %d: %v", len(want), want)
	}
	if f := CompareAssets(got, want); len(f) != 0 {
		t.Fatalf("findings on a clean snapshot: %v", f)
	}
	for _, n := range []string{"terraform-provider-docker_1.2.3_manifest.json", "terraform-provider-docker_1.2.3_SHA256SUMS.sig"} {
		if !strings.Contains(strings.Join(got, " "), n) {
			t.Errorf("%s not in the uploaded set %v", n, got)
		}
	}
}

// A stray .json among the uploadable artifacts (the v0.1.0 failure) fails, by name.
func TestDistAssetsStrayJSON(t *testing.T) {
	got, want, err := DistAssets("testdata/dist-stray", "testdata/goreleaser.yml", testTargets)
	if err != nil {
		t.Fatal(err)
	}
	f := CompareAssets(got, want)
	if len(f) != 1 || !strings.Contains(f[0], "unexpected release asset terraform-provider-docker_1.2.3_SHA256SUMS.sigstore.json") {
		t.Fatalf("want one finding for the .sigstore.json, got %v", f)
	}
}

// A missing target zip fails too.
func TestDistAssetsMissingZip(t *testing.T) {
	got, want, err := DistAssets("testdata/dist-ok", "testdata/goreleaser.yml", append(testTargets, "freebsd_amd64"))
	if err != nil {
		t.Fatal(err)
	}
	f := CompareAssets(got, want)
	if len(f) != 1 || !strings.Contains(f[0], "missing release asset terraform-provider-docker_1.2.3_freebsd_amd64.zip") {
		t.Fatalf("want one missing-zip finding, got %v", f)
	}
}

// The v0.1.0 workflow uploaded the cosign bundle with gh release upload: one finding.
func TestCheckWorkflowV010(t *testing.T) {
	out := new(bytes.Buffer)
	if code := run([]string{"-workflow", "testdata/release-v0.1.0.yml"}, out, out); code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if !strings.Contains(out.String(), `gh release upload "${SUMS}.sigstore.json"`) {
		t.Fatalf("finding does not name the upload:\n%s", out)
	}
}

func TestCheckWorkflowShapes(t *testing.T) {
	cases := []struct {
		name, yml string
		findings  int
	}{
		{"artifact upload only", `
jobs:
  sign:
    steps:
      - uses: actions/upload-artifact@x
        with: {path: dist/x_SHA256SUMS.sigstore.json}
`, 0},
		{"gh upload of allowed assets", `
jobs:
  a:
    steps:
      - run: |
          gh release upload "$TAG" dist/p_1_linux_amd64.zip p_1_SHA256SUMS p_1_SHA256SUMS.sig --repo o/r --clobber
`, 0},
		{"gh upload of a json over a continuation", `
jobs:
  a:
    steps:
      - run: |
          gh release upload "$TAG" \
            dist/provenance.intoto.jsonl --clobber
`, 1},
		{"softprops with a stray json", `
jobs:
  a:
    steps:
      - uses: softprops/action-gh-release@abc
        with:
          files: |
            dist/*.zip
            dist/*_SHA256SUMS
            dist/*.json
`, 1},
		{"softprops with no files", `
jobs:
  a:
    steps:
      - uses: softprops/action-gh-release@abc
`, 0},
		{"asset API", `
jobs:
  a:
    steps:
      - run: curl -X POST https://uploads.github.com/repos/o/r/releases/1/assets?name=x.json
`, 1},
	}
	for _, c := range cases {
		f, _, err := CheckWorkflow([]byte(c.yml))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(f) != c.findings {
			t.Errorf("%s: %d findings, want %d: %v", c.name, len(f), c.findings, f)
		}
	}
}

// The workflow in the repository passes.
func TestCheckWorkflowRepo(t *testing.T) {
	out := new(bytes.Buffer)
	if code := run([]string{"-workflow", "../../.github/workflows/release.yml"}, out, out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
