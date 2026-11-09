package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/JerryBalmer1/terraform-provider-docker/internal/provider"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// TestConvertGolden converts every testdata/<case>/compose.yml in both modes
// and compares with <mode>.tf.golden; the output must also parse as HCL and
// fit the provider's resource schemas.
func TestConvertGolden(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("testdata", "*", "compose.yml"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	schemas := providerSchemas(t)
	for _, in := range cases {
		for _, mode := range []string{modeSwarm, modeLocal} {
			dir := filepath.Dir(in)
			t.Run(filepath.Base(dir)+"/"+mode, func(t *testing.T) {
				got, err := Convert(context.Background(), filepath.ToSlash(in), Options{Mode: mode})
				if err != nil {
					t.Fatal(err)
				}
				golden := filepath.Join(dir, mode+".tf.golden")
				if *update {
					if err := os.WriteFile(golden, got, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v (run go test ./tools/compose2stack -update)", err)
				}
				if norm(string(got)) != norm(string(want)) {
					t.Errorf("output differs from %s (rerun with -update to accept):\n%s", golden, got)
				}
				validateHCL(t, got, schemas)
			})
		}
	}
}

func norm(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func TestConvertSpecifics(t *testing.T) {
	in := filepath.Join("testdata", "webapp", "compose.yml")
	swarm, err := Convert(context.Background(), in, Options{Mode: modeSwarm})
	if err != nil {
		t.Fatal(err)
	}
	s := string(swarm)
	for _, want := range []string{
		"# converted from testdata/webapp/compose.yml",
		`resource "docker_stack" "webapp"`,
		`replicas = 3`,
		`constraints = ["node.role==worker"]`,
		`preferences = ["spread=node.labels.zone"]`,
		`data = filebase64("${path.module}/secrets/db_password.txt")`,
		`data = base64encode(var.API_KEY)`,
		`data = base64encode("welcome")`,
		`variable "API_KEY"`,
		`# TODO: build (context`,
		`# TODO: depends_on [cache db]`,
		`# TODO: service debug is not converted`,
		`file_name = "api.key"`,
		`mode      = 256`,
		`memory_bytes = 134217728`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("swarm output lacks %q", want)
		}
	}
	local, err := Convert(context.Background(), in, Options{Mode: modeLocal, Profiles: []string{"debug"}})
	if err != nil {
		t.Fatal(err)
	}
	l := string(local)
	for _, want := range []string{
		`resource "docker_container" "web"`,
		`count = 3`,
		`name  = "webapp-web-${count.index + 1}"`,
		`depends_on = [docker_container.cache, docker_container.db]`,
		`source = docker_volume.pgdata.name`,
		`(docker_network.back.name) = {`,
		`# TODO: network_mode host`,
		`# TODO: secret api_key (from environment API_KEY) cannot be mounted`,
		`source    = abspath("${path.module}/secrets/db_password.txt")`,
	} {
		if !strings.Contains(l, want) {
			t.Errorf("local output lacks %q", want)
		}
	}
	if strings.Contains(l, "service debug is not converted") {
		t.Error("debug profile was enabled")
	}
	if _, err := Convert(context.Background(), in, Options{Mode: "k8s"}); err == nil {
		t.Error("bad mode accepted")
	}
	if _, err := Convert(context.Background(), filepath.Join("testdata", "nope.yml"), Options{}); err == nil {
		t.Error("missing file accepted")
	}
}

// ---------------------------------------------------------------------------
// schema validation

type blockSchema struct {
	attrs    map[string]schema.Attribute
	blocks   map[string]schema.Block
	required []string
}

func newBlockSchema(attrs map[string]schema.Attribute, blocks map[string]schema.Block) blockSchema {
	b := blockSchema{attrs: attrs, blocks: blocks}
	for k, a := range attrs {
		if a.IsRequired() {
			b.required = append(b.required, k)
		}
	}
	sort.Strings(b.required)
	return b
}

func nestedSchema(b schema.Block) blockSchema {
	switch bb := b.(type) {
	case schema.ListNestedBlock:
		return newBlockSchema(bb.NestedObject.Attributes, bb.NestedObject.Blocks)
	case schema.SetNestedBlock:
		return newBlockSchema(bb.NestedObject.Attributes, bb.NestedObject.Blocks)
	case schema.SingleNestedBlock:
		return newBlockSchema(bb.Attributes, bb.Blocks)
	}
	return blockSchema{}
}

func providerSchemas(t *testing.T) map[string]blockSchema {
	t.Helper()
	ctx := context.Background()
	out := map[string]blockSchema{}
	for _, f := range provider.New("test")().Resources(ctx) {
		r := f()
		md := &resource.MetadataResponse{}
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "docker"}, md)
		sr := &resource.SchemaResponse{}
		r.Schema(ctx, resource.SchemaRequest{}, sr)
		out[md.TypeName] = newBlockSchema(sr.Schema.Attributes, sr.Schema.Blocks)
	}
	return out
}

