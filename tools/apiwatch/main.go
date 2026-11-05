// Command apiwatch compares the Docker Engine API spec this provider has been
// reviewed against (tools/apiwatch/pinned.json + the pinned copy in
// tools/apiwatch/spec/) with a newer or older spec, and prints every new,
// changed or removed endpoint and definition field mapped to the provider type
// that should cover it. See .claude/skills/engine-api-watch/SKILL.md.
//
//	go run ./tools/apiwatch                       # pinned vs the newest published version
//	go run ./tools/apiwatch -from 1.51 -to 1.56   # any two versions
//	go run ./tools/apiwatch -todo -tag P5-0       # the same, as BUILD_REPORT.md TODO lines
//	go run ./tools/apiwatch -check                # exit 1 if the newest published spec is not the pinned one
//	go run ./tools/apiwatch -update               # pin the newest (or -to) version after filing its TODOs
//
// Specs come from the docs.docker.com per-version URLs in pinned.json
// (url_template). Exit codes: 0 ok, 1 -check found an unpinned spec, 2 error.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Pinned is tools/apiwatch/pinned.json.
type Pinned struct {
	Comment string `json:"_comment"`
	// APIVersion is the Engine API version last reviewed, e.g. "1.56".
	APIVersion string `json:"api_version"`
	// URL the pinned copy was downloaded from, and the sha256 of its bytes.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Spec is the pinned copy, relative to pinned.json.
	Spec string `json:"spec"`
	// URLTemplate builds a version's URL; {version} is replaced by e.g. 1.57.
	URLTemplate string `json:"url_template"`
	// ClientAPIVersion is the API version of the Go client the provider is
	// built with (github.com/docker/docker in go.mod), for context.
	ClientAPIVersion string `json:"client_api_version"`
	Reviewed         string `json:"reviewed"`
}

type options struct {
	Pinned           string
	From, To         string
	Todo, Check, Upd bool
	Tag              string
	MaxProbe         int
}

var errUnpinned = errors.New("the newest published Engine API spec is not the pinned one")

func main() {
	var o options
	flag.StringVar(&o.Pinned, "pinned", filepath.Join("tools", "apiwatch", "pinned.json"), "pinned.json path")
	flag.StringVar(&o.From, "from", "", "base API version (default: the pinned copy)")
	flag.StringVar(&o.To, "to", "", "API version to compare with (default: the newest published)")
	flag.BoolVar(&o.Todo, "todo", false, "print BUILD_REPORT.md TODO lines grouped by provider type instead of the table")
	flag.StringVar(&o.Tag, "tag", "P?-0", "lane tag for -todo lines, e.g. P5-0")
	flag.BoolVar(&o.Check, "check", false, "exit 1 if the newest published spec differs from pinned.json (new version, or the pinned one re-published)")
	flag.BoolVar(&o.Upd, "update", false, "pin -to (default: newest) after its changes are filed: rewrite pinned.json and the spec copy")
	flag.IntVar(&o.MaxProbe, "max-probe", 10, "how many versions above the pinned one to probe for the newest")
	flag.Parse()
	err := run(o, http.DefaultClient, os.Stdout)
	switch {
	case errors.Is(err, errUnpinned):
		fmt.Fprintln(os.Stderr, "apiwatch:", err)
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "apiwatch:", err)
		os.Exit(2)
	}
}

func run(o options, hc *http.Client, w io.Writer) error {
	p, err := readPinned(o.Pinned)
	if err != nil {
		return err
	}
	to := o.To
	if to == "" {
		to, err = newestVersion(hc, p, o.MaxProbe)
		if err != nil {
			return err
		}
	}

	if o.Check {
		b, err := fetch(hc, versionURL(p, to))
		if err != nil {
			return err
		}
		sum := sha256Hex(b)
		if to == p.APIVersion && sum == p.SHA256 {
			fmt.Fprintf(w, "apiwatch: v%s is the newest published Engine API and matches pinned.json (sha256 %s)\n", to, short(sum))
			return nil
		}
		if to == p.APIVersion {
			return fmt.Errorf("%w: v%s was re-published (sha256 %s, pinned %s); run `go run ./tools/apiwatch -from %s -to %s` against the old copy, file TODOs, then -update", errUnpinned, to, short(sum), short(p.SHA256), to, to)
		}
		return fmt.Errorf("%w: v%s is published, pinned is v%s; run `go run ./tools/apiwatch -todo`, file the TODO lines in BUILD_REPORT.md, then `go run ./tools/apiwatch -update`", errUnpinned, to, p.APIVersion)
	}

	if o.Upd {
		b, err := fetch(hc, versionURL(p, to))
		if err != nil {
			return err
		}
		if _, err := ParseSpec(b); err != nil {
			return err
		}
		spec := filepath.ToSlash(filepath.Join("spec", "engine-api-v"+to+".yaml"))
		dir := filepath.Dir(o.Pinned)
		if old := filepath.Join(dir, filepath.FromSlash(p.Spec)); p.Spec != "" && p.Spec != spec {
			_ = os.Remove(old)
		}
		if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(spec)), b, 0o644); err != nil {
			return err
		}
		p.APIVersion, p.URL, p.SHA256, p.Spec = to, versionURL(p, to), sha256Hex(b), spec
		p.Reviewed = time.Now().UTC().Format("2006-01-02")
		if err := writePinned(o.Pinned, p); err != nil {
			return err
		}
		fmt.Fprintf(w, "apiwatch: pinned v%s (sha256 %s) -> %s\n", to, short(p.SHA256), spec)
		return nil
	}

	from := o.From
	var base *Spec
	if from == "" || from == p.APIVersion {
		from = p.APIVersion
		b, err := os.ReadFile(filepath.Join(filepath.Dir(o.Pinned), filepath.FromSlash(p.Spec)))
		if err != nil {
			return fmt.Errorf("reading the pinned copy: %w", err)
		}
		if base, err = ParseSpec(b); err != nil {
			return err
		}
	} else if base, err = fetchSpec(hc, versionURL(p, from)); err != nil {
		return err
	}
	head, err := fetchSpec(hc, versionURL(p, to))
	if err != nil {
		return err
	}
	changes := Diff(base, head)
	if o.Todo {
		writeTodo(w, changes, from, to, o.Tag)
	} else {
		writeTable(w, changes, from, to)
	}
	return nil
}

