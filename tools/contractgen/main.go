// Command contractgen turns the outputs contract in reference/inventory.json
// into internal/provider/contract_gen.go: the per-item outputs table the
// provider's contract runtime (internal/provider/contract.go) reads, the
// version pins, and one embeddable model struct per item.
//
//	go run ./tools/contractgen                  # regenerate
//	go run ./tools/contractgen -check           # exit 1 when the file on disk is stale
//
// Items that are skipped, of an unknown kind, or without outputs are left
// out (not an error). The generator does not import the provider package,
// so it still runs when contract_gen.go is broken.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without os.Exit: 0 ok, 1 -check found drift, 2 usage, I/O or
// inventory error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("contractgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inv := fs.String("inventory", "reference/inventory.json", "path of the merged inventory")
	out := fs.String("out", "internal/provider/contract_gen.go", "path of the generated Go file")
	check := fs.Bool("check", false, "do not write; exit 1 when -out differs from what would be generated")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	raw, err := os.ReadFile(*inv)
	if err != nil {
		fmt.Fprintln(stderr, "contractgen:", err)
		return 2
	}
	doc, err := Parse(raw)
	if err != nil {
		fmt.Fprintln(stderr, "contractgen:", err)
		return 2
	}
	src, err := Generate(doc)
	if err != nil {
		fmt.Fprintln(stderr, "contractgen:", err)
		return 2
	}
	if *check {
		cur, err := os.ReadFile(*out)
		if err != nil {
			fmt.Fprintln(stderr, "contractgen:", err)
			return 1
		}
		if !bytes.Equal(normalizeEOL(cur), normalizeEOL(src)) {
			fmt.Fprintf(stderr, "contractgen: %s is stale; run go run ./tools/contractgen\n", *out)
			return 1
		}
		fmt.Fprintf(stdout, "contractgen: %s is up to date\n", *out)
		return 0
	}
	if err := os.WriteFile(*out, src, 0o644); err != nil {
		fmt.Fprintln(stderr, "contractgen:", err)
		return 2
	}
	fmt.Fprintf(stdout, "contractgen: wrote %s (%d items)\n", *out, len(doc.Items))
	return 0
}

// normalizeEOL drops CRs: the checkout uses autocrlf.
func normalizeEOL(b []byte) []byte {
	return []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
}
