package main

// Swagger-derived output shapes (Pass 6c lane S).
//
// The hand-written field trees in reference/_gen/fragments/outputs fix the
// names, descriptions and sensitivity of the fields a lane cared about; they
// do not fix the shape of everything below. Every structured output listed
// in swagger_map.json takes its whole shape from the Engine API swagger
// definition it mirrors instead:
//
//   - object with properties          -> object with one field per property
//   - object with additionalProperties -> map(T), T from additionalProperties
//     (keys are user data: they are never renamed)
//   - object with only `enum: [{}]`     -> object({}) (an empty object)
//   - array                            -> list(T)
//   - string (enum or not)             -> string
//   - integer / number                 -> number
//   - boolean                          -> bool
//   - anything else (free-form)        -> dynamic
//
// Fragment fields matched by upstream key keep their name, description and
// sensitivity; new fields get the mechanical snake_case of the property
// name. Fragment fields with no swagger property (provider-computed) are
// kept as they are. swagger_map.json also lists the "Definition.Property"
// pairs that are sensitive wherever they appear (command output, environment
// values, join tokens).

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SwaggerMap is tools/contractgen/swagger_map.json.
type SwaggerMap struct {
	// Outputs maps "<item id>/<output name>" to a swagger definition name;
	// a trailing "[]" means the output is a list of that definition.
	Outputs map[string]string `json:"outputs"`
	// Sensitive lists "Definition.Property" pairs that are sensitive.
	Sensitive []string `json:"sensitive"`
}

// Swagger is the part of the Engine API swagger contractgen reads.
type Swagger struct {
	Definitions map[string]any `yaml:"definitions"`
}

// LoadSwagger reads the swagger YAML and the map.
func LoadSwagger(swaggerPath, mapPath string) (*Swagger, *SwaggerMap, error) {
	raw, err := os.ReadFile(swaggerPath)
	if err != nil {
		return nil, nil, err
	}
	var sw Swagger
	if err := yaml.Unmarshal(raw, &sw); err != nil {
		return nil, nil, fmt.Errorf("decoding %s: %w", swaggerPath, err)
	}
	mraw, err := os.ReadFile(mapPath)
	if err != nil {
		return nil, nil, err
	}
	var m SwaggerMap
	if err := json.Unmarshal(mraw, &m); err != nil {
		return nil, nil, fmt.Errorf("decoding %s: %w", mapPath, err)
	}
	return &sw, &m, nil
}

// ApplySwagger replaces the field tree and tf_type of every mapped output
// with the swagger-derived one. An unknown item, output or definition is an
// error, so the map cannot rot.
func ApplySwagger(inv *Inventory, sw *Swagger, m *SwaggerMap) error {
	sens := map[string]bool{}
	for _, s := range m.Sensitive {
		sens[s] = true
	}
	used := map[string]bool{}
	for i := range inv.Items {
		it := &inv.Items[i]
		for j := range it.Outputs {
			o := &it.Outputs[j]
			key := it.ID + "/" + o.Name
			def, ok := m.Outputs[key]
			if !ok {
				continue
			}
			used[key] = true
			list := strings.HasSuffix(def, "[]")
			def = strings.TrimSuffix(def, "[]")
			schema, ok := sw.Definitions[def]
			if !ok {
				return fmt.Errorf("swagger map %s: no definition %q", key, def)
			}
			d := &deriver{sw: sw, sens: sens}
			tf, fields := d.derive(schema, def, o.Fields, map[string]bool{def: true})
			if !strings.HasPrefix(tf, "object(") {
				return fmt.Errorf("swagger map %s: definition %q is not an object (%s)", key, def, tf)
			}
			if list {
				tf = "list(" + tf + ")"
			}
			o.TFType = tf
			o.Fields = fields
			t := true
			o.FieldsComplete = &t
		}
	}
	var missing []string
	for k := range m.Outputs {
		if !used[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("swagger map: no such item output: %s", strings.Join(missing, ", "))
	}
	return nil
}

type deriver struct {
	sw   *Swagger
	sens map[string]bool
}

// resolve follows $ref and merges allOf; def is the name of the definition
// the schema came from (for the sensitive list).
func (d *deriver) resolve(s any, def string, stack map[string]bool) (map[string]any, string, bool) {
	m, _ := s.(map[string]any)
	if m == nil {
		return map[string]any{}, def, false
	}
	if ref, ok := m["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/definitions/")
		if stack[name] {
			return nil, name, true
		}
		t, ok := d.sw.Definitions[name]
		if !ok {
			return map[string]any{}, name, false
		}
		stack[name] = true
		r, n, cyc := d.resolve(t, name, stack)
		return r, n, cyc
	}
	if all, ok := m["allOf"].([]any); ok {
		merged := map[string]any{}
		props := map[string]any{}
		for k, v := range m {
			if k != "allOf" {
				merged[k] = v
			}
		}
		for _, part := range all {
			pm, _, cyc := d.resolve(part, def, stack)
			if cyc {
				return nil, def, true
			}
			for k, v := range pm {
				if k == "properties" {
					if pp, ok := v.(map[string]any); ok {
						for pk, pv := range pp {
							props[pk] = pv
						}
					}
					continue
				}
				if _, set := merged[k]; !set {
					merged[k] = v
				}
			}
		}
		if p, ok := m["properties"].(map[string]any); ok {
			for pk, pv := range p {
				props[pk] = pv
			}
		}
		if len(props) > 0 {
			merged["properties"] = props
			merged["type"] = "object"
		}
		return merged, def, false
	}
	return m, def, false
}

