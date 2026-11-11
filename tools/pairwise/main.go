// Command pairwise generates pairwise (2-way) combination tests for
// docker_container, docker_service, docker_network and docker_volume, and
// checks their results on the dind harness.
//
// Modes (exactly one):
//
//	-schema <type>                 list the live schema of a resource type (model authoring aid)
//	-gen -out <dir> [-types a,b]   generate one Terraform root module per covering-array row
//	-compare -dir <config dir>     compare the applied state with the daemon's inspect (needs DOCKER_HOST)
//	-plan-summary -dir <config dir> summarize the second plan (plan.json) into plan-summary.json
//	-report -out-dir <dir> -results <file>  merge every config's results into one JSON file and print a summary
//
// Models live in reference/_gen/pairwise/<type>.json (see model.go for the
// format). The attribute names, types and nesting come from the provider's
// own schema; field paths into the daemon's inspect come from the outputs
// contract (reference/inventory.json, the `fields` of each type's structured
// `inspect` output). Pass 6b Lane W.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var allTypes = []string{"docker_container", "docker_network", "docker_service", "docker_volume"}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pairwise:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pairwise", flag.ContinueOnError)
	schemaType := fs.String("schema", "", "list the live schema of this resource type")
	gen := fs.Bool("gen", false, "generate configs")
	compare := fs.Bool("compare", false, "compare one applied config with the daemon's inspect")
	planSummary := fs.Bool("plan-summary", false, "summarize one config's second plan")
	report := fs.Bool("report", false, "merge every config's results")
	modelDir := fs.String("models", filepath.Join("reference", "_gen", "pairwise"), "model directory")
	inventory := fs.String("inventory", filepath.Join("reference", "inventory.json"), "reference/inventory.json (outputs contract)")
	out := fs.String("out", "", "-gen: output directory (replaced)")
	typesFlag := fs.String("types", strings.Join(allTypes, ","), "-gen: comma-separated resource types")
	dir := fs.String("dir", "", "-compare / -plan-summary: one config directory")
	host := fs.String("host", os.Getenv("DOCKER_HOST"), "-compare: Engine endpoint (tcp://host:port)")
	outDir := fs.String("out-dir", "", "-report: the -gen output directory")
	results := fs.String("results", "", "-report: result JSON file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	modes := 0
	for _, b := range []bool{*schemaType != "", *gen, *compare, *planSummary, *report} {
		if b {
			modes++
		}
	}
	if modes != 1 {
		return fmt.Errorf("give exactly one of -schema, -gen, -compare, -plan-summary, -report")
	}
	ctx := context.Background()
	switch {
	case *schemaType != "":
		ss, err := loadProviderSchemas(ctx, defaultProvider, []string{*schemaType})
		if err != nil {
			return err
		}
		for _, l := range dumpSchema(ss[*schemaType]) {
			fmt.Println(l)
		}
		return nil
	case *gen:
		if *out == "" {
			return fmt.Errorf("-gen needs -out")
		}
		types := splitList(*typesFlag)
		ss, err := loadProviderSchemas(ctx, defaultProvider, types)
		if err != nil {
			return err
		}
		contract, err := loadContract(*inventory)
		if err != nil {
			return err
		}
		for _, t := range types {
			m, err := loadModel(filepath.Join(*modelDir, t+".json"))
			if err != nil {
				return err
			}
			cm, err := compileModel(m, ss[t], contract)
			if err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			rows, stats, err := cover(cm)
			if err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			if err := writeConfigs(*out, cm, rows); err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
			fmt.Printf("%s: %d attributes, %d values, %d constraints, %d pairs (%d excluded by constraints), %d configs\n",
				t, len(cm.Factors), stats.Values, len(cm.Constraints), stats.Pairs, stats.Excluded, len(rows))
		}
		return nil
	case *compare:
		if *dir == "" {
			return fmt.Errorf("-compare needs -dir")
		}
		res, err := compareDir(ctx, *dir, *host)
		if err != nil {
			return err
		}
		for _, m := range res.Mismatches {
			fmt.Printf("MISMATCH %s [%s] %s: expected %s, observed %s\n", m.Attr, m.Label, m.Upstream, m.Expected, m.Observed)
		}
		return nil
	case *planSummary:
		if *dir == "" {
			return fmt.Errorf("-plan-summary needs -dir")
		}
		ps, err := planSummaryDir(*dir)
		if err != nil {
			return err
		}
		for _, c := range ps.Changes {
			fmt.Printf("PLAN %s %s: %s\n", c.Address, strings.Join(c.Actions, ","), strings.Join(c.Paths, ", "))
		}
		return nil
	default:
		if *outDir == "" || *results == "" {
			return fmt.Errorf("-report needs -out-dir and -results")
		}
		rep, err := buildReport(*outDir)
		if err != nil {
			return err
		}
		if err := writeJSON(*results, rep); err != nil {
			return err
		}
		for _, l := range rep.summaryLines() {
			fmt.Println(l)
		}
		return nil
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
