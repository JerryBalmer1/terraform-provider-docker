package provider

// Moving resource state from kreuzwerker/docker v4.6.0 (resource.ResourceWithMoveState).
//
// A configuration migrates with a cross-provider `moved` block:
//
//	moved {
//	  from = kreuzwerker_docker_container.web   # hypothetical local name, see the guide
//	  to   = docker_container.web
//	}
//
// Terraform (>= 1.8) then calls MoveResourceState on this provider with the
// kreuzwerker resource's raw state JSON (SDKv2 shapes: every block is a list,
// sets are JSON arrays, memory in MB, ...). Each managed type that has a
// kreuzwerker counterpart declares a kwMove (move_<type>.go): the kreuzwerker
// type name, its latest SDKv2 schema version and a list of mapping rules
// (kreuzwerker path -> our path, with the value transforms recorded in
// reference/_gen/renames.json).
//
// The target value is built against the resource's own schema at run time, not
// a Go struct: a mapped attribute the current schema does not have is skipped
// with a debug log, so the movers keep compiling and working while attribute
// names evolve. A list or set of objects is unwrapped when the target is a
// single object (kreuzwerker max-1 blocks), and scalars are coerced to the
// target type (string <-> number <-> bool). SDKv2 cannot tell an unset
// attribute from an empty one, so empty strings and empty collections become
// null. Terraform refreshes the moved object before planning, which fills the
// computed attributes left null here.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	units "github.com/docker/go-units"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// kreuzwerkerProviderAddress is the source provider of every kwMove.
const kreuzwerkerProviderAddress = "registry.terraform.io/kreuzwerker/docker"

// kwIsSource reports whether a MoveResourceState source provider address is
// kreuzwerker/docker (any registry host, so mirrors work too).
func kwIsSource(addr string) bool {
	a := strings.ToLower(strings.TrimSpace(addr))
	return a == kreuzwerkerProviderAddress || a == "kreuzwerker/docker" || strings.HasSuffix(a, "/kreuzwerker/docker")
}

// kwMove moves one kreuzwerker resource type into one of ours.
type kwMove struct {
	// sourceType is the kreuzwerker resource type name (docker_container).
	sourceType string
	// schemaVersion is the latest SDKv2 schema version of sourceType in
	// kreuzwerker v4.6.0; older states must be refreshed by kreuzwerker first.
	schemaVersion int64
	rules         []mvRule
	// finish runs after the rules (identity values that combine attributes).
	finish func(src, dst map[string]any)
}

// stateMovers returns the single StateMover of m (raw JSON, no SourceSchema).
func (m kwMove) stateMovers() []resource.StateMover {
	return []resource.StateMover{{StateMover: m.move}}
}

func (m kwMove) move(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if !kwIsSource(req.SourceProviderAddress) || req.SourceTypeName != m.sourceType {
		return // not ours: the framework tries the next mover or reports it
	}
	if req.SourceRawState == nil || len(req.SourceRawState.JSON) == 0 {
		resp.Diagnostics.AddError("Unable to Move Resource State",
			fmt.Sprintf("The %s state from %s has no JSON state (flatmap states from Terraform < 0.12 are not supported). "+
				"Run `terraform apply -refresh-only` with kreuzwerker/docker v4.6.0 first, then move again.", m.sourceType, req.SourceProviderAddress))
		return
	}
	if req.SourceSchemaVersion < m.schemaVersion {
		resp.Diagnostics.AddError("Unable to Move Resource State",
			fmt.Sprintf("The %s state has kreuzwerker schema version %d; moving needs version %d (kreuzwerker/docker v4.6.0). "+
				"Run `terraform apply -refresh-only` with kreuzwerker/docker v4.6.0 first so it upgrades the state, then move again.",
				m.sourceType, req.SourceSchemaVersion, m.schemaVersion))
		return
	}
	if req.SourceSchemaVersion > m.schemaVersion {
		tflog.Warn(ctx, "kreuzwerker state schema version is newer than v4.6.0; mapping it as v4.6.0", map[string]any{
			"source_type": m.sourceType, "source_schema_version": req.SourceSchemaVersion, "known_version": m.schemaVersion})
	}
	typ := resp.TargetState.Schema.Type().TerraformType(ctx)
	val, skipped, err := m.value(typ, req.SourceRawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Move Resource State",
			fmt.Sprintf("Mapping the %s state from %s failed: %s", m.sourceType, req.SourceProviderAddress, err))
		return
	}
	for _, s := range skipped {
		tflog.Debug(ctx, "kreuzwerker move: target attribute not in this schema or value not convertible; skipped",
			map[string]any{"source_type": m.sourceType, "detail": s})
	}
	resp.TargetState.Raw = val
	if resp.TargetIdentity != nil {
		var id *string
		if d := resp.TargetState.GetAttribute(ctx, path.Root("id"), &id); d.HasError() || id == nil || *id == "" {
			return
		}
		if d := resp.TargetIdentity.SetAttribute(ctx, path.Root("id"), *id); d.HasError() {
			tflog.Debug(ctx, "kreuzwerker move: identity has no id attribute; left null", map[string]any{"source_type": m.sourceType})
		}
	}
}

