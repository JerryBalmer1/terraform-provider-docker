package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Pass 6e: surface diff of the documentation-only sources. Each kind has a
// pinned fixture (base), a release with only prose changes (same) and one
// with one added flag or option (flag).
var surfaceFixtureKinds = []struct {
	dir, kind string
	paths     []string
	added     string
}{
	{"compose", surfaceCLIYAML, []string{"docs/reference"}, "option/docker compose up --wait-timeout"},
	{"climd", surfaceCLIMD, []string{"docs/reference"}, "option/docker buildx build --sbom"},
	{"dockerfile", surfaceDockerfile, []string{"frontend/dockerfile/docs"}, "flag/FROM --reset"},
}

func surfaceFixture(t *testing.T, dir, kind, version string, paths []string) map[string]string {
	t.Helper()
	m, err := extractSurface(kind, filepath.Join("testdata", "surface", dir, version), paths)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSurfaceNoChangeP6e(t *testing.T) {
	for _, k := range surfaceFixtureKinds {
		base := surfaceFixture(t, k.dir, k.kind, "base", k.paths)
		same := surfaceFixture(t, k.dir, k.kind, "same", k.paths)
		if d := diffSurface(base, same, "src", "v2", nil); len(d) != 0 {
			t.Errorf("%s: prose-only release gave findings %+v", k.dir, d)
		}
	}
}

func TestSurfaceAddedFlagP6e(t *testing.T) {
	for _, k := range surfaceFixtureKinds {
		base := surfaceFixture(t, k.dir, k.kind, "base", k.paths)
		flag := surfaceFixture(t, k.dir, k.kind, "flag", k.paths)
		d := diffSurface(base, flag, "src", "v2", nil)
		if len(d) != 1 || d[0].Item != "src:"+k.added || d[0].Change != changeNew || d[0].Status != statusUnmapped {
			t.Errorf("%s: findings %+v, want one new unmapped src:%s", k.dir, d, k.added)
		}
		deferred := map[string]upstreamDeferral{"src:" + k.added: {Item: "src:" + k.added, Reason: "labs-only, not modelled yet"}}
		d = diffSurface(base, flag, "src", "v2", deferred)
		if len(d) != 1 || d[0].Status != statusMapped || !strings.Contains(d[0].Detail, "labs-only") {
			t.Errorf("%s: deferred item = %+v, want mapped", k.dir, d)
		}
		// The other way round it is a removal, which fails too.
		d = diffSurface(flag, base, "src", "v2", nil)
		if len(d) != 1 || d[0].Change != changeRemoved || d[0].Status != statusUnmapped {
			t.Errorf("%s: removal = %+v", k.dir, d)
		}
	}
}

func TestSurfaceExtractShapesP6e(t *testing.T) {
	compose := surfaceFixture(t, "compose", surfaceCLIYAML, "base", []string{"docs/reference"})
	wantCompose := map[string]string{
		"command/docker compose up":         "",
		"option/docker compose up --build":  "type=bool default=false",
		"option/docker compose up --detach": "type=bool default=false short=-d",
	}
	if !reflect.DeepEqual(compose, wantCompose) {
		t.Errorf("compose = %v", compose)
	}
	md := surfaceFixture(t, "climd", surfaceCLIMD, "base", []string{"docs/reference"})
	wantMD := map[string]string{
		"command/docker buildx build":           "",
		"option/docker buildx build --add-host": "type=stringSlice default=",
		"option/docker buildx build --file":     "type=string default= short=-f",
		"option/docker buildx build --push":     "type=bool default=",
	}
	if !reflect.DeepEqual(md, wantMD) {
		t.Errorf("cli-md = %v", md)
	}
	df := surfaceFixture(t, "dockerfile", surfaceDockerfile, "base", []string{"frontend/dockerfile/docs"})
	wantDF := map[string]string{
		"directive/syntax": "", "directive/escape": "",
		"instruction/FROM": "", "instruction/RUN": "", "instruction/MAINTAINER": "deprecated",
		"instruction/HEALTHCHECK": "", "instruction/ONBUILD": "", "instruction/ARG": "",
		"flag/FROM --platform": "", "flag/RUN --mount": "", "flag/HEALTHCHECK --interval": "",
		"buildarg/HTTP_PROXY": "", "buildarg/BUILDKIT_INLINE_CACHE": "",
		"check/copy-ignored-file": "",
	}
	if !reflect.DeepEqual(df, wantDF) {
		t.Errorf("dockerfile = %v", df)
	}
}

// The pinned reference/upstream copies parse, and carry items every release has.
func TestSurfacePinnedUpstreamP6e(t *testing.T) {
	up := filepath.Join("..", "..", "reference", "upstream")
	for _, s := range defaultSources {
		if s.Surface == "" {
			continue
		}
		m, err := extractSurface(s.Surface, filepath.Join(up, s.Local), s.DocPaths)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		want := map[string][]string{
			"compose":    {"command/docker compose up", "option/docker compose up --detach"},
			"docker-cli": {"command/docker container run", "option/docker container run --add-host", "command/docker trust sign"},
			"buildkit":   {"instruction/FROM", "flag/FROM --platform", "flag/COPY --from", "flag/HEALTHCHECK --interval", "directive/check", "check/copy-ignored-file", "buildarg/TARGETPLATFORM"},
			"buildx":     {"command/docker buildx build", "option/docker buildx build --push"},
		}[s.Name]
		for _, w := range want {
			if _, ok := m[w]; !ok {
				t.Errorf("%s: %s not extracted (%d items)", s.Name, w, len(m))
			}
		}
	}
}

// fakeDocs copies a fixture tree as the "newer release" checkout.
type fakeDocs struct{ from string }

func (f fakeDocs) Checkout(repo, tag string, paths []string, dir string) error {
	return filepath.Walk(f.from, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.from, p)
		dst := filepath.Join(dir, rel)
		if fi.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// surfaceRun: only buildkit moved (dockerfile/1.28.0 -> 1.29.0), the pinned
// tree is the dockerfile base fixture and the newer one is head.
func surfaceRun(t *testing.T, head string, deferred map[string]upstreamDeferral) (*report, string) {
	t.Helper()
	repo := t.TempDir()
	pinned := filepath.Join(repo, "reference", "upstream", "dockerfile")
	if err := (fakeDocs{filepath.Join("testdata", "surface", "dockerfile", "base")}).Checkout("", "", nil, pinned); err != nil {
		t.Fatal(err)
	}
	rm := &fakeRemote{tags: map[string][]string{
		"https://github.com/moby/moby":               {"api/v1.56.1"},
		"https://github.com/compose-spec/compose-go": {"v2.16.2"},
		"https://github.com/docker/compose":          {"v5.1.4"},
		"https://github.com/docker/cli":              {"v29.5.3"},
		"https://github.com/moby/buildkit":           {"dockerfile/1.28.0", "dockerfile/1.29.0"},
		"https://github.com/docker/buildx":           {"v0.38.2"},
	}, branches: map[string]string{"https://github.com/compose-spec/compose-spec#main": "3a4a6995e4a971f6173dff9130cb3e38e6738651"},
		urls: map[string][]byte{}}
	pins := testPins(t)
	rm.urls["https://docs.docker.com/reference/api/engine/version/v1.56.yaml"] = []byte("x")
	rm.urls[hubURL] = hubFixture(t, "base.yaml")
	p := pins["docs-engine-api"]
	p.SHA256 = sha256Hex([]byte("x"))
	pins["docs-engine-api"] = p
	logPath := filepath.Join(repo, "reference", "_gen", "drift-log.json")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	o := options{Repo: repo, Work: t.TempDir(), Sources: defaultSources, Pins: pins,
		Now: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC), LogPath: logPath, Deferred: deferred,
		Docs: fakeDocs{filepath.Join("testdata", "surface", "dockerfile", head)}}
	return run(o, rm, &fakeFieldcov{t: t}), logPath
}

func TestRunSurfaceNoChangeLogsP6e(t *testing.T) {
	r, logPath := surfaceRun(t, "same", nil)
	if r.verdict() != "PASS" {
		t.Fatalf("verdict %s, errors %v, findings %+v", r.verdict(), r.Errors, r.Findings)
	}
	if f := findingByItem(r, "version:buildkit"); f == nil || f.Status != statusInfo {
		t.Errorf("version:buildkit = %+v, want info", f)
	}
	if len(r.Notices) == 0 || !strings.Contains(r.Notices[0], "no surface change") {
		t.Errorf("notices = %v", r.Notices)
	}
	var lf driftLogFile
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &lf); err != nil {
		t.Fatal(err)
	}
	want := driftLogRow{Source: "buildkit", Pinned: "dockerfile/1.28.0", Newest: "dockerfile/1.29.0", Date: "2026-11-11", Result: resultNoSurfaceChange}
	if len(lf.Rows) != 1 || lf.Rows[0] != want {
		t.Errorf("drift-log rows = %+v", lf.Rows)
	}
	// A second run does not append the same pair again.
	if n, err := appendDriftLog(logPath, []driftLogRow{want}); err != nil || n != 0 {
		t.Errorf("re-append: n=%d err=%v", n, err)
	}
}

