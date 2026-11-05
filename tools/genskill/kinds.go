package main

// Provider functions and the -strict description check for the Terraform
// 1.10+ kinds (actions, ephemeral resources, list resources, functions).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// funcParam is one parameter of a provider function.
type funcParam struct {
	Name        string
	Type        string
	Description string
	AllowNull   bool
	Variadic    bool
}

// funcInfo is the definition of a provider function.
type funcInfo struct {
	Summary     string
	Return      string
	Deprecation string
	Params      []funcParam // positional parameters, then the variadic one
}

// Signature renders the call shape, e.g.
// provider::docker::parse_ref(ref string, ...extra string) object({...}).
func (f funcInfo) Signature(addr string) string {
	parts := make([]string, len(f.Params))
	for i, p := range f.Params {
		prefix := ""
		if p.Variadic {
			prefix = "..."
		}
		parts[i] = prefix + p.Name + " " + p.Type
	}
	s := addr + "(" + strings.Join(parts, ", ") + ")"
	if f.Return != "" {
		s += " " + f.Return
	}
	return s
}

func walkFunction(name string, f *tfprotov6.Function) typeDoc {
	td := typeDoc{Name: name, Kind: kindFunction, Func: &funcInfo{}}
	if f == nil {
		return td
	}
	td.Description = strings.TrimSpace(f.Description)
	if td.Description == "" {
		td.Description = strings.TrimSpace(f.Summary)
	}
	fi := td.Func
	fi.Summary = strings.TrimSpace(f.Summary)
	fi.Deprecation = strings.TrimSpace(f.DeprecationMessage)
	if f.Return != nil {
		fi.Return = typeString(f.Return.Type)
	}
	add := func(p *tfprotov6.FunctionParameter, variadic bool) {
		if p == nil {
			return
		}
		fi.Params = append(fi.Params, funcParam{
			Name: p.Name, Type: typeString(p.Type), Description: strings.TrimSpace(p.Description),
			AllowNull: p.AllowNullValue, Variadic: variadic,
		})
	}
	for _, p := range f.Parameters {
		add(p, false)
	}
	add(f.VariadicParameter, true)
	return td
}

// descriptionViolations lists every action, ephemeral resource, list resource
// and function, and every attribute, block or parameter of one, that has no
// description. Sorted, one "address[.path] (what)" string per gap.
func descriptionViolations(resp *tfprotov6.GetProviderSchemaResponse) []string {
	var out []string
	check := func(addr string, s *tfprotov6.Schema) {
		if s == nil || s.Block == nil {
			out = append(out, addr+" (no schema)")
			return
		}
		if strings.TrimSpace(s.Block.Description) == "" {
			out = append(out, addr+" (type)")
		}
		blockDescriptionGaps(addr, s.Block, &out)
	}
	for name, a := range resp.ActionSchemas {
		var s *tfprotov6.Schema
		if a != nil {
			s = a.Schema
		}
		check("action."+name, s)
	}
	for name, s := range resp.EphemeralResourceSchemas {
		check("ephemeral."+name, s)
	}
	for name, s := range resp.ListResourceSchemas {
		check("list."+name, s)
	}
	for name, f := range resp.Functions {
		addr := "provider::docker::" + name
		if f == nil {
			out = append(out, addr+" (no definition)")
			continue
		}
		if strings.TrimSpace(f.Summary) == "" && strings.TrimSpace(f.Description) == "" {
			out = append(out, addr+" (function summary/description)")
		}
		params := append([]*tfprotov6.FunctionParameter{}, f.Parameters...)
		if f.VariadicParameter != nil {
			params = append(params, f.VariadicParameter)
		}
		for i, p := range params {
			if p == nil {
				continue
			}
			if strings.TrimSpace(p.Description) == "" {
				pn := p.Name
				if pn == "" {
					pn = fmt.Sprintf("#%d", i)
				}
				out = append(out, fmt.Sprintf("%s(%s) (parameter)", addr, pn))
			}
		}
	}
	sort.Strings(out)
	return out
}

func blockDescriptionGaps(prefix string, b *tfprotov6.SchemaBlock, out *[]string) {
	attributeDescriptionGaps(prefix, b.Attributes, out)
	for _, nb := range b.BlockTypes {
		if nb == nil {
			continue
		}
		p := prefix + "." + nb.TypeName
		if nb.Block == nil || strings.TrimSpace(nb.Block.Description) == "" {
			*out = append(*out, p+" (block)")
		}
		if nb.Block != nil {
			blockDescriptionGaps(p, nb.Block, out)
		}
	}
}

func attributeDescriptionGaps(prefix string, attrs []*tfprotov6.SchemaAttribute, out *[]string) {
	for _, a := range attrs {
		if a == nil {
			continue
		}
		p := prefix + "." + a.Name
		if strings.TrimSpace(a.Description) == "" {
			*out = append(*out, p+" (attribute)")
		}
		if a.NestedType != nil {
			attributeDescriptionGaps(p, a.NestedType.Attributes, out)
		}
	}
}
