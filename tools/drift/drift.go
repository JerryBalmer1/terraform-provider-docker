package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// remote is the network: git ls-remote and HTTP GET (faked in tests).
type remote interface {
	// Tags returns the tag names of a git repository.
	Tags(repo string) ([]string, error)
	// Branch returns the commit a branch points at.
	Branch(repo, branch string) (string, error)
	// Get fetches a URL: status code and body.
	Get(url string) (int, []byte, error)
}

// fcInputs are tools/fieldcov's alternate inputs (absolute paths; empty =
// fieldcov's pinned default).
type fcInputs struct {
	Swagger, Compose, Bake string
}

func (in fcInputs) key() string { return in.Swagger + "\x00" + in.Compose + "\x00" + in.Bake }

// fieldRunner runs tools/fieldcov -fields-out (faked in tests).
type fieldRunner interface {
	Run(in fcInputs) (*fieldSet, error)
}

// fieldSet is tools/fieldcov's -fields-out document.
type fieldSet struct {
	Fields []struct {
		Pointer    string `json:"pointer"`
		Source     string `json:"source"`
		How        string `json:"how"`
		Item       string `json:"item"`
		Attr       string `json:"attr"`
		Skip       string `json:"skip"`
		Partial    string `json:"partial"`
		Deprecated bool   `json:"deprecated"`
	} `json:"fields"`
	Operations []struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Path   string `json:"path"`
	} `json:"operations"`
	Errors []string `json:"errors"`
}

