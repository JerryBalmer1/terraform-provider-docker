package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"Umask": "umask", "MemorySwappiness": "memory_swappiness", "CpuShares": "cpu_shares",
		"BlkioWeightDevice": "blkio_weight_device", "IPAddress": "ip_address", "max-used-space": "max_used_space",
		"NanoCPUs": "nano_cpus", "OomKillDisable": "oom_kill_disable",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSchemaMatch(t *testing.T) {
	ip := inputPaths{
		"docker_container": {{"name"}, {"memory"}, {"healthcheck"}, {"healthcheck", "interval"}, {"mounts"},
			{"mounts", "volume_options", "subpath"}, {"runtime"}},
	}
	cases := []struct {
		segs []string
		want string
	}{
		{[]string{"HostConfig", "Memory"}, "memory"},                            // leaf below a wrapper
		{[]string{"Healthcheck", "Interval"}, "healthcheck.interval"},           // two-segment suffix
		{[]string{"Name"}, "name"},                                              // top-level generic leaf
		{[]string{"Labels", "Name"}, ""},                                        // generic leaf below a non-wrapper
		{[]string{"HostConfig", "Mounts", "[]", "ImageOptions", "Subpath"}, ""}, // leaf alone below ImageOptions: no
		{[]string{"TaskTemplate", "ContainerSpec", "Configs", "[]", "Runtime"}, ""},
		{[]string{"HostConfig", "Umask"}, ""},
	}
	for _, c := range cases {
		_, got := schemaMatch(ip, []string{"docker_container"}, c.segs)
		if got != c.want {
			t.Errorf("schemaMatch(%v) = %q, want %q", c.segs, got, c.want)
		}
	}
}

func TestTypeMatches(t *testing.T) {
	g := func(pkg, typ string) *goField { return &goField{PkgName: pkg, Type: typ} }
	cases := []struct {
		ctx  string
		g    *goField
		want bool
	}{
		{"ContainerConfig", g("container", "Config"), true},
		{"HostConfig", g("container", "HostConfig"), true},
		{"TaskSpec.ContainerSpec", g("swarm", "ContainerSpec"), true},
		{"TaskSpec.ContainerSpec", g("container", "Config"), false},
		{"TaskSpec.Resources", g("swarm", "ResourceRequirements"), true},
		{"TaskSpec.Resources", g("container", "Resources"), false},
		{"Resources", g("container", "Resources"), true},
		{"Driver", g("mount", "Driver"), false},
		{"Driver", g("swarm", "Driver"), true},
		{"NetworkCreateRequest", g("client", "NetworkCreateOptions"), true},
	}
	for _, c := range cases {
		if got := typeMatches(c.ctx, c.g); got != c.want {
			t.Errorf("typeMatches(%q, %s.%s) = %v, want %v", c.ctx, c.g.PkgName, c.g.Type, got, c.want)
		}
	}
}

func TestCreditsItem(t *testing.T) {
	if creditsItem("service_expand.go:10", []string{"swarm:secret"}) {
		t.Error("a service file must not credit docker_secret")
	}
	if !creditsItem("service_expand.go:10", []string{"swarm:service"}) {
		t.Error("a service file credits docker_service")
	}
	if !creditsItem("container_expand.go:5", []string{"swarm:service"}) {
		t.Error("shared mount expansion credits every item")
	}
	if engineCreditFile("compose_local.go:1") {
		t.Error("compose conversion must not credit Engine resources")
	}
}

const tinySwagger = `
paths:
  /things/create:
    post:
      operationId: "ThingCreate"
      parameters:
        - name: "name"
          in: "query"
          type: "string"
          description: "Thing name. More text."
        - name: "body"
          in: "body"
          schema:
            allOf:
              - $ref: "#/definitions/ThingConfig"
              - type: "object"
                properties:
                  Host:
                    $ref: "#/definitions/HostThing"
                  Endpoints:
                    type: "object"
                    additionalProperties:
                      $ref: "#/definitions/Endpoint"
definitions:
  ThingConfig:
    type: "object"
    properties:
      Image: {type: "string"}
      Id: {type: "string", readOnly: true}
      Ports:
        type: "array"
        items:
          type: "object"
          properties:
            Port: {type: "integer"}
  HostThing:
    allOf:
      - $ref: "#/definitions/Res"
      - type: "object"
        properties:
          Umask: {type: "string", description: "Deprecated: use X."}
  Res:
    type: "object"
    properties:
      Memory: {type: "integer"}
  Endpoint:
    type: "object"
    properties:
      MacAddress: {type: "string"}
`

