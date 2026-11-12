package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hubURL = "https://docs.docker.com/reference/api/hub/latest.yaml"

func hubFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hub", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hubSource(t *testing.T) source {
	t.Helper()
	for _, s := range defaultSources {
		if s.Name == "hub" {
			return s
		}
	}
	t.Fatal("no hub source")
	return source{}
}

// hubRun runs only the hub source: the pinned copy is testdata/hub/base.yaml,
// the live URL serves testdata/hub/<live>, inventory.json is inv.
func hubRun(t *testing.T, live, inv string, acc []accepted, deferred map[string]upstreamDeferral) (*report, string, string) {
	t.Helper()
	repo, work := t.TempDir(), t.TempDir()
	s := hubSource(t)
	base := hubFixture(t, "base.yaml")
	pinned := filepath.Join(repo, filepath.FromSlash(s.Baseline))
	_ = os.MkdirAll(filepath.Dir(pinned), 0o755)
	if err := os.WriteFile(pinned, base, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "reference", "inventory.json"), []byte(inv), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(repo, "reference", "_gen", "drift-log.json")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	rm := &fakeRemote{urls: map[string][]byte{hubURL: hubFixture(t, live)}}
	o := options{Repo: repo, Work: work, Sources: []source{s},
		Pins:     map[string]pin{"hub": {Version: "2-beta", SHA256: sha256Hex(base), URL: hubURL}},
		Accepted: acc, Deferred: deferred, LogPath: logPath, Now: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)}
	return run(o, rm, &fakeFieldcov{t: t}), logPath, work
}

const emptyInv = `{"items": []}`

func TestHubPinP6g(t *testing.T) {
	p := testPins(t)["hub"]
	if p.Version != "2-beta" || len(p.SHA256) != 64 {
		t.Fatalf("hub pin %+v", p)
	}
	row := sourceRow{URL: hubURL, TagSHA: "sha256 7c280db399f81e7eaaf40c86b7cdfd9317c819b7589353e3a26e78d6a5a8d373"}
	if _, err := parsePin(hubSource(t), row); err == nil {
		t.Error("want an error for a hub pin without \"<version> @ \"")
	}
}

func TestHubUnchangedP6g(t *testing.T) {
	r, logPath, _ := hubRun(t, "base.yaml", emptyInv, nil, nil)
	if r.verdict() != "PASS" || r.Sources[0].Moved || len(r.Findings) != 0 {
		t.Fatalf("verdict %s, sources %+v, findings %+v", r.verdict(), r.Sources, r.Findings)
	}
	if !strings.HasPrefix(r.Sources[0].Newest, "2-beta@sha256:") || r.Sources[0].Newest != r.Sources[0].Pinned {
		t.Errorf("pinned %q newest %q", r.Sources[0].Pinned, r.Sources[0].Newest)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("drift-log written for an unchanged spec")
	}
}

func TestHubNoSurfaceChangeLogsP6g(t *testing.T) {
	r, logPath, work := hubRun(t, "same.yaml", emptyInv, nil, nil)
	if r.verdict() != "PASS" || !r.Sources[0].Moved {
		t.Fatalf("verdict %s, errors %v, findings %+v", r.verdict(), r.Errors, r.Findings)
	}
	if r.Sources[0].Surface != resultNoSurfaceChange || len(r.Notices) == 0 || !strings.Contains(r.Notices[0], "no surface change") {
		t.Errorf("surface %q notices %v", r.Sources[0].Surface, r.Notices)
	}
	if !strings.HasPrefix(r.Sources[0].Spec, work) || filepath.Base(r.Sources[0].Spec) != "latest.yaml" {
		t.Errorf("live spec written to %q, want under the work directory %q", r.Sources[0].Spec, work)
	}
	var lf driftLogFile
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &lf); err != nil {
		t.Fatal(err)
	}
	if len(lf.Rows) != 1 || lf.Rows[0].Source != "hub" || lf.Rows[0].Pinned == lf.Rows[0].Newest || lf.Rows[0].Result != resultNoSurfaceChange {
		t.Errorf("drift-log rows %+v", lf.Rows)
	}
}

const newOpItem = "hub:op/GET /v2/namespaces/{namespace}/repositories/{repository}/tags"

func TestHubNewOpUnmappedP6g(t *testing.T) {
	// An engine-swagger source naming the same operationId does not map it.
	inv := `{"items":[{"id":"engine:x","sources":[{"type":"swagger","operationId":"ListRepositoryTags"}]}]}`
	r, logPath, _ := hubRun(t, "newop.yaml", inv, nil, nil)
	if r.verdict() != "FAIL" {
		t.Fatalf("verdict %s, errors %v", r.verdict(), r.Errors)
	}
	un := r.unmapped()
	if len(un) != 1 || un[0].Item != newOpItem || un[0].Kind != kindOperation || un[0].Change != changeNew ||
		!strings.Contains(un[0].Detail, "operationId=ListRepositoryTags") {
		t.Errorf("unmapped %+v", un)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("drift-log written for a spec with a surface change")
	}
}

