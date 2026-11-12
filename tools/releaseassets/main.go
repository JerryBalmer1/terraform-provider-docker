// Command releaseassets is the release asset-name gate (Invoke-Build ReleaseCheck and Release).
//
// The Terraform Registry rejects a release that carries any asset besides the platform zips,
// <name>_<version>_manifest.json, <name>_<version>_SHA256SUMS and <name>_<version>_SHA256SUMS.sig
// (v0.1.0: "invalid file extension found: json" for the cosign .sigstore.json the sign job uploaded).
// Two checks:
//
//   - -dist: from a goreleaser snapshot (dist/metadata.json, dist/artifacts.json, .goreleaser.yml) the set
//     of files a real release would upload (every uploadable artifact, release.extra_files, and the
//     signatures the signs block produces, which a snapshot skips) must equal exactly one zip per
//     -targets entry, the manifest, the SHA256SUMS and its .sig.
//   - -workflow: no step of the release workflow may upload any other file to the GitHub release
//     (gh release upload, the GitHub upload-asset API, softprops/action-gh-release and similar actions).
//
// Exit 0 clean, 1 on findings (one line each), 2 on a usage or read error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("releaseassets", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dist := fs.String("dist", "", "goreleaser dist directory (metadata.json, artifacts.json) to check")
	cfg := fs.String("goreleaser", ".goreleaser.yml", "goreleaser config (release.extra_files, signs)")
	targets := fs.String("targets", "", "comma-separated <goos>_<goarch> release targets (one zip each)")
	workflow := fs.String("workflow", "", "release workflow to check for steps uploading other files to the release")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dist == "" && *workflow == "" {
		fmt.Fprintln(stderr, "releaseassets: nothing to check; pass -dist and/or -workflow")
		return 2
	}
	var findings []string
	if *dist != "" {
		tl := splitList(*targets)
		if len(tl) == 0 {
			fmt.Fprintln(stderr, "releaseassets: -dist needs -targets")
			return 2
		}
		got, want, err := DistAssets(*dist, *cfg, tl)
		if err != nil {
			fmt.Fprintln(stderr, "releaseassets:", err)
			return 2
		}
		f := CompareAssets(got, want)
		findings = append(findings, f...)
		if len(f) == 0 {
			fmt.Fprintf(stdout, "asset gate: %s would upload exactly %d assets: %d zips, manifest, SHA256SUMS, SHA256SUMS.sig\n", *dist, len(want), len(tl))
		}
	}
	if *workflow != "" {
		data, err := os.ReadFile(*workflow)
		if err != nil {
			fmt.Fprintln(stderr, "releaseassets:", err)
			return 2
		}
		f, n, err := CheckWorkflow(data)
		if err != nil {
			fmt.Fprintf(stderr, "releaseassets: %s: %v\n", *workflow, err)
			return 2
		}
		for _, x := range f {
			findings = append(findings, *workflow+": "+x)
		}
		if len(f) == 0 {
			fmt.Fprintf(stdout, "asset gate: %s: %d steps, none uploads a file outside the registry asset set\n", *workflow, n)
		}
	}
	for _, f := range findings {
		fmt.Fprintln(stdout, "ASSET "+f)
	}
	if len(findings) > 0 {
		fmt.Fprintf(stdout, "asset gate: %d finding(s); the Terraform Registry accepts only the zips, _manifest.json, _SHA256SUMS and _SHA256SUMS.sig\n", len(findings))
		return 1
	}
	return 0
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

// ExpectedAssets is the exact registry asset set for one version.
func ExpectedAssets(project, version string, targets []string) []string {
	prefix := project + "_" + version
	out := []string{prefix + "_manifest.json", prefix + "_SHA256SUMS", prefix + "_SHA256SUMS.sig"}
	for _, t := range targets {
		out = append(out, prefix+"_"+t+".zip")
	}
	sort.Strings(out)
	return out
}

