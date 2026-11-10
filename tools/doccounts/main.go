// Command doccounts keeps the type counts and short type lists in CLAUDE.md
// and README.md equal to what internal/provider/registry_gen.go registers.
//
//	go run ./tools/doccounts           # check (Invoke-Build DocCountsCheck, in the default task)
//	go run ./tools/doccounts -write    # rewrite the generated blocks (Invoke-Build DocCounts)
//
// Each document carries one generated block between
// "<!-- doccounts:begin ... -->" and "<!-- doccounts:end -->": the count of
// every kind, and the full name list of the short kinds (resources, ephemeral
// resources, provider functions, list resources). Outside the block, the
// check also fails on any "<n> <kind>" phrase ("4 ephemeral resources") whose
// number differs from the registry, and on any backticked `docker_*` name
// that no kind registers. registry_gen.go itself is generated from
// reference/inventory.json and kept in sync by RegistryCheck.
//
// Exit codes: 0 ok, 1 drift found by the check, 2 error. Run from the
// repository root.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	registry := flag.String("registry", "internal/provider/registry_gen.go", "generated registration file")
	files := flag.String("files", "CLAUDE.md,README.md", "comma-separated documents to check")
	require := flag.String("require", "CLAUDE.md,README.md", "comma-separated documents that must carry a generated block")
	write := flag.Bool("write", false, "rewrite the generated blocks instead of checking")
	flag.Parse()
	os.Exit(run(*registry, split(*files), split(*require), *write))
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func run(registryPath string, files, require []string, write bool) int {
	src, err := os.ReadFile(registryPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccounts:", err)
		return 2
	}
	reg, err := ParseRegistry(string(src))
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccounts:", err)
		return 2
	}
	req := map[string]bool{}
	for _, f := range require {
		req[f] = true
	}
	bad := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "doccounts:", err)
			return 2
		}
		doc := string(b)
		if write {
			out, ok := Write(doc, reg)
			if !ok {
				if req[f] {
					fmt.Fprintf(os.Stderr, "doccounts: %s has no %s ... %s block to write into\n", f, beginMarker, endMarker)
					return 2
				}
				continue
			}
			if out != doc {
				if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "doccounts:", err)
					return 2
				}
				fmt.Printf("doccounts: wrote %s\n", f)
			}
			doc = out
		}
		for _, p := range Check(doc, reg, req[f]) {
			fmt.Printf("%s:%s\n", f, p)
			bad++
		}
	}
	if bad > 0 {
		fmt.Printf("doccounts: %d problem(s)\n", bad)
		return 1
	}
	parts := make([]string, 0, len(Kinds))
	for _, k := range Kinds {
		parts = append(parts, fmt.Sprintf("%d %s", len(reg[k.Key]), k.Plural))
	}
	fmt.Printf("doccounts: ok (%s)\n", strings.Join(parts, ", "))
	return 0
}
