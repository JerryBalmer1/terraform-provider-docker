package main

// The upstream sources DriftCheck watches, and their pins as recorded in
// reference/SOURCES.md (the single place pins live; this file only says how
// to find the newest upstream release of each).

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Field models: which tools/fieldcov input slot a source's spec file feeds.
const (
	modelEngine    = "engine"     // -swagger
	modelCompose   = "compose"    // -compose-schema (the compose-spec repo copy, fieldcov's default)
	modelComposeGo = "compose-go" // -compose-schema, compared against the pinned compose-go embedded copy
	modelBake      = "bake"       // -bake-ref
	modelDoc       = ""           // documentation only: version movement is reported, nothing is diffed
)

// source is one watched upstream.
type source struct {
	Name  string
	Title string
	// RowKey is a substring of the upstream URL column of the source's
	// reference/SOURCES.md row (the first matching row holds the pin).
	RowKey string
	// Repo is the git URL probed with git ls-remote; empty for URL-probed
	// sources (URLTemplate).
	Repo string
	// TagPrefix selects release tags <prefix>MAJOR.MINOR.PATCH (pre-releases
	// such as -rc1 or -labs never match).
	TagPrefix string
	// Branch: the source is pinned to a branch commit (compose-spec has no
	// tags); newest is the branch HEAD.
	Branch string
	// URLTemplate: the source is probed by URL ({version} = MAJOR.MINOR).
	URLTemplate string
	Model       string
	// File is the spec file inside the repository (fetched from
	// raw.githubusercontent.com at the newest ref) for a field model.
	File string
	// Baseline is the pinned copy (repo-relative) fieldcov compares against
	// when it is not fieldcov's default input (compose-go).
	Baseline string
	// Surface is the surface extractor of a docs source (surface.go): its
	// DocPaths (repository-relative) are read from reference/upstream/<Local>
	// for the pin and from a sparse checkout of the newer release.
	Surface  string
	Local    string
	DocPaths []string
	// SpecURL: a spec published only as a URL (no repository, no tags; Model
	// modelOpenAPI, openapi.go). Pinned as "<info.version> @ sha256 <hex>";
	// Baseline is the pinned copy, InventorySpec the "spec" value of its
	// reference/inventory.json sources.
	SpecURL       string
	InventorySpec string
}

// defaultSources follow reference/SOURCES.md, row by row. The docker trust
// row shares the docker/cli pin; the live --help capture has no upstream.
var defaultSources = []source{
	{Name: "moby", Title: "moby Engine API swagger", RowKey: "github.com/moby/moby/tree/", Repo: "https://github.com/moby/moby",
		TagPrefix: "api/v", Model: modelEngine, File: "api/swagger.yaml"},
	{Name: "docs-engine-api", Title: "docs.docker.com Engine API spec", RowKey: "docs.docker.com/reference/api/engine/",
		URLTemplate: "https://docs.docker.com/reference/api/engine/version/v{version}.yaml", Model: modelEngine},
	{Name: "compose-spec", Title: "Compose Specification (schema)", RowKey: "github.com/compose-spec/compose-spec/tree/",
		Repo: "https://github.com/compose-spec/compose-spec", Branch: "main", Model: modelCompose, File: "schema/compose-spec.json"},
	{Name: "compose-go", Title: "compose-go embedded schema", RowKey: "github.com/compose-spec/compose-go/",
		Repo: "https://github.com/compose-spec/compose-go", TagPrefix: "v", Model: modelComposeGo, File: "schema/compose-spec.json",
		Baseline: "reference/upstream/compose-spec/compose-go-embedded.json"},
	{Name: "compose", Title: "docker compose CLI reference", RowKey: "github.com/docker/compose/tree/",
		Repo: "https://github.com/docker/compose", TagPrefix: "v",
		Surface: surfaceCLIYAML, Local: "compose-cli", DocPaths: []string{"docs/reference"}},
	{Name: "docker-cli", Title: "docker CLI reference", RowKey: "github.com/docker/cli/tree/",
		Repo: "https://github.com/docker/cli", TagPrefix: "v",
		Surface: surfaceCLIMD, Local: "docker-cli", DocPaths: []string{"docs/reference/commandline", "cmd/docker-trust/docs/reference"}},
	{Name: "buildkit", Title: "Dockerfile reference (buildkit)", RowKey: "github.com/moby/buildkit/tree/",
		Repo: "https://github.com/moby/buildkit", TagPrefix: "dockerfile/",
		Surface: surfaceDockerfile, Local: "dockerfile", DocPaths: []string{"frontend/dockerfile/docs"}},
	{Name: "buildx", Title: "buildx CLI and bake reference", RowKey: "github.com/docker/buildx/tree/",
		Repo: "https://github.com/docker/buildx", TagPrefix: "v", Model: modelBake, File: "docs/bake-reference.md",
		Surface: surfaceCLIMD, Local: "buildx", DocPaths: []string{"docs/reference"}},
	{Name: "hub", Title: "Docker Hub API spec", RowKey: "docs.docker.com/reference/api/hub/",
		SpecURL: "https://docs.docker.com/reference/api/hub/latest.yaml", Model: modelOpenAPI, InventorySpec: "hub",
		Baseline: "reference/upstream/hub/docs.docker.com-hub-latest.yaml"},
}

