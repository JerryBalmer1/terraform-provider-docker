package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// dockerfileDocsDir is the harvested Dockerfile reference (frontend 1.28.0).
const dockerfileDocsDir = "../../reference/upstream/dockerfile/frontend/dockerfile/docs"

var dockerfileFenceRe = regexp.MustCompile("(?ms)^[ \t]*```dockerfile[^\n]*\n(.*?)^[ \t]*```")

// dockerfileDocExamples returns every ```dockerfile fenced block of the
// reference and rules docs, keyed "<file>#<n>".
func dockerfileDocExamples(t *testing.T) map[string]string {
	t.Helper()
	files := []string{filepath.Join(dockerfileDocsDir, "reference.md")}
	rules, _ := filepath.Glob(filepath.Join(dockerfileDocsDir, "rules", "*.md"))
	files = append(files, rules...)
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Skipf("harvested Dockerfile docs not available: %v", err)
		}
		text := strings.ReplaceAll(string(b), "\r\n", "\n")
		for i, m := range dockerfileFenceRe.FindAllStringSubmatch(text, -1) {
			// Dedent fences nested in list items.
			body := m[1]
			out[fmt.Sprintf("%s#%03d", filepath.Base(f), i)] = dockerfileDedent(body)
		}
	}
	return out
}

func dockerfileDedent(s string) string {
	lines := strings.Split(s, "\n")
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if min < 0 || n < min {
			min = n
		}
	}
	if min <= 0 {
		return s
	}
	for i, l := range lines {
		if len(l) >= min {
			lines[i] = l[min:]
		}
	}
	return strings.Join(lines, "\n")
}

// dockerfileRoundTripAllowlist lists the doc examples that are not expected
// to round-trip; the format is documented at the top of the file.
const dockerfileRoundTripAllowlist = "testdata/dockerfile/roundtrip_allowlist.txt"

// dockerfileAllowEntry is one line of the allowlist.
type dockerfileAllowEntry struct {
	Hash, Kind, Reason string
	Line               int
}

// dockerfileExampleHash is the allowlist hash of an example: the first 12
// hex digits of the SHA-256 of its dedented, LF-only text.
func dockerfileExampleHash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:])[:12]
}

// dockerfileReadAllowlist parses the allowlist file.
func dockerfileReadAllowlist(t *testing.T) map[string]dockerfileAllowEntry {
	t.Helper()
	b, err := os.ReadFile(dockerfileRoundTripAllowlist)
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	out := map[string]dockerfileAllowEntry{}
	for i, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			t.Errorf("%s:%d: want `<id> <hash> <kind> <reason>`, got %q", dockerfileRoundTripAllowlist, i+1, line)
			continue
		}
		if f[2] != "fragment" && f[2] != "blocked" {
			t.Errorf("%s:%d: kind %q is not fragment or blocked", dockerfileRoundTripAllowlist, i+1, f[2])
		}
		if _, dup := out[f[0]]; dup {
			t.Errorf("%s:%d: %s is listed twice", dockerfileRoundTripAllowlist, i+1, f[0])
		}
		out[f[0]] = dockerfileAllowEntry{Hash: f[1], Kind: f[2], Reason: strings.Join(f[3:], " "), Line: i + 1}
	}
	return out
}

// dockerfileDocExampleRoundTrip round-trips one doc example exactly as the
// reference shows it and returns why it does not. The test never edits an
// example: no FROM is added to a directive-only header or to instructions
// shown before FROM, nothing is skipped. An example that is not a complete
// Dockerfile on its own is a fragment and belongs in the allowlist.
func dockerfileDocExampleRoundTrip(src string) error {
	if _, err := parser.Parse(bytes.NewReader([]byte(src))); err != nil {
		return fmt.Errorf("buildkit parser: %w", err)
	}
	p1, err := dockerfileParse(src)
	if err != nil {
		return err
	}
	if len(p1.Doc.Stages) == 0 {
		return fmt.Errorf("no stage (FROM)")
	}
	_, err = dockerfileRoundTrip(p1.Doc)
	return err
}

