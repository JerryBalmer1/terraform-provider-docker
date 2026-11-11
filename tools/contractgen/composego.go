package main

// compose-go-derived output shapes (Pass 6c).
//
// The structured `project` output of the compose items is
// json.Marshal(types.Project) from compose-go at the pinned version (go.mod,
// reference/SOURCES.md compose_go), then composeServiceFixups in
// internal/provider/compose_project_value.go. Its shape is therefore derived
// from compose-go's Go types by reflection, the way swagger.go derives the
// Engine outputs from the swagger, so it cannot disagree with what the
// loader produces:
//
//   - struct                     -> object, one field per JSON key (json tag;
//     `json:"-"` and unexported fields skipped; untagged or `,inline`
//     embedded structs flattened, as encoding/json does)
//   - map[string]T               -> map(T) (keys are user data: never renamed)
//   - slice / array              -> list(T)
//   - pointer                    -> its element
//   - string kinds               -> string
//   - integer / float kinds      -> number
//   - bool                       -> bool
//   - interface                  -> dynamic
//   - a type with its own MarshalJSON (value or pointer receiver) takes the
//     kind of what it marshals a sample to: UnitBytes ("%d", the byte sizes
//     such as mem_limit / shm_size), Duration (time.Duration text), FileMode
//     ("0444") and SSHKey are strings; a type that marshals to an object or
//     array is walked structurally.
//
// "String or number" spec fields follow the loader's normalisation: ports
// `published` is a string (it may be a range), cpus a number, byte sizes and
// durations strings, uid / gid strings, mode a string.
//
// composeFixupTypes mirrors composeServiceFixups: the few places where the
// provider rewrites compose-go's JSON into the contract shape. A fixup with no
// matching type here (or the reverse) is caught by the provider test
// TestContractComposeGoShapes.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

// composeFixupTypes: Go type name -> tf_type after composeServiceFixups.
var composeFixupTypes = map[string]string{
	// extra_hosts: compose-go writes a sorted list of "host=ip"; the fixup makes it host -> addresses.
	"HostsList": "map(list(string))",
}

// composeStructFixups: Go struct types that marshal to a union (a number or an object) and that the fixup always
// writes in their struct shape. UlimitsConfig marshals `single` as a bare number; the fixup writes {"single": n}.
var composeStructFixups = map[string]bool{"UlimitsConfig": true}

// composeSensitiveKeys are the compose secret channels (CONTRACT.md "Sensitivity"): sensitive wherever they appear.
var composeSensitiveKeys = map[string]bool{"environment": true, "env_file": true, "args": true, "content": true}

// ComposeOutputs lists the "<item id>/<output name>" outputs that serialize types.Project. full: the whole project;
// otherwise only the top-level keys the fragment names (the per-key compose items).
var ComposeOutputs = map[string]bool{
	"compose:name/project":       true,
	"compose:cli-config/project": true,
	"compose:configs/project":    false,
	"compose:include/project":    false,
	"compose:jobs/project":       false,
	"compose:models/project":     false,
	"compose:networks/project":   false,
	"compose:secrets/project":    false,
	"compose:services/project":   false,
	"compose:volumes/project":    false,
}

var jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// ApplyComposeGo replaces the field tree of every compose project output with the compose-go-derived one.
func ApplyComposeGo(inv *Inventory) error {
	used := map[string]bool{}
	for i := range inv.Items {
		it := &inv.Items[i]
		for j := range it.Outputs {
			o := &it.Outputs[j]
			key := it.ID + "/" + o.Name
			full, ok := ComposeOutputs[key]
			if !ok {
				continue
			}
			used[key] = true
			tf, fields := DeriveComposeType(reflect.TypeOf(ctypes.Project{}), o.Fields)
			if !full {
				keep := map[string]bool{}
				for _, f := range o.Fields {
					keep[f.Upstream] = true
				}
				var sub []Field
				for _, f := range fields {
					if keep[f.Upstream] {
						sub = append(sub, f)
					}
				}
				fields = sub
			}
			o.TFType = tf
			o.Fields = fields
			t := full
			o.FieldsComplete = &t
		}
	}
	var missing []string
	for k := range ComposeOutputs {
		if !used[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("compose-go map: no such item output: %s", strings.Join(missing, ", "))
	}
	return nil
}

// DeriveComposeType returns the tf_type of t and, for objects (also as list or map elements), its fields merged
// with the fragment fields frag.
func DeriveComposeType(t reflect.Type, frag []Field) (string, []Field) {
	return composeDerive(t, frag, map[reflect.Type]bool{})
}

func composeDerive(t reflect.Type, frag []Field, stack map[reflect.Type]bool) (string, []Field) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if tf, ok := composeFixupTypes[t.Name()]; ok && t.PkgPath() == reflect.TypeOf(ctypes.Project{}).PkgPath() {
		return tf, nil
	}
	if kind, ok := composeMarshalKind(t); ok {
		return kind, nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if stack[t] {
			return "dynamic", nil
		}
		s := map[reflect.Type]bool{t: true}
		for k := range stack {
			s[k] = true
		}
		return "object({...})", composeFields(t, frag, s)
	case reflect.Map:
		et, fs := composeDerive(t.Elem(), frag, stack)
		return "map(" + et + ")", fs
	case reflect.Slice, reflect.Array:
		et, fs := composeDerive(t.Elem(), frag, stack)
		return "list(" + et + ")", fs
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "bool", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8,
		reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return "number", nil
	}
	return "dynamic", nil
}

