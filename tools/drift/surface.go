package main

// Surface diff for the documentation-only sources (Pass 6e). A moved docs
// source used to need a blind version:<source> acceptance; now its user
// surface is extracted from the pinned copy under reference/upstream and from
// the newer release (a shallow sparse git checkout under -work) and diffed:
//
//   - cli-yaml (docker compose, docs/reference/*.yaml): every command and
//     each command's own options (name, shorthand, type, default, deprecated,
//     hidden, experimental).
//   - cli-md (docker CLI, docker trust, buildx docs/reference/*.md, the
//     cli-docs-tool markdown): every command (a file with the generated
//     block) and the rows of its "### Options" table (name, shorthand, type,
//     default).
//   - dockerfile (buildkit frontend/dockerfile/docs): the instructions
//     (## KEYWORD), each instruction's flags (### KEYWORD --flag headings,
//     KEYWORD [--flag] syntax lines in dockerfile fences, "- `--flag`" option
//     lists), the parser directives, the build-check rules (rules/*.md) and
//     the predefined and built-in build args.
//
// buildx's bake target attributes are diffed by fieldcov (-bake-ref), which
// also knows which ones the provider maps.
//
// A moved source whose diff (surface plus fieldcov findings) is empty passes
// with a notice and a row in reference/_gen/drift-log.json; any added,
// removed or changed item fails unless it is in drift-accepted.json or
// deferred in reference/_gen/deferred.d/upstream.json.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Surface kinds.
const (
	surfaceCLIYAML    = "cli-yaml"
	surfaceCLIMD      = "cli-md"
	surfaceDockerfile = "dockerfile"
)

const (
	kindSurface   = "surface"
	changeChanged = "changed"
	// resultNoSurfaceChange is the drift-log.json result of a moved source
	// whose diff is empty.
	resultNoSurfaceChange = "no surface change"
)

// docFetcher checks out the documentation paths of a release (faked in tests).
type docFetcher interface {
	// Checkout makes dir hold paths (repository-relative) of repo at tag.
	Checkout(repo, tag string, paths []string, dir string) error
}

// gitDocs is the real fetcher: a shallow, blob-filtered, sparse git clone.
type gitDocs struct{ git string }

