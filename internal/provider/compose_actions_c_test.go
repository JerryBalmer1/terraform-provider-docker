package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// composeCResult returns the JSON of the "<type> result: ..." progress event.
func composeCResult(t *testing.T, res actResult) map[string]any {
	t.Helper()
	for _, p := range res.Progress {
		if _, js, ok := strings.Cut(p, " result: "); ok {
			var m map[string]any
			if err := json.Unmarshal([]byte(js), &m); err != nil {
				t.Fatalf("result JSON: %v: %s", err, js)
			}
			return m
		}
	}
	t.Fatalf("no result progress in %q (errors: %s)", res.Progress, res.errText())
	return nil
}

func composeCSeen(f *p3eFake, prefix string) []string {
	var out []string
	for _, r := range f.requests() {
		if strings.HasPrefix(r, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func TestComposeVerbActionsC(t *testing.T) {
	f, pd := composeCFake(t)
	for _, v := range []string{"start", "stop", "restart", "kill", "pause", "unpause"} {
		f.on("POST /containers/c1/"+v, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		f.on("POST /containers/c2/"+v, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	}
	// stop: only the running web container.
	res := actInvokePD(t, pd, NewComposeStopAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "timeout": tftypes.NewValue(tftypes.Number, 3)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if got := composeCSeen(f, "POST /containers/"); len(got) != 1 || !strings.Contains(got[0], "c1/stop?t=3") {
		t.Errorf("stop requests %v", got)
	}
	r := composeCResult(t, res)
	states := r["result"].(map[string]any)["container_states"].([]any)
	if len(states) != 2 || states[0].(map[string]any)["service"] != "web" || r["name"] != "shop" {
		t.Errorf("result %v", r)
	}
	if r["engine_api_version"] != "1.47" {
		t.Errorf("engine_api_version %v", r["engine_api_version"])
	}
	// kill with a signal, one service.
	res = actInvokePD(t, pd, NewComposeKillAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "services": composeCStrings("web"), "signal": p3eStr("SIGHUP")})
	if res.Diags.HasError() || len(composeCSeen(f, "POST /containers/c1/kill?signal=SIGHUP")) != 1 {
		t.Errorf("kill: %s %v", res.errText(), f.requests())
	}
	// start: only the exited worker.
	res = actInvokePD(t, pd, NewComposeStartAction(), map[string]tftypes.Value{"name": p3eStr("shop")})
	if res.Diags.HasError() || len(composeCSeen(f, "POST /containers/c2/start")) != 1 || len(composeCSeen(f, "POST /containers/c1/start")) != 0 {
		t.Errorf("start: %s %v", res.errText(), f.requests())
	}
	// errors: unknown project has no containers.
	res = actInvokePD(t, pd, NewComposePauseAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "services": composeCStrings("nope")})
	if !res.Diags.HasError() {
		t.Error("no containers: no error")
	}
	// The daemon refusing fails the action.
	f.on("POST /containers/c1/restart", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 500, map[string]string{"message": "boom"})
	})
	res = actInvokePD(t, pd, NewComposeRestartAction(), map[string]tftypes.Value{"name": p3eStr("shop")})
	if !res.Diags.HasError() || !strings.Contains(res.errText(), "boom") {
		t.Errorf("restart error: %s", res.errText())
	}
	res = actInvokePD(t, pd, NewComposeUnpauseAction(), map[string]tftypes.Value{"name": p3eStr("shop")})
	if res.Diags.HasError() {
		t.Error(res.errText())
	}
}

// composeCCreateRoutes makes the fake create, start, wait and remove
// containers; created records the create requests by name.
func composeCCreateRoutes(f *p3eFake, exit int) *[]string {
	var mu sync.Mutex
	var created []string
	f.json("GET /images/busybox:1.36/json", map[string]any{"Id": "sha256:img2"})
	f.on("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		created = append(created, r.URL.Query().Get("name"))
		mu.Unlock()
		p3eWriteJSON(w, 201, map[string]string{"Id": "new1"})
	})
	f.on("POST /containers/new1/start", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	f.on("POST /containers/new1/wait", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 200, map[string]any{"StatusCode": exit})
	})
	f.on("GET /containers/new1/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(composeCFrame(1, "migrated\n"))
		_, _ = w.Write(composeCFrame(2, "note\n"))
	})
	f.on("DELETE /containers/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	return &created
}

const composeCYAML = "name: shop\nservices:\n  web: {image: \"nginx:1.27\"}\n  worker: {image: \"busybox:1.36\"}\n"

