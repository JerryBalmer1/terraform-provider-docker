package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"sort"
	"strings"
	"unicode"
)

// Inventory is the part of reference/inventory.json contractgen reads.
// Unknown keys are ignored, so the inventory can grow without touching
// the generator.
type Inventory struct {
	Pins  map[string]string `json:"pins"`
	Items []Item            `json:"items"`
}

// Item is one inventory item.
type Item struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	MinAPI     *string  `json:"min_api"`
	SkipReason string   `json:"skip_reason"`
	Outputs    []Output `json:"outputs"`
	Inputs     []Input  `json:"inputs"`
}

// Output is one contract output of an item.
type Output struct {
	Name            string  `json:"name"`
	TFType          string  `json:"tf_type"`
	Category        string  `json:"category"`
	Sensitive       bool    `json:"sensitive"`
	SensitiveReason string  `json:"sensitive_reason"`
	KnownAfterApply bool    `json:"known_after_apply"`
	Description     string  `json:"description"`
	HashOf          string  `json:"hash_of"`
	Serializes      string  `json:"serializes"`
	Fields          []Field `json:"fields"`
	FieldsComplete  *bool   `json:"fields_complete"`
}

// Field is one node of an output's upstream -> snake_case mapping.
type Field struct {
	Name        string  `json:"name"`
	Upstream    string  `json:"upstream"`
	TFType      string  `json:"tf_type"`
	Sensitive   bool    `json:"sensitive"`
	Description string  `json:"description"`
	Fields      []Field `json:"fields"`
}

// Input is an argument an item's outputs depend on (the logs bounds).
type Input struct {
	Name        string          `json:"name"`
	TFType      string          `json:"tf_type"`
	Default     json.RawMessage `json:"default"`
	Upstream    string          `json:"upstream"`
	Description string          `json:"description"`
}

// KnownKinds are the item kinds the contract covers. Items of any other
// kind ("skip", or a kind added later) are left out.
var KnownKinds = map[string]bool{
	"resource": true, "data_source": true, "action": true, "dockerfile_block": true,
	"ephemeral_resource": true, "list_resource": true, "function": true,
}

// modelKinds get an embeddable model struct: kinds whose outputs are
// Terraform attributes. Actions (no computed attributes in TF 1.14) and
// functions (a return value, not attributes) do not.
var modelKinds = map[string]bool{
	"resource": true, "data_source": true, "dockerfile_block": true,
	"ephemeral_resource": true, "list_resource": true,
}

// Parse decodes the inventory and keeps the items the contract covers,
// sorted by id.
func Parse(raw []byte) (*Inventory, error) {
	var inv Inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, fmt.Errorf("decoding inventory: %w", err)
	}
	kept := inv.Items[:0]
	seen := map[string]bool{}
	for _, it := range inv.Items {
		if !KnownKinds[it.Kind] || it.SkipReason != "" || len(it.Outputs) == 0 {
			continue
		}
		if seen[it.ID] {
			return nil, fmt.Errorf("duplicate item id %q", it.ID)
		}
		seen[it.ID] = true
		kept = append(kept, it)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })
	inv.Items = kept
	return &inv, nil
}

// ModelOutput reports whether an output of an item of kind becomes a field
// of the item's model struct (and a schema attribute): raw, structured,
// hash and version outputs, plus result outputs of non-action items.
// Identity outputs are left to the lanes.
func ModelOutput(kind, category string) bool {
	switch category {
	case "raw", "structured", "hash", "version":
		return true
	case "result":
		return kind != "action"
	}
	return false
}

// GoFieldType is the terraform-plugin-framework value type of a top-level
// output, mirroring the runtime's node rules in contract.go: an object
// without fields (and dynamic) is a types.Dynamic at top level, because
// it has no list/set/map ancestor.
func GoFieldType(o Output) string {
	t := strings.TrimSpace(o.TFType)
	head := t
	if i := strings.IndexByte(t, '('); i >= 0 {
		head = t[:i]
	}
	switch head {
	case "string":
		return "types.String"
	case "number":
		return "types.Number"
	case "bool":
		return "types.Bool"
	case "dynamic":
		return "types.Dynamic"
	case "list":
		return "types.List"
	case "set":
		return "types.Set"
	case "map":
		return "types.Map"
	case "object":
		if len(o.Fields) > 0 || !strings.Contains(t, "{...}") {
			return "types.Object"
		}
		return "types.Dynamic"
	}
	return ""
}

var goAcronyms = map[string]string{
	"json": "JSON", "id": "ID", "api": "API", "yaml": "YAML", "hcl": "HCL", "sha256": "SHA256",
	"url": "URL", "tls": "TLS", "ip": "IP", "cpu": "CPU", "cli": "CLI", "os": "OS",
}