type grConfig struct {
	ProjectName string `yaml:"project_name"`
	Checksum    struct {
		NameTemplate string `yaml:"name_template"`
	} `yaml:"checksum"`
	Signs []struct {
		Artifacts string `yaml:"artifacts"`
		Signature string `yaml:"signature"`
	} `yaml:"signs"`
	Release struct {
		ExtraFiles []struct {
			Glob         string `yaml:"glob"`
			NameTemplate string `yaml:"name_template"`
		} `yaml:"extra_files"`
	} `yaml:"release"`
}

type grArtifact struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// notUploaded are goreleaser artifact types the release pipe never uploads. Every other type (Archive, Checksum,
// Signature, Certificate, Uploadable File, Uploadable Binary, Source, SBOM, Linux Package, unknown ones) counts as
// uploaded, so a new kind of artifact fails the gate instead of slipping through.
var notUploaded = map[string]bool{"Binary": true, "Metadata": true, "Header": true, "C Header": true}

var tmplVar = regexp.MustCompile(`\{\{\s*\.(\w+)\s*\}\}`)

func render(t, project, version string) (string, error) {
	var bad error
	out := tmplVar.ReplaceAllStringFunc(t, func(m string) string {
		switch tmplVar.FindStringSubmatch(m)[1] {
		case "ProjectName":
			return project
		case "Version":
			return version
		}
		bad = fmt.Errorf("template %q: only .ProjectName and .Version are supported", t)
		return m
	})
	if bad == nil && strings.Contains(out, "{{") {
		bad = fmt.Errorf("template %q: unsupported template syntax", t)
	}
	return out, bad
}

// DistAssets returns the files a real release of the snapshot in dist would upload, and the expected set.
func DistAssets(dist, cfgPath string, targets []string) (got, want []string, err error) {
	var meta struct {
		ProjectName string `json:"project_name"`
		Version     string `json:"version"`
	}
	if err := readJSON(filepath.Join(dist, "metadata.json"), &meta); err != nil {
		return nil, nil, err
	}
	var arts []grArtifact
	if err := readJSON(filepath.Join(dist, "artifacts.json"), &arts); err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	var cfg grConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", cfgPath, err)
	}
	project := cfg.ProjectName
	if project == "" {
		project = meta.ProjectName
	}
	if meta.Version == "" || project == "" {
		return nil, nil, fmt.Errorf("%s: no version or project name", filepath.Join(dist, "metadata.json"))
	}
	set := map[string]bool{}
	var checksums []string
	for _, a := range arts {
		if notUploaded[a.Type] {
			continue
		}
		set[a.Name] = true
		if a.Type == "Checksum" {
			checksums = append(checksums, a.Name)
		}
	}
	for _, x := range cfg.Release.ExtraFiles {
		name := x.NameTemplate
		if name == "" {
			name = path.Base(x.Glob)
		}
		n, err := render(name, project, meta.Version)
		if err != nil {
			return nil, nil, fmt.Errorf("%s release.extra_files: %w", cfgPath, err)
		}
		set[n] = true
	}
	// A snapshot skips signing; add what the signs block produces on a real release.
	for _, s := range cfg.Signs {
		var subjects []string
		switch s.Artifacts {
		case "", "none":
			continue
		case "checksum":
			subjects = checksums
		default: // all, archive, binary, package, sbom, source: one signature per such artifact
			for _, a := range arts {
				if !notUploaded[a.Type] && a.Type != "Signature" {
					subjects = append(subjects, a.Name)
				}
			}
		}
		for _, subj := range subjects {
			sig := subj + ".sig"
			if s.Signature != "" {
				t := strings.ReplaceAll(s.Signature, "${artifact}", subj)
				if sig, err = render(t, project, meta.Version); err != nil {
					return nil, nil, fmt.Errorf("%s signs.signature: %w", cfgPath, err)
				}
				sig = path.Base(sig)
			}
			set[sig] = true
		}
	}
	for n := range set {
		got = append(got, n)
	}
	sort.Strings(got)
	return got, ExpectedAssets(project, meta.Version, targets), nil
}

