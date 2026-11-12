package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRemote serves tags, branches and URLs from maps (no network).
type fakeRemote struct {
	tags     map[string][]string
	branches map[string]string
	urls     map[string][]byte
	gets     []string
}

func (f *fakeRemote) Tags(repo string) ([]string, error) { return f.tags[repo], nil }

func (f *fakeRemote) Branch(repo, branch string) (string, error) {
	return f.branches[repo+"#"+branch], nil
}

func (f *fakeRemote) Get(url string) (int, []byte, error) {
	f.gets = append(f.gets, url)
	if b, ok := f.urls[url]; ok {
		return 200, b, nil
	}
	return 404, nil, nil
}

// fakeFieldcov returns the testdata field sets by input.
type fakeFieldcov struct {
	t    *testing.T
	runs []fcInputs
}

func (f *fakeFieldcov) Run(in fcInputs) (*fieldSet, error) {
	f.runs = append(f.runs, in)
	name := "base-fields.json"
	switch {
	case in.Swagger != "":
		name = "head-fields.json"
	case in.Compose != "" && !strings.Contains(in.Compose, "compose-go-embedded"):
		name = "composego-head-fields.json"
	}
	fs, err := readFieldSet(filepath.Join("testdata", name))
	if err != nil {
		f.t.Fatal(err)
	}
	return fs, nil
}

func testPins(t *testing.T) map[string]pin {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "SOURCES.md"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := pinsFrom(string(b), defaultSources)
	if err != nil {
		t.Fatal(err)
	}
	// The hub pin is the sha256 of the fixture as checked out (line endings
	// may differ between checkouts).
	h := p["hub"]
	h.SHA256 = sha256Hex(hubFixture(t, "base.yaml"))
	p["hub"] = h
	return p
}

func TestPinsFrom(t *testing.T) {
	p := testPins(t)
	want := map[string]string{
		"moby": "api/v1.56.1", "docs-engine-api": "1.56", "compose-spec": "main", "compose-go": "v2.16.2",
		"compose": "v5.1.4", "docker-cli": "v29.5.3", "buildkit": "dockerfile/1.28.0", "buildx": "v0.38.2", "hub": "2-beta",
	}
	for k, v := range want {
		if p[k].Version != v {
			t.Errorf("%s: pinned %q, want %q", k, p[k].Version, v)
		}
	}
	if p["compose-spec"].SHA != "3a4a6995e4a971f6173dff9130cb3e38e6738651" {
		t.Errorf("compose-spec sha %q", p["compose-spec"].SHA)
	}
	if !strings.HasPrefix(p["docs-engine-api"].SHA256, "00ccd7d2") {
		t.Errorf("docs sha256 %q", p["docs-engine-api"].SHA256)
	}
}

func TestPinsFromUnknownRow(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join("testdata", "SOURCES.md"))
	text := string(b) + "\n| x | new thing | https://github.com/example/new/tree/abc | v1.0.0 @ 0000000000000000000000000000000000000000 | upstream/x/ | MIT | 2026-11-09 |\n"
	if _, err := pinsFrom(text, defaultSources); err == nil || !strings.Contains(err.Error(), "not watched") {
		t.Fatalf("want a not-watched error, got %v", err)
	}
	if _, err := pinsFrom(strings.Replace(string(b), "api/v1.56.1 @", "api/v1.56 @", 1), defaultSources); err == nil {
		t.Fatal("want an error for a pinned tag that is not MAJOR.MINOR.PATCH")
	}
}

func TestNewestTag(t *testing.T) {
	tags := []string{"v1.9.0", "v1.10.0", "v1.10.1-rc1", "v2.0.0-beta", "dockerfile/1.28.0", "v1.10.0-labs", "x1.99.0", "v1.10.01x"}
	got, ok := newestTag("v", tags)
	if !ok || got != "v1.10.0" {
		t.Fatalf("newestTag v = %q %v", got, ok)
	}
	got, _ = newestTag("dockerfile/", append(tags, "dockerfile/1.28.1-labs", "dockerfile/1.29.0"))
	if got != "dockerfile/1.29.0" {
		t.Fatalf("newestTag dockerfile/ = %q", got)
	}
	if _, ok := newestTag("api/v", tags); ok {
		t.Fatal("no api/v tags expected")
	}
}

