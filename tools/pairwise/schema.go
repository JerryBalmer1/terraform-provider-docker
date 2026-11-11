package main

// The live provider schema, reduced to what the generator needs: for every
// attribute and nested block its kind, its Terraform type and, for blocks and
// nested attributes, its children. It comes from the provider itself
// (GetProviderSchema served in-process, as tools/genskill does), so a model
// that names an attribute the provider does not have fails.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/JerryBalmer1/terraform-provider-docker/internal/provider"
)

// nodeKind is how a schema node is written in HCL.
type nodeKind int

const (
	kindAttr       nodeKind = iota // name = <value of Type>
	kindNestedAttr                 // name = { ... } or [{ ... }] (nested attribute, Nesting below)
	kindBlock                      // name { ... } (repeated for list/set blocks)
)

// nesting is the shape of a block or nested attribute.
type nesting int

const (
	nestSingle nesting = iota
	nestList
	nestSet
	nestMap
)

// schemaNode is one attribute or block.
type schemaNode struct {
	Name     string
	Kind     nodeKind
	Type     tftypes.Type // kindAttr only
	Nesting  nesting      // kindNestedAttr and kindBlock
	Children map[string]*schemaNode
	Required bool
	Computed bool
	Optional bool
	MaxItems int64 // blocks only, 0 = no limit
}

// writable reports whether a configuration may set the node.
func (n *schemaNode) writable() bool {
	if n.Kind == kindBlock {
		return true
	}
	return n.Required || n.Optional
}

// resourceSchema is the root of one resource type.
type resourceSchema struct {
	Type     string
	Children map[string]*schemaNode
}

// loadProviderSchemas serves the provider in-process and returns the schema of
// each requested resource type.
func loadProviderSchemas(ctx context.Context, newProvider func() fwprovider.Provider, types []string) (map[string]*resourceSchema, error) {
	srv, err := providerserver.NewProtocol6WithError(newProvider())()
	if err != nil {
		return nil, fmt.Errorf("serving provider: %w", err)
	}
	resp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		return nil, fmt.Errorf("GetProviderSchema: %w", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return nil, fmt.Errorf("GetProviderSchema: %s: %s", d.Summary, d.Detail)
		}
	}
	out := map[string]*resourceSchema{}
	for _, t := range types {
		s, ok := resp.ResourceSchemas[t]
		if !ok || s == nil || s.Block == nil {
			return nil, fmt.Errorf("provider has no resource %q", t)
		}
		out[t] = &resourceSchema{Type: t, Children: convertBlock(s.Block)}
	}
	return out, nil
}

func defaultProvider() fwprovider.Provider { return provider.New("dev")() }

func convertBlock(b *tfprotov6.SchemaBlock) map[string]*schemaNode {
	out := map[string]*schemaNode{}
	for _, a := range b.Attributes {
		out[a.Name] = convertAttr(a)
	}
	for _, nb := range b.BlockTypes {
		n := &schemaNode{Name: nb.TypeName, Kind: kindBlock, Children: convertBlock(nb.Block), MaxItems: nb.MaxItems}
		switch nb.Nesting {
		case tfprotov6.SchemaNestedBlockNestingModeList, tfprotov6.SchemaNestedBlockNestingModeGroup:
			n.Nesting = nestList
		case tfprotov6.SchemaNestedBlockNestingModeSet:
			n.Nesting = nestSet
		case tfprotov6.SchemaNestedBlockNestingModeMap:
			n.Nesting = nestMap
		default:
			n.Nesting = nestSingle
		}
		out[nb.TypeName] = n
	}
	return out
}

func convertAttr(a *tfprotov6.SchemaAttribute) *schemaNode {
	n := &schemaNode{Name: a.Name, Required: a.Required, Optional: a.Optional, Computed: a.Computed}
	if a.NestedType == nil {
		n.Kind = kindAttr
		n.Type = a.Type
		return n
	}
	n.Kind = kindNestedAttr
	n.Children = map[string]*schemaNode{}
	for _, c := range a.NestedType.Attributes {
		n.Children[c.Name] = convertAttr(c)
	}
	switch a.NestedType.Nesting {
	case tfprotov6.SchemaObjectNestingModeList:
		n.Nesting = nestList
	case tfprotov6.SchemaObjectNestingModeSet:
		n.Nesting = nestSet
	case tfprotov6.SchemaObjectNestingModeMap:
		n.Nesting = nestMap
	default:
		n.Nesting = nestSingle
	}
	return n
}

