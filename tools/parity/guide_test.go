package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuideTemplate(t *testing.T) {
	var doc RenamesDoc
	doc.TypeRenames = append(doc.TypeRenames, TypeRename{KreuzwerkerType: "data.docker_logs", OurType: "data.docker_container_logs"})
	doc.Renames = []RenameRow{
		{Type: "docker_container", KreuzwerkerAttr: sp("ports.internal"), OurAttr: sp("ports.container_port"), Transform: "rename"},
		{Type: "docker_container", KreuzwerkerAttr: sp("name"), OurAttr: sp("name"), Transform: "identity"},
		{Type: "docker_container", KreuzwerkerAttr: sp("env"), OurAttr: sp("env"), Transform: "set(string) \"KEY=VALUE\" -> map(string)"},
		{Type: "docker_container", KreuzwerkerAttr: sp("timeouts"), Transform: "dropped", Reason: "dropped: no | timeouts."},
		{Type: "docker_container", KreuzwerkerAttr: sp("shm_size"), Transform: "dropped", Reason: "gap: if added, name it `shm_size`."},
		{Type: "data.docker_container_logs", KreuzwerkerAttr: sp("follow"), OurAttr: sp("follow"), Transform: "identity"},
		{Type: "action.docker_exec", KreuzwerkerAttr: sp("container"), OurAttr: sp("container_id"), Transform: "rename"},
		{Type: "docker_container", PreviousAttr: sp("a"), OurAttr: sp("b"), Transform: "rename"},
	}
	out := GuideTemplate(doc)
	for _, s := range []string{
		"page_title: \"Migrating from kreuzwerker/docker\"",
		"Terraform 1.8 or later",
		"Redundant move statement",
		"moved {\n  from = docker_container.web\n  to   = docker_container.web_moved\n}",
		"| `ports.internal` | `ports.container_port` | rename |",
		"| `env` | `env` | set(string) \"KEY=VALUE\" -> map(string) |",
		"Unchanged: `name`.",
		"| `timeouts` | dropped: no \\| timeouts. |",
		"| `shm_size` | if added, name it `shm_size`. |",
		"### `data.docker_container_logs (kreuzwerker data.docker_logs)`",
		"| `container` | `container_id` | rename |",
		"`LogConfig.Type`", "`LogDriver.Name`", "TaskTemplate.Networks[]",
		"required_version = \">= 1.8\"",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("guide lacks %q", s)
		}
	}
	if strings.Contains(out, "{{") {
		t.Error("guide must not contain template actions")
	}
	if strings.Index(out, "### `docker_container`") > strings.Index(out, "### `data.") || strings.Index(out, "### `data.") > strings.Index(out, "### `action.") {
		t.Error("sections not ordered resources, data sources, actions")
	}
	// Data sources and actions get no moved block.
	if strings.Contains(out, "from = data.") || strings.Contains(out, "from = action.") {
		t.Error("moved block for a data source or action")
	}
}

func TestRunGuide(t *testing.T) {
	dir := t.TempDir()
	ren := allMappedRenames(t, dir)
	g := filepath.Join(dir, "guide.md.tmpl")
	var o, e bytes.Buffer
	if code := run([]string{"-renames", ren, "-guide", g}, &o, &e); code != 0 {
		t.Fatalf("-guide = %d: %s", code, e.String())
	}
	if code := run([]string{"-renames", ren, "-guide-check", g}, &o, &e); code != 0 {
		t.Errorf("-guide-check after -guide = %d: %s", code, e.String())
	}
	b, _ := os.ReadFile(g)
	os.WriteFile(g, []byte(strings.ReplaceAll(string(b), "\n", "\r\n")), 0o644)
	if code := run([]string{"-renames", ren, "-guide-check", g}, &o, &e); code != 0 {
		t.Errorf("-guide-check with CRLF = %d (autocrlf checkouts must pass)", code)
	}
	os.WriteFile(g, []byte("stale"), 0o644)
	if code := run([]string{"-renames", ren, "-guide-check", g}, &o, &e); code != 1 {
		t.Errorf("-guide-check stale = %d, want 1", code)
	}
	if code := run([]string{"-renames", filepath.Join(dir, "none.json"), "-guide", g}, &o, &e); code != 2 {
		t.Errorf("-guide without renames = %d, want 2", code)
	}
}

// The checked-in guide template matches the checked-in renames.json.
func TestGuideTemplateCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	doc, err := LoadRenames(filepath.Join(root, "reference", "_gen", "renames.json"))
	if err != nil {
		t.Skipf("renames.json: %v", err)
	}
	if err := writeGuide(filepath.Join(root, "templates", "guides", "migrating-from-kreuzwerker.md.tmpl"), doc, true); err != nil {
		t.Error(err)
	}
}
