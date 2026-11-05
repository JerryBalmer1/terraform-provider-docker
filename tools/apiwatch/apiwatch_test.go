package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const specV1 = `swagger: "2.0"
info: {version: "1.1"}
paths:
  /containers/create:
    post:
      operationId: ContainerCreate
      parameters:
        - {name: name, in: query}
        - {name: body, in: body, schema: {$ref: "#/definitions/ContainerConfig"}}
  /containers/{id}/stats:
    get: {operationId: ContainerStats}
  /widgets:
    get: {operationId: WidgetList}
definitions:
  HostConfig:
    properties: {Memory: {type: integer}, OldField: {type: string}}
  Gone: {properties: {A: {type: string}}}
`

const specV2 = `swagger: "2.0"
info: {version: "1.2"}
paths:
  /containers/create:
    post:
      operationId: ContainerCreate
      parameters:
        - {name: name, in: query}
        - {name: platform, in: query}
        - {name: body, in: body, schema: {$ref: "#/definitions/ContainerCreateRequest"}}
  /containers/{id}/stats:
    get: {operationId: ContainerStats}
  /images/{name}/push:
    post: {operationId: ImagePush}
  /services/{id}/logs:
    get: {operationId: ServiceLogs}
definitions:
  HostConfig:
    allOf:
      - properties: {Memory: {type: integer}}
      - properties: {Annotations: {type: object}}
  NewThing: {properties: {X: {type: string}}}
`

