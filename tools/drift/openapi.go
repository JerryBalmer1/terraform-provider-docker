package main

// OpenAPI specs published only as a URL (Pass 6g): the Docker Hub API spec
// (https://docs.docker.com/reference/api/hub/latest.yaml). There is no git
// repository, no tags and no version history: docs.docker.com serves only the
// latest spec. reference/SOURCES.md pins it as "<info.version> @ sha256 <hex>"
// and reference/upstream holds the pinned bytes.
//
// DriftCheck fetches the live spec into the work directory, compares its
// sha256 with the pin and, when the bytes differ, diffs the surface of the
// pinned copy against the live one:
//
//   - <source>:op/<METHOD> <path>        detail "operationId=<id>[ deprecated]"
//   - <source>:schema/<Schema>           the component schema's own type
//   - <source>:schema/<Schema>.<prop>    each property's type, recursively
//     through inline objects (<prop>.<sub>), array items (<prop>[].<sub>),
//     and allOf / oneOf / anyOf members ($ref members of allOf resolved, with
//     a cycle guard); a property that is a $ref is typed "$ref:<Schema>" and
//     not expanded (the referenced schema has its own rows).
//
// Every added, removed or changed item fails unless it is deferred in
// reference/_gen/deferred.d/upstream.json or accepted in
// reference/_gen/drift-accepted.json (both keyed by the item above); a new or
// renamed operation whose operationId reference/inventory.json names in a
// source with "spec": "<InventorySpec>" counts as mapped. Changed bytes with
// no surface change pass with a notice and a reference/_gen/drift-log.json row.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// modelOpenAPI: a URL-published OpenAPI spec, diffed by this file.
const modelOpenAPI = "openapi"

// openAPIMethods are the operation keys of an OpenAPI 3.x path item (3.2 adds query).
var openAPIMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace", "query"}

// probeSnapshot fetches a URL-published spec (or the -to override: a local
// file or another URL) and compares its sha256 with the pin.
func probeSnapshot(o options, rm remote, s source, p pin, ov string, hasOv bool, st *sourceStatus) {
	st.Pinned = p.Version + "@sha256:" + short(p.SHA256)
	var body []byte
	switch {
	case hasOv && !strings.Contains(ov, "://"):
		b, err := os.ReadFile(ov)
		if err != nil {
			st.Error = err.Error()
			return
		}
		body = b
	default:
		u := s.SpecURL
		if hasOv {
			u = ov
		}
		code, b, err := rm.Get(u)
		if err != nil {
			st.Error = err.Error()
			return
		}
		if code != 200 {
			st.Error = fmt.Sprintf("GET %s: HTTP %d", u, code)
			return
		}
		body = b
	}
	ver, err := openAPIVersion(body)
	if err != nil {
		st.Error = "live spec: " + err.Error()
		return
	}
	sum := sha256Hex(body)
	st.Newest = ver + "@sha256:" + short(sum)
	st.body = body
	st.ref = sum
	if sum != p.SHA256 {
		st.Moved = true
		st.Note = "bytes changed (sha256 " + short(sum) + ", pinned " + short(p.SHA256) + "; info.version " + ver + ")"
	} else {
		st.Note = "sha256 unchanged"
	}
}

func openAPIVersion(b []byte) (string, error) {
	var d struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
	}
	if err := yaml.Unmarshal(b, &d); err != nil {
		return "", err
	}
	if d.Info.Version == "" {
		return "", fmt.Errorf("no info.version")
	}
	return d.Info.Version, nil
}

// extractOpenAPI returns the surface items (without the source prefix) and,
// for operations, item -> operationId.
func extractOpenAPI(b []byte) (map[string]string, map[string]string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	out, ids := map[string]string{}, map[string]string{}
	paths, _ := doc["paths"].(map[string]any)
	for p, v := range paths {
		item, _ := v.(map[string]any)
		for _, m := range openAPIMethods {
			op, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			key := "op/" + strings.ToUpper(m) + " " + p
			id, _ := op["operationId"].(string)
			det := "operationId=" + id
			if dep, _ := op["deprecated"].(bool); dep {
				det += " deprecated"
			}
			out[key], ids[key] = det, id
		}
	}
	comps, _ := doc["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	for name, v := range schemas {
		sc, _ := v.(map[string]any)
		out["schema/"+name] = schemaType(sc)
		flattenSchema("schema/"+name, sc, schemas, map[string]bool{name: true}, out)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("no operations or component schemas")
	}
	return out, ids, nil
}

func refName(s map[string]any) string {
	r, _ := s["$ref"].(string)
	if r == "" {
		return ""
	}
	return r[strings.LastIndex(r, "/")+1:]
}

