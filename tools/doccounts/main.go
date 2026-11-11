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
// README.md also carries an evidence block between
// "<!-- doccounts:evidence:begin ... -->" and "<!-- doccounts:evidence:end -->":
// a table of every other number the README states, each computed from repo
// files (see evidence.go): writable upstream fields mapped / skipped /
// partial / gaps from reference/_gen/fieldmap.json; func TestAcc* in
// internal/provider/*_test.go; the compose fixtures TestAccComposeParity
// diffs against the compose CLI; examples/**/*.tests.ps1; and the configs
// go run ./tools/pairwise -gen writes (deterministic; generated into a
// fresh directory under test/harness/.work and removed again).
//
// Exit codes: 0 ok, 1 drift found by the check, 2 error. Run from the
// repository root.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	registry := flag.String("registry", "internal/provider/registry_gen.go", "generated registration file")
	files := flag.String("files", "CLAUDE.md,README.md", "comma-separated documents to check")
	require := flag.String("require", "CLAUDE.md,README.md", "comma-separated documents that must carry a generated block")
	evidence := flag.String("evidence", "README.md", "comma-separated documents that must carry the generated evidence block")
	write := flag.Bool("write", false, "rewrite the generated blocks instead of checking")
	goExe := flag.String("go", "", "go executable for go run ./tools/pairwise (default: the go of the toolchain that built doccounts, else go on PATH)")
	skipPairwise := flag.Bool("skip-pairwise", false, "do not run tools/pairwise (the evidence block then has no pairwise row; for tests only)")
	flag.Parse()
	src := DefaultSources
	src.GoExe = *goExe
	if src.GoExe == "" {
		src.GoExe = toolchainGo()
	}
	src.SkipPairwise = *skipPairwise
	os.Exit(run(*registry, split(*files), split(*require), split(*evidence), src, *write))
}

// toolchainGo is the go binary of the toolchain this program was built with
// (go run ./tools/doccounts), falling back to go on PATH.
func toolchainGo() string {
	exe := "go"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	//lint:ignore SA1019 the build toolchain's GOROOT is the one wanted here
	if root := runtime.GOROOT(); root != "" {
		p := filepath.Join(root, "bin", exe)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "go"
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

func run(registryPath string, files, require, evidence []string, sources EvidenceSources, write bool) int {
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
	evReq := map[string]bool{}
	for _, f := range evidence {
		evReq[f] = true
	}
	var ev Evidence
	if len(evidence) > 0 {
		ev, err = CollectEvidence(sources)
		if err != nil {
			fmt.Fprintln(os.Stderr, "doccounts: evidence:", err)
			return 2
		}
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
			if evReq[f] {
				out, ok := WriteEvidence(doc, reg, ev)
				if !ok {
					fmt.Fprintf(os.Stderr, "doccounts: %s has no %s ... %s block to write into\n", f, evBeginMarker, evEndMarker)
					return 2
				}
				if out != doc {
					if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
						fmt.Fprintln(os.Stderr, "doccounts:", err)
						return 2
					}
					fmt.Printf("doccounts: wrote the evidence block of %s\n", f)
				}
				doc = out
			}
		}
		probs := Check(doc, reg, req[f])
		if evReq[f] {
			probs = append(probs, CheckEvidence(doc, reg, ev, true)...)
		}
		for _, p := range probs {
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
	if len(evidence) > 0 {
		fmt.Printf("doccounts: evidence ok (fields %d: %d mapped, %d skipped, %d partial, %d gaps; %d acceptance tests; %d compose parity fixtures; %d example tests; %d pairwise configs)\n",
			ev.Fields.Total, ev.Fields.Mapped, ev.Fields.Skipped, ev.Fields.Partial, ev.Fields.Gap, ev.AccTests, ev.ComposeFixtures, ev.ExampleTests, ev.PairwiseTotal())
	}
	return 0
}