func TestHubNewOpMappedP6g(t *testing.T) {
	inv := `{"items":[{"id":"hub:repository_tags","layer":"hub","sources":[{"type":"openapi","spec":"hub","operationId":"ListRepositoryTags","method":"GET","path":"/v2/namespaces/{namespace}/repositories/{repository}/tags","primary":true}]}]}`
	r, _, _ := hubRun(t, "newop.yaml", inv, nil, nil)
	if r.verdict() != "PASS" {
		t.Fatalf("verdict %s, unmapped %+v, errors %v", r.verdict(), r.unmapped(), r.Errors)
	}
	if f := findingByItem(r, newOpItem); f == nil || f.Status != statusMapped {
		t.Errorf("finding %+v", f)
	}
	if r.Sources[0].Surface != "1 surface change(s)" {
		t.Errorf("surface %q", r.Sources[0].Surface)
	}
}

func TestHubNewPropertyP6g(t *testing.T) {
	// repo.immutable_tags is new; repo_detail composes repo through allOf, so
	// it gains the property too.
	want := []string{"hub:schema/repo.immutable_tags", "hub:schema/repo_detail.immutable_tags"}
	r, _, _ := hubRun(t, "newprop.yaml", emptyInv, nil, nil)
	un := r.unmapped()
	if r.verdict() != "FAIL" || len(un) != 2 {
		t.Fatalf("verdict %s, unmapped %+v", r.verdict(), un)
	}
	for i, it := range want {
		if un[i].Item != it || un[i].Kind != kindField || un[i].Detail != "boolean" {
			t.Errorf("unmapped[%d] = %+v, want %s", i, un[i], it)
		}
	}
	// Accepted in drift-accepted.json and deferred in deferred.d/upstream.json: passes.
	acc := []accepted{{Item: want[0], Reason: "reviewed, not modelled yet", Date: "2026-11-11", VersionSeen: "2-beta"}}
	def := map[string]upstreamDeferral{want[1]: {Item: want[1], Reason: "read-only detail field, not modelled", VersionSeen: "2-beta"}}
	r, _, _ = hubRun(t, "newprop.yaml", emptyInv, acc, def)
	if r.verdict() != "PASS" || len(r.Stale) != 0 {
		t.Errorf("accepted+deferred: verdict %s, unmapped %+v, stale %v", r.verdict(), r.unmapped(), r.Stale)
	}
	if f := findingByItem(r, want[0]); f == nil || f.Status != statusAccepted {
		t.Errorf("accepted finding %+v", f)
	}
}

func TestHubRemovedOpFailsP6g(t *testing.T) {
	// newop.yaml as the pin and base.yaml live: the operation was removed.
	repo := t.TempDir()
	s := hubSource(t)
	pb := hubFixture(t, "newop.yaml")
	pinned := filepath.Join(repo, filepath.FromSlash(s.Baseline))
	_ = os.MkdirAll(filepath.Dir(pinned), 0o755)
	_ = os.WriteFile(pinned, pb, 0o644)
	_ = os.WriteFile(filepath.Join(repo, "reference", "inventory.json"), []byte(emptyInv), 0o644)
	o := options{Repo: repo, Work: t.TempDir(), Sources: []source{s},
		Pins: map[string]pin{"hub": {Version: "2-beta", SHA256: sha256Hex(pb)}}, Now: time.Now()}
	r := run(o, &fakeRemote{urls: map[string][]byte{hubURL: hubFixture(t, "base.yaml")}}, &fakeFieldcov{t: t})
	if un := r.unmapped(); len(un) != 1 || un[0].Item != newOpItem || un[0].Change != changeRemoved {
		t.Errorf("unmapped %+v errors %v", un, r.Errors)
	}
}

func TestHubPinnedCopyMismatchP6g(t *testing.T) {
	repo := t.TempDir()
	s := hubSource(t)
	pinned := filepath.Join(repo, filepath.FromSlash(s.Baseline))
	_ = os.MkdirAll(filepath.Dir(pinned), 0o755)
	_ = os.WriteFile(pinned, hubFixture(t, "same.yaml"), 0o644)
	o := options{Repo: repo, Work: t.TempDir(), Sources: []source{s},
		Pins: map[string]pin{"hub": {Version: "2-beta", SHA256: sha256Hex(hubFixture(t, "base.yaml"))}}, Now: time.Now()}
	r := run(o, &fakeRemote{urls: map[string][]byte{hubURL: hubFixture(t, "newop.yaml")}}, &fakeFieldcov{t: t})
	if r.verdict() != "ERROR" || len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "pins") {
		t.Errorf("verdict %s errors %v", r.verdict(), r.Errors)
	}
}

func TestSchemaTypeP6g(t *testing.T) {
	cases := map[string]map[string]any{
		"$ref:repo":            {"$ref": "#/components/schemas/repo"},
		"string|null":          {"type": []any{"string", "null"}},
		"integer(int64)":       {"type": "integer", "format": "int64"},
		"array<$ref:repo>":     {"type": "array", "items": map[string]any{"$ref": "#/components/schemas/repo"}},
		"allOf($ref:a,object)": {"allOf": []any{map[string]any{"$ref": "#/x/a"}, map[string]any{"type": "object"}}},
		"any":                  {},
	}
	for want, s := range cases {
		if got := schemaType(s); got != want {
			t.Errorf("schemaType(%v) = %q, want %q", s, got, want)
		}
	}
}