// lookup resolves a dotted path (e.g. healthcheck.interval) to its node and
// the chain of nodes leading to it. Every segment but the last must be a
// block or a nested attribute.
func (s *resourceSchema) lookup(path string) ([]*schemaNode, error) {
	segs := strings.Split(path, ".")
	children := s.Children
	var chain []*schemaNode
	for i, seg := range segs {
		n, ok := children[seg]
		if !ok {
			where := s.Type
			if i > 0 {
				where = s.Type + "." + strings.Join(segs[:i], ".")
			}
			return nil, fmt.Errorf("%s has no attribute or block %q (known: %s)", where, seg, strings.Join(sortedKeys(children), ", "))
		}
		chain = append(chain, n)
		if i < len(segs)-1 {
			if n.Kind == kindAttr {
				return nil, fmt.Errorf("%s.%s is a plain attribute; %q cannot go below it", s.Type, strings.Join(segs[:i+1], "."), strings.Join(segs[i+1:], "."))
			}
			if n.Nesting == nestMap {
				return nil, fmt.Errorf("%s.%s is map-nested; give its whole value instead of a path below it", s.Type, strings.Join(segs[:i+1], "."))
			}
			children = n.Children
		}
	}
	if !chain[len(chain)-1].writable() {
		return nil, fmt.Errorf("%s.%s is computed only; a configuration cannot set it", s.Type, path)
	}
	return chain, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkValue validates a model value (decoded JSON) against node n. A value
// may be an hclExpr (raw HCL) anywhere; it is then not checked.
func checkValue(n *schemaNode, v any, where string) error {
	if v == nil {
		return nil
	}
	if _, ok := v.(hclExpr); ok {
		return nil
	}
	switch n.Kind {
	case kindAttr:
		return checkType(n.Type, v, where)
	default:
		obj := func(x any, w string) error {
			m, ok := x.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: want an object, got %s", w, describeJSON(x))
			}
			for _, k := range sortedKeys(m) {
				c, ok := n.Children[k]
				if !ok {
					return fmt.Errorf("%s: no attribute or block %q (known: %s)", w, k, strings.Join(sortedKeys(n.Children), ", "))
				}
				if !c.writable() {
					return fmt.Errorf("%s.%s is computed only", w, k)
				}
				if err := checkValue(c, m[k], w+"."+k); err != nil {
					return err
				}
			}
			return nil
		}
		switch n.Nesting {
		case nestSingle:
			return obj(v, where)
		case nestMap:
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: want a map of objects, got %s", where, describeJSON(v))
			}
			for _, k := range sortedKeys(m) {
				if err := obj(m[k], where+"["+k+"]"); err != nil {
					return err
				}
			}
			return nil
		default:
			l, ok := v.([]any)
			if !ok {
				return fmt.Errorf("%s: want a list of objects, got %s", where, describeJSON(v))
			}
			if n.Kind == kindBlock && n.MaxItems > 0 && int64(len(l)) > n.MaxItems {
				return fmt.Errorf("%s: %d items, the block allows %d", where, len(l), n.MaxItems)
			}
			for i, e := range l {
				if err := obj(e, fmt.Sprintf("%s[%d]", where, i)); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

func checkType(t tftypes.Type, v any, where string) error {
	if _, ok := v.(hclExpr); ok || v == nil {
		return nil
	}
	switch {
	case t.Is(tftypes.DynamicPseudoType):
		return nil
	case t.Is(tftypes.String):
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s: want a string, got %s", where, describeJSON(v))
		}
	case t.Is(tftypes.Number):
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%s: want a number, got %s", where, describeJSON(v))
		}
	case t.Is(tftypes.Bool):
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: want a bool, got %s", where, describeJSON(v))
		}
	case t.Is(tftypes.List{}) || t.Is(tftypes.Set{}) || t.Is(tftypes.Tuple{}):
		l, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: want a list, got %s", where, describeJSON(v))
		}
		var elem tftypes.Type
		switch tt := t.(type) {
		case tftypes.List:
			elem = tt.ElementType
		case tftypes.Set:
			elem = tt.ElementType
		}
		if elem == nil {
			return nil
		}
		for i, e := range l {
			if err := checkType(elem, e, fmt.Sprintf("%s[%d]", where, i)); err != nil {
				return err
			}
		}
	case t.Is(tftypes.Map{}):
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want a map, got %s", where, describeJSON(v))
		}
		elem := t.(tftypes.Map).ElementType
		for _, k := range sortedKeys(m) {
			if err := checkType(elem, m[k], where+"["+k+"]"); err != nil {
				return err
			}
		}
	case t.Is(tftypes.Object{}):
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object, got %s", where, describeJSON(v))
		}
		ot := t.(tftypes.Object)
		for _, k := range sortedKeys(m) {
			at, ok := ot.AttributeTypes[k]
			if !ok {
				return fmt.Errorf("%s: object has no attribute %q (known: %s)", where, k, strings.Join(sortedKeys(ot.AttributeTypes), ", "))
			}
			if err := checkType(at, m[k], where+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func describeJSON(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a bool"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

// typeString renders a node's type for the -schema listing.
func typeString(n *schemaNode) string {
	switch n.Kind {
	case kindAttr:
		return n.Type.String()
	case kindNestedAttr:
		return [...]string{"single", "list", "set", "map"}[n.Nesting] + "-nested attribute"
	default:
		return [...]string{"single", "list", "set", "map"}[n.Nesting] + " block"
	}
}

// dumpSchema lists every node of s, one per line, for model authors.
func dumpSchema(s *resourceSchema) []string {
	var out []string
	var walk func(prefix string, children map[string]*schemaNode)
	walk = func(prefix string, children map[string]*schemaNode) {
		for _, k := range sortedKeys(children) {
			n := children[k]
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flags := ""
			switch {
			case n.Kind == kindBlock:
				flags = "block"
			case n.Required:
				flags = "required"
			case n.Optional && n.Computed:
				flags = "optional+computed"
			case n.Optional:
				flags = "optional"
			default:
				flags = "computed"
			}
			out = append(out, fmt.Sprintf("%s\t%s\t%s", p, typeString(n), flags))
			if n.Children != nil {
				walk(p, n.Children)
			}
		}
	}
	walk("", s.Children)
	return out
}