// schemaType is the one-line type of a schema: $ref:<Name>, type[(format)],
// array<items>, allOf(...), oneOf(...), anyOf(...), or any.
func schemaType(s map[string]any) string {
	if s == nil {
		return "any"
	}
	if r := refName(s); r != "" {
		return "$ref:" + r
	}
	for _, comb := range []string{"allOf", "oneOf", "anyOf"} {
		if list, ok := s[comb].([]any); ok {
			var parts []string
			for _, m := range list {
				mm, _ := m.(map[string]any)
				parts = append(parts, schemaType(mm))
			}
			return comb + "(" + strings.Join(parts, ",") + ")"
		}
	}
	var t string
	switch tv := s["type"].(type) {
	case string:
		t = tv
	case []any:
		var ts []string
		for _, x := range tv {
			ts = append(ts, fmt.Sprint(x))
		}
		t = strings.Join(ts, "|")
	}
	if t == "" {
		if _, ok := s["properties"]; ok {
			t = "object"
		} else {
			return "any"
		}
	}
	if f, _ := s["format"].(string); f != "" {
		t += "(" + f + ")"
	}
	if strings.Contains(t, "array") {
		items, _ := s["items"].(map[string]any)
		t = strings.Replace(t, "array", "array<"+schemaType(items)+">", 1)
	}
	return t
}

// flattenSchema adds prefix.<prop> rows for every property of s.
func flattenSchema(prefix string, s map[string]any, schemas map[string]any, seen map[string]bool, out map[string]string) {
	if s == nil {
		return
	}
	if props, ok := s["properties"].(map[string]any); ok {
		for name, v := range props {
			ps, _ := v.(map[string]any)
			key := prefix + "." + name
			out[key] = schemaType(ps)
			if refName(ps) != "" {
				continue
			}
			flattenSchema(key, ps, schemas, seen, out)
			if items, ok := ps["items"].(map[string]any); ok && refName(items) == "" {
				flattenSchema(key+"[]", items, schemas, seen, out)
			}
		}
	}
	for _, comb := range []string{"allOf", "oneOf", "anyOf"} {
		list, _ := s[comb].([]any)
		for _, m := range list {
			mm, _ := m.(map[string]any)
			if r := refName(mm); r != "" {
				// allOf composes the referenced schema's properties into this one.
				if comb != "allOf" || seen[r] {
					continue
				}
				rs, _ := schemas[r].(map[string]any)
				seen[r] = true
				flattenSchema(prefix, rs, schemas, seen, out)
				delete(seen, r)
				continue
			}
			flattenSchema(prefix, mm, schemas, seen, out)
		}
	}
}

// inventorySpecOps returns the operationIds reference/inventory.json names in
// a source object whose "spec" is spec.
func inventorySpecOps(path, spec string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if id, ok := t["operationId"].(string); ok && id != "" && t["spec"] == spec {
				out[id] = true
			}
			for _, x := range t {
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(raw)
	return out, nil
}

// diffOpenAPI diffs every moved URL-published OpenAPI source.
func diffOpenAPI(o options, r *report) {
	for i := range r.Sources {
		st := &r.Sources[i]
		s := sourceByName(o, st.Name)
		if s == nil || s.SpecURL == "" || !st.Moved || st.Error != "" {
			continue
		}
		fail := func(msg string) {
			st.Error = msg
			r.Errors = append(r.Errors, s.Name+": "+msg)
		}
		pinnedPath := filepath.Join(o.Repo, filepath.FromSlash(s.Baseline))
		pb, err := os.ReadFile(pinnedPath)
		if err != nil {
			fail("pinned spec: " + err.Error())
			continue
		}
		if sum := sha256Hex(pb); sum != o.Pins[s.Name].SHA256 {
			fail(fmt.Sprintf("pinned spec %s has sha256 %s, reference/SOURCES.md pins %s", s.Baseline, short(sum), short(o.Pins[s.Name].SHA256)))
			continue
		}
		hb, err := os.ReadFile(st.Spec)
		if err != nil {
			fail("live spec: " + err.Error())
			continue
		}
		base, _, err := extractOpenAPI(pb)
		if err != nil {
			fail("pinned spec: " + err.Error())
			continue
		}
		head, headIDs, err := extractOpenAPI(hb)
		if err != nil {
			fail("live spec: " + err.Error())
			continue
		}
		inv := map[string]bool{}
		if s.InventorySpec != "" {
			if inv, err = inventorySpecOps(filepath.Join(o.Repo, "reference", "inventory.json"), s.InventorySpec); err != nil {
				fail("inventory: " + err.Error())
				continue
			}
		}
		st.SurfaceItems = len(head)
		fs := diffSurface(base, head, s.Name, st.Newest, o.Deferred)
		for j := range fs {
			f := &fs[j]
			id := strings.TrimPrefix(f.Item, s.Name+":")
			switch {
			case strings.HasPrefix(id, "op/"):
				f.Kind = kindOperation
				if f.Status == statusUnmapped && f.Change != changeRemoved && inv[headIDs[id]] {
					f.Status = statusMapped
					f.Detail += " (in reference/inventory.json)"
				}
			default:
				f.Kind = kindField
			}
		}
		sort.SliceStable(fs, func(a, b int) bool { return fs[a].Item < fs[b].Item })
		r.Findings = append(r.Findings, fs...)
	}
}