// localRow marks SOURCES.md rows with no upstream to watch.
const localRow = "local binaries"

// pin is one source's pinned version from reference/SOURCES.md.
type pin struct {
	Version string // tag (api/v1.56.1), branch (main) or API version (1.56)
	SHA     string // git commit, when recorded
	SHA256  string // file sha256 (URL-probed sources)
	URL     string // the upstream URL column
}

var (
	shaRe     = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
	sha256Re  = regexp.MustCompile(`sha256 ([0-9a-f]{64})`)
	apiVerRe  = regexp.MustCompile(`/version/v(\d+\.\d+)\.yaml`)
	tableSepR = regexp.MustCompile(`^\|[-| ]+\|$`)
)

// sourceRow is one data row of the SOURCES.md table.
type sourceRow struct {
	Source, URL, TagSHA string
}

func parseSourcesTable(text string) ([]sourceRow, error) {
	var rows []sourceRow
	header := true
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		if tableSepR.MatchString(line) {
			header = false
			continue
		}
		if header {
			continue
		}
		cols := strings.Split(strings.Trim(line, "|"), "|")
		if len(cols) < 4 {
			return nil, fmt.Errorf("SOURCES.md: short table row %q", line)
		}
		rows = append(rows, sourceRow{Source: strings.TrimSpace(cols[1]), URL: strings.TrimSpace(cols[2]), TagSHA: strings.TrimSpace(cols[3])})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("SOURCES.md: no source table")
	}
	return rows, nil
}

// loadPins reads every source's pin from SOURCES.md, and fails on a row no
// source covers (a new upstream must be added to defaultSources).
func loadPins(path string, srcs []source) (map[string]pin, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return pinsFrom(string(b), srcs)
}

func pinsFrom(text string, srcs []source) (map[string]pin, error) {
	rows, err := parseSourcesTable(text)
	if err != nil {
		return nil, err
	}
	pins := map[string]pin{}
	for _, r := range rows {
		matched := r.URL == localRow
		for _, s := range srcs {
			if !strings.Contains(r.URL, s.RowKey) {
				continue
			}
			matched = true
			if _, done := pins[s.Name]; done {
				continue
			}
			p, err := parsePin(s, r)
			if err != nil {
				return nil, err
			}
			pins[s.Name] = p
		}
		if !matched {
			return nil, fmt.Errorf("SOURCES.md row %q (%s) is not watched: add it to defaultSources in tools/drift/sources.go", r.Source, r.URL)
		}
	}
	for _, s := range srcs {
		if _, ok := pins[s.Name]; !ok {
			return nil, fmt.Errorf("SOURCES.md has no row for %s (upstream URL containing %q)", s.Name, s.RowKey)
		}
	}
	return pins, nil
}

