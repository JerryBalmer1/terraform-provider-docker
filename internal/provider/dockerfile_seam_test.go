package provider

import (
	"bytes"
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

// TestDockerfileGoldenDocExamples: parse -> render -> parse round-trips for
// every Dockerfile example of the harvested reference docs. Examples the
// BuildKit parser itself rejects, or that are fragments (no FROM, unknown
// instructions, placeholders), are counted and skipped.
func TestDockerfileGoldenDocExamples(t *testing.T) {
	ex := dockerfileDocExamples(t)
	if len(ex) < 100 {
		t.Fatalf("only %d dockerfile examples found in %s", len(ex), dockerfileDocsDir)
	}
	keys := make([]string, 0, len(ex))
	for k := range ex {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	roundTripped, fragments := 0, 0
	for _, k := range keys {
		src := ex[k]
		if _, err := parser.Parse(bytes.NewReader([]byte(src))); err != nil {
			fragments++
			continue
		}
		p1, err := dockerfileParse(src)
		if err != nil && strings.Contains(err.Error(), "before the first FROM") {
			// Instruction snippets: give them a stage.
			src = dockerfileWithFrom(src)
			p1, err = dockerfileParse(src)
		}
		if err != nil {
			// Fragments: instructions without FROM, `<placeholders>`, etc.
			fragments++
			t.Logf("%s: not a whole Dockerfile (%v)", k, err)
			continue
		}
		if len(p1.Doc.Stages) == 0 {
			fragments++
			continue
		}
		dockerfileAssertRoundTrip(t, k, p1.Doc)
		roundTripped++
	}
	t.Logf("round-tripped %d doc examples, skipped %d fragments", roundTripped, fragments)
	if roundTripped < 60 {
		t.Fatalf("only %d examples round-tripped", roundTripped)
	}
}

// dockerfileWithFrom inserts "FROM scratch" after the leading parser
// directives of a snippet.
func dockerfileWithFrom(src string) string {
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") &&
		dockerfileDirectiveLikeRe.MatchString(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "#"))) {
		i++
	}
	out := append(append(append([]string{}, lines[:i]...), "FROM scratch"), lines[i:]...)
	return strings.Join(out, "\n")
}

// dockerfileAssertRoundTrip renders doc, parses the text again and checks
// the document is unchanged and rendering is idempotent.
func dockerfileAssertRoundTrip(t *testing.T, name string, doc seamDockerfileDoc) string {
	t.Helper()
	text, err := seamDockerfileImpl.Render(doc)
	if err != nil {
		t.Errorf("%s: render: %v", name, err)
		return ""
	}
	doc2, err := seamDockerfileImpl.Parse(text)
	if err != nil {
		t.Errorf("%s: re-parse of rendered text: %v\n%s", name, err, text)
		return text
	}
	if !reflect.DeepEqual(doc, doc2) {
		t.Errorf("%s: round trip changed the document\nrendered:\n%s\nbefore: %#v\nafter:  %#v", name, text, doc, doc2)
	}
	text2, err := seamDockerfileImpl.Render(doc2)
	if err != nil || text2 != text {
		t.Errorf("%s: render is not idempotent (%v)\n%s\n---\n%s", name, err, text, text2)
	}
	return text
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