func readPinned(path string) (*Pinned, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Pinned
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.APIVersion == "" || p.URLTemplate == "" || p.Spec == "" {
		return nil, fmt.Errorf("%s: api_version, url_template and spec are required", path)
	}
	return &p, nil
}

func writePinned(path string, p *Pinned) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func versionURL(p *Pinned, v string) string {
	return strings.ReplaceAll(p.URLTemplate, "{version}", v)
}

// newestVersion probes pinned+1, pinned+2, ... and returns the last version
// whose spec exists (404 ends the probe).
func newestVersion(hc *http.Client, p *Pinned, max int) (string, error) {
	major, minor, err := splitVersion(p.APIVersion)
	if err != nil {
		return "", err
	}
	newest := p.APIVersion
	for i := 1; i <= max; i++ {
		v := fmt.Sprintf("%d.%d", major, minor+i)
		ok, err := exists(hc, versionURL(p, v))
		if err != nil {
			return "", err
		}
		if !ok {
			break
		}
		newest = v
	}
	return newest, nil
}

func splitVersion(v string) (int, int, error) {
	a, b, ok := strings.Cut(v, ".")
	major, err1 := strconv.Atoi(a)
	minor, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("bad API version %q, want MAJOR.MINOR", v)
	}
	return major, minor, nil
}

func exists(hc *http.Client, url string) (bool, error) {
	resp, err := hc.Get(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("GET %s: %s", url, resp.Status)
}

func fetch(hc *http.Client, url string) ([]byte, error) {
	resp, err := hc.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func fetchSpec(hc *http.Client, url string) (*Spec, error) {
	b, err := fetch(hc, url)
	if err != nil {
		return nil, err
	}
	return ParseSpec(b)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

func writeTable(w io.Writer, changes []Change, from, to string) {
	fmt.Fprintf(w, "apiwatch: Engine API v%s -> v%s\n\n", from, to)
	if len(changes) == 0 {
		fmt.Fprintln(w, "no endpoint or definition changes")
		return
	}
	counts := map[string]int{}
	for _, c := range changes {
		counts[c.Op]++
		line := fmt.Sprintf("%-8s %-58s -> %s", c.Op, c.Subject, c.Target)
		if c.Detail != "" {
			line += "  (" + c.Detail + ")"
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "\nendpoints: %d new, %d changed, %d removed; definitions: %d new, %d removed; fields: %d added, %d removed\n",
		counts["NEW"], counts["CHANGED"], counts["REMOVED"], counts["DEF+"], counts["DEF-"], counts["FIELD+"], counts["FIELD-"])
}

// writeTodo prints one TODO line per provider type (and one per skipped or
// unmapped group) listing every change it should cover.
func writeTodo(w io.Writer, changes []Change, from, to, tag string) {
	type group struct {
		target Target
		items  []string
	}
	var order []string
	groups := map[string]*group{}
	for _, c := range changes {
		key := c.Target.String()
		g, ok := groups[key]
		if !ok {
			g = &group{target: c.Target}
			groups[key] = g
			order = append(order, key)
		}
		item := c.Op + " " + c.Subject
		if c.Detail != "" && c.Op == "CHANGED" {
			item += " [" + c.Detail + "]"
		}
		g.items = append(g.items, item)
	}
	if len(order) == 0 {
		fmt.Fprintf(w, "[%s] [VERIFIED] apiwatch Engine API v%s -> v%s: no endpoint or definition changes.\n", tag, from, to)
		return
	}
	for _, key := range order {
		g := groups[key]
		switch g.target.Kind {
		case "skip":
			fmt.Fprintf(w, "[%s] [ASSUMED] apiwatch Engine API v%s -> v%s, no provider change (%s): %s\n", tag, from, to, g.target.Reason, strings.Join(g.items, "; "))
		case "unmapped":
			fmt.Fprintf(w, "[%s] [TODO] apiwatch Engine API v%s -> v%s, unmapped (pick a provider type or skip with a reason, then add a rule to tools/apiwatch/mapping.go): %s\n", tag, from, to, strings.Join(g.items, "; "))
		default:
			fmt.Fprintf(w, "[%s] [TODO] apiwatch Engine API v%s -> v%s, %s: review and cover or log as skipped: %s\n", tag, from, to, g.target, strings.Join(g.items, "; "))
		}
	}
}