func parsePin(s source, r sourceRow) (pin, error) {
	p := pin{URL: r.URL}
	if s.URLTemplate != "" {
		m := apiVerRe.FindStringSubmatch(r.URL)
		h := sha256Re.FindStringSubmatch(r.TagSHA)
		if m == nil || h == nil {
			return p, fmt.Errorf("SOURCES.md %s: want a .../version/vX.Y.yaml URL and a sha256, got %q / %q", s.Name, r.URL, r.TagSHA)
		}
		p.Version, p.SHA256 = m[1], h[1]
		return p, nil
	}
	if s.SpecURL != "" {
		ver, rest, ok := strings.Cut(r.TagSHA, " @ ")
		h := sha256Re.FindStringSubmatch(rest)
		if !ok || h == nil || strings.TrimSpace(ver) == "" {
			return p, fmt.Errorf("SOURCES.md %s: tag/SHA column %q is not \"<info.version> @ sha256 <hex>\"", s.Name, r.TagSHA)
		}
		p.Version, p.SHA256 = strings.TrimSpace(ver), h[1]
		return p, nil
	}
	ver, rest, ok := strings.Cut(r.TagSHA, " @ ")
	if !ok {
		return p, fmt.Errorf("SOURCES.md %s: tag/SHA column %q is not \"<tag> @ <sha>\"", s.Name, r.TagSHA)
	}
	p.Version = strings.TrimSpace(ver)
	p.SHA = shaRe.FindString(rest)
	if s.Branch != "" {
		if p.Version != s.Branch || p.SHA == "" {
			return p, fmt.Errorf("SOURCES.md %s: want \"%s @ <sha>\", got %q", s.Name, s.Branch, r.TagSHA)
		}
		return p, nil
	}
	if _, ok := parseTag(s.TagPrefix, p.Version); !ok {
		return p, fmt.Errorf("SOURCES.md %s: pinned tag %q is not %sMAJOR.MINOR.PATCH", s.Name, p.Version, s.TagPrefix)
	}
	return p, nil
}

// semver is MAJOR.MINOR.PATCH.
type semver [3]int

func (a semver) less(b semver) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// parseTag parses <prefix>MAJOR.MINOR.PATCH; anything else (pre-releases,
// other tag families) does not match.
func parseTag(prefix, tag string) (semver, bool) {
	rest, ok := strings.CutPrefix(tag, prefix)
	if !ok {
		return semver{}, false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var v semver
	for i, p := range parts {
		if p == "" || strings.TrimLeft(p, "0123456789") != "" {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, false
		}
		v[i] = n
	}
	return v, true
}

// newestTag returns the highest release tag with the prefix.
func newestTag(prefix string, tags []string) (string, bool) {
	best, found := "", false
	var bv semver
	for _, t := range tags {
		v, ok := parseTag(prefix, t)
		if !ok {
			continue
		}
		if !found || bv.less(v) {
			best, bv, found = t, v, true
		}
	}
	return best, found
}

// splitAPIVersion parses MAJOR.MINOR.
func splitAPIVersion(v string) (int, int, error) {
	a, b, ok := strings.Cut(v, ".")
	major, err1 := strconv.Atoi(a)
	minor, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("bad API version %q, want MAJOR.MINOR", v)
	}
	return major, minor, nil
}

// ownerRepo turns https://github.com/o/r into o/r.
func ownerRepo(repo string) string {
	return strings.TrimSuffix(strings.TrimPrefix(repo, "https://github.com/"), ".git")
}

// rawURL is the raw.githubusercontent.com URL of path at ref (a commit SHA
// or refs/tags/<tag>).
func rawURL(repo, ref, path string) string {
	return "https://raw.githubusercontent.com/" + ownerRepo(repo) + "/" + ref + "/" + path
}
