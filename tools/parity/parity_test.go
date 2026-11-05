package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticListValid(t *testing.T) {
	if errs := Validate(kreuzwerker, extras); len(errs) > 0 {
		t.Fatalf("invalid list:\n%s", strings.Join(errs, "\n"))
	}
	counts := map[Kind]int{}
	for _, e := range kreuzwerker {
		counts[e.Kind]++
	}
	// kreuzwerker/docker v4.6.0: 12 resources, 8 data sources, 6 actions.
	if counts[Resource] != 12 || counts[DataSource] != 8 || counts[Action] != 6 {
		t.Errorf("kreuzwerker v4.6.0 counts changed: %v", counts)
	}
}

func TestValidateCatchesMistakes(t *testing.T) {
	bad := []Entry{
		{Resource, "docker_b", Have, ""},
		{Resource, "docker_a", Have, ""},
		{Resource, "docker_a", Have, ""},
		{Resource, "widget", Have, ""},
		{DataSource, "docker_x", "maybe", ""},
		{Action, "docker_p", Partial, ""},
		{"thing", "docker_q", Have, ""},
	}
	got := strings.Join(Validate(bad, []Extra{{Resource, "docker_b", ""}, {"nope", "docker_z", ""}}), "\n")
	for _, want := range []string{
		"resource docker_a: duplicate",
		"resource widget: name lacks docker_ prefix",
		`data source docker_x: unknown status "maybe"`,
		"action docker_p: partial needs a note",
		"thing docker_q: unknown kind",
		"resource: entries not sorted by name",
		"resource docker_b: listed as an extra but is a kreuzwerker type",
		"nope docker_z: unknown kind",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Validate lacks %q in:\n%s", want, got)
		}
	}
}

func TestUnblocked(t *testing.T) {
	entries := []Entry{
		{Action, "docker_image", Missing, ""},
		{Action, "docker_image_save", Missing, ""},
		{Action, "docker_exec", Have, ""},
		{Resource, "docker_tag", Missing, ""},
		{Resource, "docker_plugin", Partial, "x"},
	}
	report := strings.Join([]string{
		"[P3-D] [BLOCKED] D docker_image_save needs a tar sink, tried X",
		"[P3-D] [TODO] docker_tag later",          // not BLOCKED
		"[P3-D] [BLOCKED] D action.docker_tag: y", // address form counts
		"",
	}, "\r\n")
	var names []string
	for _, e := range Unblocked(entries, report) {
		names = append(names, e.Name)
	}
	// docker_image only appears as a prefix of docker_image_save: not a match.
	if strings.Join(names, ",") != "docker_image" {
		t.Errorf("unblocked = %v, want [docker_image]", names)
	}
	if len(Unblocked(entries, "")) != 3 {
		t.Error("empty report should leave all three missing types unblocked")
	}
}

func TestPrint(t *testing.T) {
	var b bytes.Buffer
	if err := Print(&b, []Entry{{Resource, "docker_a", Have, ""}, {Action, "docker_b", Missing, "later"}}, []Extra{{Resource, "docker_c", "ours"}}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, s := range []string{"KIND", "docker_a", "missing", "later", "2 kreuzwerker types: 1 have, 0 partial, 1 missing", "Only in this provider (1):", "docker_c"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func TestRunCheck(t *testing.T) {
	dir := t.TempDir()
	var missing []string
	for _, e := range kreuzwerker {
		if e.Status == Missing {
			missing = append(missing, e.Name)
		}
	}
	empty := filepath.Join(dir, "empty.md")
	os.WriteFile(empty, []byte("# report\n"), 0o644)
	full := filepath.Join(dir, "full.md")
	var lines []string
	for _, m := range missing {
		lines = append(lines, "[P3-D] [BLOCKED] D "+m+" not planned; tried nothing")
	}
	os.WriteFile(full, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 0 {
		t.Errorf("plain run = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "docker_buildx_builder") {
		t.Error("table not printed")
	}
	out.Reset()
	errb.Reset()
	code := run([]string{"-check", empty}, &out, &errb)
	if len(missing) > 0 && code != 1 {
		t.Errorf("check against an empty report = %d, want 1", code)
	}
	for _, m := range missing {
		if !strings.Contains(errb.String(), m) {
			t.Errorf("stderr does not name %s:\n%s", m, errb.String())
		}
	}
	errb.Reset()
	if code := run([]string{"-check", full}, &out, &errb); code != 0 {
		t.Errorf("check against a full report = %d: %s", code, errb.String())
	}
	if code := run([]string{"-check", filepath.Join(dir, "nope.md")}, &out, &errb); code != 2 {
		t.Errorf("missing file = %d, want 2", code)
	}
	if code := run([]string{"-bogus"}, &out, &errb); code != 2 {
		t.Errorf("bad flag = %d, want 2", code)
	}
}
