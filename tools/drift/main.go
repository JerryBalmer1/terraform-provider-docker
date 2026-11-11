// Command drift reports upstream drift: for every source pinned in
// reference/SOURCES.md it finds the newest upstream release and, when a
// source with a field model moved, diffs the new spec against the pinned one
// with tools/fieldcov (new and removed Engine API operations, new and
// removed writable fields, each checked against reference/inventory.json and
// the field map). It never changes a pin and never writes under reference/:
// fetched specs and the report go to -work (default
// test/harness/.logs/drift under the repository, git-ignored; never %TEMP%).
//
//	go run ./tools/drift                           probe, diff, write the report, exit 0/1/2
//	go run ./tools/drift -report FILE              Markdown report path (the JSON report is FILE with .json)
//	go run ./tools/drift -to moby=api/v1.55.0      what-if: compare against this version instead of the newest
//
// Newest release, per source:
//   - git tags (git ls-remote --tags, no token): the highest
//     <prefix>MAJOR.MINOR.PATCH tag (pre-releases never count): moby api/v*,
//     compose-go v*, docker/compose v*, docker/cli v*, buildkit dockerfile/*,
//     buildx v*.
//   - compose-spec has no tags or releases: it is pinned to a main commit and
//     tracked against main HEAD (git ls-remote refs/heads/main). Any new commit
//     counts as moved; only a schema change produces field findings.
//   - docs.docker.com Engine API spec: versions above the pinned one are
//     probed by URL until a 404; the pinned version re-published with a
//     different sha256 also counts as moved.
//
// Exit codes: 0 no unaccepted drift; 1 unmapped items that are not in
// reference/_gen/drift-accepted.json (a new operation not in the inventory,
// a new writable field fieldcov cannot map, or an added, removed or changed
// surface item of a documentation-only source not deferred in
// reference/_gen/deferred.d/upstream.json); 2 an error (network, fieldcov, bad input). Removals
// are reported, never fail. The report is written in every case.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type overrideFlag map[string]string

func (o overrideFlag) String() string { return fmt.Sprint(map[string]string(o)) }

func (o overrideFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" || val == "" {
		return errors.New("want source=version")
	}
	o[k] = val
	return nil
}

func main() {
	os.Exit(mainErr())
}

