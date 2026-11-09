package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/JerryBalmer1/terraform-provider-docker/tools/parity/paritydata"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without os.Exit: 0 ok; 1 a kreuzwerker attribute is unlisted,
// an open gap or dropped without a reason in renames.json, a missing type is
// not logged as [BLOCKED] (with -check), or the static list is invalid; 2
// usage or I/O error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("parity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.String("check", "", "path of BUILD_REPORT.md: also exit 1 unless every missing kreuzwerker type appears on a line containing [BLOCKED]")
	renames := fs.String("renames", "", "path of renames.json (default: reference/_gen/renames.json under the module root)")
	typesOnly := fs.Bool("types-only", false, "print the type table only; skip the attribute check")
	extract := fs.String("extract", "", "path of a kreuzwerker v4.6.0 checkout: rewrite paritydata/"+paritydata.KreuzwerkerAttrsFile+" from its docs/ and exit")
	commit := fs.String("commit", "1e48613cd36acab30e9146aae6f036ea8e40a288", "with -extract: the checkout's commit, recorded in the file")
	out := fs.String("out", "", "with -extract: output path (default tools/parity/paritydata/"+paritydata.KreuzwerkerAttrsFile+" under the module root)")
	extractPrev := fs.String("extract-previous", "", "path of a genskill reference directory (.claude/skills/terraform-provider-docker/reference as of -commit): rewrite paritydata/"+paritydata.PreviousAttrsFile+" from it and exit")
	reference := fs.String("reference", "", "with the attribute check: the current genskill reference directory (default .claude/skills/terraform-provider-docker/reference under the module root)")
	guide := fs.String("guide", "", "write the migration guide template (templates/guides/migrating-from-kreuzwerker.md.tmpl) from renames.json and exit")
	guideCheck := fs.String("guide-check", "", "exit 1 if this migration guide template differs from what -guide writes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	renamesPath := func() string {
		if *renames != "" {
			return *renames
		}
		return findRepoFile(filepath.Join("reference", "_gen", "renames.json"))
	}
	if *guide != "" || *guideCheck != "" {
		doc, err := LoadRenames(renamesPath())
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		if *guide != "" {
			if err := writeGuide(*guide, doc, false); err != nil {
				fmt.Fprintln(stderr, "parity:", err)
				return 2
			}
			fmt.Fprintf(stdout, "parity: wrote %s\n", *guide)
			return 0
		}
		if err := writeGuide(*guideCheck, doc, true); err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 1
		}
		fmt.Fprintf(stdout, "parity: %s is up to date\n", *guideCheck)
		return 0
	}
	if *extractPrev != "" {
		attrs, err := ParseReferenceDir(*extractPrev)
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		dst := *out
		if dst == "" {
			dst = findRepoFile(filepath.Join("tools", "parity", "paritydata", paritydata.PreviousAttrsFile))
		}
		if err := WritePreviousAttrs(dst, *commit, attrs); err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		fmt.Fprintf(stdout, "parity: wrote %d previous attributes to %s\n", len(attrs), dst)
		return 0
	}
	if *extract != "" {
		attrs, err := ExtractAttrs(filepath.Join(*extract, "docs"))
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		dst := *out
		if dst == "" {
			dst = findRepoFile(filepath.Join("tools", "parity", "paritydata", paritydata.KreuzwerkerAttrsFile))
		}
		if err := WriteAttrs(dst, *commit, attrs); err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		fmt.Fprintf(stdout, "parity: wrote %d kreuzwerker attributes to %s\n", len(attrs), dst)
		return 0
	}
	if errs := Validate(kreuzwerker, extras); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(stderr, "parity: invalid list:", e)
		}
		return 1
	}
	if err := Print(stdout, kreuzwerker, extras); err != nil {
		fmt.Fprintln(stderr, "parity:", err)
		return 2
	}
	code := 0
	if !*typesOnly {
		attrs, err := paritydata.KreuzwerkerAttrs()
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		path := renamesPath()
		doc, err := LoadRenames(path)
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		probs, counts := CheckAttrs(attrs.Attributes, doc)
		fmt.Fprintf(stdout, "\n%d kreuzwerker v4.6.0 attributes: %d mapped, %d dropped with a reason, %d open\n",
			len(attrs.Attributes), counts["mapped"], counts["dropped"], len(probs))
		if len(probs) > 0 {
			PrintAttrProblems(stderr, probs)
			fmt.Fprintf(stderr, "parity: %d kreuzwerker attribute(s) not accounted for in %s\n", len(probs), path)
			code = 1
		}
		// Our own schema changes since before Pass 5: the repo's reference
		// against the repo's renames.json (with a custom -renames, only when
		// -reference is given too).
		if *renames != "" && *reference == "" {
			return finishCheck(code, *check, stdout, stderr)
		}
		prev, err := paritydata.PreviousAttrs()
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		refDir := *reference
		if refDir == "" {
			refDir = findRepoFile(filepath.Join(".claude", "skills", "terraform-provider-docker", "reference"))
		}
		cur, err := ParseReferenceDir(refDir)
		if err != nil {
			fmt.Fprintln(stderr, "parity:", err)
			return 2
		}
		unrec := CheckPrevious(prev.Attributes, cur, doc)
		fmt.Fprintf(stdout, "%d of our pre-Pass 5 attributes (%s): %d gone or changed type with no renames.json row\n",
			len(prev.Attributes), prev.Commit, len(unrec))
		if len(unrec) > 0 {
			PrintAttrProblems(stderr, unrec)
			code = 1
		}
	}
	return finishCheck(code, *check, stdout, stderr)
}

// finishCheck runs the -check part (missing types logged as [BLOCKED]) and
// returns the exit code.
func finishCheck(code int, check string, stdout, stderr io.Writer) int {
	if check == "" {
		return code
	}
	b, err := os.ReadFile(check)
	if err != nil {
		fmt.Fprintln(stderr, "parity:", err)
		return 2
	}
	unblocked := Unblocked(kreuzwerker, string(b))
	for _, e := range unblocked {
		fmt.Fprintf(stderr, "parity: %s %s is missing and %s has no [BLOCKED] line naming it\n", e.Kind, e.Name, check)
	}
	if len(unblocked) > 0 {
		fmt.Fprintf(stderr, "parity: %d missing type(s) not logged; add `[P3-x] [BLOCKED] <lane> <type> <why> <what was tried>` lines or implement them\n", len(unblocked))
		return 1
	}
	fmt.Fprintf(stdout, "parity: every missing type is logged as [BLOCKED] in %s\n", check)
	return code
}
