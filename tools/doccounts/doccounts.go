package main

import (
	"bufio"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind is one Terraform kind as registry_gen.go names it in its trailing
// comments ("// resource docker_network").
type Kind struct {
	Key    string // registry_gen.go comment key
	Plural string // the phrase docs use after a count
	List   bool   // the generated block lists every name, not only the count
}

// Kinds in the order the generated block states them.
var Kinds = []Kind{
	{Key: "resource", Plural: "resources", List: true},
	{Key: "data_source", Plural: "data sources"},
	{Key: "action", Plural: "actions"},
	{Key: "ephemeral_resource", Plural: "ephemeral resources", List: true},
	{Key: "function", Plural: "provider functions", List: true},
	{Key: "list_resource", Plural: "list resources", List: true},
}

// Registry maps a kind key to its sorted registered names.
type Registry map[string][]string

var registryLine = regexp.MustCompile(`^\s*New\w+,\s*//\s*(\w+)\s+(\S+)\s*$`)

// ParseRegistry reads the constructor lines of internal/provider/registry_gen.go.
func ParseRegistry(src string) (Registry, error) {
	known := map[string]bool{}
	for _, k := range Kinds {
		known[k.Key] = true
	}
	reg := Registry{}
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		m := registryLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		if !known[m[1]] {
			return nil, fmt.Errorf("registry: unknown kind %q in line %q", m[1], strings.TrimSpace(sc.Text()))
		}
		reg[m[1]] = append(reg[m[1]], m[2])
	}
	total := 0
	for k := range reg {
		sort.Strings(reg[k])
		total += len(reg[k])
	}
	if total == 0 {
		return nil, fmt.Errorf("registry: no constructor lines found")
	}
	return reg, nil
}

// all is every registered name of any kind.
func (r Registry) all() map[string]bool {
	out := map[string]bool{}
	for _, names := range r {
		for _, n := range names {
			out[n] = true
		}
	}
	return out
}

const (
	beginMarker = "<!-- doccounts:begin"
	endMarker   = "<!-- doccounts:end -->"
	beginLine   = beginMarker + " (generated from internal/provider/registry_gen.go by go run ./tools/doccounts -write; Invoke-Build DocCountsCheck fails on drift) -->"
)

// Block renders the generated block, markers included.
func Block(r Registry) string {
	var b strings.Builder
	b.WriteString(beginLine + "\n")
	parts := make([]string, 0, len(Kinds))
	for _, k := range Kinds {
		parts = append(parts, fmt.Sprintf("%d %s", len(r[k.Key]), k.Plural))
	}
	fmt.Fprintf(&b, "Registered: %s.\n\n", strings.Join(parts, ", "))
	for _, k := range Kinds {
		if !k.List {
			continue
		}
		q := make([]string, len(r[k.Key]))
		for i, n := range r[k.Key] {
			q[i] = "`" + n + "`"
		}
		fmt.Fprintf(&b, "- %s (%d): %s\n", k.Plural, len(r[k.Key]), strings.Join(q, ", "))
	}
	b.WriteString(endMarker)
	return b.String()
}

// Problem is one mismatch, with a 1-based line number.
type Problem struct {
	Line int
	Msg  string
}

func (p Problem) String() string { return fmt.Sprintf("%d: %s", p.Line, p.Msg) }

// countRe matches "<n> <kind phrase>" not preceded by a digit or a dot (so
// "Terraform 1.14 actions" is not a count). Longer phrases come first.
var countRe = regexp.MustCompile(`(^|[^.\d])(\d+)\s+(ephemeral resources?|list resources?|data sources?|provider functions?|functions?|actions?|resources?)\b`)

// typeRe matches a backticked provider type name.
var typeRe = regexp.MustCompile("`(docker_[a-z0-9_]+)`")

// phraseKind maps a matched phrase (singular or plural) to its kind key.
func phraseKind(phrase string) string {
	p := strings.TrimSuffix(phrase, "s")
	switch p {
	case "resource":
		return "resource"
	case "data source":
		return "data_source"
	case "action":
		return "action"
	case "ephemeral resource":
		return "ephemeral_resource"
	case "function", "provider function":
		return "function"
	case "list resource":
		return "list_resource"
	}
	return ""
}

// Check returns every problem in doc (CLAUDE.md, README.md):
//   - a generated block that differs from Block(r) (or is missing when requireBlock);
//   - outside the block, a "<n> <kind>" count that differs from the registry;
//   - anywhere, a backticked `docker_*` name that no kind registers.
func Check(doc string, r Registry, requireBlock bool) []Problem {
	var probs []Problem
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	start, end := blockRange(lines)
	switch {
	case start < 0 && requireBlock:
		probs = append(probs, Problem{1, "no generated doccounts block (" + beginMarker + " ... " + endMarker + "); run go run ./tools/doccounts -write after adding the two marker lines"})
	case start >= 0 && end < 0:
		probs = append(probs, Problem{start + 1, "doccounts block has no end marker " + endMarker})
	case start >= 0:
		got := strings.Join(lines[start:end+1], "\n")
		if got != Block(r) {
			probs = append(probs, Problem{start + 1, "generated counts block is stale; run go run ./tools/doccounts -write (Invoke-Build DocCounts)"})
		}
	}
	names := r.all()
	for i, line := range lines {
		inBlock := start >= 0 && end >= 0 && i >= start && i <= end
		if !inBlock {
			for _, m := range countRe.FindAllStringSubmatch(line, -1) {
				n, _ := strconv.Atoi(m[2])
				key := phraseKind(m[3])
				if want := len(r[key]); n != want {
					probs = append(probs, Problem{i + 1, fmt.Sprintf("says %q, the registry has %d %s", strings.TrimSpace(m[2]+" "+m[3]), want, m[3])})
				}
			}
		}
		for _, m := range typeRe.FindAllStringSubmatch(line, -1) {
			if !names[m[1]] {
				probs = append(probs, Problem{i + 1, fmt.Sprintf("`%s` is not a registered type of any kind", m[1])})
			}
		}
	}
	return probs
}

// Write returns doc with its generated block replaced by Block(r). A document
// without a block is returned unchanged with ok=false.
func Write(doc string, r Registry) (string, bool) {
	crlf := strings.Contains(doc, "\r\n")
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	start, end := blockRange(lines)
	if start < 0 || end < 0 {
		return doc, false
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, strings.Split(Block(r), "\n")...)
	out = append(out, lines[end+1:]...)
	s := strings.Join(out, "\n")
	if crlf {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s, true
}

// blockRange returns the line indexes of the begin and end markers (-1 if absent).
func blockRange(lines []string) (int, int) {
	start, end := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if start < 0 && strings.HasPrefix(t, beginMarker) {
			start = i
		} else if start >= 0 && t == endMarker {
			end = i
			break
		}
	}
	return start, end
}