func copyStack(s map[string]bool) map[string]bool {
	c := make(map[string]bool, len(s))
	for k, v := range s {
		c[k] = v
	}
	return c
}

// derive returns the tf_type of schema s and, for objects (also as list or
// map elements), its fields merged with the fragment fields.
func (d *deriver) derive(s any, def string, frag []Field, stack map[string]bool) (string, []Field) {
	stack = copyStack(stack)
	m, def, cyc := d.resolve(s, def, stack)
	if cyc {
		return "dynamic", nil
	}
	typ, _ := m["type"].(string)
	props, hasProps := m["properties"].(map[string]any)
	switch {
	case hasProps && len(props) > 0:
		return "object({...})", d.objectFields(props, def, frag, stack)
	case typ == "array":
		et, fs := d.derive(m["items"], def, frag, stack)
		return "list(" + et + ")", fs
	case typ == "object" || m["additionalProperties"] != nil:
		switch ap := m["additionalProperties"].(type) {
		case map[string]any:
			if len(ap) == 0 {
				return "map(dynamic)", nil
			}
			et, fs := d.derive(ap, def, frag, stack)
			return "map(" + et + ")", fs
		case bool:
			if ap {
				return "map(dynamic)", nil
			}
		}
		if _, ok := m["enum"]; ok {
			return "object({})", nil
		}
		return "dynamic", nil
	case typ == "string":
		return "string", nil
	case typ == "integer" || typ == "number":
		return "number", nil
	case typ == "boolean":
		return "bool", nil
	}
	if _, ok := m["enum"]; ok {
		return "string", nil
	}
	return "dynamic", nil
}

func (d *deriver) objectFields(props map[string]any, def string, frag []Field, stack map[string]bool) []Field {
	byUp := map[string]Field{}
	for _, f := range frag {
		byUp[f.Upstream] = f
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	derived := map[string]Field{}
	for _, k := range keys {
		ff, had := byUp[k]
		tf, sub := d.derive(props[k], def, ff.Fields, stack)
		f := Field{Name: AttrName(k), Upstream: k, TFType: tf, Fields: sub}
		if had {
			f.Name, f.Description, f.Sensitive = ff.Name, ff.Description, ff.Sensitive
		}
		if d.sens[def+"."+k] {
			f.Sensitive = true
		}
		derived[k] = f
	}
	var out []Field
	done := map[string]bool{}
	for _, f := range frag {
		if df, ok := derived[f.Upstream]; ok {
			if !done[f.Upstream] {
				out = append(out, df)
				done[f.Upstream] = true
			}
			continue
		}
		out = append(out, f) // provider-computed field with no swagger property
	}
	for _, k := range keys {
		if !done[k] {
			out = append(out, derived[k])
		}
	}
	return out
}

var nonAttrRe = regexp.MustCompile(`[^a-z0-9_]+`)

// AttrName is Snake made a valid Terraform attribute name: anything but
// [a-z0-9_] becomes "_" (os.version -> os_version).
func AttrName(s string) string {
	return strings.Trim(nonAttrRe.ReplaceAllString(strings.ToLower(Snake(s)), "_"), "_")
}

var (
	snakeIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-]*$`)
	snakeFixups  = strings.NewReplacer("SELinux", "Selinux", "IOps", "Iops")
)

// Snake is contractSnake from internal/provider/contract.go (the
// CONTRACT.md mechanical rule); a provider test checks they agree.
func Snake(s string) string {
	if !snakeIdentRe.MatchString(s) {
		return s
	}
	s = snakeFixups.Replace(strings.ReplaceAll(s, "-", "_"))
	r := []rune(s)
	n := len(r)
	isUp := func(c rune) bool { return c >= 'A' && c <= 'Z' }
	isLow := func(c rune) bool { return c >= 'a' && c <= 'z' }
	isDig := func(c rune) bool { return c >= '0' && c <= '9' }
	var words []string
	for i := 0; i < n; {
		c := r[i]
		switch {
		case c == '_':
			i++
		case isUp(c):
			j := i
			for j < n && isUp(r[j]) {
				j++
			}
			run := string(r[i:j])
			if j < n && isLow(r[j]) {
				switch {
				case j-i > 1 && r[j] == 's' && (j+1 == n || !isLow(r[j+1])):
					words = append(words, run+"s")
					i = j + 1
				case j-i > 1 && r[j] == 'v' && j+1 < n && isDig(r[j+1]):
					k := j + 1
					for k < n && isDig(r[k]) {
						k++
					}
					words = append(words, run+string(r[j:k]))
					i = k
				case j-i > 1:
					words = append(words, string(r[i:j-1]))
					i = j - 1
				default:
					k := j
					for k < n && (isLow(r[k]) || isDig(r[k])) {
						k++
					}
					words = append(words, string(r[i:k]))
					i = k
				}
			} else {
				k := j
				for k < n && isDig(r[k]) {
					k++
				}
				words = append(words, string(r[i:k]))
				i = k
			}
		default:
			k := i
			for k < n && !isUp(r[k]) && r[k] != '_' {
				k++
			}
			words = append(words, string(r[i:k]))
			i = k
		}
	}
	return strings.ToLower(strings.Join(words, "_"))
}
