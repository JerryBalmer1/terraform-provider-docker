package provider

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/jsonstream"
)

func imgTestWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func imgTestRegularFiles(bc *imgBuildContext) []string {
	var out []string
	for _, f := range bc.Files {
		if f.Info.Mode().IsRegular() {
			out = append(out, f.Rel)
		}
	}
	sort.Strings(out)
	return out
}

func TestImageCollectContextDockerignore(t *testing.T) {
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{
		"Dockerfile":          "FROM scratch\nCOPY . /\n",
		".dockerignore":       "# comment\n*.log\n!keep.log\nnode_modules\nsecret/\nDockerfile\n.dockerignore\n**/*.tmp\n",
		"app.go":              "package main",
		"debug.log":           "x",
		"keep.log":            "y",
		"node_modules/a/b.js": "z",
		"secret/key":          "k",
		"src/main.go":         "m",
		"src/deep/x.tmp":      "t",
	})
	bc, err := imgCollectContext(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(imgTestRegularFiles(bc), ",")
	want := ".dockerignore,Dockerfile,app.go,keep.log,src/main.go"
	if got != want {
		t.Errorf("included files:\n got %s\nwant %s", got, want)
	}
	if bc.Dockerfile != "Dockerfile" {
		t.Errorf("dockerfile name %q", bc.Dockerfile)
	}

	// The tar holds the same files with their content.
	pr := imgTarReader(bc)
	defer pr.Close()
	tr := tar.NewReader(pr)
	contents := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, `\`) {
			t.Errorf("tar name not slash-separated: %q", h.Name)
		}
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			contents[h.Name] = string(b)
			if h.Mode&0o400 == 0 {
				t.Errorf("%s not readable: %o", h.Name, h.Mode)
			}
		}
	}
	if contents["src/main.go"] != "m" || contents["Dockerfile"] == "" || len(contents) != 5 {
		t.Errorf("tar contents: %v", contents)
	}
}

func TestImageCollectContextNoIgnoreAndErrors(t *testing.T) {
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"build/app.Dockerfile": "FROM scratch", "a": "1"})
	bc, err := imgCollectContext(dir, "build/app.Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(imgTestRegularFiles(bc), ","); got != "a,build/app.Dockerfile" {
		t.Errorf("got %s", got)
	}
	if bc.Dockerfile != "build/app.Dockerfile" {
		t.Errorf("dockerfile %q", bc.Dockerfile)
	}
	if _, err := imgCollectContext(dir, ""); err == nil {
		t.Error("missing Dockerfile should fail")
	}
	if _, err := imgCollectContext(filepath.Join(dir, "nope"), ""); err == nil {
		t.Error("missing context should fail")
	}
	if _, err := imgCollectContext(filepath.Join(dir, "a"), ""); err == nil {
		t.Error("file as context should fail")
	}
}

func TestImageDockerfileOutsideContext(t *testing.T) {
	root := t.TempDir()
	imgTestWrite(t, root, map[string]string{"ctx/a": "1", "other/Dockerfile": "FROM scratch\n"})
	bc, err := imgCollectContext(filepath.Join(root, "ctx"), filepath.Join(root, "other", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bc.Dockerfile, ".dockerfile.") || bc.extraDockerfile == nil {
		t.Fatalf("outside dockerfile not injected: %q", bc.Dockerfile)
	}
	tr := tar.NewReader(imgTarReader(bc))
	found := false
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Name == bc.Dockerfile {
			b, _ := io.ReadAll(tr)
			found = string(b) == "FROM scratch\n"
		}
	}
	if !found {
		t.Error("injected Dockerfile missing from tar")
	}
	// Relative path climbing out of the context works too.
	bc2, err := imgCollectContext(filepath.Join(root, "ctx"), "../other/Dockerfile")
	if err != nil || bc2.Dockerfile != bc.Dockerfile {
		t.Errorf("relative outside dockerfile: %v %q", err, bc2.Dockerfile)
	}
}

func TestImageContextDigest(t *testing.T) {
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": "FROM scratch", ".dockerignore": "*.log", "a.txt": "1", "x.log": "ignored"})
	digest := func() string {
		t.Helper()
		bc, err := imgCollectContext(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		d, err := imgContextDigest(bc)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d1 := digest()
	if !strings.HasPrefix(d1, "sha256:") || len(d1) != len("sha256:")+64 {
		t.Fatalf("bad digest %q", d1)
	}
	// mtime only: unchanged
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	if d := digest(); d != d1 {
		t.Error("digest changed on mtime-only change")
	}
	// ignored file edit: unchanged
	imgTestWrite(t, dir, map[string]string{"x.log": "changed"})
	if d := digest(); d != d1 {
		t.Error("digest changed on ignored file edit")
	}
	// included file edit: changed
	imgTestWrite(t, dir, map[string]string{"a.txt": "2"})
	d2 := digest()
	if d2 == d1 {
		t.Error("digest unchanged after content edit")
	}
	// new file: changed
	imgTestWrite(t, dir, map[string]string{"b.txt": ""})
	if d := digest(); d == d2 {
		t.Error("digest unchanged after adding a file")
	}
}

func TestImageDecodeStream(t *testing.T) {
	ok := `{"stream":"Step 1/2 : FROM scratch\n"}
{"stream":" ---> Running\n"}
{"aux":{"ID":"sha256:aaa"}}
{"stream":"Successfully built aaa\n"}
`
	res, err := imgDecodeStream(strings.NewReader(ok), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id := imgBuiltImageID(res.Aux); id != "sha256:aaa" {
		t.Errorf("image id %q", id)
	}
	if len(res.Tail) != 3 {
		t.Errorf("tail %v", res.Tail)
	}

	bad := `{"stream":"Step 1/2 : RUN false\n"}
{"errorDetail":{"code":1,"message":"The command '/bin/sh -c false' returned a non-zero code: 1"},"error":"The command '/bin/sh -c false' returned a non-zero code: 1"}
{"stream":"never read\n"}
`
	var seen int
	_, err = imgDecodeStream(strings.NewReader(bad), func(m jsonstream.Message) { seen++ })
	var se *imgStreamError
	if !errors.As(err, &se) {
		t.Fatalf("want imgStreamError, got %v", err)
	}
	if !strings.Contains(se.Message, "non-zero code: 1") || !strings.Contains(err.Error(), "RUN false") {
		t.Errorf("error text: %v", err)
	}
	if seen != 2 {
		t.Errorf("onMsg saw %d messages, want 2", seen)
	}

	legacy := `{"status":"Pulling"}{"error":"pull access denied"}`
	if _, err := imgDecodeStream(strings.NewReader(legacy), nil); err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Errorf("legacy error: %v", err)
	}

	if _, err := imgDecodeStream(strings.NewReader(`{"stream":`), nil); err == nil {
		t.Error("truncated stream should fail")
	}

	push := `{"status":"Pushed","id":"abc"}
{"status":"1.0: digest: sha256:bbb size: 528"}
{"aux":{"Tag":"1.0","Digest":"sha256:bbb","Size":528}}
`
	res, err = imgDecodeStream(strings.NewReader(push), nil)
	if err != nil || imgPushedDigest(res.Aux) != "sha256:bbb" {
		t.Errorf("push digest: %v %v", imgPushedDigest(res.Aux), err)
	}
	if imgPushedDigest([]json.RawMessage{json.RawMessage(`"x"`)}) != "" {
		t.Error("non-object aux should be ignored")
	}
}

func TestImageDigestHelpers(t *testing.T) {
	if d := imgDigestFromRepoDigest("nginx@sha256:abc"); d != "sha256:abc" {
		t.Errorf("got %q", d)
	}
	if d := imgDigestFromRepoDigest("nginx"); d != "" {
		t.Errorf("got %q", d)
	}
	rds := []string{"ghcr.io/x/y@sha256:111", "nginx@sha256:222"}
	if got := imgPickRepoDigest("nginx:1.27", rds); got != "nginx@sha256:222" {
		t.Errorf("pick nginx: %q", got)
	}
	if got := imgPickRepoDigest("docker.io/library/nginx", rds); got != "nginx@sha256:222" {
		t.Errorf("pick normalized: %q", got)
	}
	if got := imgPickRepoDigest("quay.io/other", rds); got != rds[0] {
		t.Errorf("fallback: %q", got)
	}
	if got := imgPickRepoDigest("nginx", nil); got != "" {
		t.Errorf("empty: %q", got)
	}
	for in, want := range map[string][3]string{
		"nginx":                        {"docker.io", "library/nginx", "latest"},
		"localhost:5000/team/app:1.2":  {"localhost:5000", "team/app", "1.2"},
		"ghcr.io/o/a@sha256:" + imgZ64: {"ghcr.io", "o/a", "sha256:" + imgZ64},
	} {
		h, r, tg, err := imgRepoAndTag(in)
		if err != nil || [3]string{h, r, tg} != want {
			t.Errorf("%s: got %s %s %s %v", in, h, r, tg, err)
		}
	}
	if _, _, _, err := imgRepoAndTag("bad ref!!"); err == nil {
		t.Error("invalid reference should fail")
	}
	if !imgIsIDRef("abc", "sha256:abcdef") || !imgIsIDRef("sha256:abc", "sha256:abcdef") || imgIsIDRef("nginx", "sha256:abc") || imgIsIDRef("", "sha256:a") {
		t.Error("imgIsIDRef")
	}
}

var imgZ64 = strings.Repeat("0", 64)
