package main

// Our own schema changes: every attribute of our schema before Pass 5 (the
// checked-in snapshot paritydata/ours_pre_pass5_attrs.json, extracted from the
// genskill reference at commit 97ec6bf) that is gone or has a different type
// in the current genskill reference (.claude/skills/terraform-provider-docker/
// reference) needs a renames.json row whose previous_attr is that attribute
// or one of its parents. Without this check a shape change of one of our own
// attributes (e.g. docker_registry_token.token string -> object) could ship
// with no row, so no migration-guide line, and no other check would notice:
// CheckAttrs only walks the kreuzwerker attributes.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JerryBalmer1/terraform-provider-docker/tools/parity/paritydata"
)

var (
	refNest = regexp.MustCompile("^### `([^`]+)`")
	refRow  = regexp.MustCompile("^\\| `([^`]+)` \\| `([^`]*)` \\|")
)

// ParseReferenceDir reads every <type>.md page of a genskill reference
// directory: the "Arguments and attributes" table and its nested "### `path`"
// tables, as attributes with full dotted paths and the Terraform type text in
// Kind. Types are the page names (kind-prefixed for non-resources).
func ParseReferenceDir(dir string) ([]paritydata.Attr, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []paritydata.Attr
	for _, n := range names {
		attrs, err := parseReferencePage(filepath.Join(dir, n), strings.TrimSuffix(n, ".md"))
		if err != nil {
			return nil, err
		}
		out = append(out, attrs...)
	}
	return out, nil
}

func parseReferencePage(path, typ string) ([]paritydata.Attr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []paritydata.Attr
	prefix, in := "", false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := refNest.FindStringSubmatch(line); m != nil {
			prefix, in = m[1]+".", true
			continue
		}
		if strings.HasPrefix(line, "## ") {
			prefix, in = "", strings.HasPrefix(line, "## Arguments and attributes")
			continue
		}
		if !in {
			continue
		}
		if m := refRow.FindStringSubmatch(line); m != nil {
			out = append(out, paritydata.Attr{Type: typ, Path: prefix + m[1], Kind: m[2]})
		}
	}
	return out, sc.Err()
}

// WritePreviousAttrs writes the snapshot of our pre-Pass 5 schema.
func WritePreviousAttrs(path, commit string, attrs []paritydata.Attr) error {
	doc := paritydata.AttrsDoc{
		About: "Every attribute of our own schema before Pass 5, extracted from the genskill reference " +
			"(.claude/skills/terraform-provider-docker/reference) at the commit below by " +
			"`go run ./tools/parity -extract-previous <dir>`; do not hand-edit. Kind is the Terraform type. " +
			"tools/parity requires a renames.json previous_attr row for each one that is gone or changed type.",
		Version:    "pre-pass5",
		Commit:     commit,
		Attributes: attrs,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// CheckPrevious returns our previous attributes that are gone or changed type
// in cur (after the renames.json type renames) with no renames.json row whose
// previous_attr is the attribute or a parent of it (kind "unrecorded").
func CheckPrevious(prev, cur []paritydata.Attr, doc RenamesDoc) []AttrProblem {
	typeMap := map[string]string{}
	for _, t := range doc.TypeRenames {
		if t.PreviousType != "" {
			typeMap[t.PreviousType] = t.OurType
		}
	}
	now := map[[2]string]string{}
	types := map[string]bool{}
	for _, a := range cur {
		now[[2]string{a.Type, a.Path}] = a.Kind
		types[a.Type] = true
	}
	rows := map[[2]string]bool{}
	for _, r := range doc.Renames {
		if r.PreviousAttr != nil {
			rows[[2]string{r.Type, *r.PreviousAttr}] = true
		}
	}
	var out []AttrProblem
	gone := map[string]bool{}
	for _, a := range prev {
		typ := a.Type
		if t, ok := typeMap[typ]; ok {
			typ = t
		}
		if !types[typ] {
			if !gone[typ] {
				gone[typ] = true
				out = append(out, AttrProblem{Type: typ, Path: "(type)", Kind: "unrecorded", Reason: "type is gone from the current reference"})
			}
			continue
		}
		k, ok := now[[2]string{typ, a.Path}]
		if ok && k == a.Kind {
			continue
		}
		covered := false
		parts := strings.Split(a.Path, ".")
		for i := len(parts); i > 0 && !covered; i-- {
			covered = rows[[2]string{typ, strings.Join(parts[:i], ".")}]
		}
		if covered {
			continue
		}
		reason := "gone"
		if ok {
			reason = fmt.Sprintf("%s -> %s", a.Kind, k)
		}
		out = append(out, AttrProblem{Type: typ, Path: a.Path, Kind: "unrecorded", Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Path < out[j].Path
	})
	return out
}
