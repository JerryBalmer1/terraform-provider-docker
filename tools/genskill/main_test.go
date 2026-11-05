package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func syntheticResponse() *tfprotov6.GetProviderSchemaResponse {
	secret := &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{
		Description: "Manages a Swarm secret. Secrets are immutable.",
		Attributes: []*tfprotov6.SchemaAttribute{
			{Name: "name", Type: tftypes.String, Required: true, Description: "Secret name. Changing this forces a new resource."},
			{Name: "data", Type: tftypes.String, Required: true, Sensitive: true, Description: "Base64 data. Changing this FORCES REPLACEMENT."},
			{Name: "labels", Type: tftypes.Map{ElementType: tftypes.String}, Optional: true, Description: "Labels | with pipe."},
			{Name: "id", Type: tftypes.String, Computed: true, Description: "Secret ID."},
		},
	}}
	service := &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{
		Description: "Manages a Swarm service.",
		Attributes: []*tfprotov6.SchemaAttribute{
			{Name: "replicas", Type: tftypes.Number, Optional: true, Computed: true, Description: "Replica count."},
			{Name: "networks", Type: tftypes.Set{ElementType: tftypes.String}, Optional: true, Description: "Networks."},
			{Name: "meta", NestedType: &tfprotov6.SchemaObject{
				Nesting:    tfprotov6.SchemaObjectNestingModeList,
				Attributes: []*tfprotov6.SchemaAttribute{{Name: "k", Type: tftypes.String, Optional: true, Description: "Key. Changing this forces a new resource."}},
			}, Optional: true, Description: "Nested attribute."},
		},
		BlockTypes: []*tfprotov6.SchemaNestedBlock{
			{TypeName: "placement", Nesting: tfprotov6.SchemaNestedBlockNestingModeSingle, Block: &tfprotov6.SchemaBlock{
				Attributes: []*tfprotov6.SchemaAttribute{{Name: "constraints", Type: tftypes.List{ElementType: tftypes.String}, Optional: true, Description: "Constraints."}},
			}},
			{TypeName: "endpoint_spec", Nesting: tfprotov6.SchemaNestedBlockNestingModeList, MaxItems: 1, Block: &tfprotov6.SchemaBlock{
				BlockTypes: []*tfprotov6.SchemaNestedBlock{{TypeName: "ports", Nesting: tfprotov6.SchemaNestedBlockNestingModeList, Block: &tfprotov6.SchemaBlock{
					Attributes: []*tfprotov6.SchemaAttribute{{Name: "target_port", Type: tftypes.Number, Required: true, Description: "Port."}},
				}}},
			}},
		},
	}}
	return &tfprotov6.GetProviderSchemaResponse{
		Provider:          &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{Description: "Docker provider."}},
		ResourceSchemas:   map[string]*tfprotov6.Schema{"docker_secret": secret, "docker_service": service, "docker_widget": secret},
		DataSourceSchemas: map[string]*tfprotov6.Schema{"docker_secret": secret},
	}
}

func TestForcesReplacement(t *testing.T) {
	for desc, want := range map[string]bool{
		"Name. Changing this forces a new resource.": true,
		"changing it Forces Replacement":             true,
		"Name of the thing.":                         false,
		"":                                           false,
	} {
		if got := forcesReplacement(desc); got != want {
			t.Errorf("forcesReplacement(%q) = %v, want %v", desc, got, want)
		}
	}
}

func TestTypeString(t *testing.T) {
	cases := map[string]tftypes.Type{
		"string":                     tftypes.String,
		"number":                     tftypes.Number,
		"bool":                       tftypes.Bool,
		"dynamic":                    tftypes.DynamicPseudoType,
		"list(string)":               tftypes.List{ElementType: tftypes.String},
		"set(number)":                tftypes.Set{ElementType: tftypes.Number},
		"map(list(bool))":            tftypes.Map{ElementType: tftypes.List{ElementType: tftypes.Bool}},
		"object({a=string, b=bool})": tftypes.Object{AttributeTypes: map[string]tftypes.Type{"b": tftypes.Bool, "a": tftypes.String}},
	}
	for want, typ := range cases {
		if got := typeString(typ); got != want {
			t.Errorf("typeString = %q, want %q", got, want)
		}
	}
}