func (g *gitDocs) Checkout(repo, tag string, paths []string, dir string) error {
	done := filepath.Join(dir, ".drift-done")
	if b, err := os.ReadFile(done); err == nil && strings.TrimSpace(string(b)) == repo+" "+tag {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	run := func(args ...string) error {
		cmd := exec.Command(g.git, args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("clone", "--quiet", "--depth", "1", "--branch", tag, "--filter=blob:none", "--no-checkout", repo, dir); err != nil {
		return err
	}
	pats := []string{"sparse-checkout", "set", "--no-cone"}
	for _, p := range paths {
		pats = append(pats, "/"+strings.TrimPrefix(p, "/"))
	}
	if err := run(append([]string{"-C", dir}, pats...)...); err != nil {
		return err
	}
	if err := run("-C", dir, "checkout", "--quiet", "refs/tags/"+tag); err != nil {
		return err
	}
	return os.WriteFile(done, []byte(repo+" "+tag+"\n"), 0o644)
}

// extractSurface returns item -> detail for one source tree (root holds the
// repository-relative paths).
func extractSurface(kind, root string, paths []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		fi, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("surface: %w", err)
		}
		var err2 error
		switch kind {
		case surfaceCLIYAML:
			err2 = extractCLIYAML(full, out)
		case surfaceCLIMD:
			if fi.IsDir() {
				err2 = extractCLIMD(full, out)
			}
		case surfaceDockerfile:
			err2 = extractDockerfile(full, out)
		default:
			return nil, fmt.Errorf("surface: unknown kind %q", kind)
		}
		if err2 != nil {
			return nil, err2
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("surface: nothing extracted from %s (%s)", root, strings.Join(paths, ", "))
	}
	return out, nil
}

type cliYAMLOpt struct {
	Option          string `yaml:"option"`
	Shorthand       string `yaml:"shorthand"`
	ValueType       string `yaml:"value_type"`
	DefaultValue    string `yaml:"default_value"`
	Deprecated      bool   `yaml:"deprecated"`
	Hidden          bool   `yaml:"hidden"`
	Experimental    bool   `yaml:"experimental"`
	ExperimentalCLI bool   `yaml:"experimentalcli"`
}

type cliYAMLDoc struct {
	Command    string       `yaml:"command"`
	Deprecated bool         `yaml:"deprecated"`
	Hidden     bool         `yaml:"hidden"`
	Options    []cliYAMLOpt `yaml:"options"`
}

func flagsDetail(deprecated, hidden, experimental bool) string {
	var f []string
	if deprecated {
		f = append(f, "deprecated")
	}
	if hidden {
		f = append(f, "hidden")
	}
	if experimental {
		f = append(f, "experimental")
	}
	return strings.Join(f, ",")
}

// extractCLIYAML reads every docs/reference *.yaml (one command each).
// inherited_options are the parent's own options and are not repeated.
func extractCLIYAML(dir string, out map[string]string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var d cliYAMLDoc
		if err := yaml.Unmarshal(b, &d); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if d.Command == "" {
			continue
		}
		out["command/"+d.Command] = flagsDetail(d.Deprecated, d.Hidden, false)
		for _, o := range d.Options {
			det := fmt.Sprintf("type=%s default=%s", o.ValueType, o.DefaultValue)
			if o.Shorthand != "" {
				det += " short=-" + o.Shorthand
			}
			if fl := flagsDetail(o.Deprecated, o.Hidden, o.Experimental || o.ExperimentalCLI); fl != "" {
				det += " " + fl
			}
			out["option/"+d.Command+" --"+o.Option] = det
		}
	}
	return nil
}

var (
	mdOptTokenRe = regexp.MustCompile("`(-{1,2}[A-Za-z0-9][A-Za-z0-9-]*)`")
	mdGenMarker  = "<!---MARKER_GEN_START-->"
)

// splitMDRow splits a markdown table row on unescaped pipes.
func splitMDRow(line string) []string {
	line = strings.ReplaceAll(strings.TrimSpace(line), `\|`, "\x00")
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	cells := strings.Split(line, "|")
	for i, c := range cells {
		cells[i] = strings.TrimSpace(strings.ReplaceAll(c, "\x00", "|"))
	}
	return cells
}

// extractCLIMD reads the cli-docs-tool markdown of one directory (flat):
// <a>_<b>.md is `docker a b`, docker.md is `docker`.
func extractCLIMD(dir string, out map[string]string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(b), "\r\n", "\n")
		if !strings.Contains(text, mdGenMarker) {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(f), ".md")
		cmd := "docker"
		if stem != "docker" {
			cmd += " " + strings.ReplaceAll(stem, "_", " ")
		}
		out["command/"+cmd] = ""
		lines := strings.Split(text, "\n")
		for i := 0; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "### Options" {
				continue
			}
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			// header, separator, rows
			for j += 2; j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "|"); j++ {
				cells := splitMDRow(lines[j])
				if len(cells) < 3 {
					continue
				}
				var long, short string
				for _, m := range mdOptTokenRe.FindAllStringSubmatch(cells[0], -1) {
					if strings.HasPrefix(m[1], "--") {
						long = m[1]
					} else {
						short = m[1]
					}
				}
				name := long
				if name == "" {
					name = short
				}
				if name == "" {
					continue
				}
				det := fmt.Sprintf("type=%s default=%s", strings.Trim(cells[1], "`"), strings.Trim(cells[2], "`"))
				if short != "" && long != "" {
					det += " short=" + short
				}
				out["option/"+cmd+" "+name] = det
			}
			break
		}
	}
	return nil
}

var (
	dfInstrRe     = regexp.MustCompile(`^## ([A-Z]+)( \(deprecated\))?\s*$`)
	dfH2Re        = regexp.MustCompile(`^## `)
	dfH3Re        = regexp.MustCompile(`^### (.+?)\s*$`)
	dfFlagHeadRe  = regexp.MustCompile(`^### ([A-Z]+) (--[a-z][a-z0-9-]*)`)
	dfFlagTokRe   = regexp.MustCompile(`(?:^|[\s\[])(--[a-z][a-z0-9-]*)`)
	dfFlagListRe  = regexp.MustCompile("^- `(--[a-z][a-z0-9-]*)")
	dfDirectiveRe = regexp.MustCompile(`^([a-z][a-z0-9-]*)$`)
	dfArgListRe   = regexp.MustCompile("^- `([A-Za-z_][A-Za-z0-9_]*)`")
	dfArgRowRe    = regexp.MustCompile("^\\| `([A-Za-z_][A-Za-z0-9_]*)`")
)

