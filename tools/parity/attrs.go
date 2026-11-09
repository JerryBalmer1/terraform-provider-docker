package main

// Attribute-level parity: every kreuzwerker/docker v4.6.0 attribute must
// appear in reference/_gen/renames.json, either mapped (our_attr set) or
// dropped (our_attr null) with a real reason. A dropped row whose reason
// starts with "gap:" is open work and fails the check; an attribute we
// deliberately do not implement carries a reason starting "won't implement:"
// (or any other non-gap reason).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JerryBalmer1/terraform-provider-docker/tools/parity/paritydata"
)

// RenamesDoc is the part of reference/_gen/renames.json this tool reads.
type RenamesDoc struct {
	Kreuzwerker struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	} `json:"kreuzwerker"`
	TypeRenames []TypeRename `json:"type_renames"`
	Renames     []RenameRow  `json:"renames"`
}

// TypeRename is one renames.json type_renames entry.
type TypeRename struct {
	KreuzwerkerType string `json:"kreuzwerker_type"`
	PreviousType    string `json:"previous_type"`
	OurType         string `json:"our_type"`
}

// RenameRow is one renames.json row.
type RenameRow struct {
	Type            string  `json:"type"`
	KreuzwerkerAttr *string `json:"kreuzwerker_attr"`
	PreviousAttr    *string `json:"previous_attr"`
	OurAttr         *string `json:"our_attr"`
	Transform       string  `json:"transform"`
	Reason          string  `json:"reason"`
}

// AttrProblem is one kreuzwerker attribute that fails the check.
type AttrProblem struct {
	Type   string // our type (after type renames), kind-prefixed
	Path   string // kreuzwerker path
	Kind   string // "unlisted", "gap", "no reason" or "unrecorded" (CheckPrevious)
	Reason string
}

// LoadRenames reads a renames.json file.
func LoadRenames(path string) (RenamesDoc, error) {
	var d RenamesDoc
	b, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// CheckAttrs returns the kreuzwerker attributes that renames.json does not
// account for: no row (unlisted), dropped with a "gap:" reason (gap), or
// dropped with no reason (no reason). The second result counts mapped and
// dropped attributes.
func CheckAttrs(attrs []paritydata.Attr, doc RenamesDoc) ([]AttrProblem, map[string]int) {
	typeMap := map[string]string{}
	for _, t := range doc.TypeRenames {
		typeMap[t.KreuzwerkerType] = t.OurType
	}
	rows := map[[2]string]RenameRow{}
	for _, r := range doc.Renames {
		if r.KreuzwerkerAttr != nil {
			rows[[2]string{r.Type, *r.KreuzwerkerAttr}] = r
		}
	}
	counts := map[string]int{}
	var out []AttrProblem
	for _, a := range attrs {
		typ := a.Type
		if t, ok := typeMap[typ]; ok {
			typ = t
		}
		r, ok := rows[[2]string{typ, a.Path}]
		switch {
		case !ok:
			out = append(out, AttrProblem{Type: typ, Path: a.Path, Kind: "unlisted"})
		case r.OurAttr != nil && *r.OurAttr != "":
			counts["mapped"]++
		case strings.HasPrefix(strings.TrimSpace(r.Reason), "gap:"):
			out = append(out, AttrProblem{Type: typ, Path: a.Path, Kind: "gap", Reason: r.Reason})
		case strings.TrimSpace(r.Reason) == "":
			out = append(out, AttrProblem{Type: typ, Path: a.Path, Kind: "no reason"})
		default:
			counts["dropped"]++
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Path < out[j].Path
	})
	return out, counts
}

// PrintAttrProblems writes the problems grouped by type.
func PrintAttrProblems(w io.Writer, probs []AttrProblem) {
	byKind := map[string][]AttrProblem{}
	for _, p := range probs {
		byKind[p.Kind] = append(byKind[p.Kind], p)
	}
	titles := []struct{ kind, title string }{
		{"gap", "open gaps (renames.json our_attr null, reason \"gap:\"): implement them, or give a reason starting \"won't implement:\""},
		{"unlisted", "kreuzwerker attributes with no renames.json row"},
		{"no reason", "dropped attributes with no reason"},
		{"unrecorded", "of our own pre-Pass 5 attributes are gone or changed type with no renames.json previous_attr row (add one through reference/_gen/pass5/renames_overrides.json `add`)"},
	}
	for _, k := range titles {
		ps := byKind[k.kind]
		if len(ps) == 0 {
			continue
		}
		fmt.Fprintf(w, "parity: %d %s\n", len(ps), k.title)
		last := ""
		for _, p := range ps {
			if p.Type != last {
				fmt.Fprintf(w, "  %s\n", p.Type)
				last = p.Type
			}
			if p.Reason != "" {
				fmt.Fprintf(w, "    %s: %s\n", p.Path, p.Reason)
			} else {
				fmt.Fprintf(w, "    %s\n", p.Path)
			}
		}
	}
}

// ---- extraction from the kreuzwerker docs ---------------------------------

var (
	kwItem = regexp.MustCompile("^- `([A-Za-z0-9_]+)` \\(([^)]*)\\)")
	kwNest = regexp.MustCompile("^### Nested Schema for `([^`]+)`")
)

// ExtractAttrs reads every attribute of every resource, data source and action
// from a kreuzwerker checkout's tfplugindocs output (<checkout>/docs), with
// full dotted paths. Types are kind-prefixed (data., action.).
func ExtractAttrs(docsDir string) ([]paritydata.Attr, error) {
	var out []paritydata.Attr
	for _, sub := range []struct{ dir, prefix string }{{"resources", ""}, {"data-sources", "data."}, {"actions", "action."}} {
		d := filepath.Join(docsDir, sub.dir)
		ents, err := os.ReadDir(d)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".md") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			typ := sub.prefix + "docker_" + strings.TrimSuffix(n, ".md")
			attrs, err := extractFile(filepath.Join(d, n), typ)
			if err != nil {
				return nil, err
			}
			out = append(out, attrs...)
		}
	}
	return out, nil
}

func extractFile(path, typ string) ([]paritydata.Attr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []paritydata.Attr
	inSchema := false
	parent := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(line, "## Schema") {
			inSchema = true
			continue
		}
		if strings.HasPrefix(line, "## ") && inSchema {
			inSchema = false
		}
		if !inSchema {
			continue
		}
		if m := kwNest.FindStringSubmatch(line); m != nil {
			parent = m[1]
			continue
		}
		if m := kwItem.FindStringSubmatch(line); m != nil {
			p := m[1]
			if parent != "" {
				p = parent + "." + p
			}
			out = append(out, paritydata.Attr{Type: typ, Path: p, Kind: m[2]})
		}
	}
	return out, sc.Err()
}

// WriteAttrs writes the attribute list in the kreuzwerker_v4.6.0_attrs.json layout.
func WriteAttrs(path, commit string, attrs []paritydata.Attr) error {
	doc := paritydata.AttrsDoc{
		About: "Every attribute of every kreuzwerker/docker v4.6.0 resource, data source and action, extracted from its " +
			"tfplugindocs output (docs/) by `go run ./tools/parity -extract <checkout>`; do not hand-edit. " +
			"tools/parity checks each one against reference/_gen/renames.json.",
		Version:    "v4.6.0",
		Commit:     commit,
		Attributes: attrs,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// findRepoFile returns rel resolved against the nearest directory at or
// above the working directory that contains go.mod.
func findRepoFile(rel string) string {
	dir, err := os.Getwd()
	if err != nil {
		return rel
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return rel
		}
		dir = parent
	}
}
