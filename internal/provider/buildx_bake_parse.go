package provider

// Pass 5 Lane E: reading bake definitions for docker_buildx_bake, as
// documented in the buildx bake reference (reference/upstream/buildx/docs/
// bake-reference.md and bake-stdlib.md):
//   - HCL and JSON files (both through hashicorp/hcl, as buildx does) and
//     compose files (through the compose seam: services with `build`);
//   - `variable` blocks (default, type, description; overrides come from
//     the action's `variables` argument, never from the environment, so a
//     run only depends on its configuration);
//   - `function` blocks (hcl userfunc) and the go-cty standard library;
//   - `group` blocks, `target` blocks with `inherits`, `matrix` and `name`;
//   - files merged in order: a later definition of a target overrides the
//     single-valued attributes and merges the rest (bake's merge rules).
// Not supported: `target.<name>.<attr>` references between targets, and
// `target:` named contexts (both need buildx's evaluation graph).

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/tryfunc"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/ext/userfunc"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// bakeFile is one input file (or the inline definition).
type bakeFile struct {
	Name string
	Data []byte
}

// bakeTarget is one target. Pointer and nil-slice fields mean "unset", so
// merges can tell an empty value from a missing one.
type bakeTarget struct {
	Name             string
	Description      *string
	Context          *string
	Contexts         map[string]string
	Dockerfile       *string
	DockerfileInline *string
	Args             map[string]*string
	Labels           map[string]*string
	Tags             []string
	Target           *string
	Call             *string
	Annotations      []string
	Attest           []string
	CacheFrom        []string
	CacheTo          []string
	Entitlements     []string
	ExtraHosts       []string
	NoCacheFilter    []string
	Outputs          []string
	Platforms        []string
	Secrets          []string
	SSH              []string
	Ulimits          []string
	Inherits         []string
	NoCache          *bool
	Pull             *bool
	Network          *string
	ShmSize          *string
	Resources        map[string]string
	Policy           []any
}

// bakeDef is a parsed definition.
type bakeDef struct {
	Groups      map[string][]string
	GroupOrder  []string
	Targets     map[string]*bakeTarget
	TargetOrder []string
	// Variables holds the resolved variable values (for --print debugging).
	Variables map[string]cty.Value
}

// bakeDefaultFiles is bake's lookup order when no file is given.
var bakeDefaultFiles = []string{
	"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml",
	"docker-bake.json", "docker-bake.hcl", "docker-bake.override.json", "docker-bake.override.hcl",
}

var bakeNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// bakeIsCompose reports a compose file by name or content.
func bakeIsCompose(f bakeFile) bool {
	ext := strings.ToLower(filepath.Ext(f.Name))
	if ext == ".yml" || ext == ".yaml" {
		return true
	}
	if ext == ".hcl" || ext == ".json" {
		return false
	}
	s := strings.TrimSpace(string(f.Data))
	return strings.HasPrefix(s, "services:") || strings.Contains(s, "\nservices:")
}

// bakeIsJSON reports a JSON bake file by name or content.
func bakeIsJSON(f bakeFile) bool {
	if strings.EqualFold(filepath.Ext(f.Name), ".json") {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(string(f.Data)), "{")
}

// ---------------------------------------------------------------------------
// Functions