// CompareAssets names every expected asset that is missing and every uploaded file outside the expected set.
func CompareAssets(got, want []string) []string {
	g, w := map[string]bool{}, map[string]bool{}
	for _, x := range got {
		g[x] = true
	}
	for _, x := range want {
		w[x] = true
	}
	var out []string
	for _, x := range want {
		if !g[x] {
			out = append(out, "missing release asset "+x)
		}
	}
	for _, x := range got {
		if !w[x] {
			out = append(out, "unexpected release asset "+x+" (the registry rejects it)")
		}
	}
	return out
}

// allowedUpload: a file name (or glob) a workflow step may upload to the release. Only the registry asset shapes.
var allowedUpload = regexp.MustCompile(`(?:\.zip|_manifest\.json|_SHA256SUMS|_SHA256SUMS\.sig)$`)

// releaseActions are actions that upload release assets, with the inputs that name the files.
var releaseActions = map[string][]string{
	"softprops/action-gh-release":             {"files"},
	"svenstaro/upload-release-action":         {"file"},
	"actions/upload-release-asset":            {"asset_path", "asset_name"},
	"ncipollo/release-action":                 {"artifacts"},
	"shogo82148/actions-upload-release-asset": {"asset_path"},
}

type wfStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type wfFile struct {
	Jobs map[string]struct {
		Steps []wfStep `yaml:"steps"`
	} `yaml:"jobs"`
}

var ghUpload = regexp.MustCompile(`\bgh\s+release\s+upload\b`)

// CheckWorkflow returns one finding per step that uploads a file outside the registry asset set, and the step count.
func CheckWorkflow(data []byte) ([]string, int, error) {
	var wf wfFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, 0, err
	}
	jobs := make([]string, 0, len(wf.Jobs))
	for j := range wf.Jobs {
		jobs = append(jobs, j)
	}
	sort.Strings(jobs)
	var out []string
	steps := 0
	for _, j := range jobs {
		for i, st := range wf.Jobs[j].Steps {
			steps++
			where := fmt.Sprintf("job %s step %d", j, i+1)
			if st.Name != "" {
				where += fmt.Sprintf(" (%s)", st.Name)
			}
			action, _, _ := strings.Cut(st.Uses, "@")
			if inputs, ok := releaseActions[action]; ok {
				for _, in := range inputs {
					for _, f := range strings.Split(st.With[in], "\n") {
						f = strings.TrimSpace(f)
						if f != "" && !allowedUpload.MatchString(f) {
							out = append(out, fmt.Sprintf("%s: %s uploads %q to the release", where, action, f))
						}
					}
				}
			}
			if st.Run == "" {
				continue
			}
			script := strings.ReplaceAll(st.Run, "\\\n", " ")
			for _, line := range strings.Split(script, "\n") {
				if strings.Contains(line, "uploads.github.com") {
					out = append(out, fmt.Sprintf("%s: uploads through the GitHub release asset API (uploads.github.com)", where))
				}
				loc := ghUpload.FindStringIndex(line)
				if loc == nil {
					continue
				}
				args := strings.Fields(line[loc[1]:])
				first := true
				for k := 0; k < len(args); k++ {
					a := args[k]
					if a == "&&" || a == "||" || a == ";" || a == "|" {
						break
					}
					if strings.HasPrefix(a, "-") {
						if !strings.Contains(a, "=") && (a == "--repo" || a == "-R") {
							k++
						}
						continue
					}
					if first { // the tag
						first = false
						continue
					}
					f := strings.Trim(a, `"'`)
					if i := strings.Index(f, "#"); i > 0 { // gh's <file>#<label>
						f = f[:i]
					}
					if !allowedUpload.MatchString(f) {
						out = append(out, fmt.Sprintf("%s: gh release upload %s", where, a))
					}
				}
			}
		}
	}
	return out, steps, nil
}

func readJSON(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}
