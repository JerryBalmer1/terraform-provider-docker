// Command deferred writes reference/_gen/deferred.json: everything the
// project knowingly does not do, each with its reason and the file it comes
// from, so a release can say exactly what is left out.
//
//	go run ./tools/deferred                     # check: exit 1 when deferred.json is stale
//	go run ./tools/deferred -write              # regenerate deferred.json (Invoke-Build DeferredReport)
//	go run ./tools/deferred -base <old.json>    # also list the items added or removed since <old.json>
//
// Kinds, and where each comes from:
//
//	field_skip         reference/_gen/fieldmap.d/<lane>.json entries with "skip": an upstream writable field the
//	                   provider deliberately does not set
//	field_partial      the same files, entries with "partial": a field set only in part
//	partial_marker     `// PARTIAL(pass5) <item id> <note>` comments in non-test internal/provider/*.go (the same
//	                   pattern reference/_gen/merge.py reads)
//	inventory_skip     reference/inventory.json items of kind "skip" (upstream operations with no provider type)
//	acceptance         reference/_gen/deferred.d/acceptance.json: acceptance tests (or test paths) that cannot run
//	                   for real on the dind harness; each names its test function and file, which must exist
//	example            reference/_gen/deferred.d/examples.json: examples/ folders whose test only validates them, or
//	                   parts of an example the test rewrites or removes before applying it on the harness; each
//	                   folder must exist
//	diagnostic         reference/_gen/deferred.d/diagnostics.json: connection failures (hijacked streams over TLS,
//	                   TLS through a proxy, ...) that still surface the raw client error instead of the Configure
//	                   preflight diagnostic; each names the file whose code would carry it, which must exist
//	known_issue        reference/_gen/deferred.d/known_issues.json: behaviour gaps a release ships with (a type
//	                   that differs from upstream or is unverified somewhere); each names its inventory item (must
//	                   be in reference/inventory.json), the file whose code carries the gap (must exist), a
//	                   reproduction (copied into the item as "reproduce"), the docs pages that describe it and
//	                   whether CHANGELOG.md lists it; every docs page, and CHANGELOG.md when changelog is true,
//	                   must mention the id
//	upstream_surface   reference/_gen/deferred.d/upstream.json: upstream surface items (DriftCheck surface
//	                   findings <source>:<kind>/<name>: a command, option, Dockerfile instruction, flag,
//	                   directive, build check or build arg) a newer release added and that are knowingly not
//	                   implemented; tools/drift reports them as mapped
//
// Every item has kind, id, reason and source (a repository path, no line
// numbers, so unrelated edits do not make the file stale); known_issue items
// also have reproduce. Items are sorted
// by kind, then id. Exit codes: 0 ok, 1 stale (check mode), 2 error. Run from
// the repository root.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "reference/_gen/deferred.json", "output file, relative to -root")
	write := flag.Bool("write", false, "rewrite the output instead of checking it")
	base := flag.String("base", "", "an earlier deferred.json to diff against (missing or empty file = no earlier version)")
	flag.Parse()
	os.Exit(run(*root, *out, *base, *write))
}

func run(root, out, base string, write bool) int {
	doc, err := Collect(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "deferred:", err)
		return 2
	}
	data, err := doc.Marshal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "deferred:", err)
		return 2
	}
	path := root + "/" + out
	fmt.Print(doc.Summary())
	if base != "" {
		var old *Doc
		if b, err := os.ReadFile(base); err == nil && len(bytes.TrimSpace(b)) > 0 {
			if old, err = Parse(b); err != nil {
				fmt.Fprintln(os.Stderr, "deferred: base:", err)
				return 2
			}
		}
		fmt.Print(Diff(old, doc))
	}
	if write {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "deferred:", err)
			return 2
		}
		fmt.Println("wrote", out)
		return 0
	}
	cur, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(normalize(cur), data) {
		fmt.Fprintf(os.Stderr, "deferred: %s is stale; run Invoke-Build DeferredReport\n", out)
		return 1
	}
	return 0
}

// normalize drops CRs so a checkout with autocrlf compares equal.
func normalize(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