// bakeFunctions is the function table: go-cty's stdlib plus the bake
// stdlib extras that are deterministic.
func bakeFunctions() map[string]function.Function {
	str1 := func(fn func(string) (string, error)) function.Function {
		return function.New(&function.Spec{
			Params: []function.Parameter{{Name: "str", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				s, err := fn(args[0].AsString())
				if err != nil {
					return cty.UnknownVal(cty.String), err
				}
				return cty.StringVal(s), nil
			},
		})
	}
	hashFn := func(sum func([]byte) []byte) function.Function {
		return str1(func(s string) (string, error) { return hex.EncodeToString(sum([]byte(s))), nil })
	}
	return map[string]function.Function{
		"absolute": stdlib.AbsoluteFunc, "add": stdlib.AddFunc, "and": stdlib.AndFunc,
		"base64decode": str1(func(s string) (string, error) {
			b, err := base64.StdEncoding.DecodeString(s)
			return string(b), err
		}),
		"base64encode": str1(func(s string) (string, error) { return base64.StdEncoding.EncodeToString([]byte(s)), nil }),
		"basename":     str1(func(s string) (string, error) { return path.Base(filepath.ToSlash(s)), nil }),
		"byteslen":     stdlib.BytesLenFunc, "bytesslice": stdlib.BytesSliceFunc,
		"can": tryfunc.CanFunc, "ceil": stdlib.CeilFunc, "chomp": stdlib.ChompFunc, "chunklist": stdlib.ChunklistFunc,
		"coalesce": stdlib.CoalesceFunc, "coalescelist": stdlib.CoalesceListFunc, "compact": stdlib.CompactFunc,
		"concat": stdlib.ConcatFunc, "contains": stdlib.ContainsFunc, "csvdecode": stdlib.CSVDecodeFunc,
		"dirname":  str1(func(s string) (string, error) { return path.Dir(filepath.ToSlash(s)), nil }),
		"distinct": stdlib.DistinctFunc, "divide": stdlib.DivideFunc, "element": stdlib.ElementFunc,
		"equal": stdlib.EqualFunc, "flatten": stdlib.FlattenFunc, "floor": stdlib.FloorFunc,
		"format": stdlib.FormatFunc, "formatdate": stdlib.FormatDateFunc, "formatlist": stdlib.FormatListFunc,
		"greaterthan": stdlib.GreaterThanFunc, "greaterthanorequalto": stdlib.GreaterThanOrEqualToFunc,
		"hasindex": stdlib.HasIndexFunc, "indent": stdlib.IndentFunc, "index": stdlib.IndexFunc,
		"int": stdlib.IntFunc, "join": stdlib.JoinFunc, "jsondecode": stdlib.JSONDecodeFunc, "jsonencode": stdlib.JSONEncodeFunc,
		"keys": stdlib.KeysFunc, "length": stdlib.LengthFunc, "lessthan": stdlib.LessThanFunc,
		"lessthanorequalto": stdlib.LessThanOrEqualToFunc, "log": stdlib.LogFunc, "lookup": stdlib.LookupFunc,
		"lower": stdlib.LowerFunc, "max": stdlib.MaxFunc,
		"md5":   hashFn(func(b []byte) []byte { s := md5.Sum(b); return s[:] }),
		"merge": stdlib.MergeFunc, "min": stdlib.MinFunc, "modulo": stdlib.ModuloFunc, "multiply": stdlib.MultiplyFunc,
		"negate": stdlib.NegateFunc, "not": stdlib.NotFunc, "notequal": stdlib.NotEqualFunc, "or": stdlib.OrFunc,
		"parseint": stdlib.ParseIntFunc, "pow": stdlib.PowFunc, "range": stdlib.RangeFunc, "regex": stdlib.RegexFunc,
		"regex_replace": stdlib.RegexReplaceFunc, "regexall": stdlib.RegexAllFunc, "replace": stdlib.ReplaceFunc,
		"reverse": stdlib.ReverseFunc, "reverselist": stdlib.ReverseListFunc,
		"sanitize": str1(func(s string) (string, error) {
			return regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(s, "_"), nil
		}),
		"sethaselement": stdlib.SetHasElementFunc, "setintersection": stdlib.SetIntersectionFunc,
		"setproduct": stdlib.SetProductFunc, "setsubtract": stdlib.SetSubtractFunc,
		"setsymmetricdifference": stdlib.SetSymmetricDifferenceFunc, "setunion": stdlib.SetUnionFunc,
		"sha1":   hashFn(func(b []byte) []byte { s := sha1.Sum(b); return s[:] }),
		"sha256": hashFn(func(b []byte) []byte { s := sha256.Sum256(b); return s[:] }),
		"sha512": hashFn(func(b []byte) []byte { s := sha512.Sum512(b); return s[:] }),
		"signum": stdlib.SignumFunc, "slice": stdlib.SliceFunc, "sort": stdlib.SortFunc, "split": stdlib.SplitFunc,
		"strlen": stdlib.StrlenFunc, "substr": stdlib.SubstrFunc, "subtract": stdlib.SubtractFunc,
		"timeadd": stdlib.TimeAddFunc, "title": stdlib.TitleFunc, "trim": stdlib.TrimFunc,
		"trimprefix": stdlib.TrimPrefixFunc, "trimspace": stdlib.TrimSpaceFunc, "trimsuffix": stdlib.TrimSuffixFunc,
		"try": tryfunc.TryFunc, "upper": stdlib.UpperFunc,
		"urlencode": str1(func(s string) (string, error) { return url.QueryEscape(s), nil }),
		"values":    stdlib.ValuesFunc, "zipmap": stdlib.ZipmapFunc,
	}
}

// ---------------------------------------------------------------------------
// Parsing

var (
	bakeFileSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "group", LabelNames: []string{"name"}},
		{Type: "target", LabelNames: []string{"name"}},
	}}
	bakeVariableSchema = &hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "default"}, {Name: "type"}, {Name: "description"}},
		Blocks:     []hcl.BlockHeaderSchema{{Type: "validation"}},
	}
)

// bakeVar is one declared variable.
type bakeVar struct {
	name    string
	def     hcl.Expression
	typ     cty.Type
	typed   bool
	defined bool
}