func TestWalkSchema(t *testing.T) {
	resp := syntheticResponse()
	td := walkSchema("docker_service", kindResource, resp.ResourceSchemas["docker_service"])
	if td.Description != "Manages a Swarm service." {
		t.Fatalf("description = %q", td.Description)
	}
	names := []string{}
	for _, a := range td.Root.Attributes {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "meta,networks,replicas" {
		t.Fatalf("attributes not sorted: %v", names)
	}
	if td.Root.Attributes[0].Type != "list(object)" || td.Root.Attributes[0].Nested == nil {
		t.Fatalf("nested attribute not walked: %+v", td.Root.Attributes[0])
	}
	if got := td.Root.Blocks[0].Name; got != "endpoint_spec" {
		t.Fatalf("blocks not sorted, first = %s", got)
	}
	ep := td.Root.Blocks[0]
	if ep.Nesting != "list of blocks" || ep.MaxItems != 1 || ep.Blocks[0].Attributes[0].Path != "endpoint_spec.ports.target_port" {
		t.Fatalf("endpoint_spec walked wrong: %+v", ep)
	}
	for _, p := range []string{"replicas", "placement.constraints", "endpoint_spec.ports.target_port", "meta.k"} {
		if !hasPath(td.Root, p) {
			t.Errorf("hasPath(%q) = false", p)
		}
	}
	if hasPath(td.Root, "placement.nope") || hasPath(td.Root, "replicas.x") {
		t.Error("hasPath matched a missing path")
	}
	if subs := subBlocks(td.Root); len(subs) != 4 {
		t.Errorf("subBlocks = %d, want 4 (meta, endpoint_spec, endpoint_spec.ports, placement)", len(subs))
	}
	repl := replacementAttrs(td.Root)
	if len(repl) != 1 || repl[0].Path != "meta.k" {
		t.Errorf("replacementAttrs = %+v", repl)
	}
}

func TestRenderSynthetic(t *testing.T) {
	ex := t.TempDir()
	dir := filepath.Join(ex, "resources", "docker_secret")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "resource.tf"), []byte("resource \"docker_secret\" \"x\" {\r\n  name = \"x\"\r\n}\r\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "import.sh"), []byte("terraform import docker_secret.x abc\n"), 0o644)

	files, readme, warnings, err := render(syntheticResponse(), renderOpts{Examples: ex, ReadmeSkillDir: "skills/x", ReadmeDocsDir: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SKILL.md", "reference/provider.md", "reference/docker_secret.md", "reference/docker_service.md", "reference/docker_widget.md", "reference/data.docker_secret.md"}
	for _, w := range want {
		if _, ok := files[w]; !ok {
			t.Errorf("missing %s; have %v", w, sortedKeys(files))
		}
	}
	if len(files) != len(want) {
		t.Errorf("got %d files, want %d", len(files), len(want))
	}
	for name, b := range files {
		if bytes.Contains(b, []byte("\r")) {
			t.Errorf("%s has CR", name)
		}
		if !bytes.Contains(b, []byte(generatedMarker)) {
			t.Errorf("%s lacks marker", name)
		}
	}
	skill := string(files["SKILL.md"])
	if !strings.HasPrefix(skill, "---\nname: terraform-provider-docker\ndescription: ") {
		t.Errorf("bad frontmatter: %.80q", skill)
	}
	for _, s := range []string{
		"| `docker_secret` | `name` |",
		"| `docker_secret` | `data` |",
		"| `docker_service` | `meta.k` |",
		"### Other", "[`docker_widget`](reference/docker_widget.md)",
		"[`docker_secret`](reference/data.docker_secret.md)",
		"resource `docker_swarm_init` is not registered",
		"`docker_service.image` is not in the schema",
	} {
		if !strings.Contains(skill, s) {
			t.Errorf("SKILL.md lacks %q", s)
		}
	}
	if len(warnings) == 0 {
		t.Error("expected example drift warnings against the synthetic schema")
	}
	ref := string(files["reference/docker_secret.md"])
	for _, s := range []string{
		"| `data` | `string` | required | yes | yes |",
		"| `labels` | `map(string)` | optional |  |  | Labels \\| with pipe. |",
		"| `id` | `string` | computed |",
		"```terraform\nresource \"docker_secret\" \"x\" {\n  name = \"x\"\n}\n```",
		"```shell\nterraform import docker_secret.x abc\n```",
	} {
		if !strings.Contains(ref, s) {
			t.Errorf("reference/docker_secret.md lacks %q\n%s", s, ref)
		}
	}
	svc := string(files["reference/docker_service.md"])
	for _, s := range []string{"### `endpoint_spec.ports`", "list of blocks (max 1).", "### `meta`", "| `replicas` | `number` | optional, computed |"} {
		if !strings.Contains(svc, s) {
			t.Errorf("reference/docker_service.md lacks %q\n%s", s, svc)
		}
	}

	rd := string(readme)
	for _, s := range []string{
		generatedMarker,
		"## 4. Swarm workload",
		"| `docker_secret` | resource | Manages a Swarm secret. | [reference/docker_secret.md](skills/x/reference/docker_secret.md) | [resources/secret.md](docs/resources/secret.md) |",
		"| `data.docker_secret` | data source | Manages a Swarm secret. | [reference/data.docker_secret.md](skills/x/reference/data.docker_secret.md) | [data-sources/secret.md](docs/data-sources/secret.md) |",
		"## Other",
		"[docs](docs/index.md)",
	} {
		if !strings.Contains(rd, s) {
			t.Errorf("README_resources.md lacks %q\n%s", s, rd)
		}
	}

	// Determinism.
	again, _, _, err := render(syntheticResponse(), renderOpts{Examples: ex})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range files {
		if !bytes.Equal(v, again[k]) {
			t.Errorf("%s not deterministic", k)
		}
	}
}

func TestWriteCheckAndStale(t *testing.T) {
	out := t.TempDir()
	files, _, _, err := render(syntheticResponse(), renderOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// A stale generated file and a hand-written file that must survive.
	os.MkdirAll(filepath.Join(out, "reference"), 0o755)
	os.WriteFile(filepath.Join(out, "reference", "docker_gone.md"), []byte("# x\n"+generatedMarker+"\n"), 0o644)
	os.WriteFile(filepath.Join(out, "reference", "notes.md"), []byte("hand written\n"), 0o644)

	diffs, err := checkFiles(out, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != len(files)+1 {
		t.Errorf("check before write: %v", diffs)
	}
	removed, err := writeFiles(out, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "reference/docker_gone.md" {
		t.Errorf("removed = %v", removed)
	}
	if _, err := os.Stat(filepath.Join(out, "reference", "notes.md")); err != nil {
		t.Error("hand-written file removed")
	}
	if diffs, _ := checkFiles(out, files); len(diffs) != 0 {
		t.Errorf("check after write: %v", diffs)
	}
	// CRLF on disk is not drift; content changes are.
	p := filepath.Join(out, "SKILL.md")
	os.WriteFile(p, bytes.ReplaceAll(files["SKILL.md"], []byte("\n"), []byte("\r\n")), 0o644)
	if diffs, _ := checkFiles(out, files); len(diffs) != 0 {
		t.Errorf("CRLF reported as drift: %v", diffs)
	}
	os.WriteFile(p, []byte("changed\n"), 0o644)
	if diffs, _ := checkFiles(out, files); len(diffs) != 1 {
		t.Errorf("edit not detected: %v", diffs)
	}
}

func TestSummary(t *testing.T) {
	for in, want := range map[string]string{
		"Image reference, e.g. `nginx:1`. Changing this forces a new resource.": "Image reference, e.g. `nginx:1`.",
		"One sentence only.":       "One sentence only.",
		"First. Second.":           "First.",
		"Uses i.e. this. And more": "Uses i.e. this.",
	} {
		if got := summary(in); got != want {
			t.Errorf("summary(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	in := "a  \r\n\r\n\r\n\r\nb\n```\nx\n\n\ny\n```\n\n\n"
	want := "a\n\nb\n```\nx\n\n\ny\n```\n"
	if got := string(normalize([]byte(in))); got != want {
		t.Errorf("normalize = %q, want %q", got, want)
	}
}

// TestGenerateRealProvider runs the generator against the real provider.
func TestGenerateRealProvider(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	o := options{
		Out:      filepath.Join(out, "skills", "terraform-provider-docker"),
		Examples: filepath.Join("..", "..", "examples"),
		Readme:   filepath.Join(out, "README_resources.md"),
		Docs:     filepath.Join(out, "docs"),
	}
	if err := run(context.Background(), o, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(o.Out, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("---\nname: terraform-provider-docker\ndescription: ")) {
		t.Errorf("SKILL.md frontmatter missing: %.120q", b)
	}
	if _, err := os.Stat(filepath.Join(o.Out, "reference", "provider.md")); err != nil {
		t.Errorf("reference/provider.md: %v", err)
	}
	rd, err := os.ReadFile(o.Readme)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rd, []byte("](skills/terraform-provider-docker/reference/docker_service.md)")) || !bytes.Contains(rd, []byte("](docs/resources/service.md)")) {
		t.Errorf("README_resources.md links wrong:\n%s", rd)
	}
	o.Check = true
	if err := run(context.Background(), o, &stdout, &stderr); err != nil {
		t.Errorf("check right after write: %v\n%s", err, stderr.String())
	}
}
