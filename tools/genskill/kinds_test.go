package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	aschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	eschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/list"
	lschema "github.com/hashicorp/terraform-plugin-framework/list/schema"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// ----- fakes for the Terraform 1.10+ kinds -----

// fakeAction is docker_exec with a configurable description so tests can
// break -strict. It lacks "service", which worked example 2 uses.
type fakeAction struct{ undescribed bool }

func (fakeAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_exec"
}

func (a fakeAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	cmdDesc := "Command and arguments."
	if a.undescribed {
		cmdDesc = ""
	}
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Runs a command in a container. Like `docker exec`.",
		Attributes: map[string]aschema.Attribute{
			"container": aschema.StringAttribute{Required: true, MarkdownDescription: "Container name or ID."},
			"command":   aschema.ListAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: cmdDesc},
		},
	}
}

func (fakeAction) Invoke(context.Context, action.InvokeRequest, *action.InvokeResponse) {}

type fakeEphemeral struct{}

func (fakeEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_token"
}

func (fakeEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = eschema.Schema{
		MarkdownDescription: "A short-lived registry token.",
		Attributes: map[string]eschema.Attribute{
			"registry": eschema.StringAttribute{Required: true, MarkdownDescription: "Registry host."},
			"token":    eschema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "The token."},
		},
	}
}

func (fakeEphemeral) Open(context.Context, ephemeral.OpenRequest, *ephemeral.OpenResponse) {}

type fakeFunction struct{}

func (fakeFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "parse_ref"
}

func (fakeFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary:     "Parses an image reference.",
		Description: "Parses an image reference into its parts.",
		Parameters: []function.Parameter{
			function.StringParameter{Name: "ref", Description: "Image reference."},
		},
		VariadicParameter: function.StringParameter{Name: "extra", Description: "Ignored extras.", AllowNullValue: true},
		Return:            function.StringReturn{},
	}
}

func (fakeFunction) Run(context.Context, function.RunRequest, *function.RunResponse) {}

// fakeList lists docker_widget (fakeResource), which the framework requires.
type fakeList struct{}

func (fakeList) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_widget"
}

func (fakeList) ListResourceConfigSchema(_ context.Context, _ list.ListResourceSchemaRequest, resp *list.ListResourceSchemaResponse) {
	resp.Schema = lschema.Schema{
		MarkdownDescription: "Lists widgets.",
		Attributes: map[string]lschema.Attribute{
			"name_prefix": lschema.StringAttribute{Optional: true, MarkdownDescription: "Only widgets whose name starts with this."},
		},
	}
}

func (fakeList) List(_ context.Context, _ list.ListRequest, stream *list.ListResultsStream) {
	stream.Results = func(func(list.ListResult) bool) {}
}

// fakeVault is a resource with a write-only argument pair.
type fakeVault struct{}

func (fakeVault) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vault"
}

func (fakeVault) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: "A vault.",
		Attributes: map[string]rschema.Attribute{
			"data_wo":         rschema.StringAttribute{Optional: true, WriteOnly: true, Sensitive: true, MarkdownDescription: "Secret data, never stored."},
			"data_wo_version": rschema.Int64Attribute{Optional: true, MarkdownDescription: "Bump to re-send data_wo."},
		},
	}
}

func (fakeVault) Create(context.Context, resource.CreateRequest, *resource.CreateResponse) {}
func (fakeVault) Read(context.Context, resource.ReadRequest, *resource.ReadResponse)       {}
func (fakeVault) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {}
func (fakeVault) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

// fakeFullProvider registers one of every kind.
type fakeFullProvider struct {
	fakeProvider
	undescribed bool
}

var (
	_ provider.ProviderWithActions            = fakeFullProvider{}
	_ provider.ProviderWithEphemeralResources = fakeFullProvider{}
	_ provider.ProviderWithFunctions          = fakeFullProvider{}
	_ provider.ProviderWithListResources      = fakeFullProvider{}
)

func (fakeFullProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		func() resource.Resource { return fakeResource{} },
		func() resource.Resource { return fakeVault{} },
	}
}

func (p fakeFullProvider) Actions(context.Context) []func() action.Action {
	return []func() action.Action{func() action.Action { return fakeAction{undescribed: p.undescribed} }}
}

func (fakeFullProvider) EphemeralResources(context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{func() ephemeral.EphemeralResource { return fakeEphemeral{} }}
}