// bakeParse reads files (in merge order) with variable overrides.
func bakeParse(files []bakeFile, overrides map[string]string, compose func(bakeFile) (*bakeDef, error)) (*bakeDef, error) {
	def := &bakeDef{Groups: map[string][]string{}, Targets: map[string]*bakeTarget{}, Variables: map[string]cty.Value{}}
	parser := hclparse.NewParser()
	var bodies []hcl.Body
	for _, f := range files {
		if bakeIsCompose(f) {
			if compose == nil {
				return nil, fmt.Errorf("%s: compose files are not supported here", f.Name)
			}
			cd, err := compose(f)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			def.mergeDef(cd)
			continue
		}
		var hf *hcl.File
		var diags hcl.Diagnostics
		if bakeIsJSON(f) {
			hf, diags = parser.ParseJSON(f.Data, f.Name)
		} else {
			hf, diags = parser.ParseHCL(f.Data, f.Name)
		}
		if diags.HasErrors() {
			return nil, errors.New(diags.Error())
		}
		bodies = append(bodies, hf.Body)
	}
	if len(bodies) == 0 {
		return def, bakeCheckOverrides(nil, overrides)
	}

	ectx := &hcl.EvalContext{Variables: map[string]cty.Value{}, Functions: bakeFunctions()}
	ectx.Variables["BAKE_LOCAL_PLATFORM"] = cty.StringVal("linux/" + runtime.GOARCH)
	ectx.Variables["BAKE_CMD_CONTEXT"] = cty.StringVal(".")

	// User functions, then the rest of each body.
	var rest []hcl.Body
	for _, b := range bodies {
		funcs, remain, diags := userfunc.DecodeUserFunctions(b, "function", func() *hcl.EvalContext { return ectx })
		if diags.HasErrors() {
			return nil, errors.New(diags.Error())
		}
		for k, fn := range funcs {
			ectx.Functions[k] = fn
		}
		rest = append(rest, remain)
	}

	// Variables.
	vars := map[string]*bakeVar{}
	var varOrder []string
	var groups, targets []*hcl.Block
	for _, b := range rest {
		content, diags := b.Content(bakeFileSchema)
		if diags.HasErrors() {
			return nil, errors.New(diags.Error())
		}
		for _, blk := range content.Blocks {
			switch blk.Type {
			case "variable":
				vc, diags := blk.Body.Content(bakeVariableSchema)
				if diags.HasErrors() {
					return nil, errors.New(diags.Error())
				}
				name := blk.Labels[0]
				v, ok := vars[name]
				if !ok {
					v = &bakeVar{name: name}
					vars[name] = v
					varOrder = append(varOrder, name)
				}
				if a, ok := vc.Attributes["type"]; ok {
					ty, diags := typeexpr.TypeConstraint(a.Expr)
					if diags.HasErrors() {
						return nil, errors.New(diags.Error())
					}
					v.typ, v.typed = ty, true
				}
				if a, ok := vc.Attributes["default"]; ok {
					v.def, v.defined = a.Expr, true
				}
			case "group":
				groups = append(groups, blk)
			case "target":
				targets = append(targets, blk)
			}
		}
	}
	if err := bakeCheckOverrides(vars, overrides); err != nil {
		return nil, err
	}
	if err := bakeResolveVars(ectx, vars, varOrder, overrides); err != nil {
		return nil, err
	}
	for k, v := range ectx.Variables {
		def.Variables[k] = v
	}

	// Groups.
	for _, g := range groups {
		attrs, diags := g.Body.JustAttributes()
		if diags.HasErrors() {
			return nil, errors.New(diags.Error())
		}
		name := g.Labels[0]
		var tgts []string
		if a, ok := attrs["targets"]; ok {
			v, diags := a.Expr.Value(ectx)
			if diags.HasErrors() {
				return nil, errors.New(diags.Error())
			}
			if tgts, diags = bakeStrings(v, a.Expr.Range()); diags.HasErrors() {
				return nil, errors.New(diags.Error())
			}
		}
		if _, ok := def.Groups[name]; !ok {
			def.GroupOrder = append(def.GroupOrder, name)
		}
		def.Groups[name] = tgts // a later file overrides the group
	}

	// Targets (with matrix expansion).
	for _, blk := range targets {
		ts, err := bakeDecodeTarget(ectx, blk)
		if err != nil {
			return nil, err
		}
		if len(ts) > 1 || (len(ts) == 1 && ts[0].Name != blk.Labels[0]) {
			// A matrix target becomes a group of its variants.
			names := make([]string, 0, len(ts))
			for _, t := range ts {
				names = append(names, t.Name)
			}
			if _, ok := def.Groups[blk.Labels[0]]; !ok {
				def.GroupOrder = append(def.GroupOrder, blk.Labels[0])
			}
			def.Groups[blk.Labels[0]] = names
		}
		for _, t := range ts {
			def.addTarget(t)
		}
	}
	return def, nil
}

