package provider

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestDockerfileBuiltinArgsMatchReferenceP6e: dockerfileBuiltinArgs holds
// exactly the build args the harvested Dockerfile reference lists in its ARG
// section (predefined, automatic platform and BuildKit built-in args), so a
// re-pin that adds one (1.29.0: BUILDKIT_DISABLE_ONBUILD,
// BUILDKIT_GIT_DEBUG_COMMANDS) fails here until the list follows.
func TestDockerfileBuiltinArgsMatchReferenceP6e(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(dockerfileDocsDir, "reference.md"))
	if err != nil {
		t.Skipf("harvested Dockerfile docs not available: %v", err)
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	start := strings.Index(text, "\n## ARG\n")
	end := strings.Index(text[start+1:], "\n## ")
	if start < 0 || end < 0 {
		t.Fatal("reference.md has no ## ARG section")
	}
	section := text[start : start+1+end]
	re := regexp.MustCompile("(?m)^(?:- |\\| )`([A-Za-z_][A-Za-z0-9_]*)`")
	var doc []string
	for _, m := range re.FindAllStringSubmatch(section, -1) {
		doc = append(doc, m[1])
	}
	var have []string
	for k := range dockerfileBuiltinArgs {
		have = append(have, k)
	}
	sort.Strings(doc)
	sort.Strings(have)
	if strings.Join(doc, ",") != strings.Join(have, ",") {
		t.Errorf("dockerfileBuiltinArgs = %v\nreference.md ARG section = %v", have, doc)
	}
	for _, n := range []string{"BUILDKIT_DISABLE_ONBUILD", "BUILDKIT_GIT_DEBUG_COMMANDS"} {
		if dockerfileBuiltinArgs[n] != "buildkit" {
			t.Errorf("%s = %q, want buildkit", n, dockerfileBuiltinArgs[n])
		}
	}
}
