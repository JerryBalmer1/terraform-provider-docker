package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Row is one test unit's last outcome in a gate ledger (test/harness/.ledger/<gate>.json).
// Status "pass" (also a test that skipped itself under the same inputs and environment) is the
// only status that lets a later run skip the unit; "fail" always runs again.
type Row struct {
	Key        string `json:"key"`
	Hash       string `json:"hash"`
	Status     string `json:"status"`
	PassedAt   string `json:"passed_at,omitempty"`
	Commit     string `json:"commit"`
	DurationMs int64  `json:"duration_ms"`
}

// Ledger is a gate's ledger file. Rows are kept sorted by key so diffs stay readable.
type Ledger struct {
	Gate string `json:"gate"`
	Rows []Row  `json:"rows"`
}

// Unit is one test unit with the repo-relative files its outcome depends on (besides the
// always-in set). Unknown is set, with the reason, when its inputs cannot be inferred.
type Unit struct {
	Key     string            `json:"key"`
	Files   []string          `json:"-"`
	Unknown string            `json:"unknown,omitempty"`
	Extra   map[string]string `json:"extra,omitempty"`
}

// Decision is the plan for one unit.
type Decision struct {
	Key     string            `json:"key"`
	Hash    string            `json:"hash"`
	Run     bool              `json:"run"`
	Reason  string            `json:"reason"`
	Files   int               `json:"files"`
	Unknown string            `json:"unknown,omitempty"`
	Extra   map[string]string `json:"extra,omitempty"`
}

// Result is the outcome of one unit in this run, as the gate task reports it.
type Result struct {
	Key        string `json:"key"`
	Status     string `json:"status"` // pass, skip (the test skipped itself), fail
	DurationMs int64  `json:"duration_ms"`
}

func loadLedger(path, gate string) (*Ledger, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Ledger{Gate: gate}, nil
	}
	if err != nil {
		return nil, err
	}
	var l Ledger
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Gate == "" {
		l.Gate = gate
	}
	return &l, nil
}

func (l *Ledger) find(key string) *Row {
	for i := range l.Rows {
		if l.Rows[i].Key == key {
			return &l.Rows[i]
		}
	}
	return nil
}

func (l *Ledger) save(path string) error {
	sort.Slice(l.Rows, func(i, j int) bool { return l.Rows[i].Key < l.Rows[j].Key })
	if l.Rows == nil {
		l.Rows = []Row{}
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// hashSet hashes a set of repo-relative files (content with CRLF folded to LF, so an autocrlf
// checkout hashes like a Linux one) together with extra strings (the environment fingerprint).
// It returns the hex digest; a missing file hashes as "missing".
func hashSet(root string, files []string, extra []string, cache map[string]string) string {
	files = uniqSorted(files)
	h := sha256.New()
	for _, f := range files {
		fh, ok := cache[f]
		if !ok {
			p := filepath.FromSlash(f)
			if !filepath.IsAbs(p) {
				p = filepath.Join(root, p)
			}
			fh = fileHash(p)
			if cache != nil {
				cache[f] = fh
			}
		}
		fmt.Fprintf(h, "%s\x00%s\n", f, fh)
	}
	for _, e := range extra {
		fmt.Fprintf(h, "extra\x00%s\n", e)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fileHash(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "missing"
	}
	b = []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func uniqSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// plan decides, for each unit, whether it runs. global is the always-in set (harness, build/,
// the ledger tool, go.mod, go.sum, the generated registry and contract, the pins); it is part of
// every unit's hash, so any change to it runs every unit. full ignores the ledger.
func plan(root string, units []Unit, global []string, env []string, led *Ledger, full bool) []Decision {
	cache := map[string]string{}
	out := make([]Decision, 0, len(units))
	for _, u := range units {
		d := Decision{Key: u.Key, Unknown: u.Unknown, Extra: u.Extra, Files: len(uniqSorted(u.Files))}
		d.Hash = hashSet(root, append(append([]string{}, global...), u.Files...), env, cache)
		row := led.find(u.Key)
		switch {
		case full:
			d.Run, d.Reason = true, "full run (ledger off)"
		case u.Unknown != "":
			d.Run, d.Reason = true, "inputs unknown: "+u.Unknown
		case row == nil:
			d.Run, d.Reason = true, "no record"
		case row.Status != "pass":
			d.Run, d.Reason = true, "last run failed at "+row.Commit
		case row.Hash != d.Hash:
			d.Run, d.Reason = true, "inputs changed since "+row.Commit
		default:
			d.Run, d.Reason = false, "skipped: unchanged since "+row.Commit
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// record writes this run's outcomes into the ledger: a unit that ran and passed (or skipped
// itself) gets a pass row with the hash it was planned with; a unit that ran and failed gets a
// fail row (never a pass row); a unit that ran but has no result (harness error, crash) loses
// its row, so it runs next time. Units not run keep their rows. Unknown-input units never get a
// pass row either: they always run.
func record(led *Ledger, decisions []Decision, results []Result, commit, now string) (ran, skipped, failed int) {
	res := map[string]Result{}
	for _, r := range results {
		res[r.Key] = r
	}
	keep := map[string]bool{}
	for _, d := range decisions {
		keep[d.Key] = true
	}
	var rows []Row
	for _, r := range led.Rows { // drop rows of units that no longer exist
		if keep[r.Key] {
			rows = append(rows, r)
		}
	}
	led.Rows = rows
	for _, d := range decisions {
		r, ok := res[d.Key]
		if !d.Run || (ok && r.Status == "notrun") {
			// skipped by the ledger, or left out by the gate's own selection (-Filter): row untouched
			skipped++
			continue
		}
		ran++
		pass := ok && (r.Status == "pass" || r.Status == "skip")
		if !pass {
			failed++
		}
		led.remove(d.Key)
		switch {
		case !ok:
			// no result: no row, so it runs next time
		case d.Unknown != "":
			// always runs; nothing to remember
		case pass:
			led.Rows = append(led.Rows, Row{Key: d.Key, Hash: d.Hash, Status: "pass", PassedAt: now, Commit: commit, DurationMs: r.DurationMs})
		default:
			led.Rows = append(led.Rows, Row{Key: d.Key, Hash: d.Hash, Status: "fail", Commit: commit, DurationMs: r.DurationMs})
		}
	}
	return ran, skipped, failed
}

func (l *Ledger) remove(key string) {
	out := l.Rows[:0]
	for _, r := range l.Rows {
		if r.Key != key {
			out = append(out, r)
		}
	}
	l.Rows = out
}