// GoFieldName turns an output name into an exported Go field name
// (inspect_json -> InspectJSON).
func GoFieldName(name string) string {
	var b strings.Builder
	for _, part := range strings.Split(name, "_") {
		if part == "" {
			continue
		}
		if a, ok := goAcronyms[part]; ok {
			b.WriteString(a)
			continue
		}
		r := []rune(part)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// StructName is the model struct of an item: "contract" + the id in
// CamelCase (engine:container -> contractEngineContainer).
func StructName(id string) string {
	var b strings.Builder
	b.WriteString("contract")
	for _, part := range strings.FieldsFunc(id, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		r := []rune(part)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// PinVersion turns an inventory pin ("v2.16.2 @ <sha>", "dockerfile/1.28.0
// @ <sha>", "main @ <sha>") into the version value the contract reports:
// the release ("v2.16.2", "1.28.0"), or "<branch>@<7-char sha>" when the
// pin follows a branch.
func PinVersion(pin string) string {
	ver, sha, _ := strings.Cut(pin, " @ ")
	ver = strings.TrimSpace(ver)
	if i := strings.LastIndexByte(ver, '/'); i >= 0 {
		ver = ver[i+1:]
	}
	if strings.IndexFunc(ver, unicode.IsDigit) >= 0 {
		return ver
	}
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if sha == "" {
		return ver
	}
	return ver + "@" + sha
}

// Generate renders contract_gen.go.
func Generate(inv *Inventory) ([]byte, error) {
	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("// Code generated by tools/contractgen. DO NOT EDIT.\n")
	w("// Source: reference/inventory.json. Regenerate: go run ./tools/contractgen\n\n")
	w("package provider\n\n")
	w("import (\n\t\"context\"\n\n\t\"github.com/hashicorp/terraform-plugin-framework/attr\"\n")
	w("\t\"github.com/hashicorp/terraform-plugin-framework/diag\"\n\t\"github.com/hashicorp/terraform-plugin-framework/types\"\n)\n\n")

	// Pins.
	w("// contractPins are the inventory pins (reference/SOURCES.md), verbatim.\n")
	w("var contractPins = map[string]string{\n")
	pinKeys := make([]string, 0, len(inv.Pins))
	for k := range inv.Pins {
		pinKeys = append(pinKeys, k)
	}
	sort.Strings(pinKeys)
	for _, k := range pinKeys {
		w("\t%q: %q,\n", k, inv.Pins[k])
	}
	w("}\n\n")
	w("// Version values derived from the pins (see PinVersion in tools/contractgen).\n")
	w("const (\n")
	w("\t// contractComposeSpecVersion is compose_spec_version.\n\tcontractComposeSpecVersion = %q\n", PinVersion(inv.Pins["compose_spec"]))
	w("\t// contractComposeGoVersion is compose_go_version.\n\tcontractComposeGoVersion = %q\n", PinVersion(inv.Pins["compose_go"]))
	w("\t// contractBuildxVersion is buildx_version.\n\tcontractBuildxVersion = %q\n", PinVersion(inv.Pins["buildx"]))
	w("\t// contractDockerfileDefaultSyntax is dockerfile_syntax when a Dockerfile has no # syntax= line.\n")
	w("\tcontractDockerfileDefaultSyntax = %q\n", "docker/dockerfile:"+PinVersion(inv.Pins["dockerfile"]))
	w(")\n\n")

	// Items table.
	w("// contractItems is the outputs contract of every item, keyed by item id.\n")
	w("var contractItems = map[string]*contractItem{\n")
	for _, it := range inv.Items {
		minAPI := ""
		if it.MinAPI != nil {
			minAPI = *it.MinAPI
		}
		w("\t%q: {\n\t\tID: %q, Kind: %q, Name: %q, MinAPI: %q,\n", it.ID, it.ID, it.Kind, it.Name, minAPI)
		w("\t\tOutputs: []contractOutput{\n")
		for _, o := range it.Outputs {
			fc := o.FieldsComplete == nil || *o.FieldsComplete
			w("\t\t\t{Name: %q, Category: %q, TFType: %q, Sensitive: %t, KnownAfterApply: %t, Description: %q",
				o.Name, o.Category, o.TFType, o.Sensitive, o.KnownAfterApply, o.Description)
			if o.HashOf != "" {
				w(", HashOf: %q", o.HashOf)
			}
			if o.Serializes != "" {
				w(", Serializes: %q", o.Serializes)
			}
			if o.SensitiveReason != "" {
				w(", SensitiveReason: %q", o.SensitiveReason)
			}
			if len(o.Fields) > 0 {
				w(", FieldsComplete: %t, Fields: ", fc)
				writeFields(&b, o.Fields, 3)
			}
			w("},\n")
		}
		w("\t\t},\n")
		if len(it.Inputs) > 0 {
			w("\t\tInputs: []contractInputSpec{\n")
			for _, in := range it.Inputs {
				def := strings.TrimSpace(string(in.Default))
				if def == "null" {
					def = ""
				}
				w("\t\t\t{Name: %q, TFType: %q, Default: %q, Upstream: %q, Description: %q},\n", in.Name, in.TFType, def, in.Upstream, in.Description)
			}
			w("\t\t},\n")
		}
		w("\t},\n")
	}
	w("}\n\n")

	// Model structs.
	names := map[string]string{}
	var registry []string
	for _, it := range inv.Items {
		if !modelKinds[it.Kind] {
			continue
		}
		var outs []Output
		for _, o := range it.Outputs {
			if ModelOutput(it.Kind, o.Category) {
				outs = append(outs, o)
			}
		}
		if len(outs) == 0 {
			continue
		}
		sn := StructName(it.ID)
		if other, dup := names[sn]; dup {
			return nil, fmt.Errorf("items %q and %q both map to struct %s", other, it.ID, sn)
		}
		names[sn] = it.ID
		registry = append(registry, it.ID)
		w("// %s holds the contract outputs of %s (%s %s). Embed it by value (not a\n", sn, it.ID, strings.ReplaceAll(it.Kind, "_", " "), it.Name)
		w("// pointer) in the type's tfsdk model, and call contractFill, contractSetUnknown\n")
		w("// or contractSetNull before resp.State.Set.\n")
		w("type %s struct {\n", sn)
		fields := map[string]string{}
		for _, o := range outs {
			fn := GoFieldName(o.Name)
			ft := GoFieldType(o)
			if ft == "" {
				return nil, fmt.Errorf("%s output %s: unsupported tf_type %q", it.ID, o.Name, o.TFType)
			}
			if other, dup := fields[fn]; dup {
				return nil, fmt.Errorf("%s outputs %s and %s both map to Go field %s", it.ID, other, o.Name, fn)
			}
			fields[fn] = o.Name
			w("\t%s %s `tfsdk:%q`\n", fn, ft, o.Name)
		}
		w("}\n\n")
		w("func (*%s) contractItemID() string { return %q }\n\n", sn, it.ID)
		w("func (m *%s) contractAssign(v map[string]attr.Value) {\n", sn)
		for _, o := range outs {
			w("\tm.%s = v[%q].(%s)\n", GoFieldName(o.Name), o.Name, GoFieldType(o))
		}
		w("}\n\n")
		w("// contractFill sets every contract output of %s from in (see contractFillInput).\n", it.ID)
		w("func (m *%s) contractFill(ctx context.Context, pd *providerData, in contractFillInput) diag.Diagnostics {\n", sn)
		w("\treturn contractFillModel(ctx, pd, m, in)\n}\n\n")
		w("// contractSetUnknown marks every contract output unknown (plan time).\n")
		w("func (m *%s) contractSetUnknown() { m.contractAssign(contractUnknownValues(%q)) }\n\n", sn, it.ID)
		w("// contractSetNull sets every contract output to a typed null.\n")
		w("func (m *%s) contractSetNull() { m.contractAssign(contractNullValues(%q)) }\n\n", sn, it.ID)
	}
	w("// contractModels returns a new model struct per item id that has one.\n")
	w("var contractModels = map[string]func() contractModel{\n")
	for _, id := range registry {
		w("\t%q: func() contractModel { return &%s{} },\n", id, StructName(id))
	}
	w("}\n")

	src, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("formatting generated code: %w", err)
	}
	return src, nil
}

func writeFields(b *bytes.Buffer, fs []Field, depth int) {
	ind := strings.Repeat("\t", depth)
	b.WriteString("[]contractField{\n")
	for _, f := range fs {
		fmt.Fprintf(b, "%s\t{Name: %q, Upstream: %q, TFType: %q", ind, f.Name, f.Upstream, f.TFType)
		if f.Sensitive {
			b.WriteString(", Sensitive: true")
		}
		if f.Description != "" {
			fmt.Fprintf(b, ", Description: %q", f.Description)
		}
		if len(f.Fields) > 0 {
			b.WriteString(", Fields: ")
			writeFields(b, f.Fields, depth+1)
		}
		b.WriteString("},\n")
	}
	b.WriteString(ind + "}")
}