// composeMarshalKind: the kind a type with its own MarshalJSON writes, from a sample value. ok is false when the
// type has none, writes an object or array (walked structurally), or is a struct fixup.
func composeMarshalKind(t reflect.Type) (string, bool) {
	if composeStructFixups[t.Name()] {
		return "", false
	}
	if !t.Implements(jsonMarshaler) && !reflect.PointerTo(t).Implements(jsonMarshaler) {
		return "", false
	}
	v := reflect.New(t)
	if t.Kind() == reflect.Struct {
		// A struct marshaler may write a scalar (SSHKey: its ID): fill string fields so the sample is not empty.
		for i := 0; i < t.NumField(); i++ {
			if f := v.Elem().Field(i); f.CanSet() && f.Kind() == reflect.String {
				f.SetString("x")
				break // the first string field only (SSHKey: its ID; with a Path compose-go writes invalid JSON)
			}
		}
	}
	m, ok := v.Interface().(json.Marshaler)
	if !ok {
		m, ok = v.Elem().Interface().(json.Marshaler)
	}
	if !ok {
		return "", false
	}
	b, err := m.MarshalJSON()
	if err != nil || len(b) == 0 {
		return "", false
	}
	switch b[0] {
	case '"':
		return "string", true
	case 't', 'f':
		return "bool", true
	case '{', '[', 'n':
		return "", false
	}
	return "number", true
}

type composeKey struct {
	key string
	typ reflect.Type
}

// composeJSONFields lists the JSON keys of struct t in encoding/json order rules: tagged name or field name,
// `-` and unexported skipped, untagged embedded structs flattened (outer fields win).
func composeJSONFields(t reflect.Type) []composeKey {
	var out []composeKey
	seen := map[string]bool{}
	var embedded []reflect.Type
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if f.Anonymous && name == "" && ft.Kind() == reflect.Struct {
			embedded = append(embedded, ft)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		seen[name] = true
		out = append(out, composeKey{name, f.Type})
	}
	for _, et := range embedded {
		for _, k := range composeJSONFields(et) {
			if !seen[k.key] {
				seen[k.key] = true
				out = append(out, k)
			}
		}
	}
	return out
}

func composeFields(t reflect.Type, frag []Field, stack map[reflect.Type]bool) []Field {
	byUp := map[string]Field{}
	for _, f := range frag {
		byUp[f.Upstream] = f
	}
	keys := composeJSONFields(t)
	sort.Slice(keys, func(i, j int) bool { return keys[i].key < keys[j].key })
	derived := map[string]Field{}
	for _, k := range keys {
		ff, had := byUp[k.key]
		tf, sub := composeDerive(k.typ, ff.Fields, stack)
		f := Field{Name: AttrName(k.key), Upstream: k.key, TFType: tf, Fields: sub}
		if had {
			f.Name, f.Description, f.Sensitive = ff.Name, ff.Description, ff.Sensitive
		}
		if composeSensitiveKeys[k.key] {
			f.Sensitive = true
		}
		derived[k.key] = f
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
		out = append(out, f) // provider-computed field with no compose-go key
	}
	for _, k := range keys {
		if !done[k.key] {
			out = append(out, derived[k.key])
			done[k.key] = true
		}
	}
	return out
}
