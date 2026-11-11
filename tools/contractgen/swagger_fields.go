package main

// Field-level swagger derivation (Pass 6c verification).
//
// ApplySwagger derives whole outputs listed in swagger_map.json. Outputs that
// are provider-shaped (a subset, a list of a definition, an action result)
// still cite swagger schemas field by field in their fragment pointers. Where
// such a field is untyped in the fragment (dynamic, or object({...}) with no
// fields) or has a different kind than the schema it cites (a map written as
// a list), ApplyFieldPointers takes the field's shape from the swagger, the
// same way ApplySwagger does for a whole output. A field cites a schema when
// its pointer is a whole definition (#/definitions/X) or a property whose key
// is the field's upstream key (#/definitions/X/properties/<upstream>); a
// property cited for a value derived from it (a label, Names[0]) is left
// alone. Typed fields of the cited kind keep their fragment subset and are
// walked recursively.

import "strings"

// ApplyFieldPointers rewrites the untyped or mis-kinded swagger-cited fields
// of every output that ApplySwagger did not derive.
func ApplyFieldPointers(inv *Inventory, sw *Swagger, m *SwaggerMap) {
	sens := map[string]bool{}
	for _, s := range m.Sensitive {
		sens[s] = true
	}
	d := &deriver{sw: sw, sens: sens}
	for i := range inv.Items {
		it := &inv.Items[i]
		if it.Kind == "skip" {
			continue
		}
		for j := range it.Outputs {
			o := &it.Outputs[j]
			if _, mapped := m.Outputs[it.ID+"/"+o.Name]; mapped {
				continue
			}
			d.fieldPointers(o.Fields)
		}
	}
}

func (d *deriver) fieldPointers(fs []Field) {
	for i := range fs {
		f := &fs[i]
		if schema, def, whole, ok := d.pointer(f.Pointer, f.Upstream); ok {
			stack := map[string]bool{}
			if whole {
				stack[def] = true
			}
			dtf, dfields := d.derive(schema, def, f.Fields, stack)
			cur := tfHeads(f.TFType)
			want := tfHeads(dtf)
			// A whole definition cited for a list / map field describes
			// its elements.
			prefix := []string(nil)
			if whole && len(want) == 1 && want[0] == "object" && len(cur) > 1 {
				prefix = cur[:len(cur)-1]
				want = append(append([]string{}, prefix...), want...)
				for k := len(prefix) - 1; k >= 0; k-- {
					dtf = prefix[k] + "(" + dtf + ")"
				}
			}
			// Never trade a typed object for dynamic; a scalar standing in
			// for a free-form object (JSON-encoded into a string) is replaced.
			loses := want[len(want)-1] == "dynamic" && cur[len(cur)-1] == "object" && len(f.Fields) > 0
			if !loses && (tfUntyped(f.TFType, f.Fields) || !sameHeads(cur, want)) {
				f.TFType = dtf
				f.Fields = dfields
				continue
			}
		}
		d.fieldPointers(f.Fields)
	}
}

// pointer resolves a fragment pointer to a swagger schema; whole is true for
// #/definitions/X. A property pointer must end in the field's upstream key.
func (d *deriver) pointer(ptr, upstream string) (schema any, def string, whole, ok bool) {
	rest, found := strings.CutPrefix(ptr, "#/definitions/")
	if !found {
		return nil, "", false, false
	}
	segs := strings.Split(rest, "/")
	def = segs[0]
	cur, exists := d.sw.Definitions[def]
	if !exists {
		return nil, "", false, false
	}
	if len(segs) == 1 {
		return cur, def, true, true
	}
	if len(segs)%2 == 0 || segs[len(segs)-1] != upstream {
		return nil, "", false, false
	}
	for k := 1; k+1 < len(segs); k += 2 {
		if segs[k] != "properties" {
			return nil, "", false, false
		}
		m, name, cyc := d.resolve(cur, def, map[string]bool{})
		if cyc || m == nil {
			return nil, "", false, false
		}
		def = name
		props, _ := m["properties"].(map[string]any)
		if cur, exists = props[segs[k+1]]; !exists {
			return nil, "", false, false
		}
	}
	return cur, def, false, true
}

// tfHeads returns the type constructors from the outside in:
// "list(object({...}))" -> [list object].
func tfHeads(tf string) []string {
	var out []string
	for {
		head, rest, found := strings.Cut(tf, "(")
		out = append(out, strings.TrimSpace(head))
		if !found || (head != "list" && head != "set" && head != "map") {
			return out
		}
		tf = strings.TrimSuffix(rest, ")")
	}
}

func sameHeads(a, b []string) bool {
	norm := func(h []string) string {
		c := append([]string{}, h...)
		for i, x := range c {
			if x == "set" {
				c[i] = "list"
			}
		}
		return strings.Join(c, "/")
	}
	return norm(a) == norm(b)
}

// tfUntyped: the innermost type is dynamic, or an object({...}) whose fields
// the fragment never wrote.
func tfUntyped(tf string, fields []Field) bool {
	h := tfHeads(tf)
	in := h[len(h)-1]
	return in == "dynamic" || (in == "object" && strings.Contains(tf, "{...}") && len(fields) == 0)
}