func readFieldSet(path string) (*fieldSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fs fieldSet
	if err := json.Unmarshal(b, &fs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &fs, nil
}

// Finding kinds, changes and statuses.
const (
	kindVersion   = "version"
	kindOperation = "operation"
	kindField     = "field"

	changeNew     = "new"
	changeRemoved = "removed"
	changeMoved   = "moved"

	statusMapped   = "mapped"   // new, and already covered (inventory, fieldmap: mapped, skip or partial)
	statusAccepted = "accepted" // new and unmapped, listed in reference/_gen/drift-accepted.json
	statusUnmapped = "unmapped" // new and neither: fails DriftCheck
	statusInfo     = "info"     // reported only (removals, version movement of a diffed source)
)

// finding is one reported drift item.
type finding struct {
	Kind    string `json:"kind"`
	Change  string `json:"change"`
	Source  string `json:"source"`
	Item    string `json:"item"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Version string `json:"version_seen"`
}

// sourceStatus is one source's probe result.
type sourceStatus struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Model    string `json:"model"`
	Pinned   string `json:"pinned"`
	Newest   string `json:"newest"`
	Moved    bool   `json:"moved"`
	Override bool   `json:"override,omitempty"`
	Note     string `json:"note,omitempty"`
	Error    string `json:"error,omitempty"`
	// Spec is the fetched spec file (temp directory), for a moved field model.
	Spec string `json:"spec,omitempty"`
	// Surface is the surface verdict of a moved docs source: "no surface
	// change" or "<n> surface change(s)"; SurfaceItems the items compared.
	Surface      string `json:"surface,omitempty"`
	SurfaceItems int    `json:"surface_items,omitempty"`
	ref          string
	body         []byte
}

// report is the whole result.
type report struct {
	Generated string         `json:"generated"`
	Sources   []sourceStatus `json:"sources"`
	Findings  []finding      `json:"findings"`
	// Notes are fieldcov error lines on a newer spec (dropped operations,
	// overlay pointers the newer spec no longer has): context, never failures.
	Notes []string `json:"notes,omitempty"`
	// Notices: moved sources with no surface change, drift-log.json rows.
	Notices []string `json:"notices,omitempty"`
	// Stale are drift-accepted.json items nothing matched this run.
	Stale  []string `json:"stale_accepted,omitempty"`
	Errors []string `json:"errors,omitempty"`
}

// counts returns the unmapped (failing) findings and totals per kind.
func (r *report) unmapped() []finding {
	var out []finding
	for _, f := range r.Findings {
		if f.Status == statusUnmapped {
			out = append(out, f)
		}
	}
	return out
}

func (r *report) count(kind, change string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Kind == kind && f.Change == change {
			n++
		}
	}
	return n
}

// options configure one run.
type options struct {
	Repo     string
	Work     string
	Sources  []source
	Pins     map[string]pin
	Accepted []accepted
	// Overrides replace the newest version of a source (what-if runs:
	// moby=api/v1.55.0, docs-engine-api=1.55, compose-spec=<sha>).
	Overrides map[string]string
	MaxProbe  int
	Now       time.Time
	// Docs fetches a docs source's newer release for the surface diff; nil
	// keeps the old rule (a moved docs source needs a version: acceptance).
	Docs docFetcher
	// Deferred are reference/_gen/deferred.d/upstream.json entries (surface
	// items knowingly not implemented: reported as mapped).
	Deferred map[string]upstreamDeferral
	// LogPath is reference/_gen/drift-log.json ("" = do not write).
	LogPath string
}

// run probes every source, diffs the moved field models with fieldcov and
// applies drift-accepted.json. Errors are collected in the report.
func run(o options, rm remote, fr fieldRunner) *report {
	r := &report{Generated: o.Now.UTC().Format(time.RFC3339)}
	if o.MaxProbe <= 0 {
		o.MaxProbe = 10
	}
	for _, s := range o.Sources {
		st := probe(o, rm, s)
		if st.Moved && s.Model != modelDoc && st.Error == "" {
			if err := fetchSpec(o, rm, s, &st); err != nil {
				st.Error = err.Error()
			}
		}
		if st.Error != "" {
			r.Errors = append(r.Errors, s.Name+": "+st.Error)
		}
		r.Sources = append(r.Sources, st)
		if st.Moved && st.Error == "" {
			f := finding{Kind: kindVersion, Change: changeMoved, Source: s.Name, Item: "version:" + s.Name,
				Version: st.Newest, Detail: "pinned " + st.Pinned + ", newest " + st.Newest}
			if s.Surface != "" && o.Docs != nil {
				f.Status = statusInfo
				f.Detail += " (surface diffed below)"
			} else if s.Model == modelDoc {
				// Documentation only: nothing to diff, so the movement itself must be
				// reviewed (re-pin, or accept this version).
				f.Status = statusUnmapped
			} else if s.Model == modelOpenAPI {
				f.Status = statusInfo
				f.Detail += " (operations and schema properties diffed below)"
			} else {
				f.Status = statusInfo
				f.Detail += " (operations and fields diffed below)"
			}
			r.Findings = append(r.Findings, f)
		}
	}
	diffModels(o, r, fr)
	diffSurfaces(o, r)
	diffOpenAPI(o, r)
	judgeNoChange(o, r)
	applyAccepted(r, o.Accepted)
	sortFindings(r.Findings)
	return r
}

func probe(o options, rm remote, s source) sourceStatus {
	p := o.Pins[s.Name]
	st := sourceStatus{Name: s.Name, Title: s.Title, Model: s.Model, Pinned: p.Version}
	if s.Model == modelDoc {
		st.Model = "doc"
	}
	ov, hasOv := o.Overrides[s.Name]
	st.Override = hasOv
	switch {
	case s.SpecURL != "":
		probeSnapshot(o, rm, s, p, ov, hasOv, &st)
	case s.URLTemplate != "":
		probeURL(o, rm, s, p, ov, hasOv, &st)
	case s.Branch != "":
		st.Pinned = s.Branch + "@" + short(p.SHA)
		sha := ov
		if !hasOv {
			var err error
			if sha, err = rm.Branch(s.Repo, s.Branch); err != nil {
				st.Error = err.Error()
				return st
			}
		}
		st.Newest = s.Branch + "@" + short(sha)
		st.Moved = sha != p.SHA
		st.ref = sha
		st.Note = "no release tags: tracks " + s.Branch + " HEAD"
	default:
		tag := ov
		if !hasOv {
			tags, err := rm.Tags(s.Repo)
			if err != nil {
				st.Error = err.Error()
				return st
			}
			var ok bool
			if tag, ok = newestTag(s.TagPrefix, tags); !ok {
				st.Error = fmt.Sprintf("no %sMAJOR.MINOR.PATCH tag in %s", s.TagPrefix, s.Repo)
				return st
			}
		}
		st.Newest = tag
		nv, ok1 := parseTag(s.TagPrefix, tag)
		pv, ok2 := parseTag(s.TagPrefix, p.Version)
		switch {
		case hasOv:
			st.Moved = tag != p.Version
		case ok1 && ok2:
			st.Moved = pv.less(nv)
		}
		st.ref = "refs/tags/" + tag
	}
	return st
}

// probeURL finds the newest published API version above the pin (a 404 ends
// the probe), and treats a re-published pinned version (sha256 changed) as
// moved.
func probeURL(o options, rm remote, s source, p pin, ov string, hasOv bool, st *sourceStatus) {
	url := func(v string) string { return strings.ReplaceAll(s.URLTemplate, "{version}", v) }
	newest := ov
	var body []byte
	if !hasOv {
		major, minor, err := splitAPIVersion(p.Version)
		if err != nil {
			st.Error = err.Error()
			return
		}
		newest = p.Version
		for i := 1; i <= o.MaxProbe; i++ {
			v := fmt.Sprintf("%d.%d", major, minor+i)
			code, b, err := rm.Get(url(v))
			if err != nil {
				st.Error = err.Error()
				return
			}
			if code == 404 {
				break
			}
			if code != 200 {
				st.Error = fmt.Sprintf("GET %s: HTTP %d", url(v), code)
				return
			}
			newest, body = v, b
		}
	}
	if body == nil {
		code, b, err := rm.Get(url(newest))
		if err != nil {
			st.Error = err.Error()
			return
		}
		if code != 200 {
			st.Error = fmt.Sprintf("GET %s: HTTP %d", url(newest), code)
			return
		}
		body = b
	}
	st.Newest = newest
	st.body = body
	sum := sha256Hex(body)
	switch {
	case newest != p.Version:
		st.Moved = true
	case sum != p.SHA256:
		st.Moved = true
		st.Note = "v" + newest + " was re-published (sha256 " + short(sum) + ", pinned " + short(p.SHA256) + ")"
	}
}

// fetchSpec writes the newest spec file of a moved field-model source under
// the work directory (never under reference/).
func fetchSpec(o options, rm remote, s source, st *sourceStatus) error {
	body := st.body
	if body == nil {
		u := rawURL(s.Repo, st.ref, s.File)
		code, b, err := rm.Get(u)
		if err != nil {
			return err
		}
		if code != 200 {
			return fmt.Errorf("GET %s: HTTP %d", u, code)
		}
		body = b
	}
	name := filepath.Base(s.File)
	switch {
	case s.SpecURL != "":
		name = s.SpecURL[strings.LastIndex(s.SpecURL, "/")+1:]
	case s.File == "":
		name = "spec.yaml"
	}
	dir := filepath.Join(o.Work, s.Name, safeName(st.Newest))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return err
	}
	st.Spec = path
	st.body = nil
	return nil
}

var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string { return unsafeRe.ReplaceAllString(s, "_") }

// diffModels runs fieldcov on the pinned and the newer specs of every moved
// field-model source and records new / removed operations and fields.
//
// One combined run covers the engine, compose-spec and bake slots (the
// engine slot takes the moby tag spec, else the docs.docker.com spec);
// compose-go shares the compose slot, so it gets its own pair of runs with
// its pinned embedded copy as the baseline.
func diffModels(o options, r *report, fr fieldRunner) {
	var head fcInputs
	origin := map[string]string{} // fieldcov source -> watched source
	var composeGo *sourceStatus
	for i := range r.Sources {
		st := &r.Sources[i]
		if !st.Moved || st.Spec == "" {
			continue
		}
		switch st.Model {
		case modelEngine:
			if head.Swagger == "" {
				head.Swagger, origin["engine"] = st.Spec, st.Name
			}
		case modelCompose:
			head.Compose, origin["compose"] = st.Spec, st.Name
		case modelBake:
			head.Bake, origin["bake"] = st.Spec, st.Name
		case modelComposeGo:
			composeGo = st
		}
	}
	cache := map[string]*fieldSet{}
	get := func(in fcInputs) (*fieldSet, error) {
		if fs, ok := cache[in.key()]; ok {
			return fs, nil
		}
		fs, err := fr.Run(in)
		if err != nil {
			return nil, err
		}
		cache[in.key()] = fs
		return fs, nil
	}
	inv, invErr := inventoryOps(filepath.Join(o.Repo, "reference", "inventory.json"))
	if invErr != nil && head.Swagger != "" {
		r.Errors = append(r.Errors, "inventory: "+invErr.Error())
	}
	if head != (fcInputs{}) {
		base, err1 := get(fcInputs{})
		hs, err2 := get(head)
		switch {
		case err1 != nil:
			r.Errors = append(r.Errors, "fieldcov (pinned): "+err1.Error())
		case err2 != nil:
			r.Errors = append(r.Errors, "fieldcov (newer specs): "+err2.Error())
		default:
			for _, src := range []string{"engine", "compose", "bake"} {
				if name, ok := origin[src]; ok {
					ver := versionOf(r, name)
					r.Findings = append(r.Findings, diffFields(base, hs, src, name, ver)...)
					if src == "engine" && invErr == nil {
						r.Findings = append(r.Findings, diffOps(base, hs, inv, name, ver)...)
					}
				}
			}
			r.Notes = append(r.Notes, newErrors(base, hs, "newer specs")...)
		}
	}
	if composeGo != nil {
		base, err1 := get(fcInputs{Compose: filepath.Join(o.Repo, filepath.FromSlash(sourceBaseline(o, composeGo.Name)))})
		hs, err2 := get(fcInputs{Compose: composeGo.Spec})
		switch {
		case err1 != nil:
			r.Errors = append(r.Errors, "fieldcov (pinned compose-go): "+err1.Error())
		case err2 != nil:
			r.Errors = append(r.Errors, "fieldcov (newer compose-go): "+err2.Error())
		default:
			r.Findings = append(r.Findings, diffFields(base, hs, "compose", composeGo.Name, composeGo.Newest)...)
			r.Notes = append(r.Notes, newErrors(base, hs, "compose-go "+composeGo.Newest)...)
		}
	}
}

func sourceBaseline(o options, name string) string {
	for _, s := range o.Sources {
		if s.Name == name {
			return s.Baseline
		}
	}
	return ""
}

func versionOf(r *report, name string) string {
	for _, s := range r.Sources {
		if s.Name == name {
			return s.Newest
		}
	}
	return ""
}

// newErrors are the head run's fieldcov error lines the base run did not have.
func newErrors(base, head *fieldSet, label string) []string {
	had := map[string]bool{}
	for _, e := range base.Errors {
		had[e] = true
	}
	var out []string
	for _, e := range head.Errors {
		if !had[e] {
			out = append(out, "fieldcov on "+label+": "+e)
		}
	}
	return out
}

// diffFields compares the fields of one fieldcov source ("engine",
// "compose", "bake") between the pinned and the newer spec.
func diffFields(base, head *fieldSet, fcSource, origin, version string) []finding {
	type row = struct {
		how, item, attr, skip, partial string
		deprecated                     bool
	}
	collect := func(fs *fieldSet) map[string]row {
		m := map[string]row{}
		for _, f := range fs.Fields {
			if f.Source == fcSource {
				m[f.Pointer] = row{f.How, f.Item, f.Attr, f.Skip, f.Partial, f.Deprecated}
			}
		}
		return m
	}
	b, h := collect(base), collect(head)
	var out []finding
	for p, e := range h {
		if _, ok := b[p]; ok {
			continue
		}
		f := finding{Kind: kindField, Change: changeNew, Source: origin, Item: p, Version: version}
		switch {
		case e.skip != "":
			f.Status, f.Detail = statusMapped, "skipped: "+e.skip
		case e.partial != "":
			f.Status, f.Detail = statusMapped, "partial ("+e.item+"): "+e.partial
		case e.how != "gap" && e.how != "":
			f.Status, f.Detail = statusMapped, e.how+" "+e.item+" "+e.attr
		default:
			f.Status, f.Detail = statusUnmapped, "owner "+e.item
		}
		if e.deprecated {
			f.Detail += " (deprecated)"
		}
		out = append(out, f)
	}
	for p, e := range b {
		if _, ok := h[p]; ok {
			continue
		}
		f := finding{Kind: kindField, Change: changeRemoved, Source: origin, Item: p, Version: version, Status: statusInfo}
		switch {
		case e.skip != "":
			f.Detail = "was skipped"
		case e.how == "gap":
			f.Detail = "was unmapped"
		default:
			f.Detail = "provider maps it: " + strings.TrimSpace(e.how+" "+e.item+" "+e.attr)
		}
		out = append(out, f)
	}
	return out
}

// diffOps compares the swagger operations: a new operation is mapped when
// reference/inventory.json already names it (any kind, skips included).
func diffOps(base, head *fieldSet, inv map[string]bool, origin, version string) []finding {
	ops := func(fs *fieldSet) map[string]string {
		m := map[string]string{}
		for _, o := range fs.Operations {
			m[o.ID] = o.Method + " " + o.Path
		}
		return m
	}
	b, h := ops(base), ops(head)
	var out []finding
	for id, mp := range h {
		if _, ok := b[id]; ok {
			continue
		}
		f := finding{Kind: kindOperation, Change: changeNew, Source: origin, Item: "operation:" + id, Version: version, Detail: mp}
		if inv[id] {
			f.Status = statusMapped
			f.Detail += " (in reference/inventory.json)"
		} else {
			f.Status = statusUnmapped
		}
		out = append(out, f)
	}
	for id, mp := range b {
		if _, ok := h[id]; ok {
			continue
		}
		f := finding{Kind: kindOperation, Change: changeRemoved, Source: origin, Item: "operation:" + id, Version: version,
			Status: statusInfo, Detail: mp}
		if inv[id] {
			f.Detail += " (referenced by reference/inventory.json)"
		}
		out = append(out, f)
	}
	return out
}

// inventoryOps returns every operationId reference/inventory.json names.
func inventoryOps(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if s, ok := x.(string); ok && k == "operationId" && s != "" {
					out[s] = true
					continue
				}
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(raw)
	return out, nil
}

func sortFindings(fs []finding) {
	rank := map[string]int{statusUnmapped: 0, statusAccepted: 1, statusMapped: 2, statusInfo: 3}
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Item < b.Item
	})
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
