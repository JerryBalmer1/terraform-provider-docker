package provider

// Pass 5.3 Lane K: docker_compose_publish options (--with-env,
// --oci-version) and the env_file rewrite.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestComposeRewriteEnvFilesK(t *testing.T) {
	dir := t.TempDir()
	in := []byte(`services:
  a:
    image: x
    env_file: a.env # comment
  b:
    image: x
    env_file:
      - "b.env"
      - path: ./c.env
        required: false
  c:
    image: x
    env_file: [d.env, e.env]
`)
	out, paths, err := composeRewriteEnvFiles(in, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 5 {
		t.Fatalf("paths %v", paths)
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			t.Errorf("not absolute: %s", p)
		}
		if !strings.Contains(string(out), composeEnvFileLayerName(p)) {
			t.Errorf("%s not rewritten:\n%s", p, out)
		}
	}
	if strings.Contains(string(out), " a.env") || !strings.Contains(string(out), "# comment") || !strings.Contains(string(out), "required: false") {
		t.Errorf("rewrite:\n%s", out)
	}
	if want := composeEnvFileLayerName(filepath.Join(dir, "a.env")); want != composeEnvFileLayerName(paths[0]) {
		t.Errorf("hash of the absolute path")
	}
}

func TestComposePublishWithEnvK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := "name: pe\nservices:\n  a:\n    image: busybox\n    env_file: app.env\n"
	d := composeMustLoad(t, composeInputs{Content: yaml, WorkingDir: dir})
	files := []composeArtifactFile{{Name: composeContentFilename, Content: []byte(yaml)}}
	for _, withEnv := range []bool{false, true} {
		out, err := composePublishLayers(context.Background(), nil, d.Project, files, withEnv, false)
		if err != nil {
			t.Fatal(err)
		}
		name := composeEnvFileLayerName(filepath.Join(dir, "app.env"))
		if !strings.Contains(string(out[0].Content), "env_file: "+name) {
			t.Errorf("compose file not rewritten: %s", out[0].Content)
		}
		if withEnv != (len(out) == 2) {
			t.Fatalf("with_env=%v layers %d", withEnv, len(out))
		}
		if withEnv {
			if out[1].Name != name || out[1].MediaType != composeEnvFileMedia || string(out[1].Content) != "A=1\n" {
				t.Errorf("env layer %+v", out[1])
			}
			_, _, layers := composeArtifactManifest(out)
			if layers[1].MediaType != composeEnvFileMedia || layers[1].Annotations[composeEnvFileAnnotate] != name {
				t.Errorf("env descriptor %+v", layers[1])
			}
		}
	}
}

// TestComposePublishOCIVersionK: unset tries OCI 1.1 and falls back to
// 1.0 on a non-auth 4xx; "1.0" pushes the 1.0 form directly.
func TestComposePublishOCIVersionK(t *testing.T) {
	var mu sync.Mutex
	var manifests [][]byte
	reject11 := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(200) // every blob is there already
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			b, _ := io.ReadAll(r.Body)
			if reject11 && strings.Contains(string(b), "artifactType") {
				w.WriteHeader(400)
				return
			}
			manifests = append(manifests, b)
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	reg := &composeRegistry{hc: srv.Client(), base: srv.URL, repo: "apps/x"}
	files := []composeArtifactFile{{Name: "compose.yaml", Content: []byte("services: {}\n")}}
	check := func(oci string) {
		t.Helper()
		if _, _, err := composePublishVersion(context.Background(), reg, "1", files, oci); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(manifests[len(manifests)-1], &m); err != nil {
			t.Fatal(err)
		}
		cfg := m["config"].(map[string]any)
		if _, has := m["artifactType"]; has || cfg["mediaType"] != composeEmptyConfig10 {
			t.Errorf("oci %q: want the 1.0 form, got %v", oci, m)
		}
	}
	check("")
	check(composeOCI10)
	if _, _, err := composePublishVersion(context.Background(), reg, "1", files, composeOCI11); err == nil {
		t.Error("an explicit 1.1 must not fall back")
	}
	reject11 = false
	if _, _, err := composePublishVersion(context.Background(), reg, "1", files, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifests[len(manifests)-1]), composeArtifactType) {
		t.Error("1.1 accepted: want the artifact type")
	}
}
