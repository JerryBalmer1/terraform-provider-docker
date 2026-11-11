package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pass 6c Lane F: the default work directory is under the repository's
// git-ignored test/harness/.logs, never the system temp directory.
func TestDefaultWorkDirP6cF(t *testing.T) {
	repo := filepath.Join("C:", "repo")
	got := defaultWorkDir(repo)
	if got != filepath.Join(repo, "test", "harness", ".logs", "drift") {
		t.Errorf("defaultWorkDir = %q", got)
	}
	if strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("default work directory is under the temp directory: %q", got)
	}
}