// value maps the kreuzwerker raw state JSON onto typ. skipped lists the
// target paths that typ does not have (or could not take the value).
func (m kwMove) value(typ tftypes.Type, raw []byte) (tftypes.Value, []string, error) {
	src, err := kwDecode(raw)
	if err != nil {
		return tftypes.Value{}, nil, err
	}
	dst := map[string]any{}
	for _, r := range m.rules {
		r.apply(src, dst)
	}
	if m.finish != nil {
		m.finish(src, dst)
	}
	var skipped []string
	v := kwToValue(typ, dst, "", &skipped)
	if v.IsNull() {
		return tftypes.Value{}, skipped, fmt.Errorf("no attribute of the source state maps to the target schema")
	}
	return v, skipped, nil
}

// kwDecode decodes a raw state JSON object, keeping numbers exact.
func kwDecode(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var src map[string]any
	if err := dec.Decode(&src); err != nil {
		return nil, fmt.Errorf("decoding the source state JSON: %w", err)
	}
	if src == nil {
		return nil, fmt.Errorf("the source state JSON is null")
	}
	return src, nil
}

// ---- rules ----------------------------------------------------------------

// mvRule is one mapping rule. pairs lists the (kreuzwerker path, our path)
// pairs it accounts for, the same dotted paths as reference/_gen/renames.json
// (TestMoveCoversRenames checks them).
type mvRule struct {
	pairs [][2]string
	apply func(src, dst map[string]any)
}

// kwGet returns the value at a dotted path of src. A list met before the last
// segment is a kreuzwerker max-1 block: its first element is used.
func kwGet(src map[string]any, p string) any {
	var cur any = src
	for _, seg := range strings.Split(p, ".") {
		cur = kwFirst(cur)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

// kwFirst returns the first element of a list (nil when empty), else v.
func kwFirst(v any) any {
	if l, ok := v.([]any); ok {
		if len(l) == 0 {
			return nil
		}
		return l[0]
	}
	return v
}

// kwSet stores v at a dotted path of dst, creating intermediate objects.
// A nil v is not stored.
func kwSet(dst map[string]any, p string, v any) {
	if v == nil {
		return
	}
	segs := strings.Split(p, ".")
	cur := dst
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = v
}

// kwObjects returns the elements of a block list/set that are objects.
func kwObjects(v any) []map[string]any {
	var out []map[string]any
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	case map[string]any:
		out = append(out, t)
	}
	return out
}

func prefixPairs(pairs [][2]string, from, to string) [][2]string {
	out := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, [2]string{from + "." + p[0], to + "." + p[1]})
	}
	return out
}

// cp copies from -> to, through optional value transforms (a transform
// returning nil drops the value).
func cp(from, to string, fns ...func(any) any) mvRule {
	return mvRule{
		pairs: [][2]string{{from, to}},
		apply: func(src, dst map[string]any) {
			v := kwGet(src, from)
			for _, f := range fns {
				if v == nil {
					return
				}
				v = f(v)
			}
			kwSet(dst, to, v)
		},
	}
}

// same copies each named attribute to the same path.
func same(names ...string) []mvRule {
	out := make([]mvRule, 0, len(names))
	for _, n := range names {
		out = append(out, cp(n, n))
	}
	return out
}

// blocks maps a kreuzwerker block list/set to our list of objects (or single
// object: the converter unwraps), applying sub to each element.
func blocks(from, to string, sub ...mvRule) mvRule {
	pairs := [][2]string{{from, to}}
	for _, s := range sub {
		pairs = append(pairs, prefixPairs(s.pairs, from, to)...)
	}
	return mvRule{
		pairs: pairs,
		apply: func(src, dst map[string]any) {
			var out []any
			for _, e := range kwObjects(kwGet(src, from)) {
				d := map[string]any{}
				for _, s := range sub {
					s.apply(e, d)
				}
				if len(d) > 0 {
					out = append(out, d)
				}
			}
			if len(out) > 0 {
				kwSet(dst, to, out)
			}
		},
	}
}

