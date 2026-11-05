// Command parity prints which kreuzwerker/docker v4.6.0 types this provider
// has, has partially or is missing, plus the types only this provider has.
// The list is maintained by hand: update it whenever a type lands or changes
// status.
//
//	go run ./tools/parity                          # print the table
//	go run ./tools/parity -check BUILD_REPORT.md   # also exit 1 if a missing type
//	                                               # has no [BLOCKED] line naming it
//
// The lists themselves are in tools/parity/paritydata.
package main

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/JerryBalmer1/terraform-provider-docker/tools/parity/paritydata"
)

// The types and static lists live in tools/parity/paritydata so tools/genskill
// can read them too; these aliases keep this command's code unchanged.
type (
	Status = paritydata.Status
	Kind   = paritydata.Kind
	Entry  = paritydata.Entry
	Extra  = paritydata.Extra
)

const (
	Have       = paritydata.Have
	Partial    = paritydata.Partial
	Missing    = paritydata.Missing
	Resource   = paritydata.Resource
	DataSource = paritydata.DataSource
	Action     = paritydata.Action
	Ephemeral  = paritydata.Ephemeral
	List       = paritydata.List
	Function   = paritydata.Function
)

var (
	kreuzwerker = paritydata.Kreuzwerker
	extras      = paritydata.Extras
)

// Print writes the parity table and a summary.
func Print(w io.Writer, entries []Entry, extra []Extra) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tTYPE (kreuzwerker/docker v4.6.0)\tSTATUS\tNOTE")
	counts := map[Status]int{}
	for _, e := range entries {
		counts[e.Status]++
		note := e.Note
		if note == "" {
			note = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Kind, e.Name, e.Status, note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%d kreuzwerker types: %d have, %d partial, %d missing\n",
		len(entries), counts[Have], counts[Partial], counts[Missing])
	if len(extra) == 0 {
		return nil
	}
	fmt.Fprintf(w, "\nOnly in this provider (%d):\n\n", len(extra))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tTYPE\tNOTE")
	for _, x := range extra {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", x.Kind, x.Name, x.Note)
	}
	return tw.Flush()
}

// Unblocked returns the missing entries that no line of report both marks
// [BLOCKED] and names (as a whole word, so docker_image does not match
// docker_image_save).
func Unblocked(entries []Entry, report string) []Entry {
	var blocked []string
	for _, l := range strings.Split(report, "\n") {
		if strings.Contains(l, "[BLOCKED]") {
			blocked = append(blocked, l)
		}
	}
	var out []Entry
	for _, e := range entries {
		if e.Status != Missing {
			continue
		}
		re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(e.Name) + `([^A-Za-z0-9_]|$)`)
		found := false
		for _, l := range blocked {
			if re.MatchString(l) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, e)
		}
	}
	return out
}

// Validate checks the static lists: known kinds and statuses, docker_
// prefixes, no duplicates, sorted within a kind, and no extra that is also a
// kreuzwerker type.
func Validate(entries []Entry, extra []Extra) []string {
	var errs []string
	kinds := map[Kind]bool{Resource: true, DataSource: true, Action: true, Ephemeral: true, List: true, Function: true}
	seen := map[string]bool{}
	byKind := map[Kind][]string{}
	for _, e := range entries {
		key := string(e.Kind) + " " + e.Name
		switch {
		case !kinds[e.Kind]:
			errs = append(errs, key+": unknown kind")
		case e.Status != Have && e.Status != Partial && e.Status != Missing:
			errs = append(errs, fmt.Sprintf("%s: unknown status %q", key, e.Status))
		case e.Kind != Function && !strings.HasPrefix(e.Name, "docker_"):
			errs = append(errs, key+": name lacks docker_ prefix")
		case e.Status == Partial && e.Note == "":
			errs = append(errs, key+": partial needs a note saying what is missing")
		}
		if seen[key] {
			errs = append(errs, key+": duplicate")
		}
		seen[key] = true
		byKind[e.Kind] = append(byKind[e.Kind], e.Name)
	}
	for k, names := range byKind {
		if !sort.StringsAreSorted(names) {
			errs = append(errs, string(k)+": entries not sorted by name")
		}
	}
	for _, x := range extra {
		key := string(x.Kind) + " " + x.Name
		if !kinds[x.Kind] {
			errs = append(errs, key+": unknown kind")
		}
		if seen[key] {
			errs = append(errs, key+": listed as an extra but is a kreuzwerker type (or a duplicate)")
		}
		seen[key] = true
	}
	sort.Strings(errs)
	return errs
}
