package main

// Framework-level schema walk. The protocol schema (GetProviderSchema) has no
// plan modifiers, so the only way to know for certain that an attribute forces
// replacement is to ask each resource for its terraform-plugin-framework
// schema and inspect the plan modifiers directly.

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// fwNode is one attribute or block found by the framework walk.
type fwNode struct {
	Path            string // dotted path from the resource root, e.g. build.context
	Block           bool
	RequiresReplace bool // carries a RequiresReplace* plan modifier
	Description     string
}

// replaceSet maps resource type -> dotted path -> true for every attribute or
// block whose framework schema carries a RequiresReplace* plan modifier.
type replaceSet map[string]map[string]bool

func (r replaceSet) has(typeName, path string) bool {
	if r == nil {
		return false
	}
	return r[typeName][path]
}

// violation is an attribute or block with a RequiresReplace plan modifier
// whose description does not say it forces replacement.
type violation struct {
	Type  string
	Path  string
	Block bool
}

func (v violation) String() string {
	kind := "attribute"
	if v.Block {
		kind = "block"
	}
	return fmt.Sprintf("%s.%s (%s)", v.Type, v.Path, kind)
}

// frameworkResources returns each resource of p keyed by its full type name
// with the nodes of its framework schema.
func frameworkResources(ctx context.Context, p provider.Provider) (map[string][]fwNode, error) {
	var meta provider.MetadataResponse
	p.Metadata(ctx, provider.MetadataRequest{}, &meta)
	out := map[string][]fwNode{}
	for _, newRes := range p.Resources(ctx) {
		r := newRes()
		var mr resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: meta.TypeName}, &mr)
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		if sr.Diagnostics.HasError() {
			return nil, fmt.Errorf("schema of %s: %v", mr.TypeName, sr.Diagnostics)
		}
		out[mr.TypeName] = walkFrameworkSchema(ctx, sr.Schema)
	}
	return out, nil
}

// walkFrameworkSchema flattens every attribute and nested block of s, sorted
// by path.
func walkFrameworkSchema(ctx context.Context, s rschema.Schema) []fwNode {
	var out []fwNode
	walkFwAttributes(ctx, "", s.Attributes, &out)
	walkFwBlocks(ctx, "", s.Blocks, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func walkFwAttributes(ctx context.Context, prefix string, attrs map[string]rschema.Attribute, out *[]fwNode) {
	for name, a := range attrs {
		p := joinPath(prefix, name)
		*out = append(*out, fwNode{
			Path:            p,
			RequiresReplace: hasRequiresReplace(ctx, a),
			Description:     describe(a.GetDescription(), a.GetMarkdownDescription()),
		})
		switch v := a.(type) {
		case rschema.ListNestedAttribute:
			walkFwAttributes(ctx, p, v.NestedObject.Attributes, out)
		case rschema.SetNestedAttribute:
			walkFwAttributes(ctx, p, v.NestedObject.Attributes, out)
		case rschema.MapNestedAttribute:
			walkFwAttributes(ctx, p, v.NestedObject.Attributes, out)
		case rschema.SingleNestedAttribute:
			walkFwAttributes(ctx, p, v.Attributes, out)
		}
	}
}

func walkFwBlocks(ctx context.Context, prefix string, blocks map[string]rschema.Block, out *[]fwNode) {
	for name, b := range blocks {
		p := joinPath(prefix, name)
		node := fwNode{
			Path:            p,
			Block:           true,
			RequiresReplace: hasRequiresReplace(ctx, b),
			Description:     describe(b.GetDescription(), b.GetMarkdownDescription()),
		}
		var attrs map[string]rschema.Attribute
		var children map[string]rschema.Block
		switch v := b.(type) {
		case rschema.ListNestedBlock:
			attrs, children = v.NestedObject.Attributes, v.NestedObject.Blocks
			node.RequiresReplace = node.RequiresReplace || hasRequiresReplace(ctx, v.NestedObject)
		case rschema.SetNestedBlock:
			attrs, children = v.NestedObject.Attributes, v.NestedObject.Blocks
			node.RequiresReplace = node.RequiresReplace || hasRequiresReplace(ctx, v.NestedObject)
		case rschema.SingleNestedBlock:
			attrs, children = v.Attributes, v.Blocks
		}
		*out = append(*out, node)
		walkFwAttributes(ctx, p, attrs, out)
		walkFwBlocks(ctx, p, children, out)
	}
}

func describe(desc, md string) string {
	if strings.TrimSpace(md) != "" {
		return strings.TrimSpace(md)
	}
	return strings.TrimSpace(desc)
}

// planModifiers calls every exported, argument-free method of v whose name
// ends in PlanModifiers (StringPlanModifiers, ListPlanModifiers,
// ObjectPlanModifiers, ...) and returns the elements of the slices they
// return. Reflection keeps this independent of the dozen attribute types.
func planModifiers(v any) []any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	var out []any
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if !strings.HasSuffix(m.Name, "PlanModifiers") {
			continue
		}
		fn := rv.Method(i)
		if fn.Type().NumIn() != 0 || fn.Type().NumOut() != 1 || fn.Type().Out(0).Kind() != reflect.Slice {
			continue
		}
		s := fn.Call(nil)[0]
		for j := 0; j < s.Len(); j++ {
			if e := s.Index(j); e.IsValid() && !(e.Kind() == reflect.Interface && e.IsNil()) {
				out = append(out, e.Interface())
			}
		}
	}
	return out
}

// frameworkPkgPrefix is where the framework's built-in plan modifier packages
// (stringplanmodifier, listplanmodifier, ...) live.
const frameworkPkgPrefix = "github.com/hashicorp/terraform-plugin-framework/resource/schema/"

// isRequiresReplace reports whether a plan modifier is one of the framework's
// RequiresReplace, RequiresReplaceIf or RequiresReplaceIfConfigured. In
// framework v1.19.0 all three return the unexported type
// requiresReplaceIfModifier in each <type>planmodifier package; the built-in
// descriptions say "Terraform will destroy and recreate the resource", which
// is the fallback for custom modifiers that say the same.
func isRequiresReplace(ctx context.Context, m any) bool {
	t := reflect.TypeOf(m)
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if strings.HasPrefix(t.PkgPath(), frameworkPkgPrefix) && strings.HasPrefix(t.Name(), "requiresReplace") {
		return true
	}
	if d, ok := m.(interface{ Description(context.Context) string }); ok {
		if strings.Contains(strings.ToLower(d.Description(ctx)), "destroy and recreate") {
			return true
		}
	}
	return false
}

func hasRequiresReplace(ctx context.Context, v any) bool {
	for _, m := range planModifiers(v) {
		if isRequiresReplace(ctx, m) {
			return true
		}
	}
	return false
}

// frameworkReplacements builds the replace set and the strict violations from
// framework schemas keyed by resource type.
func frameworkReplacements(byType map[string][]fwNode) (replaceSet, []violation) {
	set := replaceSet{}
	var vs []violation
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		for _, n := range byType[t] {
			if !n.RequiresReplace {
				continue
			}
			if set[t] == nil {
				set[t] = map[string]bool{}
			}
			set[t][n.Path] = true
			if !forcesReplacement(n.Description) {
				vs = append(vs, violation{Type: t, Path: n.Path, Block: n.Block})
			}
		}
	}
	return set, vs
}
