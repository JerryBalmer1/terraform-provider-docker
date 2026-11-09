package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefinedFuncs returns the top-level function names declared in the non-test,
// non-generated Go files of dir.
func DefinedFuncs(dir string) (map[string]bool, error) {
	fset := token.NewFileSet()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == "registry_gen.go" {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				out[fd.Name.Name] = true
			}
		}
	}
	return out, nil
}

// stubHelper is the generic stub constructor in stub_pass5.go per kind.
var stubHelper = map[string]string{
	"resource":           "newPass5StubResource",
	"data_source":        "newPass5StubDataSource",
	"action":             "newPass5StubAction",
	"ephemeral_resource": "newPass5StubEphemeralResource",
}

// WriteStubs writes a stub constructor for every type whose constructor dir
// does not define, into stubs_pass5_<lane>.go (lane from lanesPath, keyed by
// item id). Each stub carries one "STUB(pass5) <item id>" marker line per
// item of the type; merge.py reads them to compute status. Existing stub
// files are appended to. It returns the number of stubs written.
func WriteStubs(types []Type, dir, lanesPath string) (int, error) {
	b, err := os.ReadFile(lanesPath)
	if err != nil {
		return 0, err
	}
	var lanes map[string]string
	if err := json.Unmarshal(b, &lanes); err != nil {
		return 0, fmt.Errorf("%s: %w", lanesPath, err)
	}
	have, err := DefinedFuncs(dir)
	if err != nil {
		return 0, err
	}
	byLane := map[string][]Type{}
	for _, t := range types {
		if have[Constructor(t)] {
			continue
		}
		if stubHelper[t.Kind] == "" {
			return 0, fmt.Errorf("%s %s: no stub helper for kind %s (list resources and functions need a real constructor)", t.Kind, t.Name, t.Kind)
		}
		lane := lanes[t.Items[0].ID]
		if lane == "" {
			return 0, fmt.Errorf("%s: no lane in %s", t.Items[0].ID, lanesPath)
		}
		byLane[lane] = append(byLane[lane], t)
	}
	keys := make([]string, 0, len(byLane))
	for k := range byLane {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 0
	for _, lane := range keys {
		path := filepath.Join(dir, "stubs_pass5_"+strings.ToLower(lane)+".go")
		var buf bytes.Buffer
		if old, err := os.ReadFile(path); err == nil {
			buf.Write(bytes.TrimRight(old, "\n"))
			buf.WriteString("\n")
		} else {
			fmt.Fprintf(&buf, "package provider\n\n// Pass 5 Lane %s owns this file: stub constructors written by tools/registrygen -stubs\n"+
				"// for types the inventory registers but nobody has implemented yet. Replace each stub with the real\n"+
				"// constructor (same name, in your own file) and delete it here together with its marker lines; delete\n"+
				"// the file when it is empty. Invoke-Build StubReport lists what is left.\n\nimport (\n"+
				"\t\"github.com/hashicorp/terraform-plugin-framework/action\"\n"+
				"\t\"github.com/hashicorp/terraform-plugin-framework/datasource\"\n"+
				"\t\"github.com/hashicorp/terraform-plugin-framework/ephemeral\"\n"+
				"\t\"github.com/hashicorp/terraform-plugin-framework/resource\"\n)\n\n"+
				"var (\n\t_ action.Action\n\t_ datasource.DataSource\n\t_ ephemeral.EphemeralResource\n\t_ resource.Resource\n)\n", lane)
		}
		for _, t := range byLane[lane] {
			buf.WriteString("\n")
			for _, it := range t.Items {
				fmt.Fprintf(&buf, "// STUB(pass5) %s\n", it.ID)
			}
			summary := strings.TrimSpace(t.Items[0].Summary)
			fmt.Fprintf(&buf, "func %s() %s {\n\treturn %s(%q, %q, %q)\n}\n",
				Constructor(t), kinds[t.Kind].iface, stubHelper[t.Kind], TypeSuffix(t), t.Items[0].ID, summary)
			n++
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			return n, fmt.Errorf("formatting %s: %w", path, err)
		}
		if err := os.WriteFile(path, src, 0o644); err != nil {
			return n, err
		}
	}
	return n, nil
}