func TestComposeRunActionC(t *testing.T) {
	f, pd := composeCFake(t)
	created := composeCCreateRoutes(f, 0)
	dir := t.TempDir()
	out := filepath.Join(dir, "run.json")
	res := actInvokePD(t, pd, NewComposeRunAction(), map[string]tftypes.Value{
		"content": p3eStr(composeCYAML), "project_directory": p3eStr(dir), "service": p3eStr("worker"),
		"command": composeCStrings("sh", "-c", "migrate"), "result_path": p3eStr(out),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(*created) != 1 || !strings.HasPrefix((*created)[0], "shop-worker-run-") {
		t.Errorf("created %v", *created)
	}
	if len(composeCSeen(f, "DELETE /containers/new1")) != 1 {
		t.Error("run container not removed")
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	_ = json.Unmarshal(b, &r)
	rr := r["result"].(map[string]any)
	if rr["exit_code"] != float64(0) || rr["stdout"] != "migrated\n" || rr["stderr"] != "note\n" || r["container_id"] != "new1" {
		t.Errorf("result file %s", b)
	}
	// The progress copy hides the output.
	if p := composeCResult(t, res); p["result"].(map[string]any)["stdout"] != actSensitiveMark {
		t.Errorf("stdout not redacted in progress: %v", p)
	}
	// Unknown service / job.
	res = actInvokePD(t, pd, NewComposeRunAction(), map[string]tftypes.Value{"content": p3eStr(composeCYAML), "project_directory": p3eStr(dir), "service": p3eStr("nope")})
	if !res.Diags.HasError() {
		t.Error("unknown service: no error")
	}
}

func TestComposeRunActionFailureC(t *testing.T) {
	f, pd := composeCFake(t)
	composeCCreateRoutes(f, 3)
	res := actInvokePD(t, pd, NewComposeRunAction(), map[string]tftypes.Value{
		"content": p3eStr(composeCYAML), "project_directory": p3eStr(t.TempDir()), "service": p3eStr("worker"),
	})
	if !res.Diags.HasError() || !strings.Contains(res.errText(), "exited with code 3") || !strings.Contains(res.errText(), "migrated") {
		t.Errorf("errors: %s", res.errText())
	}
}

func TestComposeScaleActionC(t *testing.T) {
	f, pd := composeCFake(t)
	created := composeCCreateRoutes(f, 0)
	f.on("POST /containers/c2/start", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	res := actInvokePD(t, pd, NewComposeScaleAction(), map[string]tftypes.Value{
		"content": p3eStr(composeCYAML), "project_directory": p3eStr(t.TempDir()),
		"replicas": tftypes.NewValue(tftypes.Map{ElementType: tftypes.Number}, map[string]tftypes.Value{
			"worker": tftypes.NewValue(tftypes.Number, 2), "web": tftypes.NewValue(tftypes.Number, 0)}),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(*created) != 1 || (*created)[0] != "shop-worker-2" {
		t.Errorf("created %v", *created)
	}
	if len(composeCSeen(f, "DELETE /containers/c1")) != 1 || len(composeCSeen(f, "DELETE /containers/c2")) != 0 {
		t.Errorf("removals %v", composeCSeen(f, "DELETE"))
	}
	// Like `docker compose scale`, the kept exited shop-worker-1 is started.
	if len(composeCSeen(f, "POST /containers/c2/start")) != 1 {
		t.Errorf("exited worker not started: %v", f.requests())
	}
	if r := composeCResult(t, res); r["replicas"].(map[string]any)["worker"] != float64(2) {
		t.Errorf("result %v", r)
	}
	res = actInvokePD(t, pd, NewComposeScaleAction(), map[string]tftypes.Value{
		"content": p3eStr(composeCYAML), "project_directory": p3eStr(t.TempDir()),
		"replicas": tftypes.NewValue(tftypes.Map{ElementType: tftypes.Number}, map[string]tftypes.Value{"nope": tftypes.NewValue(tftypes.Number, 1)}),
	})
	if !res.Diags.HasError() {
		t.Error("unknown service: no error")
	}
}

func TestComposeContainerActionsC(t *testing.T) {
	f, pd := composeCFake(t)
	f.json("GET /containers/c1/changes", []map[string]any{{"Path": "/etc/x", "Kind": 1}})
	f.on("POST /commit", func(w http.ResponseWriter, r *http.Request) {
		p3eWriteJSON(w, 201, map[string]string{"Id": "sha256:new"})
	})
	f.on("GET /containers/c1/export", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("tarbytes")) })
	f.on("GET /containers/c1/archive", func(w http.ResponseWriter, _ *http.Request) {
		st, _ := json.Marshal(map[string]any{"name": "conf.d", "size": 4, "mode": 2147484141, "mtime": "2026-01-01T00:00:00Z", "linkTarget": ""})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(st))
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "conf.d/", Typeflag: tar.TypeDir, Mode: 0o755})
		_ = tw.WriteHeader(&tar.Header{Name: "conf.d/a.conf", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4})
		_, _ = tw.Write([]byte("abcd"))
		_ = tw.WriteHeader(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
		_, _ = w.Write(buf.Bytes())
	})
	f.on("PUT /containers/c1/archive", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	f.on("HEAD /containers/c1/archive", func(w http.ResponseWriter, _ *http.Request) {
		st, _ := json.Marshal(map[string]any{"name": "app", "size": 1})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(st))
		w.WriteHeader(200)
	})
	f.on("POST /containers/c1/wait", func(w http.ResponseWriter, _ *http.Request) { p3eWriteJSON(w, 200, map[string]any{"StatusCode": 0}) })
	f.on("POST /containers/c2/wait", func(w http.ResponseWriter, _ *http.Request) { p3eWriteJSON(w, 200, map[string]any{"StatusCode": 3}) })

	res := actInvokePD(t, pd, NewComposeCommitAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "service": p3eStr("web"), "repository": p3eStr("snap:1")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if r := composeCResult(t, res); r["id"] != "sha256:new" || r["container_id"] != "c1" || len(r["result"].(map[string]any)["changes"].([]any)) != 1 {
		t.Errorf("commit result %v", r)
	}

	dir := t.TempDir()
	res = actInvokePD(t, pd, NewComposeExportAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "service": p3eStr("web"), "output": p3eStr(filepath.Join(dir, "web.tar"))})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "web.tar")); string(b) != "tarbytes" {
		t.Errorf("export file %q", b)
	}
	if r := composeCResult(t, res); r["result"].(map[string]any)["archive_size"] != float64(8) {
		t.Errorf("export result %v", r)
	}

	// cp from the container: an entry naming ../ stays inside the destination.
	res = actInvokePD(t, pd, NewComposeCpAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "service": p3eStr("web"),
		"direction": p3eStr(composeCpFrom), "container_path": p3eStr("/etc/nginx/conf.d"), "local_path": p3eStr(filepath.Join(dir, "out"))})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
		t.Error("archive entry escaped the destination")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out", "evil")); string(b) != "x" {
		t.Errorf("sanitized entry %q", b)
	}
	if r := composeCResult(t, res); r["result"].(map[string]any)["path_stat"].(map[string]any)["name"] != "conf.d" {
		t.Errorf("cp from result %v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out", "conf.d", "a.conf")); string(b) != "abcd" {
		t.Errorf("extracted %q", b)
	}
	// cp to the container.
	src := filepath.Join(dir, "app")
	_ = os.WriteFile(src, []byte("x"), 0o600)
	res = actInvokePD(t, pd, NewComposeCpAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "service": p3eStr("web"),
		"direction": p3eStr(composeCpTo), "container_path": p3eStr("/srv"), "local_path": p3eStr(src)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if r := composeCResult(t, res); r["path"] != "/srv" || r["result"].(map[string]any)["path_stat"].(map[string]any)["name"] != "app" {
		t.Errorf("cp result %v", r)
	}

	res = actInvokePD(t, pd, NewComposeWaitAction(), map[string]tftypes.Value{"name": p3eStr("shop")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	results := composeCResult(t, res)["result"].(map[string]any)["results"].(map[string]any)
	if results["shop-worker-1"].(map[string]any)["exit_code"] != float64(3) {
		t.Errorf("wait results %v", results)
	}
	res = actInvokePD(t, pd, NewComposeWaitAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "fail_on_error": tftypes.NewValue(tftypes.Bool, true)})
	if !res.Diags.HasError() {
		t.Error("fail_on_error: no error")
	}

	// exec: the exited worker has no running container.
	res = actInvokePD(t, pd, NewComposeExecAction(), map[string]tftypes.Value{"name": p3eStr("shop"), "service": p3eStr("worker"), "command": composeCStrings("true")})
	if !res.Diags.HasError() || !strings.Contains(res.errText(), "no running container 1 of service worker") {
		t.Errorf("exec error: %s", res.errText())
	}
}

// composeCBuildKit is a fake BuildKit seam.
type composeCBuildKit struct{ reqs []seamBuildRequest }

func (b *composeCBuildKit) Build(_ context.Context, _ *providerData, req seamBuildRequest) (*seamBuildResult, error) {
	b.reqs = append(b.reqs, req)
	if req.Progress != nil {
		req.Progress("#1 DONE")
	}
	return &seamBuildResult{ImageID: "sha256:built"}, nil
}

func TestComposeImageActionsC(t *testing.T) {
	f, pd := composeCFake(t)
	fake := &composeCBuildKit{}
	prev := seamBuildKitImpl
	seamBuildKitImpl = fake
	t.Cleanup(func() { seamBuildKitImpl = prev })
	dir := t.TempDir()
	yaml := "name: app\nservices:\n  api:\n    build:\n      context: .\n      args: {V: \"1\"}\n      tags: [\"reg/api:extra\"]\n  web: {image: \"nginx:1.27\"}\n"
	f.json("GET /images/app-api/json", map[string]any{"Id": "sha256:built", "Os": "linux", "Architecture": "arm64", "Size": 5})
	res := actInvokePD(t, pd, NewComposeBuildAction(), map[string]tftypes.Value{"content": p3eStr(yaml), "project_directory": p3eStr(dir)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(fake.reqs) != 1 || fake.reqs[0].Outputs.Names[0] != "app-api" || fake.reqs[0].Outputs.Names[1] != "reg/api:extra" ||
		fake.reqs[0].BuildArgs["V"] != "1" || !fake.reqs[0].Outputs.Load {
		t.Errorf("build requests %+v", fake.reqs)
	}
	r := composeCResult(t, res)
	if r["image_ids"].(map[string]any)["api"] != "sha256:built" ||
		r["result"].(map[string]any)["built_images"].(map[string]any)["api"].(map[string]any)["architecture"] != "arm64" {
		t.Errorf("build result %v", r)
	}

	f.on("POST /images/create", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"Pulled"}` + "\n")) })
	f.json("GET /images/nginx:1.27/json", map[string]any{"Id": "sha256:img1", "RepoDigests": []string{"nginx@sha256:" + strings.Repeat("c", 64)}})
	res = actInvokePD(t, pd, NewComposePullAction(), map[string]tftypes.Value{"content": p3eStr(yaml), "project_directory": p3eStr(dir)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	r = composeCResult(t, res)
	if st := r["result"].(map[string]any)["pull_status"].(map[string]any); st["api"] != "skipped (built)" || st["web"] != "pulled" {
		t.Errorf("pull status %v", st)
	}
	if !strings.HasPrefix(r["image_digest_refs"].(map[string]any)["web"].(string), "nginx@sha256:ccc") {
		t.Errorf("pull digests %v", r["image_digest_refs"])
	}

	f.on("POST /images/docker.io/library/nginx/push", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pushed"}` + "\n" + `{"aux":{"Tag":"1.27","Digest":"sha256:` + strings.Repeat("d", 64) + `","Size":1}}` + "\n"))
	})
	// compose push only pushes a built service (pkg/compose/push.go:50-60, v5.7.0).
	pushYAML := "name: app\nservices:\n  web:\n    build: {context: .}\n    image: \"nginx:1.27\"\n"
	res = actInvokePD(t, pd, NewComposePushAction(), map[string]tftypes.Value{"content": p3eStr(pushYAML), "project_directory": p3eStr(dir), "services": composeCStrings("web")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if got := composeCResult(t, res)["image_digest_refs"].(map[string]any)["web"]; got != "nginx@sha256:"+strings.Repeat("d", 64) {
		t.Errorf("push digest %v", got)
	}
	res = actInvokePD(t, pd, NewComposePushAction(), map[string]tftypes.Value{"content": p3eStr(yaml), "project_directory": p3eStr(dir), "services": composeCStrings("nope")})
	if !res.Diags.HasError() {
		t.Error("unknown service: no error")
	}
}

func TestComposePublishActionC(t *testing.T) {
	var mu sync.Mutex
	blobs := map[string][]byte{}
	var manifest []byte
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
			w.WriteHeader(404)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
			w.Header().Set("Location", "/v2/apps/shop/blobs/uploads/u1?state=x")
			w.WriteHeader(202)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			b, _ := io.ReadAll(r.Body)
			d := r.URL.Query().Get("digest")
			if composeDigest(b) != d || r.URL.Query().Get("state") != "x" {
				w.WriteHeader(400)
				return
			}
			blobs[d] = b
			w.WriteHeader(201)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/manifests/1.0"):
			manifest, _ = io.ReadAll(r.Body)
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "http://")
	_, pd := composeCFake(t)
	res := actInvokePD(t, pd, NewComposePublishAction(), map[string]tftypes.Value{
		"content": p3eStr(composeCYAML), "project_directory": p3eStr(t.TempDir()), "repository": p3eStr(host + "/apps/shop:1.0"),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	var man struct {
		ArtifactType string              `json:"artifactType"`
		Layers       []composeDescriptor `json:"layers"`
	}
	if err := json.Unmarshal(manifest, &man); err != nil || man.ArtifactType != composeArtifactType || len(man.Layers) != 1 ||
		string(blobs[man.Layers[0].Digest]) != composeCYAML || man.Layers[0].Annotations[composeFileAnnotation] != composeContentFilename {
		t.Fatalf("manifest %s blobs %d", manifest, len(blobs))
	}
	r := composeCResult(t, res)
	if r["digest"] != composeDigest(manifest) || r["image_digest_ref"] != host+"/apps/shop@"+composeDigest(manifest) || r["image_ref"] != host+"/apps/shop:1.0" {
		t.Errorf("publish result %v", r)
	}
}
