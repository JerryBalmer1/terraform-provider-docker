package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without os.Exit: 0 ok, 1 a missing type is not logged as
// [BLOCKED] (or the static list is invalid), 2 usage or I/O error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("parity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.String("check", "", "path of BUILD_REPORT.md: exit 1 unless every missing kreuzwerker type appears on a line containing [BLOCKED]")
	if err := fs.Parse(args); err != nil {
		return 2
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
	if *check == "" {
		return 0
	}
	b, err := os.ReadFile(*check)
	if err != nil {
		fmt.Fprintln(stderr, "parity:", err)
		return 2
	}
	unblocked := Unblocked(kreuzwerker, string(b))
	for _, e := range unblocked {
		fmt.Fprintf(stderr, "parity: %s %s is missing and %s has no [BLOCKED] line naming it\n", e.Kind, e.Name, *check)
	}
	if len(unblocked) > 0 {
		fmt.Fprintf(stderr, "parity: %d missing type(s) not logged; add `[P3-x] [BLOCKED] <lane> <type> <why> <what was tried>` lines or implement them\n", len(unblocked))
		return 1
	}
	fmt.Fprintf(stdout, "parity: every missing type is logged as [BLOCKED] in %s\n", *check)
	return 0
}
