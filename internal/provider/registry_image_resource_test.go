package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestRegistryImageBackoff(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := registryImageBackoff(i); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i, got, w)
		}
	}
	if registryImageBackoff(100) != 30*time.Second {
		t.Error("large n should cap")
	}
}

func TestRegistryImagePushWithRetry(t *testing.T) {
	var waits []time.Duration
	old := registryImageSleep
	registryImageSleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	t.Cleanup(func() { registryImageSleep = old })
	ctx := context.Background()

	// Fails twice (once at the API, once inside the stream), then succeeds.
	calls := 0
	digest, err := registryImagePushWithRetry(ctx, 3, func(context.Context) (io.ReadCloser, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New("connection reset by peer")
		case 2:
			return io.NopCloser(strings.NewReader(`{"errorDetail":{"message":"blob upload unknown"}}`)), nil
		default:
			return io.NopCloser(strings.NewReader(`{"status":"Pushed"}` + "\n" + `{"aux":{"Tag":"1","Digest":"sha256:ccc","Size":1}}`)), nil
		}
	})
	if err != nil || digest != "sha256:ccc" || calls != 3 {
		t.Fatalf("digest %q err %v calls %d", digest, err, calls)
	}
	if len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Errorf("waits %v", waits)
	}

	// Gives up after retries+1 attempts.
	calls = 0
	_, err = registryImagePushWithRetry(ctx, 2, func(context.Context) (io.ReadCloser, error) {
		calls++
		return nil, errors.New("i/o timeout")
	})
	if err == nil || calls != 3 {
		t.Errorf("exhausted: err %v calls %d", err, calls)
	}

	// Non-retryable errors stop at once.
	calls = 0
	_, err = registryImagePushWithRetry(ctx, 5, func(context.Context) (io.ReadCloser, error) {
		calls++
		return io.NopCloser(strings.NewReader(`{"error":"denied: requested access to the resource is denied"}`)), nil
	})
	if err == nil || calls != 1 {
		t.Errorf("non-retryable: err %v calls %d", err, calls)
	}
}

func TestRegistryImageParseChallenge(t *testing.T) {
	s, p := registryImageParseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:a/b:pull,push"`)
	if s != "bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:a/b:pull,push" {
		t.Errorf("got %s %v", s, p)
	}
	s, p = registryImageParseChallenge(`Basic realm="Registry Realm"`)
	if s != "basic" || p["realm"] != "Registry Realm" {
		t.Errorf("got %s %v", s, p)
	}
}

func TestRegistryImageIsLocal(t *testing.T) {
	for h, want := range map[string]bool{"localhost:5000": true, "127.0.0.1:5000": true, "[::1]:5000": true, "localhost": true, "ghcr.io": false, "10.0.0.1:5000": false} {
		if got := registryImageIsLocal(h); got != want {
			t.Errorf("%s: %v", h, got)
		}
	}
	if registryImageAPIHost("docker.io") != "registry-1.docker.io" || registryImageAPIHost("ghcr.io") != "ghcr.io" {
		t.Error("api host")
	}
}

func TestRegistryImageDeleteManifestBearer(t *testing.T) {
	const digest = "sha256:abc"
	var deleted string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "user" || p != "pw" || r.URL.Query().Get("service") != "reg" || r.URL.Query().Get("scope") != "repository:team/app:delete,pull" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok123"}`)
	})
	mux.HandleFunc("/v2/team/app/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="reg"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		deleted = strings.TrimPrefix(r.URL.Path, "/v2/team/app/manifests/")
		w.WriteHeader(http.StatusAccepted)
	})
	err := registryImageDeleteManifest(context.Background(), srv.Client(), srv.URL, "team/app", digest, "user", "pw")
	if err != nil || deleted != digest {
		t.Fatalf("err %v deleted %q", err, deleted)
	}
	// Wrong credentials fail at the token endpoint.
	if err := registryImageDeleteManifest(context.Background(), srv.Client(), srv.URL, "team/app", digest, "user", "bad"); err == nil {
		t.Error("bad credentials should fail")
	}
}

func TestRegistryImageDeleteManifestBasicAndStatus(t *testing.T) {
	status := http.StatusAccepted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
			w.Header().Set("WWW-Authenticate", `Basic realm="r"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"errors":[{"code":"UNSUPPORTED"}]}`)
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := registryImageDeleteManifest(ctx, srv.Client(), srv.URL, "a", "sha256:1", "u", "p"); err != nil {
		t.Errorf("basic: %v", err)
	}
	status = http.StatusNotFound
	if err := registryImageDeleteManifest(ctx, srv.Client(), srv.URL, "a", "sha256:1", "u", "p"); err != nil {
		t.Errorf("404 should count as deleted: %v", err)
	}
	status = http.StatusMethodNotAllowed
	if err := registryImageDeleteManifest(ctx, srv.Client(), srv.URL, "a", "sha256:1", "u", "p"); err == nil || !strings.Contains(err.Error(), "does not allow deletes") {
		t.Errorf("405: %v", err)
	}
	if err := registryImageDeleteManifest(ctx, srv.Client(), srv.URL, "a", "sha256:1", "", ""); err == nil {
		t.Error("basic challenge without credentials should fail")
	}
}

func TestRegistryImageNotFound(t *testing.T) {
	if !registryImageNotFound(errors.New("Error response from daemon: manifest unknown")) || registryImageNotFound(errors.New("connection refused")) {
		t.Error("registryImageNotFound")
	}
	if registryImageRetryable(context.Canceled) || registryImageRetryable(errors.New("unauthorized: authentication required")) || !registryImageRetryable(errors.New("EOF")) {
		t.Error("registryImageRetryable")
	}
}

// TestAccRegistryImage pushes to a throwaway registry. It needs
// TF_ACC_REGISTRY (e.g. "localhost:5000", a registry:2 with
// REGISTRY_STORAGE_DELETE_ENABLED=true reachable by the daemon).
func TestAccRegistryImage(t *testing.T) {
	testAccPreCheck(t)
	reg := os.Getenv("TF_ACC_REGISTRY")
	if reg == "" {
		t.Skip("TF_ACC_REGISTRY not set; skipping registry push test")
	}
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": "FROM alpine:3.20\nRUN echo tfacc > /tfacc\n"})
	cfg := fmt.Sprintf(`
resource "docker_image" "app" {
  name = "%s/tfacc/app:1"
  build {
    context = %q
  }
}
resource "docker_registry_image" "app" {
  name          = docker_image.app.name
  keep_remotely = false
  push_retries  = 1
  triggers      = { image = docker_image.app.id }
}
data "docker_registry_image" "app" {
  name       = docker_registry_image.app.name
  depends_on = [docker_registry_image.app]
}
`, reg, strings.ReplaceAll(dir, `\`, "/"))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("docker_registry_image.app", "digest", imgTestSHA),
					resource.TestCheckResourceAttrPair("data.docker_registry_image.app", "digest", "docker_registry_image.app", "digest"),
				),
			},
			{
				ResourceName:            "docker_registry_image.app",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"keep_remotely", "push_retries", "triggers", "insecure_skip_verify"},
			},
		},
	})
}
