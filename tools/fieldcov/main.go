// Command fieldcov is the field-level coverage gate: every writable upstream
// field (Engine API request bodies and query parameters, compose-spec
// properties, bake target attributes) must be implemented by a provider
// attribute, or carry a reasoned skip or partial.
//
//	go run ./tools/fieldcov                       report (gaps are listed, exit 0)
//	go run ./tools/fieldcov -enforce              exit 1 on any gap, bad overlay entry or stale fieldmap.json
//	go run ./tools/fieldcov -write                regenerate reference/_gen/fieldmap.json
//	go run ./tools/fieldcov -gaps DIR             write DIR/<lane>.json, the gaps per Pass 5.3 lane
//	go run ./tools/fieldcov -v                    list every gap in the report
//	go run ./tools/fieldcov -summary FILE         write the counts as JSON
//
// Mapping, in order (the first that applies wins):
//
//  1. A manual entry in reference/_gen/fieldmap.d/<lane>.json:
//     {"item": "...", "attr": "..."} | {"skip": "reason"} | {"partial": "what is missing", "item": "..."}.
//     Each lane owns its own overlay file; only lane overlays (not 0.json)
//     may skip, and a skip needs a real reason (at least 20 characters).
//  2. auto-code: the provider writes the field (engine: a moby API or client
//     struct field set in non-test code; compose: a compose-go field read by
//     the compose conversion outside its *Unsupported warnings; bake: an
//     attribute the bake parser switches on).
//  3. auto-schema: an input attribute of the owning item's Terraform type ends
//     in the field's path (see schemaMatch).
//
// Anything else is a gap, filed against the lane that owns the item.
// reference/_gen/fieldmap.json is generated output: never hand-edit it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// entry is one fieldmap.json value.
type entry struct {
	Item    string `json:"item,omitempty"`
	Attr    string `json:"attr,omitempty"`
	Skip    string `json:"skip,omitempty"`
	Partial string `json:"partial,omitempty"`
	// How: manual:<lane> | auto-code | auto-schema | gap.
	How string `json:"how"`
	// Code is where the provider writes or reads the field (auto-code).
	Code string `json:"code,omitempty"`
	// Lane is the Pass 5.3 lane that owns the item (gaps and skips).
	Lane string `json:"lane,omitempty"`
}

type overlayFile struct {
	About  string           `json:"about,omitempty"`
	Fields map[string]entry `json:"fields"`
}

type result struct {
	Fields  []field
	Map     map[string]entry
	Errors  []string
	Overlay map[string]string // pointer -> overlay file
}

func repoRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "reference", "inventory.json")); err == nil {
				return d, nil
			}
		}
		p := filepath.Dir(d)
		if p == d {
			return "", errors.New("run from inside the terraform-provider-docker repository")
		}
		d = p
	}
}