func TestParseLsRemote(t *testing.T) {
	out := "aaa\trefs/tags/v1.0.0\r\nbbb\trefs/heads/main\n\n"
	got := parseLsRemote(out)
	if len(got) != 2 || got[0] != "refs/tags/v1.0.0\taaa" || got[1] != "refs/heads/main\tbbb" {
		t.Fatalf("parseLsRemote = %q", got)
	}
}

func TestParseAccepted(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "accepted.json"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := parseAccepted(b)
	if err != nil || len(acc) != 3 {
		t.Fatalf("parseAccepted: %v (%d)", err, len(acc))
	}
	bad := []string{
		`{"accepted":[{"item":"x","reason":"short","date":"2026-11-09","version_seen":"v1"}]}`,
		`{"accepted":[{"item":"x","reason":"a long enough reason for this","date":"09/11/2026","version_seen":"v1"}]}`,
		`{"accepted":[{"item":"x","reason":"a long enough reason for this","date":"2026-11-09"}]}`,
		`{"accepted":[{"item":"","reason":"a long enough reason for this","date":"2026-11-09","version_seen":"v1"}]}`,
		`{"accepted":[{"item":"x","reason":"a long enough reason for this","date":"2026-11-09","version_seen":"v1"},` +
			`{"item":"x","reason":"a long enough reason for this","date":"2026-11-09","version_seen":"v1"}]}`,
		`{"accepted":[],"extra":1}`,
	}
	for _, s := range bad {
		if _, err := parseAccepted([]byte(s)); err == nil {
			t.Errorf("want an error for %s", s)
		}
	}
	if acc, err := parseAccepted([]byte(`{"about":"x","accepted":[]}`)); err != nil || len(acc) != 0 {
		t.Errorf("empty file: %v", err)
	}
}

// fixtureRun is the end-to-end scenario: moby, docs and compose-go moved
// (field models), compose and docker-cli moved (documentation only), the
// rest up to date.
func fixtureRun(t *testing.T, overrides map[string]string) (*report, *fakeRemote, *fakeFieldcov) {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "reference"), 0o755); err != nil {
		t.Fatal(err)
	}
	inv, _ := os.ReadFile(filepath.Join("testdata", "inventory.json"))
	if err := os.WriteFile(filepath.Join(repo, "reference", "inventory.json"), inv, 0o644); err != nil {
		t.Fatal(err)
	}
	accB, _ := os.ReadFile(filepath.Join("testdata", "accepted.json"))
	acc, err := parseAccepted(accB)
	if err != nil {
		t.Fatal(err)
	}
	rm := &fakeRemote{
		tags: map[string][]string{
			"https://github.com/moby/moby":               {"api/v1.56.1", "api/v1.57.0", "api/v1.58.0-rc.1", "docker-v29.9.3", "v28.0.0"},
			"https://github.com/compose-spec/compose-go": {"v2.16.2", "v2.17.0"},
			"https://github.com/docker/compose":          {"v5.1.4", "v5.7.0", "v5.8.0-rc.1"},
			"https://github.com/docker/cli":              {"v29.5.3", "v29.9.3"},
			"https://github.com/moby/buildkit":           {"dockerfile/1.28.0", "dockerfile/1.29.0-labs", "v0.30.0"},
			"https://github.com/docker/buildx":           {"v0.38.2", "v0.38.2-desktop.1"},
		},
		branches: map[string]string{"https://github.com/compose-spec/compose-spec#main": "3a4a6995e4a971f6173dff9130cb3e38e6738651"},
		urls: map[string][]byte{
			"https://raw.githubusercontent.com/moby/moby/refs/tags/api/v1.57.0/api/swagger.yaml":                   []byte("swagger: new"),
			"https://raw.githubusercontent.com/moby/moby/refs/tags/api/v1.55.0/api/swagger.yaml":                   []byte("swagger: old"),
			"https://docs.docker.com/reference/api/engine/version/v1.57.yaml":                                      []byte("swagger: docs"),
			"https://raw.githubusercontent.com/compose-spec/compose-go/refs/tags/v2.17.0/schema/compose-spec.json": []byte("{}"),
			hubURL: hubFixture(t, "base.yaml"),
		},
	}
	fc := &fakeFieldcov{t: t}
	o := options{Repo: repo, Work: t.TempDir(), Sources: defaultSources, Pins: testPins(t), Accepted: acc,
		Overrides: overrides, Now: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	return run(o, rm, fc), rm, fc
}