// blocksMap maps a kreuzwerker block list/set to our map of objects keyed by
// the element's key attribute, merging into an existing map at to. key may
// list fallbacks separated by "|" (name|id: the first non-empty one wins).
func blocksMap(from, to, key string, sub ...mvRule) mvRule {
	keys := strings.Split(key, "|")
	pairs := [][2]string{{from, to}, {from + "." + keys[0], to}}
	for _, s := range sub {
		pairs = append(pairs, prefixPairs(s.pairs, from, to)...)
	}
	return mvRule{
		pairs: pairs,
		apply: func(src, dst map[string]any) {
			for _, e := range kwObjects(kwGet(src, from)) {
				k := ""
				for _, kk := range keys {
					if k = kwString(e[kk]); k != "" {
						break
					}
				}
				if k == "" {
					continue
				}
				m, _ := kwGet(dst, to).(map[string]any)
				if m == nil {
					m = map[string]any{}
					kwSet(dst, to, m)
				}
				d, _ := m[k].(map[string]any)
				if d == nil {
					d = map[string]any{}
				}
				for _, s := range sub {
					s.apply(e, d)
				}
				m[k] = d
			}
		},
	}
}

// labelsRule maps a kreuzwerker set of {label, value} blocks to a map(string).
func labelsRule(from, to string) mvRule {
	return mvRule{
		pairs: [][2]string{{from, to}, {from + ".label", to}, {from + ".value", to}},
		apply: func(src, dst map[string]any) {
			out := map[string]any{}
			for _, e := range kwObjects(kwGet(src, from)) {
				if k, ok := e["label"].(string); ok && k != "" {
					out[k] = kwString(e["value"])
				}
			}
			if len(out) > 0 {
				kwSet(dst, to, out)
			}
		},
	}
}

// custom is a rule with hand-written logic; pairs are the renames.json pairs
// it accounts for.
func custom(pairs [][2]string, fn func(src, dst map[string]any)) mvRule {
	return mvRule{pairs: pairs, apply: fn}
}

// note accounts for renames.json pairs that need no state (config-only, or
// handled by another rule or by finish).
func note(pairs ...[2]string) mvRule {
	return mvRule{pairs: pairs, apply: func(_, _ map[string]any) {}}
}

// ---- value transforms -----------------------------------------------------

func kwString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// kwNumber returns v as a *big.Float when it is numeric (or a numeric string).
func kwNumber(v any) (*big.Float, bool) {
	var s string
	switch t := v.(type) {
	case json.Number:
		s = t.String()
	case string:
		s = strings.TrimSpace(t)
	case int:
		return new(big.Float).SetInt64(int64(t)), true
	case int64:
		return new(big.Float).SetInt64(t), true
	case float64:
		return big.NewFloat(t), true
	case *big.Float:
		return t, true
	default:
		return nil, false
	}
	f, _, err := big.ParseFloat(s, 10, 512, big.ToNearestEven)
	if err != nil {
		return nil, false
	}
	return f, true
}

// tfMBToBytes: number MB -> number bytes (x 1048576); keepNeg keeps -1.
func tfMBToBytes(keepNeg bool) func(any) any {
	return func(v any) any {
		f, ok := kwNumber(v)
		if !ok {
			return nil
		}
		if f.Sign() < 0 && keepNeg {
			return int64(-1)
		}
		return new(big.Float).Mul(f, big.NewFloat(1048576))
	}
}

// tfCPUsToNano: "1.5" -> 1500000000.
func tfCPUsToNano(v any) any {
	f, ok := kwNumber(v)
	if !ok || f.Sign() == 0 {
		return nil
	}
	n, _ := new(big.Float).Mul(f, big.NewFloat(1e9)).Int64()
	return n
}

// tfSecondsToDuration: 60 -> "60s".
func tfSecondsToDuration(v any) any {
	f, ok := kwNumber(v)
	if !ok {
		return nil
	}
	return f.Text('f', -1) + "s"
}

