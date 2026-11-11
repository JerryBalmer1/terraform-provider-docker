package main

// Result checking for one applied config:
//
//   - -compare reads <config>/state.json (terraform show -json after apply),
//     takes the resource's id_attr, GETs the inspect endpoint from the Engine
//     (DOCKER_HOST, tcp only) and checks each expectation of expect.json
//     against the field the contract maps it to. Writes compare.json.
//   - -plan-summary reads <config>/plan.json (terraform show -json of the
//     second plan) and lists every resource change that is not a no-op with
//     the attribute paths that differ. Writes plan-summary.json.
//   - -report merges run.json (written by the PairwiseTest runner),
//     compare.json and plan-summary.json of every config into one file.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type mismatch struct {
	Attr     string `json:"attr"`
	Label    string `json:"label"`
	Upstream string `json:"upstream"`
	Match    string `json:"match"`
	Expected string `json:"expected"`
	Observed string `json:"observed"`
}

type compareResult struct {
	Config     string     `json:"config"`
	ID         string     `json:"id"`
	Checked    int        `json:"checked"`
	Mismatches []mismatch `json:"mismatches"`
	Error      string     `json:"error,omitempty"`
}

// compareDir runs -compare on one config directory and writes compare.json.
func compareDir(ctx context.Context, dir, host string) (*compareResult, error) {
	var e expectFile
	if err := readJSON(filepath.Join(dir, "expect.json"), &e); err != nil {
		return nil, err
	}
	res := &compareResult{Config: e.Config, Mismatches: []mismatch{}}
	err := func() error {
		var state any
		if err := readJSON(filepath.Join(dir, "state.json"), &state); err != nil {
			return err
		}
		id, err := stateAttr(state, e.Address, e.IDAttr)
		if err != nil {
			return err
		}
		res.ID = id
		doc, err := engineGet(ctx, host, strings.ReplaceAll(e.Endpoint, "{id}", url.PathEscape(id)))
		if err != nil {
			return err
		}
		res.Checked = len(e.Checks)
		res.Mismatches = checkAll(e.Checks, doc)
		return nil
	}()
	if err != nil {
		res.Error = err.Error()
	}
	if werr := writeJSON(filepath.Join(dir, "compare.json"), res); werr != nil {
		return nil, werr
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// stateAttr finds a string attribute of a resource in terraform show -json output.
func stateAttr(state any, address, attr string) (string, error) {
	m, _ := state.(map[string]any)
	vals, _ := m["values"].(map[string]any)
	root, _ := vals["root_module"].(map[string]any)
	rs, _ := root["resources"].([]any)
	for _, r := range rs {
		rm, _ := r.(map[string]any)
		if rm["address"] != address {
			continue
		}
		v, _ := rm["values"].(map[string]any)
		s, ok := v[attr].(string)
		if !ok || s == "" {
			return "", fmt.Errorf("%s has no %s in the state", address, attr)
		}
		return s, nil
	}
	return "", fmt.Errorf("%s is not in the state", address)
}

// engineGet fetches an Engine API path over plain TCP (the harness manager).
func engineGet(ctx context.Context, host, path string) (any, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("DOCKER_HOST %q: %w", host, err)
	}
	if u.Scheme != "tcp" && u.Scheme != "http" {
		return nil, fmt.Errorf("DOCKER_HOST %q: only tcp:// is supported", host)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+u.Host+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return doc, nil
}

func checkAll(checks []expectCheck, doc any) []mismatch {
	out := []mismatch{}
	for _, c := range checks {
		obs, present := lookupUpstream(doc, c.Upstream)
		ok := false
		switch c.Match {
		case "subset":
			ok = present && subsetOf(c.Expected, obs)
		default:
			ok = present && deepEqual(c.Expected, obs)
			if (!present || obs == nil) && isZero(c.Expected) {
				ok = true // omitempty / unset pointer fields hold the zero value
			}
		}
		if !ok {
			o := "<absent>"
			if present {
				o = canonical(obs)
			}
			out = append(out, mismatch{Attr: c.Attr, Label: c.Label, Upstream: strings.Join(c.Upstream, "."), Match: c.Match, Expected: canonical(c.Expected), Observed: o})
		}
	}
	return out
}

func isZero(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case float64:
		return t == 0
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// deepEqual compares decoded JSON; numbers by value, null equal to an empty
// list or map (the Engine writes either).
func deepEqual(a, b any) bool {
	if isEmptyColl(a) && isEmptyColl(b) {
		return true
	}
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && math.Abs(x-y) < 1e-9
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !deepEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !deepEqual(v, w) {
				return false
			}
		}
		return true
	}
	return a == b
}

func isEmptyColl(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// subsetOf: every key of an expected object is in the observed object with a
// subset value; every element of an expected list subset-matches a distinct
// observed element; scalars are equal.
func subsetOf(exp, obs any) bool {
	switch x := exp.(type) {
	case map[string]any:
		y, ok := obs.(map[string]any)
		if !ok {
			return len(x) == 0 && obs == nil
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok {
				if isZero(v) {
					continue
				}
				return false
			}
			if !subsetOf(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, _ := obs.([]any)
		used := make([]bool, len(y))
		for _, e := range x {
			found := false
			for i, o := range y {
				if !used[i] && subsetOf(e, o) {
					used[i], found = true, true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return deepEqual(exp, obs)
}

// ---- plan summary ----

type planChange struct {
	Address string   `json:"address"`
	Actions []string `json:"actions"`
	Paths   []string `json:"paths"`
}

type planSummary struct {
	Config  string       `json:"config"`
	Empty   bool         `json:"empty"`
	Changes []planChange `json:"changes"`
}

func planSummaryDir(dir string) (*planSummary, error) {
	var e expectFile
	if err := readJSON(filepath.Join(dir, "expect.json"), &e); err != nil {
		return nil, err
	}
	var plan any
	if err := readJSON(filepath.Join(dir, "plan.json"), &plan); err != nil {
		return nil, err
	}
	ps := summarizePlan(plan)
	ps.Config = e.Config
	return ps, writeJSON(filepath.Join(dir, "plan-summary.json"), ps)
}

func summarizePlan(plan any) *planSummary {
	ps := &planSummary{Changes: []planChange{}}
	m, _ := plan.(map[string]any)
	rcs, _ := m["resource_changes"].([]any)
	for _, rc := range rcs {
		r, _ := rc.(map[string]any)
		ch, _ := r["change"].(map[string]any)
		var actions []string
		for _, a := range asList(ch["actions"]) {
			if s, ok := a.(string); ok {
				actions = append(actions, s)
			}
		}
		if len(actions) == 0 || (len(actions) == 1 && (actions[0] == "no-op" || actions[0] == "read")) {
			continue
		}
		addr, _ := r["address"].(string)
		paths := map[string]bool{}
		diffPaths("", ch["before"], ch["after"], paths)
		unknownPaths("", ch["after_unknown"], paths)
		for _, rp := range asList(ch["replace_paths"]) {
			paths["(replace) "+pathString(rp)] = true
		}
		pc := planChange{Address: addr, Actions: actions, Paths: sortedKeys(paths)}
		ps.Changes = append(ps.Changes, pc)
	}
	ps.Empty = len(ps.Changes) == 0
	return ps
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func pathString(p any) string {
	var parts []string
	for _, s := range asList(p) {
		switch t := s.(type) {
		case string:
			parts = append(parts, t)
		case float64:
			parts = append(parts, fmt.Sprintf("%d", int(t)))
		}
	}
	return strings.Join(parts, ".")
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// diffPaths records the leaf paths where before and after differ.
func diffPaths(prefix string, a, b any, out map[string]bool) {
	if deepEqual(a, b) {
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			diffPaths(join(prefix, k), am[k], bm[k], out)
		}
		return
	}
	al, aok := a.([]any)
	bl, bok := b.([]any)
	if aok && bok && len(al) == len(bl) {
		for i := range al {
			diffPaths(join(prefix, fmt.Sprint(i)), al[i], bl[i], out)
		}
		return
	}
	if prefix == "" {
		prefix = "(resource)"
	}
	out[prefix] = true
}

// unknownPaths records after_unknown leaves that are true.
func unknownPaths(prefix string, u any, out map[string]bool) {
	switch t := u.(type) {
	case bool:
		if t {
			out[prefix+" (known after apply)"] = true
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			unknownPaths(join(prefix, k), t[k], out)
		}
	case []any:
		for i, e := range t {
			unknownPaths(join(prefix, fmt.Sprint(i)), e, out)
		}
	}
}

// ---- report ----

// runFile is <config>/run.json, written by the PairwiseTest runner.
type runFile struct {
	Init          bool   `json:"init"`
	Applied       bool   `json:"applied"`
	ApplyError    string `json:"apply_error,omitempty"`
	PlanExitCode  int    `json:"plan_exit_code"`
	PlanError     string `json:"plan_error,omitempty"`
	Destroyed     bool   `json:"destroyed"`
	DestroyError  string `json:"destroy_error,omitempty"`
	ApplySeconds  int    `json:"apply_seconds,omitempty"`
	CompareError  string `json:"compare_error,omitempty"`
	SkippedReason string `json:"skipped_reason,omitempty"`
}

type configResult struct {
	Type       string            `json:"type"`
	Config     string            `json:"config"`
	Dir        string            `json:"dir"`
	Assignment map[string]string `json:"assignment"`
	Run        *runFile          `json:"run"`
	Compare    *compareResult    `json:"compare,omitempty"`
	Plan       *planSummary      `json:"plan,omitempty"`
	Clean      bool              `json:"clean"`
	Findings   []string          `json:"findings"`
}

type typeTotals struct {
	Configs   int `json:"configs"`
	Applied   int `json:"applied"`
	Clean     int `json:"clean"`
	Findings  int `json:"findings"`
	Destroyed int `json:"destroyed"`
}

type report struct {
	Totals  map[string]*typeTotals `json:"totals"`
	Configs []configResult         `json:"configs"`
}

func buildReport(outDir string) (*report, error) {
	rep := &report{Totals: map[string]*typeTotals{}, Configs: []configResult{}}
	matches, err := filepath.Glob(filepath.Join(outDir, "*", "*", "expect.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	for _, m := range matches {
		dir := filepath.Dir(m)
		var e expectFile
		if err := readJSON(m, &e); err != nil {
			return nil, err
		}
		cr := configResult{Type: e.Type, Config: e.Config, Dir: dir, Assignment: e.Assignment, Findings: []string{}}
		var rf runFile
		if err := readJSON(filepath.Join(dir, "run.json"), &rf); err == nil {
			cr.Run = &rf
		}
		var cmp compareResult
		if err := readJSON(filepath.Join(dir, "compare.json"), &cmp); err == nil {
			cr.Compare = &cmp
		}
		var ps planSummary
		if err := readJSON(filepath.Join(dir, "plan-summary.json"), &ps); err == nil {
			cr.Plan = &ps
		}
		cr.Findings = classify(&cr)
		cr.Clean = len(cr.Findings) == 0
		t := rep.Totals[e.Type]
		if t == nil {
			t = &typeTotals{}
			rep.Totals[e.Type] = t
		}
		t.Configs++
		if cr.Run != nil && cr.Run.Applied {
			t.Applied++
		}
		if cr.Run != nil && cr.Run.Destroyed {
			t.Destroyed++
		}
		if cr.Clean {
			t.Clean++
		} else {
			t.Findings++
		}
		rep.Configs = append(rep.Configs, cr)
	}
	return rep, nil
}

// classify lists what makes a config not clean.
func classify(cr *configResult) []string {
	out := []string{}
	if cr.Run == nil {
		return append(out, "not run")
	}
	r := cr.Run
	if r.SkippedReason != "" {
		return append(out, "skipped: "+r.SkippedReason)
	}
	if !r.Applied {
		return append(out, "apply failed: "+firstLine(r.ApplyError))
	}
	if cr.Compare == nil {
		out = append(out, "no inspect comparison: "+firstLine(r.CompareError))
	} else {
		if cr.Compare.Error != "" {
			out = append(out, "inspect comparison error: "+cr.Compare.Error)
		}
		for _, m := range cr.Compare.Mismatches {
			out = append(out, fmt.Sprintf("inspect mismatch %s [%s] %s: expected %s, observed %s", m.Attr, m.Label, m.Upstream, m.Expected, m.Observed))
		}
	}
	switch r.PlanExitCode {
	case 0:
	case 2:
		if cr.Plan != nil {
			for _, c := range cr.Plan.Changes {
				out = append(out, fmt.Sprintf("non-empty second plan: %s %s: %s", c.Address, strings.Join(c.Actions, ","), strings.Join(c.Paths, ", ")))
			}
		} else {
			out = append(out, "non-empty second plan")
		}
	default:
		out = append(out, fmt.Sprintf("second plan failed (exit %d): %s", r.PlanExitCode, firstLine(r.PlanError)))
	}
	if !r.Destroyed {
		out = append(out, "destroy failed: "+firstLine(r.DestroyError))
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = s[:600] + "..."
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

func (r *report) summaryLines() []string {
	var out []string
	for _, t := range sortedKeys(r.Totals) {
		x := r.Totals[t]
		out = append(out, fmt.Sprintf("SUMMARY %s: configs %d, applied %d, clean %d, findings %d, destroyed %d", t, x.Configs, x.Applied, x.Clean, x.Findings, x.Destroyed))
	}
	for _, c := range r.Configs {
		for _, f := range c.Findings {
			out = append(out, fmt.Sprintf("FINDING %s: %s", c.Config, f))
		}
	}
	return out
}
