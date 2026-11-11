package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// maxRows caps each findings table in the Markdown report (the GitHub issue
// body limit is 65536 characters); the JSON report always has every finding.
const maxRows = 150

func (r *report) passed() bool { return len(r.unmapped()) == 0 && len(r.Errors) == 0 }

func (r *report) verdict() string {
	switch {
	case len(r.Errors) > 0:
		return "ERROR"
	case len(r.unmapped()) > 0:
		return "FAIL"
	}
	return "PASS"
}

// summaryLine is the one-line totals.
func (r *report) summaryLine() string {
	moved := 0
	for _, s := range r.Sources {
		if s.Moved {
			moved++
		}
	}
	unmappedBy := map[string]int{}
	for _, f := range r.unmapped() {
		unmappedBy[f.Kind]++
	}
	surface := 0
	for _, f := range r.Findings {
		if f.Kind == kindSurface {
			surface++
		}
	}
	return fmt.Sprintf("%s: %d of %d sources moved; new operations %d, new writable fields %d, removed operations %d, removed fields %d, surface changes %d; "+
		"unaccepted: %d version, %d operation, %d field, %d surface; errors %d",
		r.verdict(), moved, len(r.Sources), r.count(kindOperation, changeNew), r.count(kindField, changeNew),
		r.count(kindOperation, changeRemoved), r.count(kindField, changeRemoved), surface,
		unmappedBy[kindVersion], unmappedBy[kindOperation], unmappedBy[kindField], unmappedBy[kindSurface], len(r.Errors))
}

func (r *report) writeConsole(w io.Writer) {
	fmt.Fprintf(w, "%-16s %-10s %-20s %-20s %s\n", "source", "model", "pinned", "newest", "status")
	for _, s := range r.Sources {
		status := "up to date"
		switch {
		case s.Error != "":
			status = "ERROR " + s.Error
		case s.Moved:
			status = "MOVED"
		}
		if s.Override {
			status += " (override)"
		}
		if s.Note != "" && s.Error == "" {
			status += "; " + s.Note
		}
		if s.Surface != "" {
			status += "; " + s.Surface
		}
		fmt.Fprintf(w, "%-16s %-10s %-20s %-20s %s\n", s.Name, s.Model, s.Pinned, s.Newest, status)
	}
	un := r.unmapped()
	if len(un) > 0 {
		fmt.Fprintf(w, "\nunmapped and not in reference/_gen/drift-accepted.json (%d):\n", len(un))
		for i, f := range un {
			if i == 20 {
				fmt.Fprintf(w, "  ... and %d more (see the report)\n", len(un)-20)
				break
			}
			fmt.Fprintf(w, "  %-9s %-7s %s  [%s %s]\n", f.Kind, f.Change, f.Item, f.Source, f.Version)
		}
	}
	for _, n := range r.Notices {
		fmt.Fprintln(w, "NOTICE", n)
	}
	for _, e := range r.Errors {
		fmt.Fprintln(w, "ERROR", e)
	}
	if len(r.Stale) > 0 {
		fmt.Fprintf(w, "\nstale drift-accepted.json entries (matched nothing; remove them): %s\n", strings.Join(r.Stale, ", "))
	}
	fmt.Fprintln(w, "\ndrift:", r.summaryLine())
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", "").Replace(s)
}

func (r *report) writeMarkdown(w io.Writer) {
	fmt.Fprintf(w, "# Upstream drift\n\n")
	fmt.Fprintf(w, "Generated %s by `Invoke-Build DriftCheck` (tools/drift) against the pins in reference/SOURCES.md.\n\n", r.Generated)
	fmt.Fprintf(w, "**%s**\n\n", mdEscape(r.summaryLine()))
	fmt.Fprintf(w, "## Sources\n\n| source | model | pinned | newest | status |\n|---|---|---|---|---|\n")
	for _, s := range r.Sources {
		status := "up to date"
		switch {
		case s.Error != "":
			status = "ERROR: " + s.Error
		case s.Moved:
			status = "moved"
		}
		if s.Override {
			status += " (override)"
		}
		if s.Note != "" {
			status += "; " + s.Note
		}
		if s.Surface != "" {
			status += "; " + s.Surface
		}
		fmt.Fprintf(w, "| %s (%s) | %s | `%s` | `%s` | %s |\n", s.Name, mdEscape(s.Title), s.Model, s.Pinned, s.Newest, mdEscape(status))
	}
	groups := []struct {
		title  string
		status string
	}{
		{"Unmapped (fails DriftCheck)", statusUnmapped},
		{"Accepted (reference/_gen/drift-accepted.json)", statusAccepted},
		{"New and already mapped", statusMapped},
		{"Reported only (removed fields and operations, version movement of a diffed source)", statusInfo},
	}
	for _, g := range groups {
		var rows []finding
		for _, f := range r.Findings {
			if f.Status == g.status {
				rows = append(rows, f)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n## %s (%d)\n\n| kind | change | item | source | version | detail |\n|---|---|---|---|---|---|\n", g.title, len(rows))
		for i, f := range rows {
			if i == maxRows {
				fmt.Fprintf(w, "\n%d more rows: see the JSON report.\n", len(rows)-maxRows)
				break
			}
			fmt.Fprintf(w, "| %s | %s | `%s` | %s | `%s` | %s |\n", f.Kind, f.Change, mdEscape(f.Item), f.Source, f.Version, mdEscape(f.Detail))
		}
	}
	if len(r.Notes) > 0 {
		fmt.Fprintf(w, "\n## fieldcov notes on the newer specs (%d)\n\n", len(r.Notes))
		for i, n := range r.Notes {
			if i == maxRows {
				fmt.Fprintf(w, "- ... %d more: see the JSON report.\n", len(r.Notes)-maxRows)
				break
			}
			fmt.Fprintf(w, "- %s\n", mdEscape(n))
		}
	}
	if len(r.Stale) > 0 {
		fmt.Fprintf(w, "\n## Stale drift-accepted.json entries\n\nThese matched nothing this run; remove them.\n\n")
		for _, s := range r.Stale {
			fmt.Fprintf(w, "- `%s`\n", mdEscape(s))
		}
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(w, "\n## Errors\n\n")
		for _, e := range r.Errors {
			fmt.Fprintf(w, "- %s\n", mdEscape(e))
		}
	}
	fmt.Fprint(w, `
## Resolving

For each unmapped item, either
- map it: a new operation goes into reference/_gen/fragments (Invoke-Build Inventory) as an item or a reasoned skip; a new field is implemented under its upstream name, or gets a skip / partial in reference/_gen/fieldmap.d/<lane>.json, after re-pinning;
- for a surface item (kind surface: a command, option, Dockerfile instruction, flag, directive, build check or build arg), implement it after re-pinning, or record it in reference/_gen/deferred.d/upstream.json {"item", "version_seen", "date", "reason"} (it then shows as mapped here and as kind upstream_surface in reference/_gen/deferred.json);
- accept it for now: add {"item", "reason", "date", "version_seen"} to reference/_gen/drift-accepted.json (a version:<source> entry accepts that exact release only); or
- re-pin: refresh reference/upstream, reference/SOURCES.md and $ReferenceSources (Invoke-Build ReferenceVerify), then FieldMap / FieldCoverage.

DriftCheck never changes a pin.
`)
}

func (r *report) json() ([]byte, error) {
	if r.Findings == nil {
		r.Findings = []finding{}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