func (fakeFullProvider) Functions(context.Context) []func() function.Function {
	return []func() function.Function{func() function.Function { return fakeFunction{} }}
}

func (fakeFullProvider) ListResources(context.Context) []func() list.ListResource {
	return []func() list.ListResource{func() list.ListResource { return fakeList{} }}
}

func fullSchema(t *testing.T, undescribed bool) *tfprotov6.GetProviderSchemaResponse {
	t.Helper()
	resp, err := loadSchema(context.Background(), func() provider.Provider { return fakeFullProvider{undescribed: undescribed} })
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// ----- tests -----

func TestNewKindsFromFakeProvider(t *testing.T) {
	resp := fullSchema(t, false)
	if len(resp.ActionSchemas) != 1 || len(resp.EphemeralResourceSchemas) != 1 || len(resp.Functions) != 1 || len(resp.ListResourceSchemas) != 1 {
		t.Fatalf("fake provider not walked: actions=%d ephemeral=%d functions=%d lists=%d",
			len(resp.ActionSchemas), len(resp.EphemeralResourceSchemas), len(resp.Functions), len(resp.ListResourceSchemas))
	}

	ex := t.TempDir()
	for rel, body := range map[string]string{
		"actions/docker_exec/action.tf":                                   "action \"docker_exec\" \"x\" {\r\n  config {}\r\n}\r\n",
		"ephemeral-resources/docker_registry_token/ephemeral-resource.tf": "ephemeral \"docker_registry_token\" \"x\" {}\n",
		"list-resources/docker_widget/list-resource.tfquery.hcl":          "list \"docker_widget\" \"all\" {\n  provider = docker\n}\n",
		"functions/parse_ref/function.tf":                                 "output \"r\" { value = provider::docker::parse_ref(\"nginx\") }\n",
	} {
		p := filepath.Join(ex, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, readme, warnings, err := render(resp, renderOpts{Examples: ex, ReadmeSkillDir: "skills/x", ReadmeDocsDir: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"reference/action.docker_exec.md", "reference/ephemeral.docker_registry_token.md",
		"reference/list.docker_widget.md", "reference/function.parse_ref.md",
		"reference/docker_widget.md", "reference/docker_vault.md",
	} {
		if _, ok := files[f]; !ok {
			t.Errorf("missing %s; have %v", f, sortedKeys(files))
		}
	}

	skill := string(files["SKILL.md"])
	for _, s := range []string{
		"This build registers 2 resources, 0 data sources,\n1 actions, 1 ephemeral resources, 1 list resources\nand 1 provider functions.",
		"## 4. Write-only arguments (`*_wo`)",
		"| `docker_vault` | `data_wo` | Secret data, never stored. |",
		"## 5. Actions",
		"- [`action.docker_exec`](reference/action.docker_exec.md): Runs a command in a container.",
		"action_trigger {",
		"terraform apply -invoke=action.docker_exec.migrate",
		"## 6. Ephemeral resources",
		"- [`ephemeral.docker_registry_token`](reference/ephemeral.docker_registry_token.md): A short-lived registry token.",
		"## 7. Provider functions",
		"| [`parse_ref`](reference/function.parse_ref.md) | `provider::docker::parse_ref(ref string, ...extra string) string` | Parses an image reference into its parts. |",
		"## 8. List resources and `terraform query`",
		"- [`list.docker_widget`](reference/list.docker_widget.md): Lists widgets.",
		"## 10. Worked example 2: compose file to stack, seeded and scaled with actions",
		"go run ./tools/compose2stack -in docker-compose.yml -out stack.tf -mode swarm",
		"terraform apply -invoke=action.docker_service_scale.web_peak",
		"`action.docker_exec.service` is not in the schema",
		"action `docker_service_scale` is not registered in this build",
		"resource `docker_stack` is not registered in this build",
		"- [`provider::docker::parse_ref`](reference/function.parse_ref.md): Parses an image reference into its parts.",
		"- Functions: [`provider::docker::parse_ref`](reference/function.parse_ref.md)",
	} {
		if !strings.Contains(skill, s) {
			t.Errorf("SKILL.md lacks %q", s)
		}
	}
	var w2 int
	for _, w := range warnings {
		if strings.HasPrefix(w, "worked example 2: ") {
			w2++
		}
	}
	if w2 == 0 {
		t.Errorf("no worked example 2 drift warnings: %v", warnings)
	}

	act := string(files["reference/action.docker_exec.md"])
	for _, s := range []string{
		"# action.docker_exec", "Kind: action.", "Terraform >= 1.14",
		"action \"docker_exec\" \"<name>\" {\n  config {",
		"| `command` | `list(string)` | required |",
		"```terraform\naction \"docker_exec\" \"x\" {\n  config {}\n}\n```",
	} {
		if !strings.Contains(act, s) {
			t.Errorf("action reference lacks %q\n%s", s, act)
		}
	}
	eph := string(files["reference/ephemeral.docker_registry_token.md"])
	for _, s := range []string{"Kind: ephemeral-resource.", "never written to\nplan or state", "| `token` | `string` | computed |  | yes |"} {
		if !strings.Contains(eph, s) {
			t.Errorf("ephemeral reference lacks %q\n%s", s, eph)
		}
	}
	lst := string(files["reference/list.docker_widget.md"])
	for _, s := range []string{"# list.docker_widget", "terraform query", "| `name_prefix` | `string` | optional |", "list \"docker_widget\" \"all\" {\n  provider = docker\n}"} {
		if !strings.Contains(lst, s) {
			t.Errorf("list reference lacks %q\n%s", s, lst)
		}
	}
	fn := string(files["reference/function.parse_ref.md"])
	for _, s := range []string{
		"# provider::docker::parse_ref", "Kind: function.",
		"provider::docker::parse_ref(ref string, ...extra string) string",
		"| 1 | `ref` | `string` |  | Image reference. |",
		"| variadic | `extra` | `string` | yes | Ignored extras. |",
		"Returns `string`.",
	} {
		if !strings.Contains(fn, s) {
			t.Errorf("function reference lacks %q\n%s", s, fn)
		}
	}
	if strings.Contains(fn, "## Arguments and attributes") {
		t.Error("function reference renders an attribute table")
	}
	vault := string(files["reference/docker_vault.md"])
	if !strings.Contains(vault, "| `data_wo` | `string` | optional, write-only |  | yes |") {
		t.Errorf("write-only not marked:\n%s", vault)
	}

	rd := string(readme)
	for _, s := range []string{
		"| 2 | 0 | 1 | 1 | 1 | 1 |",
		"| `action.docker_exec` | action | Runs a command in a container. | [reference/action.docker_exec.md](skills/x/reference/action.docker_exec.md) | [actions/exec.md](docs/actions/exec.md) |",
		"| `ephemeral.docker_registry_token` | ephemeral resource |",
		"[ephemeral-resources/registry_token.md](docs/ephemeral-resources/registry_token.md)",
		"| `list.docker_widget` | list resource | Lists widgets. |",
		"[list-resources/widget.md](docs/list-resources/widget.md)",
		"| `provider::docker::parse_ref` | function |",
		"[functions/parse_ref.md](docs/functions/parse_ref.md)",
		"## 2. Single-host primitives", // docker_exec
		"## Other",                     // docker_widget, docker_vault, parse_ref
	} {
		if !strings.Contains(rd, s) {
			t.Errorf("README_resources.md lacks %q\n%s", s, rd)
		}
	}
	if strings.Index(rd, "`action.docker_exec`") < strings.Index(rd, "## 2. Single-host primitives") {
		t.Error("docker_exec not grouped under layer 2")
	}
}

func TestDescriptionViolations(t *testing.T) {
	if v := descriptionViolations(fullSchema(t, false)); len(v) != 0 {
		t.Errorf("described fake provider has violations: %v", v)
	}
	got := descriptionViolations(fullSchema(t, true))
	if strings.Join(got, ",") != "action.docker_exec.command (attribute)" {
		t.Errorf("violations = %v", got)
	}

	// Synthetic gaps for every kind, including nested attributes, blocks and
	// function parameters.
	resp := &tfprotov6.GetProviderSchemaResponse{
		ActionSchemas: map[string]*tfprotov6.ActionSchema{
			"docker_a": {Schema: &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{Description: "A."}}},
			"docker_b": {Schema: &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{}}},
			"docker_c": nil,
		},
		EphemeralResourceSchemas: map[string]*tfprotov6.Schema{"docker_e": {Block: &tfprotov6.SchemaBlock{
			Description: "E.",
			Attributes: []*tfprotov6.SchemaAttribute{{Name: "o", Description: "O.", NestedType: &tfprotov6.SchemaObject{
				Attributes: []*tfprotov6.SchemaAttribute{{Name: "k"}},
			}}},
		}}},
		ListResourceSchemas: map[string]*tfprotov6.Schema{"docker_l": {Block: &tfprotov6.SchemaBlock{
			Description: "L.",
			BlockTypes:  []*tfprotov6.SchemaNestedBlock{{TypeName: "filter", Block: &tfprotov6.SchemaBlock{Attributes: []*tfprotov6.SchemaAttribute{{Name: "x", Description: "X."}}}}},
		}}},
		Functions: map[string]*tfprotov6.Function{
			"f": {Parameters: []*tfprotov6.FunctionParameter{{Name: "a", Description: "A."}, {Name: "b"}}, VariadicParameter: &tfprotov6.FunctionParameter{Name: "rest"}},
			"g": {Summary: "G."},
		},
	}
	want := []string{
		"action.docker_b (type)",
		"action.docker_c (no schema)",
		"ephemeral.docker_e.o.k (attribute)",
		"list.docker_l.filter (block)",
		"provider::docker::f (function summary/description)",
		"provider::docker::f(b) (parameter)",
		"provider::docker::f(rest) (parameter)",
	}
	if got := descriptionViolations(resp); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStrictFailsOnMissingDescriptions(t *testing.T) {
	for _, undescribed := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		o := options{Strict: true, Provider: func() provider.Provider { return fakeFullProvider{undescribed: undescribed} }}
		err := run(context.Background(), o, &stdout, &stderr)
		if undescribed {
			if !errors.Is(err, errStrict) || !strings.Contains(stderr.String(), "missing description: action.docker_exec.command (attribute)") {
				t.Errorf("strict did not fail on the undescribed action: err=%v\n%s", err, stderr.String())
			}
			continue
		}
		// fakeResource has deliberate RequiresReplace violations; only the
		// description part must be clean.
		if strings.Contains(stderr.String(), "missing description") {
			t.Errorf("unexpected description violations:\n%s", stderr.String())
		}
	}
}

func TestRunWritesNewKinds(t *testing.T) {
	out := t.TempDir()
	o := options{
		Out:      filepath.Join(out, "skill"),
		Readme:   filepath.Join(out, "README_resources.md"),
		Docs:     filepath.Join(out, "docs"),
		Provider: func() provider.Provider { return fakeFullProvider{} },
	}
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), o, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	for _, f := range []string{"action.docker_exec.md", "ephemeral.docker_registry_token.md", "list.docker_widget.md", "function.parse_ref.md"} {
		if _, err := os.Stat(filepath.Join(o.Out, "reference", f)); err != nil {
			t.Error(err)
		}
	}
	o.Check = true
	if err := run(context.Background(), o, &stdout, &stderr); err != nil {
		t.Errorf("check after write: %v\n%s", err, stderr.String())
	}
}

func TestTypeDocAddresses(t *testing.T) {
	for _, c := range []struct {
		kind, name, addr, file, page, label string
	}{
		{kindResource, "docker_x", "docker_x", "reference/docker_x.md", "resources/x.md", "resource"},
		{kindDataSource, "docker_x", "data.docker_x", "reference/data.docker_x.md", "data-sources/x.md", "data source"},
		{kindAction, "docker_x", "action.docker_x", "reference/action.docker_x.md", "actions/x.md", "action"},
		{kindEphemeral, "docker_x", "ephemeral.docker_x", "reference/ephemeral.docker_x.md", "ephemeral-resources/x.md", "ephemeral resource"},
		{kindList, "docker_x", "list.docker_x", "reference/list.docker_x.md", "list-resources/x.md", "list resource"},
		{kindFunction, "parse", "provider::docker::parse", "reference/function.parse.md", "functions/parse.md", "function"},
	} {
		td := typeDoc{Kind: c.kind, Name: c.name}
		if td.Address() != c.addr || td.File() != c.file || td.DocPage() != c.page || td.KindLabel() != c.label {
			t.Errorf("%s %s: got %s %s %s %s", c.kind, c.name, td.Address(), td.File(), td.DocPage(), td.KindLabel())
		}
	}
}
