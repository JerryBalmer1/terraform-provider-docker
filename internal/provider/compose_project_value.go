package provider

// The structured `project` output (Q42): the loaded compose project as
// plain JSON values in compose-spec shape, which the outputs contract maps
// onto its fields (contract.go). compose-go serializes a few attributes in
// a shape other than the compose-spec one; composeProjectFixups converts
// those so no value is dropped.

import (
	"encoding/json"
	"sort"
	"strings"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

// composeProjectValue returns the project as map[string]any.
func composeProjectValue(p *ctypes.Project) map[string]any {
	if p == nil {
		return nil
	}
	b, err := json.Marshal(*p)
	if err != nil {
		return map[string]any{"name": p.Name}
	}
	var out map[string]any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		return map[string]any{"name": p.Name}
	}
	for _, key := range []string{"services", "jobs"} {
		if m, ok := out[key].(map[string]any); ok {
			for _, v := range m {
				if svc, ok := v.(map[string]any); ok {
					composeServiceFixups(svc)
				}
			}
		}
	}
	return out
}

// composeServiceFixups rewrites compose-go JSON shapes to the contract's.
func composeServiceFixups(svc map[string]any) {
	// extra_hosts: compose-go writes a list of "host=ip" (or "host:ip");
	// the contract has map(list(string)) host -> addresses.
	if l, ok := svc["extra_hosts"].([]any); ok {
		m := map[string]any{}
		for _, e := range l {
			s, _ := e.(string)
			k, v, found := strings.Cut(s, "=")
			if !found {
				k, v, _ = strings.Cut(s, ":")
			}
			prev, _ := m[k].([]any)
			m[k] = append(prev, v)
		}
		svc["extra_hosts"] = m
	}
	// command / entrypoint may be a string in YAML; compose-go already
	// writes lists. tmpfs, dns, dns_search are StringOrList: lists.
	for _, k := range []string{"tmpfs", "dns", "dns_search"} {
		if s, ok := svc[k].(string); ok {
			svc[k] = []any{s}
		}
	}
	// expose items may be numbers.
	if l, ok := svc["expose"].([]any); ok {
		for i, e := range l {
			if n, ok := e.(json.Number); ok {
				l[i] = n.String()
			}
		}
	}
	// labels / annotations / sysctls / storage_opt as list "k=v" -> map.
	for _, k := range []string{"labels", "annotations", "sysctls", "storage_opt"} {
		if l, ok := svc[k].([]any); ok {
			m := map[string]any{}
			for _, e := range l {
				s, _ := e.(string)
				kk, v, _ := strings.Cut(s, "=")
				m[kk] = v
			}
			svc[k] = m
		}
	}
	// environment values may be null (variable without value).
	if m, ok := svc["environment"].(map[string]any); ok {
		for k, v := range m {
			if v == nil {
				delete(m, k)
			}
		}
	}
}

// composeSortedAny returns the sorted keys of a JSON object.
func composeSortedAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