func TestRunSurfaceAddedFlagFailsP6e(t *testing.T) {
	r, logPath := surfaceRun(t, "flag", nil)
	if r.verdict() != "FAIL" {
		t.Fatalf("verdict %s, findings %+v", r.verdict(), r.Findings)
	}
	un := r.unmapped()
	if len(un) != 1 || un[0].Item != "buildkit:flag/FROM --reset" || un[0].Kind != kindSurface {
		t.Errorf("unmapped = %+v", un)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Errorf("drift-log written for a source with a surface change")
	}
	// Accepted in drift-accepted.json: passes.
	applyAccepted(r, []accepted{{Item: "buildkit:flag/FROM --reset", Reason: "labs-only flag, reviewed", Date: "2026-11-11", VersionSeen: "dockerfile/1.29.0"}})
	if r.verdict() != "PASS" {
		t.Errorf("accepted: verdict %s", r.verdict())
	}
	// Deferred in deferred.d/upstream.json: mapped, passes.
	r, _ = surfaceRun(t, "flag", map[string]upstreamDeferral{"buildkit:flag/FROM --reset": {Item: "buildkit:flag/FROM --reset", Reason: "labs-only flag, deferred"}})
	if r.verdict() != "PASS" {
		t.Errorf("deferred: verdict %s, %+v", r.verdict(), r.unmapped())
	}
}

func TestLoadUpstreamDeferralsP6e(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "upstream.json")
	if m, err := loadUpstreamDeferrals(p); err != nil || len(m) != 0 {
		t.Fatalf("absent file: %v %v", m, err)
	}
	_ = os.WriteFile(p, []byte(`{"about":"x","items":[{"item":"buildkit:flag/FROM --reset","version_seen":"dockerfile/1.29.0","date":"2026-11-11","reason":"short"}]}`), 0o644)
	if _, err := loadUpstreamDeferrals(p); err == nil {
		t.Error("short reason accepted")
	}
	_ = os.WriteFile(p, []byte(`{"about":"x","items":[{"item":"buildkit:flag/FROM --reset","version_seen":"dockerfile/1.29.0","date":"2026-11-11","reason":"labs-only flag, not modelled"}]}`), 0o644)
	if m, err := loadUpstreamDeferrals(p); err != nil || len(m) != 1 {
		t.Errorf("valid file: %v %v", m, err)
	}
}