// extractDockerfile reads reference.md and rules/*.md of the frontend docs.
func extractDockerfile(dir string, out map[string]string) error {
	b, err := os.ReadFile(filepath.Join(dir, "reference.md"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var section, instr string
	fence := ""
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			if fence == "" {
				fence = strings.TrimPrefix(t, "```")
				if fence == "" {
					fence = "-"
				}
			} else {
				fence = ""
			}
			continue
		}
		if fence != "" {
			if strings.HasPrefix(fence, "dockerfile") && instr != "" && instr != "ONBUILD" {
				if kw, rest, ok := strings.Cut(t, " "); ok && strings.EqualFold(kw, instr) {
					for _, m := range dfFlagTokRe.FindAllStringSubmatch(rest, -1) {
						out["flag/"+instr+" "+m[1]] = ""
					}
				}
			}
			continue
		}
		if m := dfInstrRe.FindStringSubmatch(line); m != nil {
			section, instr = m[1], m[1]
			det := ""
			if m[2] != "" {
				det = "deprecated"
			}
			out["instruction/"+instr] = det
			continue
		}
		if dfH2Re.MatchString(line) {
			section, instr = strings.TrimSpace(strings.TrimPrefix(line, "## ")), ""
			continue
		}
		if m := dfFlagHeadRe.FindStringSubmatch(line); m != nil {
			out["flag/"+m[1]+" "+m[2]] = ""
			continue
		}
		if m := dfH3Re.FindStringSubmatch(line); m != nil && section == "Parser directives" {
			if d := dfDirectiveRe.FindStringSubmatch(m[1]); d != nil {
				out["directive/"+d[1]] = ""
			}
			continue
		}
		if instr != "" {
			if m := dfFlagListRe.FindStringSubmatch(t); m != nil {
				out["flag/"+instr+" "+m[1]] = ""
			}
		}
		if instr == "ARG" {
			if m := dfArgListRe.FindStringSubmatch(t); m != nil {
				out["buildarg/"+m[1]] = ""
			} else if m := dfArgRowRe.FindStringSubmatch(t); m != nil {
				out["buildarg/"+m[1]] = ""
			}
		}
	}
	rules, err := filepath.Glob(filepath.Join(dir, "rules", "*.md"))
	if err != nil {
		return err
	}
	for _, r := range rules {
		stem := strings.TrimSuffix(filepath.Base(r), ".md")
		if strings.HasPrefix(stem, "_") {
			continue
		}
		out["check/"+stem] = ""
	}
	return nil
}

// diffSurface compares two extracted surfaces of one source.
func diffSurface(base, head map[string]string, src, version string, deferred map[string]upstreamDeferral) []finding {
	var out []finding
	add := func(id, change, detail string) {
		f := finding{Kind: kindSurface, Change: change, Source: src, Item: src + ":" + id, Version: version, Detail: detail,
			Status: statusUnmapped}
		if d, ok := deferred[f.Item]; ok {
			f.Status = statusMapped
			f.Detail = strings.TrimSpace(f.Detail + "; deferred (reference/_gen/deferred.d/upstream.json): " + d.Reason)
		}
		out = append(out, f)
	}
	for id, h := range head {
		b, ok := base[id]
		switch {
		case !ok:
			add(id, changeNew, h)
		case b != h:
			add(id, changeChanged, "was "+b+"; now "+h)
		}
	}
	for id, b := range base {
		if _, ok := head[id]; !ok {
			add(id, changeRemoved, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item < out[j].Item })
	return out
}

// upstreamDeferral is one reference/_gen/deferred.d/upstream.json entry: an
// upstream surface item reviewed and knowingly not implemented (it also
// appears in reference/_gen/deferred.json as kind upstream_surface).
type upstreamDeferral struct {
	Item        string `json:"item"`
	VersionSeen string `json:"version_seen"`
	Date        string `json:"date"`
	Reason      string `json:"reason"`
}

type upstreamDeferralFile struct {
	About string             `json:"about"`
	Items []upstreamDeferral `json:"items"`
}

// loadUpstreamDeferrals reads deferred.d/upstream.json (absent = none).
func loadUpstreamDeferrals(path string) (map[string]upstreamDeferral, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]upstreamDeferral{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f upstreamDeferralFile
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := map[string]upstreamDeferral{}
	for i, d := range f.Items {
		if d.Item == "" || len(strings.TrimSpace(d.Reason)) < minReason || d.VersionSeen == "" {
			return nil, fmt.Errorf("%s: items[%d] %q needs item, version_seen and a reason of at least %d characters", path, i, d.Item, minReason)
		}
		if _, err := time.Parse("2006-01-02", d.Date); err != nil {
			return nil, fmt.Errorf("%s: items[%d] %q: date must be YYYY-MM-DD", path, i, d.Item)
		}
		if _, dup := out[d.Item]; dup {
			return nil, fmt.Errorf("%s: items[%d] %q listed twice", path, i, d.Item)
		}
		out[d.Item] = d
	}
	return out, nil
}

// driftLogRow is one reference/_gen/drift-log.json row: a moved source
// judged to have no surface change.
type driftLogRow struct {
	Source string `json:"source"`
	Pinned string `json:"pinned"`
	Newest string `json:"newest"`
	Date   string `json:"date"`
	Result string `json:"result"`
}

type driftLogFile struct {
	About string        `json:"about"`
	Rows  []driftLogRow `json:"rows"`
}

const driftLogAbout = "Moved upstream documentation sources that Invoke-Build DriftCheck (tools/drift) diffed and found to have no surface change (commands, options, Dockerfile instructions, flags, directives, build checks, build args; plus fieldcov's operations and fields). Appended by DriftCheck, one row per source and version pair; a row is not a re-pin."

// appendDriftLog adds rows not yet present (same source, pinned, newest).
func appendDriftLog(path string, rows []driftLogRow) (int, error) {
	if path == "" || len(rows) == 0 {
		return 0, nil
	}
	f := driftLogFile{About: driftLogAbout}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &f); err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
		f.About = driftLogAbout
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		dup := false
		for _, e := range f.Rows {
			if e.Source == r.Source && e.Pinned == r.Pinned && e.Newest == r.Newest {
				dup = true
				break
			}
		}
		if !dup {
			f.Rows = append(f.Rows, r)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return 0, err
	}
	return n, os.WriteFile(path, append(b, '\n'), 0o644)
}

