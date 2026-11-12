// Command ledger is the gate ledger (Pass 6g): it records, per test unit, the hash of the inputs
// the unit depends on when it passes, and plans the next run of a gate so that units whose input
// hash is unchanged since a recorded pass are skipped.
//
// Gates and units:
//
//	unit      one Go package (go test in the default task's Test)
//	testacc   one top-level TestAcc* function in internal/provider
//	examples  one examples/<kind>/<name> folder with a <name>.tests.ps1
//	pairwise  one generated pairwise config <type>/<nn>
//
// Usage:
//
//	ledger plan   -gate <gate> -out <plan.json> [-full] [-swarm-regex <re>] [-configs <dir>]
//	ledger record -plan <plan.json> -results <results.json> [-summary <file>]
//
// plan prints one line per unit (run or skipped, and why) and writes the plan; the gate task runs
// the units the plan marks to run and writes results.json ([{key, status, duration_ms}], status
// pass, skip or fail); record then writes test/harness/.ledger/<gate>.json once, at the end of
// the gate. A unit's hash covers its inferred inputs plus the always-in set (go.mod, go.sum,
// registry_gen.go, contract_gen.go, reference/SOURCES.md, the build script, everything under
// test/harness/ except git-ignored files and the ledgers, build/ and tools/ledger/) and, for the
// harness gates, the TF_ACC_* / TPD_ACC_* / DOCKER_HUB_* environment. Rows are never edited by hand.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var gates = map[string]bool{"unit": true, "testacc": true, "examples": true, "pairwise": true}

type planFile struct {
	Gate      string     `json:"gate"`
	Full      bool       `json:"full"`
	Commit    string     `json:"commit"`
	Decisions []Decision `json:"decisions"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ledger plan|record ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "plan":
		err = cmdPlan(os.Args[2:])
	case "record":
		err = cmdRecord(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ledger:", err)
		os.Exit(1)
	}
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no go.mod above %s", wd)
		}
	}
}

func ledgerPath(root, gate string) string {
	return filepath.Join(root, filepath.FromSlash(LedgerDir), gate+".json")
}

// envFingerprint: the variables that decide whether a harness test runs or skips itself. Values
// are hashed, never stored.
func envFingerprint() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		ku := strings.ToUpper(k)
		if strings.HasPrefix(ku, "TF_ACC_") || strings.HasPrefix(ku, "TPD_ACC_") || strings.HasPrefix(ku, "DOCKER_HUB_") {
			s := sha256.Sum256([]byte(v))
			out = append(out, ku+"="+hex.EncodeToString(s[:8]))
		}
	}
	sort.Strings(out)
	return out
}

func currentCommit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	c := strings.TrimSpace(string(out))
	st, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err == nil {
		for _, l := range strings.Split(string(st), "\n") {
			if len(l) > 3 && !strings.Contains(l, LedgerDir) {
				return c + "+dirty"
			}
		}
	}
	return c
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	gate := fs.String("gate", "", "unit, testacc, examples or pairwise")
	out := fs.String("out", "", "plan file to write")
	full := fs.Bool("full", false, "ignore the ledger: run every unit")
	swarm := fs.String("swarm-regex", "", "testacc: the swarm-pass regex ($SwarmAccRegex)")
	configs := fs.String("configs", "", "pairwise: the generated configs directory")
	_ = fs.Parse(args)
	if !gates[*gate] || *out == "" {
		return fmt.Errorf("plan needs -gate (unit, testacc, examples, pairwise) and -out")
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	units, global, env, err := gatherUnits(root, *gate, *swarm, *configs)
	if err != nil {
		return err
	}
	led, err := loadLedger(ledgerPath(root, *gate), *gate)
	if err != nil {
		return err
	}
	ds := plan(root, units, global, env, led, *full)
	p := planFile{Gate: *gate, Full: *full, Commit: currentCommit(root), Decisions: ds}
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return err
	}
	run, unknown := 0, 0
	for _, d := range ds {
		state := "skip"
		if d.Run {
			run++
			state = "RUN "
		}
		if d.Unknown != "" {
			unknown++
		}
		fmt.Printf("ledger %s %s %-60s %s\n", *gate, state, d.Key, d.Reason)
	}
	fmt.Printf("ledger %s plan: %d units, run %d, skip %d; inputs inferred %d, unknown (always run) %d\n",
		*gate, len(ds), run, len(ds)-run, len(ds)-unknown, unknown)
	return nil
}

func gatherUnits(root, gate, swarm, configs string) ([]Unit, []string, []string, error) {
	r, err := loadRepo(root)
	if err != nil {
		return nil, nil, nil, err
	}
	static, err := loadStatic(root)
	if err != nil {
		return nil, nil, nil, err
	}
	global := r.globalFiles()
	if gate == "unit" {
		u, err := unitUnits(r, static)
		return u, global, nil, err
	}
	env := envFingerprint()
	g, err := loadProvGraph(r, providerEmbeds(root))
	if err != nil {
		return nil, nil, nil, err
	}
	switch gate {
	case "testacc":
		var re *regexp.Regexp
		if swarm != "" {
			if re, err = regexp.Compile(swarm); err != nil {
				return nil, nil, nil, err
			}
		}
		return testAccUnits(r, g, static, re), global, env, nil
	case "examples":
		return exampleUnits(r, g, static), global, env, nil
	default:
		if configs == "" {
			return nil, nil, nil, fmt.Errorf("pairwise needs -configs")
		}
		abs, _ := filepath.Abs(configs)
		u, err := pairwiseUnits(r, g, static, abs)
		return u, global, env, err
	}
}

func providerEmbeds(root string) []string {
	cmd := exec.Command("go", "list", "-f", "{{range .EmbedFiles}}{{.}}\n{{end}}", "./"+providerDir)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, providerDir+"/"+filepath.ToSlash(l))
		}
	}
	return files
}

func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	planPath := fs.String("plan", "", "plan file written by ledger plan")
	resPath := fs.String("results", "", "results file written by the gate")
	summary := fs.String("summary", os.Getenv("TPD_LEDGER_SUMMARY"), "append the summary line to this file (Invoke-Logged)")
	_ = fs.Parse(args)
	root, err := repoRoot()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*planPath)
	if err != nil {
		return err
	}
	var p planFile
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var results []Result
	if *resPath != "" {
		if rb, err := os.ReadFile(*resPath); err == nil && len(strings.TrimSpace(string(rb))) > 0 {
			if err := json.Unmarshal(rb, &results); err != nil {
				return fmt.Errorf("%s: %w", *resPath, err)
			}
		}
	}
	lp := ledgerPath(root, p.Gate)
	led, err := loadLedger(lp, p.Gate)
	if err != nil {
		return err
	}
	ran, skipped, failed := record(led, p.Decisions, results, p.Commit, time.Now().UTC().Format(time.RFC3339))
	if err := led.save(lp); err != nil {
		return err
	}
	line := fmt.Sprintf("%s: ran %d, skipped %d, failed %d", p.Gate, ran, skipped, failed)
	fmt.Println("ledger " + line)
	if *summary != "" {
		f, err := os.OpenFile(*summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, line)
			f.Close()
		}
	}
	return nil
}
