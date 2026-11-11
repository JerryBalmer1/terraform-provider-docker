package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeUpstreamP6e(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "reference", "_gen", "deferred.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "upstream.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestUpstreamSurfaceP6e(t *testing.T) {
	root := writeUpstreamP6e(t, `{"about":"x","items":[{"item":"buildkit:flag/FROM --reset","version_seen":"dockerfile/1.29.0","date":"2026-11-11","reason":"labs-only"}]}`)
	items, err := upstreamSurface(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "upstream_surface" || items[0].ID != "buildkit:flag/FROM --reset" ||
		!strings.HasPrefix(items[0].Reason, "dockerfile/1.29.0: ") || items[0].Source != "reference/_gen/deferred.d/upstream.json" {
		t.Errorf("items = %+v", items)
	}
	if items, err := upstreamSurface(t.TempDir()); err != nil || items != nil {
		t.Errorf("absent file: %v %v", items, err)
	}
}

func TestUpstreamSurfaceBadP6e(t *testing.T) {
	for _, body := range []string{
		`{"items":[{"item":"FROM --reset","version_seen":"v","date":"2026-11-11","reason":"r"}]}`,
		`{"items":[{"item":"buildkit:flag/FROM --reset","version_seen":"","date":"2026-11-11","reason":"r"}]}`,
		`{"items":[{"item":"buildkit:flag/FROM --reset","version_seen":"v","date":"2026-11-11","reason":" "}]}`,
	} {
		if _, err := upstreamSurface(writeUpstreamP6e(t, body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}