func findingByItem(r *report, item string) *finding {
	for i := range r.Findings {
		if r.Findings[i].Item == item {
			return &r.Findings[i]
		}
	}
	return nil
}

func TestRunScenario(t *testing.T) {
	r, _, fc := fixtureRun(t, nil)
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	moved := map[string]bool{}
	for _, s := range r.Sources {
		moved[s.Name] = s.Moved
	}
	wantMoved := map[string]bool{"moby": true, "docs-engine-api": true, "compose-spec": false, "compose-go": true,
		"compose": true, "docker-cli": true, "buildkit": false, "buildx": false, "hub": false}
	for k, v := range wantMoved {
		if moved[k] != v {
			t.Errorf("%s moved = %v, want %v", k, moved[k], v)
		}
	}
	// One combined pinned/newer pair (engine from moby) plus the compose-go pair.
	if len(fc.runs) != 4 {
		t.Errorf("fieldcov runs = %d (%v), want 4", len(fc.runs), fc.runs)
	}
	for _, in := range fc.runs {
		if in.Swagger != "" && !strings.Contains(in.Swagger, filepath.Join("moby", "api_v1.57.0")) {
			t.Errorf("engine slot should take the moby tag spec, got %s", in.Swagger)
		}
	}
	want := map[string]string{
		"version:docker-cli":              statusUnmapped,
		"version:compose":                 statusAccepted,
		"version:moby":                    statusInfo,
		"engine:ContainerConfig/Shiny":    statusUnmapped,
		"engine:ContainerConfig/Known":    statusMapped,
		"engine:ContainerConfig/Waived":   statusAccepted,
		"engine:ContainerConfig/OldThing": statusInfo,
		"operation:ContainerBrandNew":     statusUnmapped,
		"operation:ImagePlanned":          statusMapped,
		"operation:ContainerLegacy":       statusInfo,
		"compose:service/sparkle":         statusUnmapped,
	}
	for item, st := range want {
		f := findingByItem(r, item)
		if f == nil {
			t.Errorf("no finding %s", item)
			continue
		}
		if f.Status != st {
			t.Errorf("%s: status %s, want %s (%s)", item, f.Status, st, f.Detail)
		}
	}
	if f := findingByItem(r, "compose:service/sparkle"); f != nil && (f.Source != "compose-go" || f.Version != "v2.17.0") {
		t.Errorf("compose-go finding: %+v", *f)
	}
	if f := findingByItem(r, "engine:ContainerConfig/OldThing"); f != nil && !strings.Contains(f.Detail, "provider maps it") {
		t.Errorf("removed mapped field detail: %q", f.Detail)
	}
	if got := len(r.unmapped()); got != 4 {
		t.Errorf("unmapped = %d, want 4", got)
	}
	if r.verdict() != "FAIL" {
		t.Errorf("verdict %s", r.verdict())
	}
	if len(r.Stale) != 1 || !strings.HasPrefix(r.Stale[0], "operation:GoneAway") {
		t.Errorf("stale = %v", r.Stale)
	}
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "stale pointer") {
		t.Errorf("notes = %v", r.Notes)
	}
	// Unmapped findings sort first.
	if r.Findings[0].Status != statusUnmapped {
		t.Errorf("first finding %+v", r.Findings[0])
	}

	var md, con bytes.Buffer
	r.writeMarkdown(&md)
	r.writeConsole(&con)
	for _, s := range []string{"# Upstream drift", "## Unmapped (fails DriftCheck) (4)", "`operation:ContainerBrandNew`", "DriftCheck never changes a pin"} {
		if !strings.Contains(md.String(), s) {
			t.Errorf("markdown lacks %q", s)
		}
	}
	if !strings.Contains(con.String(), "drift: FAIL: 5 of 9 sources moved; new operations 2, new writable fields 4") {
		t.Errorf("console summary:\n%s", con.String())
	}
	if _, err := r.json(); err != nil {
		t.Fatal(err)
	}
}

