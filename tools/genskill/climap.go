package main

// The docker-cli-map skill: a table from docker / docker compose / docker
// swarm CLI commands to the provider type and arguments that replace them,
// rendered from the hand-maintained tools/climap/commands.json, checked
// against the live provider schema, and cross-referenced with the parity list
// in tools/parity/paritydata.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/JerryBalmer1/terraform-provider-docker/tools/parity/paritydata"
)

type cliMapFile struct {
	Comment  string       `json:"_comment"`
	Commands []cliCommand `json:"commands"`
}

type cliCommand struct {
	CLI   string      `json:"cli"`
	Group string      `json:"group"`
	Map   []cliTarget `json:"map"`
	None  string      `json:"none"`
	Note  string      `json:"note"`
}

type cliTarget struct {
	Kind  string   `json:"kind"`
	Type  string   `json:"type"`
	Attrs []string `json:"attrs"`
}

// cliGroups is the section order and heading of each group.
var cliGroups = []struct{ Key, Title string }{
	{"docker", "docker"},
	{"compose", "docker compose"},
	{"swarm", "docker swarm, node, service, stack, secret, config"},
}

func loadCLIMap(path string) (*cliMapFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f cliMapFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// cliSchema returns the schema block a target's attrs are resolved against,
// the function for kind function, and whether the type exists.
func cliSchema(resp *tfprotov6.GetProviderSchemaResponse, t cliTarget) (*tfprotov6.SchemaBlock, *tfprotov6.Function, bool) {
	block := func(s *tfprotov6.Schema, ok bool) (*tfprotov6.SchemaBlock, *tfprotov6.Function, bool) {
		if !ok || s == nil {
			return nil, nil, false
		}
		return s.Block, nil, true
	}
	switch t.Kind {
	case "resource":
		s, ok := resp.ResourceSchemas[t.Type]
		return block(s, ok)
	case "data":
		s, ok := resp.DataSourceSchemas[t.Type]
		return block(s, ok)
	case "ephemeral":
		s, ok := resp.EphemeralResourceSchemas[t.Type]
		return block(s, ok)
	case "list":
		s, ok := resp.ListResourceSchemas[t.Type]
		return block(s, ok)
	case "action":
		a, ok := resp.ActionSchemas[t.Type]
		if !ok || a == nil {
			return nil, nil, false
		}
		return block(a.Schema, true)
	case "provider":
		return block(resp.Provider, t.Type == "docker")
	case "function":
		f, ok := resp.Functions[t.Type]
		return nil, f, ok && f != nil
	}
	return nil, nil, false
}

// schemaHasPath reports whether a dotted attribute / block path exists.
func schemaHasPath(b *tfprotov6.SchemaBlock, path []string) bool {
	if b == nil || len(path) == 0 {
		return b != nil
	}
	for _, a := range b.Attributes {
		if a.Name != path[0] {
			continue
		}
		if len(path) == 1 {
			return true
		}
		if a.NestedType == nil {
			return false
		}
		return schemaHasPath(&tfprotov6.SchemaBlock{Attributes: a.NestedType.Attributes}, path[1:])
	}
	for _, nb := range b.BlockTypes {
		if nb.TypeName == path[0] {
			return schemaHasPath(nb.Block, path[1:])
		}
	}
	return false
}

func functionHasParam(f *tfprotov6.Function, name string) bool {
	for _, p := range f.Parameters {
		if p.Name == name {
			return true
		}
	}
	return f.VariadicParameter != nil && f.VariadicParameter.Name == name
}

// validateCLIMap returns one error string per command or target that does not
// match the provider schema or the file's own rules. repoRoot resolves tool
// paths.
func validateCLIMap(resp *tfprotov6.GetProviderSchemaResponse, f *cliMapFile, repoRoot string) []string {
	var errs []string
	groups := map[string]bool{}
	for _, g := range cliGroups {
		groups[g.Key] = true
	}
	seen := map[string]bool{}
	for _, c := range f.Commands {
		key := fmt.Sprintf("%q", c.CLI)
		switch {
		case c.CLI == "":
			errs = append(errs, "command with an empty cli")
		case !groups[c.Group]:
			errs = append(errs, fmt.Sprintf("%s: unknown group %q", key, c.Group))
		case (len(c.Map) == 0) == (c.None == ""):
			errs = append(errs, key+": needs exactly one of map and none")
		}
		if seen[c.CLI] {
			errs = append(errs, key+": duplicate")
		}
		seen[c.CLI] = true
		for _, t := range c.Map {
			tk := fmt.Sprintf("%s -> %s %s", key, t.Kind, t.Type)
			if t.Kind == "tool" {
				if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(t.Type))); err != nil {
					errs = append(errs, tk+": tool path not found")
				}
				continue
			}
			b, fn, ok := cliSchema(resp, t)
			if !ok {
				errs = append(errs, tk+": no such type in the provider schema")
				continue
			}
			for _, a := range t.Attrs {
				if fn != nil {
					if !functionHasParam(fn, a) {
						errs = append(errs, fmt.Sprintf("%s: function has no parameter %q", tk, a))
					}
				} else if !schemaHasPath(b, strings.Split(a, ".")) {
					errs = append(errs, fmt.Sprintf("%s: no attribute or block %q", tk, a))
				}
			}
		}
	}
	return errs
}