func validateHCL(t *testing.T, src []byte, schemas map[string]blockSchema) {
	t.Helper()
	f, diags := hclsyntax.ParseConfig(src, "out.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("output is not valid HCL: %v", diags)
	}
	body := f.Body.(*hclsyntax.Body)
	resources := 0
	for _, blk := range body.Blocks {
		switch blk.Type {
		case "resource":
			s, ok := schemas[blk.Labels[0]]
			if !ok {
				t.Errorf("unknown resource type %s", blk.Labels[0])
				continue
			}
			resources++
			checkBody(t, blk.Labels[0]+"."+blk.Labels[1], blk.Body, s, true)
		case "variable":
		default:
			t.Errorf("unexpected top-level block %s", blk.Type)
		}
	}
	if resources == 0 {
		t.Error("no resources emitted")
	}
}

func checkBody(t *testing.T, where string, b *hclsyntax.Body, s blockSchema, top bool) {
	t.Helper()
	for name, a := range b.Attributes {
		if top && (name == "count" || name == "depends_on") {
			continue
		}
		sa, ok := s.attrs[name]
		if !ok {
			t.Errorf("%s: attribute %q is not in the schema", where, name)
			continue
		}
		if !sa.IsRequired() && !sa.IsOptional() {
			t.Errorf("%s: attribute %q is computed only", where, name)
		}
		if want, got := attrShape(sa), exprShape(a.Expr); want != "" && got != "" && want != got {
			t.Errorf("%s: attribute %q is a %s in the schema, the output writes a %s", where, name, want, got)
		}
	}
	for _, r := range s.required {
		if _, ok := b.Attributes[r]; !ok {
			t.Errorf("%s: required attribute %q missing", where, r)
		}
	}
	counts := map[string]int{}
	for _, blk := range b.Blocks {
		sb, ok := s.blocks[blk.Type]
		if !ok {
			t.Errorf("%s: block %q is not in the schema", where, blk.Type)
			continue
		}
		counts[blk.Type]++
		if _, single := sb.(schema.SingleNestedBlock); single && counts[blk.Type] > 1 {
			t.Errorf("%s: single block %q repeated", where, blk.Type)
		}
		checkBody(t, fmt.Sprintf("%s.%s", where, blk.Type), blk.Body, nestedSchema(sb), false)
	}
}

// attrShape is "list" for list/set attributes and "object" for map/object
// ones ("" for scalars).
func attrShape(a schema.Attribute) string {
	switch a.(type) {
	case schema.ListAttribute, schema.SetAttribute, schema.ListNestedAttribute, schema.SetNestedAttribute:
		return "list"
	case schema.MapAttribute, schema.MapNestedAttribute, schema.SingleNestedAttribute, schema.ObjectAttribute:
		return "object"
	}
	return ""
}

// exprShape is the literal shape of an expression ("" when it is not a
// tuple or object literal, e.g. a reference or a function call).
func exprShape(e hclsyntax.Expression) string {
	switch e.(type) {
	case *hclsyntax.TupleConsExpr:
		return "list"
	case *hclsyntax.ObjectConsExpr:
		return "object"
	}
	return ""
}