func TestRunVersionAcceptIsExact(t *testing.T) {
	// A newer compose release than the accepted v5.7.0 fails again.
	r, _, _ := fixtureRun(t, map[string]string{"compose": "v5.8.0"})
	if f := findingByItem(r, "version:compose"); f == nil || f.Status != statusUnmapped {
		t.Fatalf("version:compose = %+v", f)
	}
}

func TestRunNothingMoved(t *testing.T) {
	r, rm, fc := fixtureRun(t, map[string]string{
		"moby": "api/v1.56.1", "compose-go": "v2.16.2", "compose": "v5.1.4", "docker-cli": "v29.5.3",
	})
	// docs.docker.com: v1.57 exists in the fake, so pin the probe by override too.
	_ = rm
	for _, s := range r.Sources {
		if s.Moved && s.Name != "docs-engine-api" {
			t.Errorf("%s moved", s.Name)
		}
	}
	if len(fc.runs) != 2 {
		t.Errorf("fieldcov runs = %d, want 2 (the docs.docker.com engine pair)", len(fc.runs))
	}

	r, _, fc = fixtureRun(t, map[string]string{
		"moby": "api/v1.56.1", "compose-go": "v2.16.2", "compose": "v5.1.4", "docker-cli": "v29.5.3", "docs-engine-api": "1.56",
	})
	// The pinned docs version is fetched to compare its sha256; the fake has no
	// such URL, so that is an error, and nothing is diffed.
	if len(fc.runs) != 0 {
		t.Errorf("fieldcov runs = %d, want 0", len(fc.runs))
	}
	if r.verdict() != "ERROR" || len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "docs-engine-api") {
		t.Errorf("verdict %s errors %v", r.verdict(), r.Errors)
	}
}

func TestProbeURLRepublished(t *testing.T) {
	s := defaultSources[1]
	p := pin{Version: "1.56", SHA256: sha256Hex([]byte("old"))}
	rm := &fakeRemote{urls: map[string][]byte{"https://docs.docker.com/reference/api/engine/version/v1.56.yaml": []byte("new")}}
	var st sourceStatus
	probeURL(options{MaxProbe: 3}, rm, s, p, "", false, &st)
	if st.Error != "" || !st.Moved || st.Newest != "1.56" || !strings.Contains(st.Note, "re-published") {
		t.Fatalf("status %+v", st)
	}
	rm.urls["https://docs.docker.com/reference/api/engine/version/v1.56.yaml"] = []byte("old")
	st = sourceStatus{}
	probeURL(options{MaxProbe: 3}, rm, s, p, "", false, &st)
	if st.Moved {
		t.Fatalf("same bytes should not move: %+v", st)
	}
}

func TestSafeNameAndRawURL(t *testing.T) {
	if got := safeName("api/v1.57.0"); got != "api_v1.57.0" {
		t.Errorf("safeName = %q", got)
	}
	if got := rawURL("https://github.com/moby/moby", "refs/tags/api/v1.57.0", "api/swagger.yaml"); got !=
		"https://raw.githubusercontent.com/moby/moby/refs/tags/api/v1.57.0/api/swagger.yaml" {
		t.Errorf("rawURL = %q", got)
	}
}