func TestEngineFields(t *testing.T) {
	var s swagger
	if err := yaml.Unmarshal([]byte(tinySwagger), &s); err != nil {
		t.Fatal(err)
	}
	fs, err := engineFields(&s, []string{"ThingCreate"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]field{}
	for _, f := range fs {
		got[f.Pointer] = f
	}
	want := map[string]string{
		"engine:ThingCreate?name":                    "ThingCreate",
		"engine:ThingCreate/Image":                   "ThingConfig",
		"engine:ThingCreate/Ports/[]/Port":           "ThingConfig.Ports",
		"engine:ThingCreate/Host/Memory":             "Res",
		"engine:ThingCreate/Host/Umask":              "HostThing",
		"engine:ThingCreate/Endpoints/{}/MacAddress": "Endpoint",
	}
	for p, ctx := range want {
		f, ok := got[p]
		if !ok {
			t.Errorf("missing %s (have %v)", p, keys(got))
			continue
		}
		if f.Context != ctx {
			t.Errorf("%s: context %q, want %q", p, f.Context, ctx)
		}
	}
	if _, ok := got["engine:ThingCreate/Id"]; ok {
		t.Error("readOnly field enumerated")
	}
	if !got["engine:ThingCreate/Host/Umask"].Deprecated {
		t.Error("Umask should be flagged deprecated")
	}
	if len(got) != len(want) {
		t.Errorf("got %d fields, want %d: %v", len(got), len(want), keys(got))
	}
}

func keys(m map[string]field) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBakeFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bake.md")
	md := "| Name | Type | Description |\n|---|---|---|\n" +
		"| [`args`](#targetargs)       | Map     | Build arguments |\n" +
		"| [`cache-from`](#targetcache-from) | List | External cache sources |\n"
	if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := bakeFields(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[1].Pointer != "bake:target/cache-from" || fs[0].Type != "map" {
		t.Fatalf("bakeFields = %+v", fs)
	}
}

func TestOverlayValidation(t *testing.T) {
	repo := t.TempDir()
	d := filepath.Join(repo, "reference", "_gen", "fieldmap.d")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("0.json", `{"fields": {"engine:X/A": {"skip": "Lane 0 is not allowed to skip anything at all"}}}`)
	write("A.json", `{"fields": {
		"engine:X/B": {"skip": "too short"},
		"engine:X/C": {"item": "engine:container", "attr": "c"},
		"engine:X/D": {"skip": "Daemon-assigned at connect time; never set by a client."},
		"engine:X/E": {"item": "engine:nope", "attr": "e"},
		"engine:X/F": {"attr": "f"},
		"engine:X/Z": {"item": "engine:container", "attr": "z"}
	}}`)
	res := &result{Map: map[string]entry{}, Overlay: map[string]string{}}
	for _, p := range []string{"A", "B", "C", "D", "E", "F"} {
		ptr := "engine:X/" + p
		res.Fields = append(res.Fields, field{Pointer: ptr, Source: "engine"})
		res.Map[ptr] = entry{Item: "engine:container", How: "gap", Lane: "A"}
	}
	byID := map[string]invItem{"engine:container": {ID: "engine:container"}}
	if err := applyOverlays(repo, res, byID); err != nil {
		t.Fatal(err)
	}
	errs := strings.Join(res.Errors, "\n")
	for _, want := range []string{"engine:X/A: Lane 0 may not skip", "engine:X/B: a skip needs a real reason",
		"engine:X/E: unknown inventory item", "engine:X/F: item is required", "engine:X/Z: no such upstream field"} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing error %q in:\n%s", want, errs)
		}
	}
	if e := res.Map["engine:X/C"]; e.How != "manual:A" || e.Attr != "c" {
		t.Errorf("C = %+v", e)
	}
	if e := res.Map["engine:X/D"]; e.Skip == "" || e.Lane != "A" {
		t.Errorf("D = %+v", e)
	}
	s := summarize(res)
	if s.Total.Gap != 4 || s.Total.Skipped != 1 || s.Total.Manual != 1 {
		t.Errorf("summary = %+v", s.Total)
	}
}

func TestRenderMapDropsLineNumbers(t *testing.T) {
	res := &result{Map: map[string]entry{"engine:X/A": {Item: "engine:container", Attr: "a", How: "auto-code",
		Code: "container.HostConfig.A @ container_resource.go:12, ctr_gap.go:7"}}}
	b, err := renderMap(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "container_resource.go, ctr_gap.go") || strings.Contains(string(b), ".go:") {
		t.Errorf("rendered map keeps line numbers:\n%s", b)
	}
}