func mainErr() int {
	ov := overrideFlag{}
	repoFlag := flag.String("repo", "", "repository root (default: found from the working directory)")
	work := flag.String("work", "", "work directory for fetched specs and fieldcov output (default REPO/test/harness/.logs/drift)")
	reportPath := flag.String("report", "", "Markdown report path (default WORK/drift-report.md; the JSON report is the same path with .json)")
	goExe := flag.String("go", envOr("GO", "go"), "go command (runs tools/fieldcov)")
	gitExe := flag.String("git", "git", "git command (git ls-remote)")
	flag.Var(ov, "to", "source=version: compare against this version instead of the newest (repeatable; what-if runs)")
	maxProbe := flag.Int("max-probe", 10, "docs.docker.com: API versions above the pinned one to probe")
	logPath := flag.String("log", "", "drift log of moved sources with no surface change (default REPO/reference/_gen/drift-log.json; \"-\" = do not write)")
	flag.Parse()

	repo := *repoFlag
	if repo == "" {
		var err error
		if repo, err = repoRoot(); err != nil {
			fmt.Fprintln(os.Stderr, "drift:", err)
			return 2
		}
	}
	if *work == "" {
		*work = defaultWorkDir(repo)
	}
	if *reportPath == "" {
		*reportPath = filepath.Join(*work, "drift-report.md")
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	pins, err := loadPins(filepath.Join(repo, "reference", "SOURCES.md"), defaultSources)
	if err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	acc, err := loadAccepted(filepath.Join(repo, "reference", "_gen", "drift-accepted.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	deferred, err := loadUpstreamDeferrals(filepath.Join(repo, "reference", "_gen", "deferred.d", "upstream.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	switch *logPath {
	case "":
		*logPath = filepath.Join(repo, "reference", "_gen", "drift-log.json")
	case "-":
		*logPath = ""
	}
	for k := range ov {
		if _, ok := pins[k]; !ok {
			fmt.Fprintf(os.Stderr, "drift: -to %s: unknown source\n", k)
			return 2
		}
	}
	o := options{Repo: repo, Work: *work, Sources: defaultSources, Pins: pins, Accepted: acc, Overrides: ov,
		MaxProbe: *maxProbe, Now: time.Now(), Docs: &gitDocs{git: *gitExe}, Deferred: deferred, LogPath: *logPath}
	rm := &netRemote{git: *gitExe, hc: &http.Client{Timeout: 2 * time.Minute}}
	fr := &goFieldcov{goExe: *goExe, repo: repo, work: *work}
	r := run(o, rm, fr)

	var md bytes.Buffer
	r.writeMarkdown(&md)
	js, err := r.json()
	if err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	jsonPath := strings.TrimSuffix(*reportPath, filepath.Ext(*reportPath)) + ".json"
	if err := os.MkdirAll(filepath.Dir(*reportPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	if err := os.WriteFile(*reportPath, md.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	if err := os.WriteFile(jsonPath, js, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "drift:", err)
		return 2
	}
	r.writeConsole(os.Stdout)
	fmt.Println("drift: report", *reportPath)
	fmt.Println("drift: json  ", jsonPath)
	switch r.verdict() {
	case "ERROR":
		return 2
	case "FAIL":
		return 1
	}
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func repoRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "reference", "SOURCES.md")); err == nil {
				return d, nil
			}
		}
		p := filepath.Dir(d)
		if p == d {
			return "", errors.New("run from inside the terraform-provider-docker repository")
		}
		d = p
	}
}

// netRemote is the real network: git ls-remote (works without a token in CI)
// and HTTP GET.
type netRemote struct {
	git string
	hc  *http.Client
}

func (n *netRemote) lsRemote(args ...string) ([]string, error) {
	cmd := exec.Command(n.git, append([]string{"ls-remote"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return parseLsRemote(string(out)), nil
}

// parseLsRemote returns the ref names of git ls-remote output, with the
// commit before a tab: "<sha>\t<ref>" lines become "<ref>\t<sha>" pairs
// split by the callers.
func parseLsRemote(out string) []string {
	var refs []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		refs = append(refs, ref+"\t"+sha)
	}
	return refs
}

func (n *netRemote) Tags(repo string) ([]string, error) {
	refs, err := n.lsRemote("--tags", "--refs", repo)
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, r := range refs {
		ref, _, _ := strings.Cut(r, "\t")
		if t, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

func (n *netRemote) Branch(repo, branch string) (string, error) {
	refs, err := n.lsRemote(repo, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	for _, r := range refs {
		ref, sha, _ := strings.Cut(r, "\t")
		if ref == "refs/heads/"+branch {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%s has no branch %s", repo, branch)
}

func (n *netRemote) Get(url string) (int, []byte, error) {
	resp, err := n.hc.Get(url)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return resp.StatusCode, b, nil
}

// goFieldcov runs go run ./tools/fieldcov -fields-out in the repository.
type goFieldcov struct {
	goExe, repo, work string
	n                 int
}

func (g *goFieldcov) Run(in fcInputs) (*fieldSet, error) {
	g.n++
	out := filepath.Join(g.work, fmt.Sprintf("fieldcov-%d.json", g.n))
	logPath := filepath.Join(g.work, fmt.Sprintf("fieldcov-%d.log", g.n))
	args := []string{"run", "./tools/fieldcov", "-fields-out", out}
	if in.Swagger != "" {
		args = append(args, "-swagger", in.Swagger)
	}
	if in.Compose != "" {
		args = append(args, "-compose-schema", in.Compose)
	}
	if in.Bake != "" {
		args = append(args, "-bake-ref", in.Bake)
	}
	cmd := exec.Command(g.goExe, args...)
	cmd.Dir = g.repo
	b, err := cmd.CombinedOutput()
	_ = os.WriteFile(logPath, b, 0o644)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w (output in %s)", g.goExe, strings.Join(args, " "), err, logPath)
	}
	return readFieldSet(out)
}

// defaultWorkDir is where drift writes fetched specs, fieldcov output and the
// report without -work: test/harness/.logs/drift under the repository
// (git-ignored), never the system temp directory.
func defaultWorkDir(repo string) string {
	return filepath.Join(repo, "test", "harness", ".logs", "drift")
}