// tfKVListToMap: ["A=1", "B"] -> {A: "1", B: ""}.
func tfKVListToMap(v any) any {
	l, ok := v.([]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for _, e := range l {
		s := kwString(e)
		if s == "" {
			continue
		}
		k, val, _ := strings.Cut(s, "=")
		out[k] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tfBase64 base64-encodes a plain string (docker_config data_raw -> data).
func tfBase64(v any) any {
	s := kwString(v)
	if s == "" {
		return nil
	}
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// ---- tftypes conversion ---------------------------------------------------

// kwToValue converts the generic value v to a tftypes.Value of typ. Paths of
// v that typ lacks, or values that cannot be converted, are appended to
// skipped and become null.
func kwToValue(typ tftypes.Type, v any, at string, skipped *[]string) tftypes.Value {
	null := tftypes.NewValue(typ, nil)
	if v == nil {
		return null
	}
	join := func(k string) string {
		if at == "" {
			return k
		}
		return at + "." + k
	}
	switch {
	case typ.Is(tftypes.DynamicPseudoType):
		*skipped = append(*skipped, at+" (dynamic attribute: not moved)")
		return null
	case typ.Is(tftypes.Object{}):
		ot := typ.(tftypes.Object)
		if l, ok := v.([]any); ok { // max-1 block -> single object
			if len(l) > 1 {
				*skipped = append(*skipped, fmt.Sprintf("%s (%d elements into a single object: kept the first)", at, len(l)))
			}
			v = kwFirst(l)
		}
		m, ok := v.(map[string]any)
		if !ok {
			if v != nil {
				*skipped = append(*skipped, at+" (not an object)")
			}
			return null
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, ok := ot.AttributeTypes[k]; !ok && !kwEmpty(m[k]) {
				*skipped = append(*skipped, join(k)+" (not in the target schema)")
			}
		}
		vals := make(map[string]tftypes.Value, len(ot.AttributeTypes))
		has := false
		for k, at2 := range ot.AttributeTypes {
			vals[k] = kwToValue(at2, m[k], join(k), skipped)
			if !vals[k].IsNull() {
				has = true
			}
		}
		if !has {
			return null
		}
		return tftypes.NewValue(ot, vals)
	case typ.Is(tftypes.List{}), typ.Is(tftypes.Set{}):
		var et tftypes.Type
		if lt, ok := typ.(tftypes.List); ok {
			et = lt.ElementType
		} else {
			et = typ.(tftypes.Set).ElementType
		}
		var l []any
		switch t := v.(type) {
		case []any:
			l = t
		case map[string]any:
			l = []any{t}
		default:
			l = []any{t}
		}
		var elems []tftypes.Value
		for i, e := range l {
			ev := kwToValue(et, e, fmt.Sprintf("%s[%d]", at, i), skipped)
			if !ev.IsNull() {
				elems = append(elems, ev)
			}
		}
		if len(elems) == 0 {
			return null
		}
		return tftypes.NewValue(typ, elems)
	case typ.Is(tftypes.Map{}):
		et := typ.(tftypes.Map).ElementType
		m, ok := v.(map[string]any)
		if !ok {
			*skipped = append(*skipped, at+" (not a map)")
			return null
		}
		elems := map[string]tftypes.Value{}
		for k, e := range m {
			ev := kwToValue(et, e, join(k), skipped)
			if ot, isObj := et.(tftypes.Object); isObj && ev.IsNull() {
				if _, isMap := e.(map[string]any); isMap {
					// A keyed entry is the data (networks = { app_net = {} }):
					// keep it with null attributes.
					vals := make(map[string]tftypes.Value, len(ot.AttributeTypes))
					for an, at2 := range ot.AttributeTypes {
						vals[an] = tftypes.NewValue(at2, nil)
					}
					ev = tftypes.NewValue(ot, vals)
				}
			}
			if !ev.IsNull() {
				elems[k] = ev
			}
		}
		if len(elems) == 0 {
			return null
		}
		return tftypes.NewValue(typ, elems)
	case typ.Is(tftypes.String):
		switch v.(type) {
		case map[string]any, []any:
			*skipped = append(*skipped, at+" (not a string)")
			return null
		}
		s := kwString(v)
		if s == "" {
			return null
		}
		return tftypes.NewValue(typ, s)
	case typ.Is(tftypes.Number):
		f, ok := kwNumber(v)
		if !ok {
			if s, isStr := v.(string); isStr && s == "" {
				return null
			}
			*skipped = append(*skipped, fmt.Sprintf("%s (%v is not a number)", at, v))
			return null
		}
		return tftypes.NewValue(typ, f)
	case typ.Is(tftypes.Bool):
		switch t := v.(type) {
		case bool:
			return tftypes.NewValue(typ, t)
		case string:
			if b, err := strconv.ParseBool(t); err == nil {
				return tftypes.NewValue(typ, b)
			}
		}
		*skipped = append(*skipped, fmt.Sprintf("%s (%v is not a bool)", at, v))
		return null
	}
	*skipped = append(*skipped, at+" (unsupported target type "+typ.String()+")")
	return null
}

// tfMemoryString: "512m" / "1g" / "1048576" -> bytes (docker/go-units RAMInBytes).
func tfMemoryString(v any) any {
	s := strings.TrimSpace(kwString(v))
	if s == "" {
		return nil
	}
	n, err := units.RAMInBytes(s)
	if err != nil {
		return v // left as is: the converter skips it if the target is a number
	}
	return n
}

// nonEmpty returns s, or nil when s is empty.
func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// kwRepoOf strips the tag and digest from an image reference
// ("host:5000/app:1.0" -> "host:5000/app").
func kwRepoOf(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	slash := strings.LastIndex(ref, "/")
	if c := strings.LastIndex(ref, ":"); c > slash {
		ref = ref[:c]
	}
	return ref
}

// kwEmpty reports whether v is null, "" or an empty list or map (SDKv2's
// representation of an unset attribute).
func kwEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}