// diffSurfaces runs the surface diff of every moved source that has one.
func diffSurfaces(o options, r *report) {
	if o.Docs == nil {
		return
	}
	for i := range r.Sources {
		st := &r.Sources[i]
		s := sourceByName(o, st.Name)
		if s == nil || s.Surface == "" || !st.Moved || st.Error != "" {
			continue
		}
		baseRoot := filepath.Join(o.Repo, "reference", "upstream", filepath.FromSlash(s.Local))
		base, err := extractSurface(s.Surface, baseRoot, s.DocPaths)
		if err != nil {
			st.Error = "pinned surface: " + err.Error()
			r.Errors = append(r.Errors, s.Name+": "+st.Error)
			continue
		}
		headRoot := filepath.Join(o.Work, s.Name, safeName(st.Newest), "src")
		if err := o.Docs.Checkout(s.Repo, st.Newest, s.DocPaths, headRoot); err != nil {
			st.Error = "fetch " + st.Newest + ": " + err.Error()
			r.Errors = append(r.Errors, s.Name+": "+st.Error)
			continue
		}
		head, err := extractSurface(s.Surface, headRoot, s.DocPaths)
		if err != nil {
			st.Error = "newer surface: " + err.Error()
			r.Errors = append(r.Errors, s.Name+": "+st.Error)
			continue
		}
		st.SurfaceItems = len(head)
		r.Findings = append(r.Findings, diffSurface(base, head, s.Name, st.Newest, o.Deferred)...)
	}
}

// judgeNoChange marks every moved, surface-diffed source (docs surface or a
// URL-published OpenAPI spec) with no findings besides its version movement,
// and logs it.
func judgeNoChange(o options, r *report) {
	var rows []driftLogRow
	for i := range r.Sources {
		st := &r.Sources[i]
		s := sourceByName(o, st.Name)
		if s == nil || !st.Moved || st.Error != "" || !((s.Surface != "" && o.Docs != nil) || s.SpecURL != "") {
			continue
		}
		n := 0
		for _, f := range r.Findings {
			if f.Source == st.Name && f.Kind != kindVersion {
				n++
			}
		}
		if n > 0 {
			st.Surface = fmt.Sprintf("%d surface change(s)", n)
			continue
		}
		st.Surface = resultNoSurfaceChange
		r.Notices = append(r.Notices, fmt.Sprintf("%s %s -> %s: no surface change (%d items compared)", st.Name, st.Pinned, st.Newest, st.SurfaceItems))
		if !st.Override {
			rows = append(rows, driftLogRow{Source: st.Name, Pinned: st.Pinned, Newest: st.Newest, Date: o.Now.Format("2006-01-02"),
				Result: resultNoSurfaceChange})
		}
	}
	if n, err := appendDriftLog(o.LogPath, rows); err != nil {
		r.Errors = append(r.Errors, "drift-log: "+err.Error())
	} else if n > 0 {
		r.Notices = append(r.Notices, fmt.Sprintf("%d row(s) appended to %s", n, filepath.Base(o.LogPath)))
	}
}

func sourceByName(o options, name string) *source {
	for i := range o.Sources {
		if o.Sources[i].Name == name {
			return &o.Sources[i]
		}
	}
	return nil
}