// cliAddress is how a target is written in Terraform, and cliRef the skill
// reference file documenting it (relative to the docker-cli-map skill).
func cliAddress(t cliTarget) string {
	switch t.Kind {
	case "resource":
		return t.Type
	case "data":
		return "data." + t.Type
	case "action":
		return "action." + t.Type
	case "ephemeral":
		return "ephemeral." + t.Type
	case "list":
		return "list." + t.Type
	case "function":
		return "provider::docker::" + t.Type
	case "provider":
		return `provider "docker"`
	}
	return t.Type
}

func cliRef(t cliTarget, skillRel string) string {
	var f string
	switch t.Kind {
	case "resource":
		f = t.Type
	case "data", "action", "ephemeral", "list", "function":
		f = map[string]string{"data": "data", "action": "action", "ephemeral": "ephemeral", "list": "list", "function": "function"}[t.Kind] + "." + t.Type
	case "provider":
		f = "provider"
	default:
		return ""
	}
	return skillRel + "/reference/" + f + ".md"
}

func cliEscape(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// renderCLIMap renders the docker-cli-map SKILL.md. skillRel is the main
// skill's directory relative to the output file's directory.
func renderCLIMap(f *cliMapFile, skillRel string) []byte {
	var b strings.Builder
	b.WriteString("---\nname: docker-cli-map\n")
	b.WriteString("description: Use before running, suggesting or translating any docker, docker compose or docker swarm CLI command for infrastructure this repo manages, or when writing Terraform for the JerryBalmer1/docker provider from CLI habits. Maps each command to the resource, data source, action, ephemeral resource, list resource, function or provider argument that replaces it, or says there is no equivalent and why.\n---\n\n")
	b.WriteString(generatedMarker + "\n\n")
	b.WriteString("# Docker CLI to provider map\n\n")
	b.WriteString("This provider replaces the docker CLI: it talks to the Engine API and never shells out. ")
	b.WriteString("When you are about to type a `docker ...` command against infrastructure Terraform manages, find it below and write the Terraform instead. ")
	b.WriteString("Running the CLI against managed objects makes state drift: the next plan undoes it or fails.\n\n")
	b.WriteString("- **Arguments** link to the reference file in the `terraform-provider-docker` skill; read it before choosing argument names.\n")
	b.WriteString("- **Actions** (`action.docker_*`) are for one-off operations: trigger them from `lifecycle { action_trigger { ... } }` or run `terraform apply -invoke=action.<type>.<name>`. They need Terraform >= 1.14.\n")
	b.WriteString("- **No equivalent** rows say what to do instead. `BLOCKED:` means the Engine API or the plugin framework cannot do it.\n")
	b.WriteString("- Read-only CLI use (`docker ps`, `docker logs`, `docker inspect`) against the harness or a dev daemon is fine; the map tells you the declarative form when the answer has to feed configuration.\n\n")
	b.WriteString("Source: `tools/climap/commands.json` (hand-maintained) + `tools/parity/paritydata` (types and kreuzwerker parity). Regenerate with `go run ./tools/genskill`; `-check` fails if this file is stale or names a type or argument the provider does not have.\n")

	for _, g := range cliGroups {
		fmt.Fprintf(&b, "\n## %s\n\n", g.Title)
		b.WriteString("| CLI | Use instead | Arguments | Notes |\n|---|---|---|---|\n")
		for _, c := range f.Commands {
			if c.Group != g.Key {
				continue
			}
			if c.None != "" {
				fmt.Fprintf(&b, "| `%s` | no equivalent | - | %s |\n", cliEscape(c.CLI), cliEscape(c.None))
				continue
			}
			var use, args []string
			for _, t := range c.Map {
				addr := "`" + cliEscape(cliAddress(t)) + "`"
				if ref := cliRef(t, skillRel); ref != "" {
					addr = "[" + addr + "](" + ref + ")"
				}
				use = append(use, addr)
				for _, a := range t.Attrs {
					args = append(args, "`"+a+"`")
				}
			}
			note := c.Note
			if note == "" {
				note = "-"
			}
			argCell := "-"
			if len(args) > 0 {
				argCell = strings.Join(args, ", ")
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", cliEscape(c.CLI), strings.Join(use, "<br>"), argCell, cliEscape(note))
		}
	}

	// Reverse index: every type tools/parity knows, its parity status and the
	// commands it replaces.
	byType := map[string][]string{}
	for _, c := range f.Commands {
		for _, t := range c.Map {
			k := parityKind(t.Kind) + " " + t.Type
			byType[k] = append(byType[k], "`"+cliEscape(c.CLI)+"`")
		}
	}
	b.WriteString("\n## Coverage by provider type\n\n")
	b.WriteString("Every type in `tools/parity/paritydata`: its status against kreuzwerker/docker v4.6.0 (`provider-only` when kreuzwerker has no such type) and the commands above it replaces.\n\n")
	b.WriteString("| Kind | Type | Parity | Replaces |\n|---|---|---|---|\n")
	type row struct{ kind, name, status string }
	var rows []row
	for _, e := range paritydata.Kreuzwerker {
		rows = append(rows, row{string(e.Kind), e.Name, string(e.Status)})
	}
	for _, x := range paritydata.Extras {
		rows = append(rows, row{string(x.Kind), x.Name, "provider-only"})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].kind != rows[j].kind {
			return kindOrder(rows[i].kind) < kindOrder(rows[j].kind)
		}
		return rows[i].name < rows[j].name
	})
	for _, r := range rows {
		cmds := byType[r.kind+" "+r.name]
		cell := "-"
		if len(cmds) > 0 {
			cell = strings.Join(cmds, ", ")
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s |\n", r.kind, r.name, r.status, cell)
	}
	return []byte(b.String())
}

// parityKind maps a commands.json kind to a paritydata.Kind string.
func parityKind(k string) string {
	switch k {
	case "data":
		return string(paritydata.DataSource)
	case "resource", "action", "ephemeral", "list", "function":
		return k
	}
	return k
}

func kindOrder(k string) int {
	for i, o := range []paritydata.Kind{paritydata.Resource, paritydata.DataSource, paritydata.Action, paritydata.Ephemeral, paritydata.List, paritydata.Function} {
		if string(o) == k {
			return i
		}
	}
	return 99
}

// cliMapWarnings lists provider types the parity list does not know, so a new
// type cannot land without a parity row (and with it a coverage row here).
func cliMapWarnings(resp *tfprotov6.GetProviderSchemaResponse) []string {
	known := map[string]bool{}
	for _, e := range paritydata.Kreuzwerker {
		known[string(e.Kind)+" "+e.Name] = true
	}
	for _, x := range paritydata.Extras {
		known[string(x.Kind)+" "+x.Name] = true
	}
	var out []string
	add := func(kind paritydata.Kind, names []string) {
		sort.Strings(names)
		for _, n := range names {
			if !known[string(kind)+" "+n] {
				out = append(out, fmt.Sprintf("%s %s is not in tools/parity/paritydata", kind, n))
			}
		}
	}
	keys := func(n int, each func(func(string))) []string {
		s := make([]string, 0, n)
		each(func(k string) { s = append(s, k) })
		return s
	}
	add(paritydata.Resource, keys(len(resp.ResourceSchemas), func(f func(string)) {
		for k := range resp.ResourceSchemas {
			f(k)
		}
	}))
	add(paritydata.DataSource, keys(len(resp.DataSourceSchemas), func(f func(string)) {
		for k := range resp.DataSourceSchemas {
			f(k)
		}
	}))
	add(paritydata.Action, keys(len(resp.ActionSchemas), func(f func(string)) {
		for k := range resp.ActionSchemas {
			f(k)
		}
	}))
	add(paritydata.Ephemeral, keys(len(resp.EphemeralResourceSchemas), func(f func(string)) {
		for k := range resp.EphemeralResourceSchemas {
			f(k)
		}
	}))
	add(paritydata.List, keys(len(resp.ListResourceSchemas), func(f func(string)) {
		for k := range resp.ListResourceSchemas {
			f(k)
		}
	}))
	add(paritydata.Function, keys(len(resp.Functions), func(f func(string)) {
		for k := range resp.Functions {
			f(k)
		}
	}))
	return out
}