func main() {
	enforce := flag.Bool("enforce", false, "fail on any gap, invalid overlay entry or stale fieldmap.json")
	write := flag.Bool("write", false, "regenerate reference/_gen/fieldmap.json")
	gapsDir := flag.String("gaps", "", "write the gaps per lane to DIR/<lane>.json")
	verbose := flag.Bool("v", false, "list every gap")
	summary := flag.String("summary", "", "write the counts as JSON to FILE")
	flag.Parse()
	repo, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fieldcov:", err)
		os.Exit(2)
	}
	res, err := run(context.Background(), repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fieldcov:", err)
		os.Exit(2)
	}
	sum := summarize(res)
	printReport(os.Stdout, res, sum, *verbose)
	mapPath := filepath.Join(repo, "reference", "_gen", "fieldmap.json")
	gen, err := renderMap(res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fieldcov:", err)
		os.Exit(2)
	}
	if *write {
		if err := os.WriteFile(mapPath, gen, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "fieldcov:", err)
			os.Exit(2)
		}
		fmt.Println("fieldcov: wrote reference/_gen/fieldmap.json")
	}
	if *gapsDir != "" {
		if err := writeGaps(*gapsDir, res); err != nil {
			fmt.Fprintln(os.Stderr, "fieldcov:", err)
			os.Exit(2)
		}
	}
	if *summary != "" {
		b, _ := json.MarshalIndent(sum, "", "  ")
		if err := os.WriteFile(*summary, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "fieldcov:", err)
			os.Exit(2)
		}
	}
	if *enforce {
		failed := false
		if len(res.Errors) > 0 {
			failed = true
		}
		if n := sum.Total.Gap; n > 0 {
			fmt.Printf("fieldcov: ENFORCE: %d unmapped field(s)\n", n)
			failed = true
		}
		cur, err := os.ReadFile(mapPath)
		if err != nil || normalizeEOL(string(cur)) != string(gen) {
			fmt.Println("fieldcov: ENFORCE: reference/_gen/fieldmap.json is stale; run go run ./tools/fieldcov -write")
			failed = true
		}
		if failed {
			os.Exit(1)
		}
		fmt.Println("fieldcov: ENFORCE ok")
	}
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func normalizeEOL(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func run(ctx context.Context, repo string) (*result, error) {
	items, err := loadInventory(filepath.Join(repo, "reference", "inventory.json"))
	if err != nil {
		return nil, err
	}
	byID := map[string]invItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	sw, err := loadSwagger(filepath.Join(repo, "reference", "upstream", "engine-api", "api", "swagger.yaml"))
	if err != nil {
		return nil, err
	}
	scope := engineScope(items)
	ops := make([]string, 0, len(scope))
	opInfo := map[string]opOwner{}
	for _, o := range scope {
		ops = append(ops, o.Op)
		opInfo[o.Op] = o
	}
	eng, err := engineFields(sw, ops)
	if err != nil {
		return nil, err
	}
	cmp, err := composeFields(filepath.Join(repo, "reference", "upstream", "compose-spec", "schema", "compose-spec.json"))
	if err != nil {
		return nil, err
	}
	bake, err := bakeFields(filepath.Join(repo, "reference", "upstream", "buildx", "docs", "bake-reference.md"))
	if err != nil {
		return nil, err
	}
	ip, err := loadInputPaths(ctx)
	if err != nil {
		return nil, err
	}
	cf, err := loadCodeFacts(repo)
	if err != nil {
		return nil, err
	}
	cl, err := loadComposeLookup(repo)
	if err != nil {
		return nil, err
	}

	res := &result{Map: map[string]entry{}, Overlay: map[string]string{}}
	res.Fields = append(append(append(res.Fields, eng...), cmp...), bake...)

	// Automatic mapping.
	for _, f := range eng {
		owner := opInfo[f.Ops[0]].Owner
		var tfTypes []string
		seen := map[string]bool{}
		for _, op := range f.Ops {
			for _, it := range append([]invItem{opInfo[op].Owner}, opInfo[op].Items...) {
				if t := it.tfType(); !seen[t] {
					seen[t] = true
					tfTypes = append(tfTypes, t)
				}
			}
		}
		tfType, attr := schemaMatch(ip, tfTypes, f.Segs)
		item := owner.ID
		if tfType != "" {
			for _, op := range f.Ops {
				for _, it := range opInfo[op].Items {
					if it.tfType() == tfType {
						item = it.ID
					}
				}
			}
		}
		var itemIDs []string
		for _, op := range f.Ops {
			for _, it := range opInfo[op].Items {
				itemIDs = append(itemIDs, it.ID)
			}
		}
		if g := cf.codeMatch(f, itemIDs); g != nil {
			e := entry{Item: item, How: "auto-code", Code: g.PkgName + "." + g.Type + "." + g.Field + " @ " + strings.Join(firstN(creditPos(g, itemIDs), 3), ", ")}
			e.Attr = attr
			if e.Attr == "" {
				e.Attr = "(provider-set)"
			}
			if tfType == "" {
				e.Item = owner.ID
			}
			res.Map[f.Pointer] = e
			continue
		}
		if attr != "" {
			res.Map[f.Pointer] = entry{Item: item, Attr: attr, How: "auto-schema"}
			continue
		}
		res.Map[f.Pointer] = entry{Item: owner.ID, How: "gap", Lane: laneOf(owner.ID)}
	}
	for i, f := range cmp {
		item := composeItem(f.Context)
		chain, err := cl.resolve(f.Context, f.Segs)
		if err != nil {
			cmp[i].Doc = strings.TrimSpace(cmp[i].Doc + " [compose-go: " + err.Error() + "]")
			res.Map[f.Pointer] = entry{Item: item, How: "gap", Lane: laneOf(item)}
			continue
		}
		leaf := chain[len(chain)-1]
		if g, ok := cf.ComposeRead[leaf.key()]; ok {
			res.Map[f.Pointer] = entry{Item: item, Attr: "content", How: "auto-code",
				Code: "types." + leaf.Type + "." + leaf.Field + " @ " + strings.Join(firstN(g.Pos, 3), ", ")}
			continue
		}
		res.Map[f.Pointer] = entry{Item: item, How: "gap", Lane: laneOf(item),
			Code: "types." + leaf.Type + "." + leaf.Field + " (not read)"}
	}
	res.Fields = append(append(append([]field{}, eng...), cmp...), bake...)
	for _, f := range bake {
		name := f.Segs[0]
		if pos, ok := cf.BakeCases[name]; ok {
			res.Map[f.Pointer] = entry{Item: "buildx:bake", Attr: "target." + name, How: "auto-code", Code: pos}
			continue
		}
		res.Map[f.Pointer] = entry{Item: "buildx:bake", How: "gap", Lane: "B"}
	}

	// Manual overlays.
	if err := applyOverlays(repo, res, byID); err != nil {
		return nil, err
	}
	return res, nil
}

func applyOverlays(repo string, res *result, byID map[string]invItem) error {
	dir := filepath.Join(repo, "reference", "_gen", "fieldmap.d")
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, f := range res.Fields {
		known[f.Pointer] = true
	}
	for _, de := range ents {
		if de.IsDir() || filepath.Ext(de.Name()) != ".json" {
			continue
		}
		lane := strings.TrimSuffix(de.Name(), ".json")
		b, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			return err
		}
		var of overlayFile
		if err := json.Unmarshal(b, &of); err != nil {
			return fmt.Errorf("fieldmap.d/%s: %w", de.Name(), err)
		}
		ptrs := make([]string, 0, len(of.Fields))
		for p := range of.Fields {
			ptrs = append(ptrs, p)
		}
		sort.Strings(ptrs)
		for _, p := range ptrs {
			e := of.Fields[p]
			bad := func(msg string) {
				res.Errors = append(res.Errors, fmt.Sprintf("fieldmap.d/%s: %s: %s", de.Name(), p, msg))
			}
			if !known[p] {
				bad("no such upstream field (stale pointer)")
				continue
			}
			if prev, dup := res.Overlay[p]; dup {
				bad("also mapped in fieldmap.d/" + prev)
				continue
			}
			n := 0
			for _, s := range []string{e.Attr, e.Skip, e.Partial} {
				if s != "" {
					n++
				}
			}
			if n != 1 {
				bad("exactly one of attr, skip, partial must be set")
				continue
			}
			switch {
			case e.Skip != "":
				if lane == "0" {
					bad("Lane 0 may not skip fields")
					continue
				}
				if len(strings.TrimSpace(e.Skip)) < 20 {
					bad("a skip needs a real reason (at least 20 characters)")
					continue
				}
			case e.Attr != "" || e.Partial != "":
				if e.Item == "" {
					bad("item is required with attr or partial")
					continue
				}
				if _, ok := byID[e.Item]; !ok {
					bad("unknown inventory item " + e.Item)
					continue
				}
			}
			e.How = "manual:" + lane
			e.Lane = lane
			if e.Item == "" {
				e.Item = res.Map[p].Item
			}
			res.Map[p] = e
			res.Overlay[p] = de.Name()
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Output

type counts struct {
	Total      int `json:"total"`
	Mapped     int `json:"mapped"`
	AutoCode   int `json:"auto_code"`
	AutoSchema int `json:"auto_schema"`
	Manual     int `json:"manual"`
	Skipped    int `json:"skipped"`
	Partial    int `json:"partial"`
	Gap        int `json:"gap"`
}

type summaryOut struct {
	BySource map[string]*counts `json:"by_source"`
	Total    counts             `json:"total"`
	GapsLane map[string]int     `json:"gaps_by_lane"`
	Skips    map[string]string  `json:"skips"`
	Partials map[string]string  `json:"partials"`
	Errors   []string           `json:"errors,omitempty"`
}

func summarize(res *result) summaryOut {
	s := summaryOut{BySource: map[string]*counts{}, GapsLane: map[string]int{}, Skips: map[string]string{},
		Partials: map[string]string{}, Errors: res.Errors}
	for _, f := range res.Fields {
		c := s.BySource[f.Source]
		if c == nil {
			c = &counts{}
			s.BySource[f.Source] = c
		}
		e := res.Map[f.Pointer]
		for _, x := range []*counts{c, &s.Total} {
			x.Total++
			switch {
			case e.Skip != "":
				x.Skipped++
			case e.Partial != "":
				x.Partial++
			case e.How == "gap":
				x.Gap++
			default:
				x.Mapped++
				switch {
				case e.How == "auto-code":
					x.AutoCode++
				case e.How == "auto-schema":
					x.AutoSchema++
				default:
					x.Manual++
				}
			}
		}
		switch {
		case e.Skip != "":
			s.Skips[f.Pointer] = "[" + e.Lane + "] " + e.Skip
		case e.Partial != "":
			s.Partials[f.Pointer] = "[" + e.Lane + "] " + e.Item + ": " + e.Partial
		case e.How == "gap":
			s.GapsLane[e.Lane]++
		}
	}
	return s
}

func printReport(w *os.File, res *result, s summaryOut, verbose bool) {
	mode := "REPORT"
	for _, a := range os.Args[1:] {
		if a == "-enforce" || a == "--enforce" {
			mode = "ENFORCE"
		}
	}
	fmt.Fprintf(w, "fieldcov (%s): writable upstream fields vs provider\n\n", mode)
	fmt.Fprintf(w, "%-8s %6s %6s %6s %6s %6s %6s %6s %6s\n", "source", "total", "mapped", "code", "schema", "manual", "skip", "partial", "gap")
	srcs := []string{"engine", "compose", "bake"}
	for _, k := range srcs {
		c := s.BySource[k]
		if c == nil {
			c = &counts{}
		}
		fmt.Fprintf(w, "%-8s %6d %6d %6d %6d %6d %6d %6d %6d\n", k, c.Total, c.Mapped, c.AutoCode, c.AutoSchema, c.Manual, c.Skipped, c.Partial, c.Gap)
	}
	t := s.Total
	fmt.Fprintf(w, "%-8s %6d %6d %6d %6d %6d %6d %6d %6d\n\n", "total", t.Total, t.Mapped, t.AutoCode, t.AutoSchema, t.Manual, t.Skipped, t.Partial, t.Gap)
	lanes := make([]string, 0, len(s.GapsLane))
	for l := range s.GapsLane {
		lanes = append(lanes, l)
	}
	sort.Strings(lanes)
	for _, l := range lanes {
		fmt.Fprintf(w, "gaps lane %s: %d\n", l, s.GapsLane[l])
	}
	if len(s.Skips) > 0 {
		fmt.Fprintf(w, "\nskips (%d):\n", len(s.Skips))
		for _, p := range sortedKeys(s.Skips) {
			fmt.Fprintf(w, "  %s: %s\n", p, s.Skips[p])
		}
	}
	if len(s.Partials) > 0 {
		fmt.Fprintf(w, "\npartials (%d):\n", len(s.Partials))
		for _, p := range sortedKeys(s.Partials) {
			fmt.Fprintf(w, "  %s: %s\n", p, s.Partials[p])
		}
	}
	if verbose {
		fmt.Fprintln(w, "\ngaps:")
		for _, f := range res.Fields {
			if e := res.Map[f.Pointer]; e.How == "gap" {
				fmt.Fprintf(w, "  [%s] %s (%s)\n", e.Lane, f.Pointer, e.Item)
			}
		}
	}
	for _, e := range res.Errors {
		fmt.Fprintln(w, "ERROR", e)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// codeLineRe matches the ":<line>" of a "file.go:<line>" position.
var codeLineRe = regexp.MustCompile(`(\.go):\d+`)

func renderMap(res *result) ([]byte, error) {
	type out struct {
		About  string           `json:"about"`
		Fields map[string]entry `json:"fields"`
	}
	// Code positions keep the file but not the line: fieldmap.json is checked
	// in and enforced, and line numbers would make it stale on every
	// unrelated edit.
	fields := make(map[string]entry, len(res.Map))
	for k, e := range res.Map {
		e.Code = codeLineRe.ReplaceAllString(e.Code, "$1")
		fields[k] = e
	}
	o := out{
		About: "Generated by go run ./tools/fieldcov -write: every writable upstream field (Engine API v1.56 request " +
			"bodies and query parameters of the write operations reference/inventory.json maps to a resource, action " +
			"or ephemeral resource; compose-spec service/network/volume/config/secret/model/include properties; bake " +
			"target attributes) -> {item, attr} | {skip} | {partial} | gap. Do not hand-edit: lanes add manual " +
			"entries to reference/_gen/fieldmap.d/<lane>.json.",
		Fields: fields,
	}
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type gapEntry struct {
	Pointer    string   `json:"pointer"`
	Item       string   `json:"item"`
	Ops        []string `json:"ops,omitempty"`
	Context    string   `json:"context,omitempty"`
	Type       string   `json:"type,omitempty"`
	Deprecated bool     `json:"deprecated,omitempty"`
	Doc        string   `json:"doc,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func writeGaps(dir string, res *result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	by := map[string][]gapEntry{}
	for _, f := range res.Fields {
		e := res.Map[f.Pointer]
		if e.How != "gap" {
			continue
		}
		by[e.Lane] = append(by[e.Lane], gapEntry{Pointer: f.Pointer, Item: e.Item, Ops: f.Ops, Context: f.Context,
			Type: f.Type, Deprecated: f.Deprecated, Doc: f.Doc, Note: e.Code})
	}
	for _, l := range []string{"A", "R", "S", "K", "B"} {
		g := by[l]
		if g == nil {
			g = []gapEntry{}
		}
		doc := map[string]any{
			"lane": l,
			"about": "Pass 5.3 Lane 0: the fields go run ./tools/fieldcov found unmapped on items lane " + l + " owns. " +
				"Close each one in reference/_gen/fieldmap.d/" + l + ".json (attr after implementing it under the upstream " +
				"name, or skip / partial with a real reason), or by implementing it so auto-mapping finds it.",
			"count": len(g),
			"gaps":  g,
		}
		b, _ := json.MarshalIndent(doc, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, l+".json"), append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}