// TestDockerfileGoldenDocExamples: parse -> render -> parse round-trips for
// every Dockerfile example of the harvested reference docs, exactly as the
// reference shows it (no test-side completion). Every example
// must round-trip unless testdata/dockerfile/roundtrip_allowlist.txt lists it
// (a fragment, or a BuildKit parser limit); a listed example that now
// round-trips, changed, or no longer exists fails the test too.
func TestDockerfileGoldenDocExamples(t *testing.T) {
	ex := dockerfileDocExamples(t)
	if len(ex) < 100 {
		t.Fatalf("only %d dockerfile examples found in %s", len(ex), dockerfileDocsDir)
	}
	allow := dockerfileReadAllowlist(t)
	keys := make([]string, 0, len(ex))
	for k := range ex {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	roundTripped, kinds := 0, map[string]int{}
	for _, k := range keys {
		src := ex[k]
		err := dockerfileDocExampleRoundTrip(src)
		a, listed := allow[k]
		switch {
		case !listed && err != nil:
			t.Errorf("%s (hash %s): does not round-trip: %v\n%s", k, dockerfileExampleHash(src), err, src)
		case !listed:
			roundTripped++
		case a.Hash != dockerfileExampleHash(src):
			t.Errorf("%s:%d: %s changed (hash %s, listed %s): review it and update the entry", dockerfileRoundTripAllowlist, a.Line, k, dockerfileExampleHash(src), a.Hash)
		case err == nil:
			t.Errorf("%s:%d: %s round-trips now: remove it from the allowlist", dockerfileRoundTripAllowlist, a.Line, k)
		default:
			kinds[a.Kind]++
			t.Logf("%s: allowlisted (%s: %s): %v", k, a.Kind, a.Reason, err)
		}
	}
	for k, a := range allow {
		if _, ok := ex[k]; !ok {
			t.Errorf("%s:%d: %s is not an example of %s any more: remove it", dockerfileRoundTripAllowlist, a.Line, k, dockerfileDocsDir)
		}
	}
	// The counts are part of the test output (-v) so the honest round-trip
	// number is visible. Every unlisted example must round-trip (above), so
	// the round-trip count can only drop by adding an allowlist entry.
	t.Logf("%d doc examples: %d round-trip with no test-side edits, %d allowlisted fragment, %d allowlisted blocked",
		len(ex), roundTripped, kinds["fragment"], kinds["blocked"])
	if !t.Failed() && roundTripped+kinds["fragment"]+kinds["blocked"] != len(ex) {
		t.Errorf("counts do not add up: %d round-trip + %d fragment + %d blocked != %d examples",
			roundTripped, kinds["fragment"], kinds["blocked"], len(ex))
	}
}

// dockerfileAssertRoundTrip renders doc, parses the text again and checks
// the document is unchanged and rendering is idempotent.
func dockerfileAssertRoundTrip(t *testing.T, name string, doc seamDockerfileDoc) string {
	t.Helper()
	text, err := dockerfileRoundTrip(doc)
	if err != nil {
		t.Errorf("%s: %v", name, err)
	}
	return text
}

// dockerfileRoundTrip renders doc, parses the text again and returns an error
// unless the document is unchanged and rendering is idempotent.
func dockerfileRoundTrip(doc seamDockerfileDoc) (string, error) {
	text, err := seamDockerfileImpl.Render(doc)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	doc2, err := seamDockerfileImpl.Parse(text)
	if err != nil {
		return text, fmt.Errorf("re-parse of rendered text: %w\n%s", err, text)
	}
	if !reflect.DeepEqual(doc, doc2) {
		return text, fmt.Errorf("round trip changed the document\nrendered:\n%s\nbefore: %#v\nafter:  %#v", text, doc, doc2)
	}
	text2, err := seamDockerfileImpl.Render(doc2)
	if err != nil || text2 != text {
		return text, fmt.Errorf("render is not idempotent (%v)\n%s\n---\n%s", err, text, text2)
	}
	return text, nil
}

// TestDockerfileGoldenFixtures round-trips testdata/dockerfile/*.Dockerfile
// and compares the rendered text with the matching .golden file.
func TestDockerfileGoldenFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "dockerfile", "*.Dockerfile"))
	if len(files) == 0 {
		t.Fatal("no fixtures in testdata/dockerfile")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		p, err := dockerfileParse(strings.ReplaceAll(string(b), "\r\n", "\n"))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		text := dockerfileAssertRoundTrip(t, f, p.Doc)
		golden := strings.TrimSuffix(f, ".Dockerfile") + ".golden"
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			_ = os.WriteFile(golden, []byte(text), 0o644)
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Errorf("%s: %v (run with UPDATE_GOLDEN=1)", f, err)
			continue
		}
		if strings.ReplaceAll(string(want), "\r\n", "\n") != text {
			t.Errorf("%s: rendered text differs from %s\n%s", f, golden, text)
		}
	}
}