// bakeCheckOverrides rejects overrides of undeclared variables.
func bakeCheckOverrides(vars map[string]*bakeVar, overrides map[string]string) error {
	var unknown []string
	for k := range overrides {
		base := k
		if _, ok := vars[base]; !ok {
			if b, ok := strings.CutSuffix(k, "_JSON"); ok {
				if _, ok := vars[b]; ok {
					continue
				}
			}
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("variables %s are not declared in the bake definition (declare each with a variable block)", strings.Join(unknown, ", "))
	}
	return nil
}

// bakeResolveVars evaluates variable values in dependency order.
func bakeResolveVars(ectx *hcl.EvalContext, vars map[string]*bakeVar, order []string, overrides map[string]string) error {
	pending := append([]string(nil), order...)
	for pass := 0; len(pending) > 0; pass++ {
		var next []string
		var lastErr error
		for _, name := range pending {
			v := vars[name]
			val, err := bakeVarValue(ectx, v, overrides)
			if err != nil {
				var dep *bakeDepError
				if errors.As(err, &dep) {
					next = append(next, name)
					lastErr = err
					continue
				}
				return err
			}
			ectx.Variables[name] = val
		}
		if len(next) == len(pending) {
			return lastErr
		}
		pending = next
	}
	return nil
}

// bakeDepError is a variable default that refers to a variable not yet
// resolved.
type bakeDepError struct{ msg string }

func (e *bakeDepError) Error() string { return e.msg }

// bakeVarValue returns one variable's value: the override, else the
// default, else "" (an untyped variable without default is a string).
func bakeVarValue(ectx *hcl.EvalContext, v *bakeVar, overrides map[string]string) (cty.Value, error) {
	if s, ok := overrides[v.name+"_JSON"]; ok && v.typed {
		val, err := ctyjson.Unmarshal([]byte(s), v.typ)
		if err != nil {
			return cty.NilVal, fmt.Errorf("variable %s: %s_JSON is not a valid %s: %w", v.name, v.name, v.typ.FriendlyName(), err)
		}
		return val, nil
	}
	if s, ok := overrides[v.name]; ok {
		return bakeOverrideValue(v, s)
	}
	if !v.defined {
		if v.typed {
			return cty.NullVal(v.typ), nil
		}
		return cty.StringVal(""), nil
	}
	val, diags := v.def.Value(ectx)
	if diags.HasErrors() {
		for _, d := range diags {
			if strings.Contains(d.Summary, "Unknown variable") {
				return cty.NilVal, &bakeDepError{msg: fmt.Sprintf("variable %s: %s", v.name, diags.Error())}
			}
		}
		return cty.NilVal, fmt.Errorf("variable %s: %s", v.name, diags.Error())
	}
	if v.typed && !val.IsNull() {
		c, err := convert.Convert(val, v.typ)
		if err != nil {
			return cty.NilVal, fmt.Errorf("variable %s: default is not a %s: %w", v.name, v.typ.FriendlyName(), err)
		}
		val = c
	}
	return val, nil
}

// bakeOverrideValue converts an override string as bake does: primitives
// by conversion, lists and sets from CSV, other types from JSON.
func bakeOverrideValue(v *bakeVar, s string) (cty.Value, error) {
	if !v.typed {
		return cty.StringVal(s), nil
	}
	ty := v.typ
	switch {
	case ty.IsPrimitiveType() || ty == cty.DynamicPseudoType:
		if ty == cty.DynamicPseudoType {
			return cty.StringVal(s), nil
		}
		val, err := convert.Convert(cty.StringVal(s), ty)
		if err != nil {
			return cty.NilVal, fmt.Errorf("variable %s: %q is not a %s", v.name, s, ty.FriendlyName())
		}
		return val, nil
	case ty.IsListType() || ty.IsSetType():
		r := csv.NewReader(strings.NewReader(s))
		fields, err := r.Read()
		if err != nil {
			return cty.NilVal, fmt.Errorf("variable %s: %w", v.name, err)
		}
		vals := make([]cty.Value, 0, len(fields))
		for _, f := range fields {
			ev, err := convert.Convert(cty.StringVal(f), ty.ElementType())
			if err != nil {
				return cty.NilVal, fmt.Errorf("variable %s: %q is not a %s", v.name, f, ty.ElementType().FriendlyName())
			}
			vals = append(vals, ev)
		}
		if len(vals) == 0 {
			if ty.IsListType() {
				return cty.ListValEmpty(ty.ElementType()), nil
			}
			return cty.SetValEmpty(ty.ElementType()), nil
		}
		if ty.IsListType() {
			return cty.ListVal(vals), nil
		}
		return cty.SetVal(vals), nil
	default:
		val, err := ctyjson.Unmarshal([]byte(s), ty)
		if err != nil {
			return cty.NilVal, fmt.Errorf("variable %s: %s values are given as JSON: %w", v.name, ty.FriendlyName(), err)
		}
		return val, nil
	}
}

// bakeStrings turns a value into a string list.
func bakeStrings(v cty.Value, rng hcl.Range) ([]string, hcl.Diagnostics) {
	g, err := bakeGo(v)
	if err != nil {
		return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Invalid value", Detail: err.Error(), Subject: &rng}}
	}
	out, err := bakeAnyStrings(g)
	if err != nil {
		return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Invalid value", Detail: err.Error(), Subject: &rng}}
	}
	return out, nil
}

// bakeGo turns a cty value into plain Go (JSON) values.
func bakeGo(v cty.Value) (any, error) {
	if v.IsNull() {
		return nil, nil
	}
	if !v.IsWhollyKnown() {
		return nil, errors.New("value is not known")
	}
	b, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}

// bakeAnyString formats a scalar as bake does (numbers without
// trailing zeros, bools as true/false).
func bakeAnyString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	case map[string]any:
		// Structured entries (output, cache, secret, ssh) become CSV.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			s, err := bakeAnyString(t[k])
			if err != nil {
				return "", err
			}
			if strings.ContainsAny(s, ",\"") {
				s = `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
			}
			parts = append(parts, k+"="+s)
		}
		return strings.Join(parts, ","), nil
	case nil:
		return "", nil
	}
	return "", fmt.Errorf("expected a string, got %T", v)
}

func bakeAnyStrings(v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if e == nil {
				continue
			}
			s, err := bakeAnyString(e)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	case string:
		return []string{t}, nil
	}
	return nil, fmt.Errorf("expected a list of strings, got %T", v)
}

// bakeAnyMap turns an object into a string map; null values stay nil.
func bakeAnyMap(v any) (map[string]*string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		if v == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("expected a map, got %T", v)
	}
	out := make(map[string]*string, len(m))
	for k, e := range m {
		if e == nil {
			out[k] = nil
			continue
		}
		s, err := bakeAnyString(e)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = &s
	}
	return out, nil
}

// bakeDecodeTarget evaluates one target block, expanding its matrix.
func bakeDecodeTarget(ectx *hcl.EvalContext, blk *hcl.Block) ([]*bakeTarget, error) {
	attrs, diags := blk.Body.JustAttributes()
	if diags.HasErrors() {
		return nil, errors.New(diags.Error())
	}
	label := blk.Labels[0]
	norm := map[string]*hcl.Attribute{}
	for k, a := range attrs {
		norm[strings.ReplaceAll(k, "_", "-")] = a
	}
	matrixAttr, hasMatrix := norm["matrix"]
	if !hasMatrix {
		if na, ok := norm["name"]; ok {
			v, diags := na.Expr.Value(ectx)
			if diags.HasErrors() {
				return nil, errors.New(diags.Error())
			}
			if v.Type() == cty.String && !v.IsNull() && v.AsString() != label {
				return nil, fmt.Errorf("target %q: name can only be set together with matrix", label)
			}
		}
		t, err := bakeEvalTarget(ectx, label, norm)
		if err != nil {
			return nil, err
		}
		return []*bakeTarget{t}, nil
	}
	mv, diags := matrixAttr.Expr.Value(ectx)
	if diags.HasErrors() {
		return nil, errors.New(diags.Error())
	}
	if !mv.Type().IsObjectType() && !mv.Type().IsMapType() {
		return nil, fmt.Errorf("target %q: matrix must be a map of lists", label)
	}
	var keys []string
	axes := map[string][]cty.Value{}
	for it := mv.ElementIterator(); it.Next(); {
		k, v := it.Element()
		key := k.AsString()
		if !v.CanIterateElements() {
			return nil, fmt.Errorf("target %q: matrix.%s must be a list", label, key)
		}
		var vals []cty.Value
		for it2 := v.ElementIterator(); it2.Next(); {
			_, e := it2.Element()
			vals = append(vals, e)
		}
		keys = append(keys, key)
		axes[key] = vals
	}
	sort.Strings(keys)
	combos := []map[string]cty.Value{{}}
	for _, k := range keys {
		var next []map[string]cty.Value
		for _, c := range combos {
			for _, v := range axes[k] {
				n := map[string]cty.Value{}
				for kk, vv := range c {
					n[kk] = vv
				}
				n[k] = v
				next = append(next, n)
			}
		}
		combos = next
	}
	seen := map[string]bool{}
	var out []*bakeTarget
	for _, c := range combos {
		child := ectx.NewChild()
		child.Variables = c
		name := label
		if na, ok := norm["name"]; ok {
			v, diags := na.Expr.Value(child)
			if diags.HasErrors() {
				return nil, errors.New(diags.Error())
			}
			if v.IsNull() || v.Type() != cty.String {
				return nil, fmt.Errorf("target %q: name must be a string", label)
			}
			name = v.AsString()
		} else {
			parts := []string{label}
			for _, k := range keys {
				s, _ := bakeGo(c[k])
				str, _ := bakeAnyString(s)
				parts = append(parts, regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(str, "_"))
			}
			name = strings.Join(parts, "-")
		}
		if !bakeNameRe.MatchString(name) {
			return nil, fmt.Errorf("target %q: matrix name %q may only contain letters, digits, '_' and '-'", label, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("target %q: matrix produces the name %q twice; make name unique per combination", label, name)
		}
		seen[name] = true
		t, err := bakeEvalTarget(child, name, norm)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// bakeEvalTarget evaluates the attributes of a target.
func bakeEvalTarget(ectx *hcl.EvalContext, name string, attrs map[string]*hcl.Attribute) (*bakeTarget, error) {
	t := &bakeTarget{Name: name}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "matrix" || k == "name" {
			continue
		}
		a := attrs[k]
		v, diags := a.Expr.Value(ectx)
		if diags.HasErrors() {
			return nil, fmt.Errorf("target %q: %s", name, diags.Error())
		}
		g, err := bakeGo(v)
		if err != nil {
			return nil, fmt.Errorf("target %q: %s: %w", name, k, err)
		}
		if err := t.set(k, g); err != nil {
			return nil, fmt.Errorf("target %q: %s: %w", name, k, err)
		}
	}
	return t, nil
}

// set assigns one attribute by its bake name.
func (t *bakeTarget) set(k string, v any) error {
	strp := func() (*string, error) {
		if v == nil {
			return nil, nil
		}
		s, err := bakeAnyString(v)
		return &s, err
	}
	boolp := func() (*bool, error) {
		switch b := v.(type) {
		case nil:
			return nil, nil
		case bool:
			return &b, nil
		case string:
			x, err := strconv.ParseBool(b)
			return &x, err
		}
		return nil, fmt.Errorf("expected a bool, got %T", v)
	}
	list := func() ([]string, error) {
		l, err := bakeAnyStrings(v)
		if err == nil && l == nil && v != nil {
			l = []string{}
		}
		return l, err
	}
	var err error
	switch k {
	case "description":
		t.Description, err = strp()
	case "context":
		t.Context, err = strp()
	case "dockerfile":
		t.Dockerfile, err = strp()
	case "dockerfile-inline":
		t.DockerfileInline, err = strp()
	case "target":
		t.Target, err = strp()
	case "call":
		t.Call, err = strp()
	case "network":
		t.Network, err = strp()
	case "shm-size":
		t.ShmSize, err = strp()
	case "args":
		t.Args, err = bakeAnyMap(v)
	case "labels":
		t.Labels, err = bakeAnyMap(v)
	case "contexts", "resources":
		var m map[string]*string
		m, err = bakeAnyMap(v)
		out := map[string]string{}
		for kk, vv := range m {
			if vv != nil {
				out[kk] = *vv
			}
		}
		if k == "contexts" {
			t.Contexts = out
		} else {
			t.Resources = out
		}
	case "tags":
		t.Tags, err = list()
	case "annotations":
		t.Annotations, err = list()
	case "attest":
		t.Attest, err = list()
	case "cache-from":
		t.CacheFrom, err = list()
	case "cache-to":
		t.CacheTo, err = list()
	case "entitlements":
		t.Entitlements, err = list()
	case "extra-hosts":
		t.ExtraHosts, err = list()
	case "no-cache-filter":
		t.NoCacheFilter, err = list()
	case "output":
		t.Outputs, err = list()
	case "platforms":
		t.Platforms, err = list()
	case "secret":
		t.Secrets, err = list()
	case "ssh":
		t.SSH, err = list()
	case "ulimits":
		t.Ulimits, err = list()
	case "inherits":
		t.Inherits, err = list()
	case "no-cache":
		t.NoCache, err = boolp()
	case "pull":
		t.Pull, err = boolp()
	case "policy":
		switch p := v.(type) {
		case nil:
		case []any:
			t.Policy = p
		default:
			err = fmt.Errorf("expected a list, got %T", v)
		}
	default:
		return fmt.Errorf("unsupported target attribute %q", k)
	}
	return err
}

// addTarget merges t into the definition (a later definition of the same
// target merges into the earlier one).
func (d *bakeDef) addTarget(t *bakeTarget) {
	if old, ok := d.Targets[t.Name]; ok {
		old.merge(t)
		old.Inherits = bakeDedupe(append(old.Inherits[:len(old.Inherits):len(old.Inherits)], t.Inherits...))
		return
	}
	d.Targets[t.Name] = t
	d.TargetOrder = append(d.TargetOrder, t.Name)
}

// mergeDef merges another definition (a compose file) into d.
func (d *bakeDef) mergeDef(o *bakeDef) {
	for _, g := range o.GroupOrder {
		if _, ok := d.Groups[g]; !ok {
			d.GroupOrder = append(d.GroupOrder, g)
		}
		d.Groups[g] = o.Groups[g]
	}
	for _, n := range o.TargetOrder {
		d.addTarget(o.Targets[n])
	}
}

func bakeDedupe(s []string) []string {
	seen := map[string]bool{}
	out := s[:0:0]
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// merge applies t2 over t with bake's rules: single-valued attributes and
// tags, platforms, cache-to, output are overridden; maps and the other
// lists are merged.
func (t *bakeTarget) merge(t2 *bakeTarget) {
	override := func(dst **string, src *string) {
		if src != nil {
			*dst = src
		}
	}
	override(&t.Description, t2.Description)
	override(&t.Context, t2.Context)
	override(&t.Dockerfile, t2.Dockerfile)
	override(&t.DockerfileInline, t2.DockerfileInline)
	override(&t.Target, t2.Target)
	override(&t.Call, t2.Call)
	override(&t.Network, t2.Network)
	override(&t.ShmSize, t2.ShmSize)
	if t2.DockerfileInline != nil && t2.Dockerfile == nil {
		// An inline Dockerfile replaces an inherited file.
		t.Dockerfile = nil
	}
	mergeMap := func(dst *map[string]*string, src map[string]*string) {
		for k, v := range src {
			if v == nil {
				continue
			}
			if *dst == nil {
				*dst = map[string]*string{}
			}
			(*dst)[k] = v
		}
	}
	mergeMap(&t.Args, t2.Args)
	mergeMap(&t.Labels, t2.Labels)
	for k, v := range t2.Contexts {
		if t.Contexts == nil {
			t.Contexts = map[string]string{}
		}
		t.Contexts[k] = v
	}
	for k, v := range t2.Resources {
		if t.Resources == nil {
			t.Resources = map[string]string{}
		}
		t.Resources[k] = v
	}
	if t2.Tags != nil {
		t.Tags = t2.Tags
	}
	if t2.Platforms != nil {
		t.Platforms = t2.Platforms
	}
	if t2.CacheTo != nil {
		t.CacheTo = t2.CacheTo
	}
	if t2.Outputs != nil {
		t.Outputs = t2.Outputs
	}
	appendList := func(dst *[]string, src []string) {
		if src != nil {
			*dst = bakeDedupe(append(append([]string{}, *dst...), src...))
		}
	}
	appendList(&t.Annotations, t2.Annotations)
	appendList(&t.Attest, t2.Attest)
	appendList(&t.CacheFrom, t2.CacheFrom)
	appendList(&t.Secrets, t2.Secrets)
	appendList(&t.SSH, t2.SSH)
	appendList(&t.NoCacheFilter, t2.NoCacheFilter)
	appendList(&t.Ulimits, t2.Ulimits)
	appendList(&t.Entitlements, t2.Entitlements)
	appendList(&t.ExtraHosts, t2.ExtraHosts)
	if t2.Policy != nil {
		t.Policy = append(append([]any{}, t.Policy...), t2.Policy...)
	}
	if t2.Pull != nil {
		t.Pull = t2.Pull
	}
	if t2.NoCache != nil {
		t.NoCache = t2.NoCache
	}
}

// ---------------------------------------------------------------------------
// Resolution

// bakeResolved is the outcome of selecting targets.
type bakeResolved struct {
	// Targets are the resolved targets to build, in order.
	Targets []*bakeTarget
	// Groups are the groups the selection went through, plus default.
	Groups map[string][]string
}

// resolve expands the requested names (groups and targets) and applies
// inherits, overrides and defaults.
func (d *bakeDef) resolve(names []string, set map[string]string) (*bakeResolved, error) {
	if len(names) == 0 {
		names = []string{"default"}
	}
	res := &bakeResolved{Groups: map[string][]string{}}
	var order []string
	seen := map[string]bool{}
	var expand func(n string, stack []string) error
	expand = func(n string, stack []string) error {
		for _, s := range stack {
			if s == n {
				return fmt.Errorf("group cycle: %s -> %s", strings.Join(stack, " -> "), n)
			}
		}
		if g, ok := d.Groups[n]; ok {
			res.Groups[n] = g
			for _, x := range g {
				if err := expand(x, append(stack, n)); err != nil {
					return err
				}
			}
			return nil
		}
		if _, ok := d.Targets[n]; ok {
			if !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
			return nil
		}
		if n == "default" && len(stack) == 0 && len(d.TargetOrder) == 1 {
			// bake builds the only target when there is no default.
			return expand(d.TargetOrder[0], stack)
		}
		return fmt.Errorf("failed to find target or group %q (targets: %s; groups: %s)", n,
			strings.Join(d.TargetOrder, ", "), strings.Join(d.GroupOrder, ", "))
	}
	for _, n := range names {
		if err := expand(n, nil); err != nil {
			return nil, err
		}
	}
	if _, ok := res.Groups["default"]; !ok {
		res.Groups["default"] = append([]string(nil), names...)
	}
	for _, n := range order {
		t, err := d.inherit(n, nil)
		if err != nil {
			return nil, err
		}
		if err := t.applySet(set); err != nil {
			return nil, err
		}
		if t.Context == nil {
			s := "."
			t.Context = &s
		}
		if t.Dockerfile == nil && t.DockerfileInline == nil {
			s := "Dockerfile"
			t.Dockerfile = &s
		}
		res.Targets = append(res.Targets, t)
	}
	return res, nil
}

// inherit returns a copy of target n with its parents merged in.
func (d *bakeDef) inherit(n string, stack []string) (*bakeTarget, error) {
	for _, s := range stack {
		if s == n {
			return nil, fmt.Errorf("inherits cycle: %s -> %s", strings.Join(stack, " -> "), n)
		}
	}
	src, ok := d.Targets[n]
	if !ok {
		return nil, fmt.Errorf("target %q inherits from %q, which is not defined", stack[len(stack)-1], n)
	}
	out := &bakeTarget{Name: n}
	for _, p := range src.Inherits {
		pt, err := d.inherit(p, append(stack, n))
		if err != nil {
			return nil, err
		}
		out.merge(pt)
	}
	out.merge(src)
	out.Name = n
	out.Inherits = nil
	return out, nil
}

// applySet applies --set overrides: keys "<target>.<attr>[.<key>]", where
// target may be "*" or a path.Match pattern.
func (t *bakeTarget) applySet(set map[string]string) error {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pat, rest, ok := strings.Cut(k, ".")
		if !ok {
			return fmt.Errorf("set %q: keys are <target>.<attribute>", k)
		}
		if m, err := path.Match(pat, t.Name); err != nil {
			return fmt.Errorf("set %q: %w", k, err)
		} else if !m {
			continue
		}
		v := set[k]
		attr, sub, _ := strings.Cut(rest, ".")
		attr = strings.ReplaceAll(attr, "_", "-")
		switch attr {
		case "args", "labels", "contexts":
			if sub == "" {
				return fmt.Errorf("set %q: %s needs a key (%s.<key>)", k, attr, attr)
			}
			vv := v
			switch attr {
			case "args":
				if t.Args == nil {
					t.Args = map[string]*string{}
				}
				t.Args[sub] = &vv
			case "labels":
				if t.Labels == nil {
					t.Labels = map[string]*string{}
				}
				t.Labels[sub] = &vv
			default:
				if t.Contexts == nil {
					t.Contexts = map[string]string{}
				}
				t.Contexts[sub] = vv
			}
		case "platform":
			t.Platforms = strings.Split(v, ",")
		case "push":
			if b, _ := strconv.ParseBool(v); b {
				t.Outputs = append(t.Outputs, "type=registry")
			}
		case "load":
			if b, _ := strconv.ParseBool(v); b {
				t.Outputs = append(t.Outputs, "type=docker")
			}
		case "tags", "platforms", "output", "cache-to":
			if err := t.set(attr, bakeCSVList(v)); err != nil {
				return fmt.Errorf("set %q: %w", k, err)
			}
		case "annotations", "attest", "cache-from", "secrets", "secret", "ssh", "no-cache-filter", "ulimits", "entitlements", "extra-hosts":
			if attr == "secrets" {
				attr = "secret"
			}
			cur := &bakeTarget{}
			if err := cur.set(attr, bakeCSVList(v)); err != nil {
				return fmt.Errorf("set %q: %w", k, err)
			}
			t.merge(cur)
		default:
			if err := t.set(attr, v); err != nil {
				return fmt.Errorf("set %q: %w", k, err)
			}
		}
	}
	return nil
}

// bakeCSVList splits a --set list value: one entry, or several separated
// by ";" (bake uses repeated --set flags; a map holds one value per key).
func bakeCSVList(v string) []any {
	var out []any
	for _, p := range strings.Split(v, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Printing

// printMap is the target in `bake --print` form (dashed keys).
func (t *bakeTarget) printMap() map[string]any {
	m := map[string]any{}
	str := func(k string, v *string) {
		if v != nil {
			m[k] = *v
		}
	}
	lst := func(k string, v []string) {
		if v != nil {
			m[k] = v
		}
	}
	mp := func(k string, v map[string]*string) {
		if v == nil {
			return
		}
		out := map[string]string{}
		for kk, vv := range v {
			if vv != nil {
				out[kk] = *vv
			}
		}
		m[k] = out
	}
	str("description", t.Description)
	str("context", t.Context)
	str("dockerfile", t.Dockerfile)
	str("dockerfile-inline", t.DockerfileInline)
	str("target", t.Target)
	str("call", t.Call)
	str("network", t.Network)
	str("shm-size", t.ShmSize)
	mp("args", t.Args)
	mp("labels", t.Labels)
	if t.Contexts != nil {
		m["contexts"] = t.Contexts
	}
	if t.Resources != nil {
		m["resources"] = t.Resources
	}
	lst("tags", t.Tags)
	lst("annotations", t.Annotations)
	lst("attest", t.Attest)
	lst("cache-from", t.CacheFrom)
	lst("cache-to", t.CacheTo)
	lst("entitlements", t.Entitlements)
	lst("extra-hosts", t.ExtraHosts)
	lst("no-cache-filter", t.NoCacheFilter)
	lst("output", t.Outputs)
	lst("platforms", t.Platforms)
	lst("secret", t.Secrets)
	lst("ssh", t.SSH)
	lst("ulimits", t.Ulimits)
	if t.Policy != nil {
		m["policy"] = t.Policy
	}
	if t.NoCache != nil {
		m["no-cache"] = *t.NoCache
	}
	if t.Pull != nil {
		m["pull"] = *t.Pull
	}
	return m
}

// printObject is the `bake --print` document.
func (r *bakeResolved) printObject() map[string]any {
	groups := map[string]any{}
	for g, ts := range r.Groups {
		groups[g] = map[string]any{"targets": ts}
	}
	targets := map[string]any{}
	for _, t := range r.Targets {
		targets[t.Name] = t.printMap()
	}
	return map[string]any{"group": groups, "target": targets}
}

// printJSON is printObject as indented JSON.
func (r *bakeResolved) printJSON() []byte {
	b, _ := json.MarshalIndent(r.printObject(), "", "  ")
	return b
}

// printHCL renders the resolved definition as a bake HCL file.
func (r *bakeResolved) printHCL() []byte {
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	gnames := make([]string, 0, len(r.Groups))
	for g := range r.Groups {
		gnames = append(gnames, g)
	}
	sort.Strings(gnames)
	for _, g := range gnames {
		blk := body.AppendNewBlock("group", []string{g})
		blk.Body().SetAttributeValue("targets", bakeCty(r.Groups[g]))
		body.AppendNewline()
	}
	for i, t := range r.Targets {
		blk := body.AppendNewBlock("target", []string{t.Name})
		pm := t.printMap()
		keys := make([]string, 0, len(pm))
		for k := range pm {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			blk.Body().SetAttributeValue(k, bakeCty(pm[k]))
		}
		if i < len(r.Targets)-1 {
			body.AppendNewline()
		}
	}
	return hclwrite.Format(f.Bytes())
}

// bakeCty converts plain values for hclwrite.
func bakeCty(v any) cty.Value {
	switch t := v.(type) {
	case string:
		return cty.StringVal(t)
	case bool:
		return cty.BoolVal(t)
	case []string:
		if len(t) == 0 {
			return cty.ListValEmpty(cty.String)
		}
		vals := make([]cty.Value, 0, len(t))
		for _, s := range t {
			vals = append(vals, cty.StringVal(s))
		}
		return cty.ListVal(vals)
	case map[string]string:
		if len(t) == 0 {
			return cty.MapValEmpty(cty.String)
		}
		vals := map[string]cty.Value{}
		for k, s := range t {
			vals[k] = cty.StringVal(s)
		}
		return cty.MapVal(vals)
	case []any:
		vals := make([]cty.Value, 0, len(t))
		for _, e := range t {
			vals = append(vals, bakeCty(e))
		}
		return cty.TupleVal(vals)
	case map[string]any:
		vals := map[string]cty.Value{}
		for k, e := range t {
			vals[k] = bakeCty(e)
		}
		return cty.ObjectVal(vals)
	case float64:
		return cty.NumberFloatVal(t)
	case nil:
		return cty.NullVal(cty.DynamicPseudoType)
	}
	return cty.StringVal(fmt.Sprint(v))
}
