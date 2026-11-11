package main

// The outputs contract's field maps (reference/inventory.json, the same data
// internal/provider/contract_gen.go is generated from): each structured
// output lists its fields as snake_case name -> upstream (Engine API) name,
// nested. A model names an inspect field by its snake_case path; the
// generator resolves it to the upstream path through these maps, so there is
// no second, hand-written mapping. Below the levels the contract lists
// (fields_complete false), a segment is kept as "?<snake>" and matched at
// compare time against the inspect JSON's keys by normalized name, which is
// the contract's rule for unlisted levels (snake_case of the upstream name).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type contractField struct {
	Name     string          `json:"name"`
	Upstream string          `json:"upstream"`
	TFType   string          `json:"tf_type"`
	Fields   []contractField `json:"fields"`
}

type contractOutput struct {
	Name     string          `json:"name"`
	Category string          `json:"category"`
	Fields   []contractField `json:"fields"`
}

type contractItem struct {
	ID      string           `json:"id"`
	Outputs []contractOutput `json:"outputs"`
}

type contract struct {
	items map[string]contractItem
}

func loadContract(path string) (*contract, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseContract(b)
}

func parseContract(b []byte) (*contract, error) {
	var inv struct {
		Items []contractItem `json:"items"`
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		return nil, fmt.Errorf("inventory: %w", err)
	}
	c := &contract{items: map[string]contractItem{}}
	for _, it := range inv.Items {
		c.items[it.ID] = it
	}
	return c, nil
}

func (c *contract) fields(item, output string) ([]contractField, error) {
	it, ok := c.items[item]
	if !ok {
		return nil, fmt.Errorf("inventory has no item %q", item)
	}
	for _, o := range it.Outputs {
		if o.Name == output {
			if len(o.Fields) == 0 {
				return nil, fmt.Errorf("%s output %s has no fields mapping", item, output)
			}
			return o.Fields, nil
		}
	}
	return nil, fmt.Errorf("%s has no output %q", item, output)
}

// resolveInspect turns a snake_case path (host_config.memory) into upstream
// segments (HostConfig, Memory). A segment the contract does not list fails
// while the contract still has fields at that level; below the listed levels
// it becomes "?<snake>".
func resolveInspect(fields []contractField, snake string) ([]string, error) {
	var out []string
	level := fields
	for i, seg := range strings.Split(snake, ".") {
		if seg == "" {
			return nil, fmt.Errorf("empty segment")
		}
		if level == nil {
			out = append(out, "?"+seg)
			continue
		}
		var hit *contractField
		for j := range level {
			if level[j].Name == seg {
				hit = &level[j]
				break
			}
		}
		if hit == nil {
			names := make([]string, len(level))
			for j, f := range level {
				names[j] = f.Name
			}
			return nil, fmt.Errorf("the contract has no field %q at %q (known: %s)", seg, strings.Join(strings.Split(snake, ".")[:i], "."), strings.Join(names, ", "))
		}
		out = append(out, hit.Upstream)
		level = hit.Fields
		if len(level) == 0 {
			level = nil
		}
	}
	return out, nil
}

// normName folds a key for "?<snake>" matching: lower case, no underscores
// or dashes.
func normName(s string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(s))
}

// lookupUpstream walks decoded inspect JSON along resolved segments. ok is
// false when a segment is absent (the field is unset).
func lookupUpstream(doc any, segs []string) (any, bool) {
	cur := doc
	for _, seg := range segs {
		m, isMap := cur.(map[string]any)
		if !isMap {
			return nil, false
		}
		if strings.HasPrefix(seg, "?") {
			want := normName(seg[1:])
			found := false
			for _, k := range sortedKeys(m) {
				if normName(k) == want {
					cur, found = m[k], true
					break
				}
			}
			if !found {
				return nil, false
			}
			continue
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}