func TestParseAndDiff(t *testing.T) {
	a, err := ParseSpec([]byte(specV1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseSpec([]byte(specV2))
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != "1.1" || len(a.Endpoints) != 3 || strings.Join(a.Defs["HostConfig"], ",") != "Memory,OldField" {
		t.Fatalf("parsed v1: %+v", a)
	}
	var got []string
	for _, c := range Diff(a, b) {
		got = append(got, c.Op+" "+c.Subject+" | "+c.Detail+" | "+c.Target.String())
	}
	want := []string{
		"NEW GET /services/{id}/logs | operationId ServiceLogs | skip: aggregated service logs have no provider equivalent (docker_logs reads one container)",
		"CHANGED POST /containers/create | +query:platform, body #/definitions/ContainerConfig -> #/definitions/ContainerCreateRequest | resource docker_container",
		"NEW POST /images/{name}/push | operationId ImagePush | resource docker_registry_image",
		"REMOVED GET /widgets | operationId WidgetList | " + (Target{Kind: "unmapped"}).String(),
		"FIELD+ HostConfig.Annotations |  | resource docker_container",
		"FIELD- HostConfig.OldField |  | resource docker_container",
		"DEF+ NewThing | 1 field(s) | " + (Target{Kind: "unmapped"}).String(),
		"DEF- Gone |  | " + (Target{Kind: "unmapped"}).String(),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("diff:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestParseRejectsNonSpec(t *testing.T) {
	if _, err := ParseSpec([]byte("<html>not found</html>")); err == nil {
		t.Error("an HTML page parsed as a spec")
	}
}

func TestMapping(t *testing.T) {
	for _, c := range []struct{ method, path, want string }{
		{"POST", "/containers/{id}/exec", "action docker_exec"},
		{"GET", "/containers/{id}/logs", "data docker_logs"},
		{"POST", "/containers/{id}/kill", "skip"},
		{"POST", "/containers/create", "resource docker_container"},
		{"GET", "/images/get", "action docker_image_save"},
		{"POST", "/swarm/join", "resource docker_swarm_node"},
		{"POST", "/swarm/update", "resource docker_swarm_init"},
		{"POST", "/auth", "provider registry_auth"},
		{"GET", "/nope", "unmapped"},
	} {
		if got := MapEndpoint(c.method, c.path).String(); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s %s -> %q, want prefix %q", c.method, c.path, got, c.want)
		}
	}
	for name, want := range map[string]string{
		"HostConfig": "resource docker_container", "NetworkingConfig": "resource docker_container",
		"TaskSpec": "resource docker_service", "Task": "skip", "SwarmSpec": "resource docker_swarm_init",
		"ConfigSpec": "resource docker_config", "ContainerConfig": "resource docker_container",
	} {
		if got := MapDefinition(name).String(); !strings.HasPrefix(got, want) {
			t.Errorf("%s -> %q, want prefix %q", name, got, want)
		}
	}
}

// fakeDocs serves /v{version}.yaml for the given specs and 404 otherwise.
func fakeDocs(t *testing.T, specs map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v"), ".yaml")
		s, ok := specs[v]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(s))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeTestPinned(t *testing.T, srv *httptest.Server, version, spec string) string {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "pinned.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Pinned{APIVersion: version, SHA256: sha256Hex([]byte(spec)), Spec: "spec/pinned.yaml", URLTemplate: srv.URL + "/v{version}.yaml"}
	path := filepath.Join(dir, "pinned.json")
	if err := writePinned(path, p); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCheckAndUpdate(t *testing.T) {
	srv := fakeDocs(t, map[string]string{"1.1": specV1, "1.2": specV2})
	pinned := writeTestPinned(t, srv, "1.1", specV1)
	var out bytes.Buffer

	// A newer version is published: -check fails.
	err := run(options{Pinned: pinned, Check: true, MaxProbe: 5}, srv.Client(), &out)
	if !errors.Is(err, errUnpinned) || !strings.Contains(err.Error(), "v1.2 is published, pinned is v1.1") {
		t.Fatalf("check with a newer version: %v", err)
	}

	// The default run diffs the pinned copy against the newest.
	out.Reset()
	if err := run(options{Pinned: pinned, MaxProbe: 5}, srv.Client(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Engine API v1.1 -> v1.2") || !strings.Contains(out.String(), "endpoints: 2 new, 1 changed, 1 removed") {
		t.Errorf("table:\n%s", out.String())
	}

	// -todo groups by provider type.
	out.Reset()
	if err := run(options{Pinned: pinned, Todo: true, Tag: "P9-0", MaxProbe: 5}, srv.Client(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[P9-0] [TODO] apiwatch Engine API v1.1 -> v1.2, resource docker_container: review and cover or log as skipped: CHANGED POST /containers/create [+query:platform, body #/definitions/ContainerConfig -> #/definitions/ContainerCreateRequest]; FIELD+ HostConfig.Annotations; FIELD- HostConfig.OldField",
		"[P9-0] [ASSUMED] apiwatch Engine API v1.1 -> v1.2, no provider change (aggregated service logs",
		"[P9-0] [TODO] apiwatch Engine API v1.1 -> v1.2, unmapped",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("todo output lacks %q:\n%s", want, out.String())
		}
	}

	// -update pins the newest; then -check passes and the old copy is gone.
	out.Reset()
	if err := run(options{Pinned: pinned, Upd: true, MaxProbe: 5}, srv.Client(), &out); err != nil {
		t.Fatal(err)
	}
	p, err := readPinned(pinned)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != "1.2" || p.SHA256 != sha256Hex([]byte(specV2)) || p.Spec != "spec/engine-api-v1.2.yaml" || p.Reviewed == "" {
		t.Errorf("pinned after update: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(pinned), "spec", "pinned.yaml")); !os.IsNotExist(err) {
		t.Errorf("old spec copy not removed: %v", err)
	}
	out.Reset()
	if err := run(options{Pinned: pinned, Check: true, MaxProbe: 5}, srv.Client(), &out); err != nil {
		t.Fatalf("check after update: %v", err)
	}
	out.Reset()
	if err := run(options{Pinned: pinned, Todo: true, Tag: "P9-0", MaxProbe: 5}, srv.Client(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[P9-0] [VERIFIED] apiwatch Engine API v1.2 -> v1.2: no endpoint or definition changes.") {
		t.Errorf("todo with nothing new:\n%s", out.String())
	}
}

func TestCheckRepublished(t *testing.T) {
	srv := fakeDocs(t, map[string]string{"1.1": specV1 + "# edited\n"})
	pinned := writeTestPinned(t, srv, "1.1", specV1)
	err := run(options{Pinned: pinned, Check: true, MaxProbe: 5}, srv.Client(), &bytes.Buffer{})
	if !errors.Is(err, errUnpinned) || !strings.Contains(err.Error(), "re-published") {
		t.Fatalf("check on a re-published spec: %v", err)
	}
}
