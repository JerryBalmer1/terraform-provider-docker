package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func climapSchema() *tfprotov6.GetProviderSchemaResponse {
	str := func(n string) *tfprotov6.SchemaAttribute { return &tfprotov6.SchemaAttribute{Name: n} }
	return &tfprotov6.GetProviderSchemaResponse{
		Provider: &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{Attributes: []*tfprotov6.SchemaAttribute{str("host")}}},
		ResourceSchemas: map[string]*tfprotov6.Schema{
			"docker_widget": {Block: &tfprotov6.SchemaBlock{
				Attributes: []*tfprotov6.SchemaAttribute{str("name"), {Name: "port", NestedType: &tfprotov6.SchemaObject{Attributes: []*tfprotov6.SchemaAttribute{str("target")}}}},
				BlockTypes: []*tfprotov6.SchemaNestedBlock{{TypeName: "build", Block: &tfprotov6.SchemaBlock{Attributes: []*tfprotov6.SchemaAttribute{str("context")}}}},
			}},
		},
		ActionSchemas: map[string]*tfprotov6.ActionSchema{
			"docker_exec": {Schema: &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{Attributes: []*tfprotov6.SchemaAttribute{str("container")}}}},
		},
		Functions: map[string]*tfprotov6.Function{"parse_ref": {Parameters: []*tfprotov6.FunctionParameter{{Name: "ref"}}}},
	}
}

func TestValidateCLIMap(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := &cliMapFile{Commands: []cliCommand{
		{CLI: "docker build", Group: "docker", Map: []cliTarget{{Kind: "resource", Type: "docker_widget", Attrs: []string{"name", "build.context", "port.target"}}}},
		{CLI: "docker exec", Group: "docker", Map: []cliTarget{{Kind: "action", Type: "docker_exec", Attrs: []string{"container"}}}},
		{CLI: "parse", Group: "docker", Map: []cliTarget{{Kind: "function", Type: "parse_ref", Attrs: []string{"ref"}}}},
		{CLI: "docker -H", Group: "docker", Map: []cliTarget{{Kind: "provider", Type: "docker", Attrs: []string{"host"}}}},
		{CLI: "convert", Group: "compose", Map: []cliTarget{{Kind: "tool", Type: "tools/x"}}},
		{CLI: "docker commit", Group: "docker", None: "no equivalent by design"},
	}}
	if errs := validateCLIMap(climapSchema(), good, root); len(errs) != 0 {
		t.Fatalf("valid map rejected: %v", errs)
	}

	bad := &cliMapFile{Commands: []cliCommand{
		{CLI: "a", Group: "docker", Map: []cliTarget{{Kind: "resource", Type: "docker_widget", Attrs: []string{"nope", "build.nope"}}}},
		{CLI: "b", Group: "docker", Map: []cliTarget{{Kind: "resource", Type: "docker_gone"}}},
		{CLI: "c", Group: "docker", Map: []cliTarget{{Kind: "function", Type: "parse_ref", Attrs: []string{"x"}}}},
		{CLI: "d", Group: "nowhere", None: "x"},
		{CLI: "e", Group: "docker"},
		{CLI: "e", Group: "docker", None: "dup"},
		{CLI: "f", Group: "compose", Map: []cliTarget{{Kind: "tool", Type: "tools/missing"}}},
	}}
	got := strings.Join(validateCLIMap(climapSchema(), bad, root), "\n")
	for _, want := range []string{
		`"a" -> resource docker_widget: no attribute or block "nope"`,
		`"a" -> resource docker_widget: no attribute or block "build.nope"`,
		`"b" -> resource docker_gone: no such type`,
		`"c" -> function parse_ref: function has no parameter "x"`,
		`"d": unknown group "nowhere"`,
		`"e": needs exactly one of map and none`,
		`"e": duplicate`,
		`"f" -> tool tools/missing: tool path not found`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing error %q in:\n%s", want, got)
		}
	}
}

func TestRenderCLIMap(t *testing.T) {
	f := &cliMapFile{Commands: []cliCommand{
		{CLI: "docker exec", Group: "docker", Map: []cliTarget{{Kind: "action", Type: "docker_exec", Attrs: []string{"container"}}}, Note: "a | b"},
		{CLI: "docker commit", Group: "docker", None: "BLOCKED: not declarative"},
		{CLI: "docker compose up", Group: "compose", Map: []cliTarget{{Kind: "resource", Type: "docker_compose"}}},
	}}
	out := string(renderCLIMap(f, "../terraform-provider-docker"))
	for _, want := range []string{
		"---\nname: docker-cli-map\ndescription: ",
		generatedMarker,
		"| `docker exec` | [`action.docker_exec`](../terraform-provider-docker/reference/action.docker_exec.md) | `container` | a \\| b |",
		"| `docker commit` | no equivalent | - | BLOCKED: not declarative |",
		"## docker compose",
		// Coverage rows come from tools/parity/paritydata.
		"| action | `docker_exec` | have | `docker exec` |",
		"| resource | `docker_compose` | partial | `docker compose up` |",
		"| resource | `docker_stack` | provider-only | - |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered map lacks %q", want)
		}
	}
}

// TestCLIMapRealProvider renders the repository's commands.json against the
// real provider, then checks -check notices drift in the output file.
func TestCLIMapRealProvider(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	o := options{
		Out:       filepath.Join(out, "skill"),
		Examples:  filepath.Join("..", "..", "examples"),
		Docs:      filepath.Join(out, "docs"),
		CLIMap:    filepath.Join("..", "climap", "commands.json"),
		CLIMapOut: filepath.Join(out, "docker-cli-map", "SKILL.md"),
	}
	if err := run(context.Background(), o, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "climap") {
		t.Errorf("climap warnings for the real provider:\n%s", stderr.String())
	}
	b, err := os.ReadFile(o.CLIMapOut)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("](../skill/reference/docker_container.md)")) {
		t.Errorf("reference links not relative to the main skill:\n%.400s", b)
	}
	o.Check = true
	if err := run(context.Background(), o, &stdout, &stderr); err != nil {
		t.Fatalf("check right after write: %v\n%s", err, stderr.String())
	}
	if err := os.WriteFile(o.CLIMapOut, append(b, "edited\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), o, &stdout, &stderr); !errors.Is(err, errDrift) {
		t.Errorf("check after editing the map: got %v, want errDrift", err)
	}
}
