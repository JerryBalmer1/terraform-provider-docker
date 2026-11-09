package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
)

// copFake is a fake Engine with container "web" (ID c1) and /version.
func copFake(t *testing.T) (*p3eFake, *providerData) {
	t.Helper()
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	insp := container.InspectResponse{
		ID: "c1", Name: "/web", Image: "sha256:img1",
		State:  &container.State{Status: "running", Running: true, Pid: 9},
		Config: &container.Config{Labels: map[string]string{"app": "shop"}, Env: []string{"SECRET=x"}},
	}
	f.json("GET /containers/web/json", insp)
	f.json("GET /containers/c1/json", insp)
	return f, pd
}

func copStatHeader(t *testing.T, st container.PathStat) string {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestCopContainerDataSource(t *testing.T) {
	_, pd := copFake(t)
	state, diags := p3eReadDS(t, NewContainerDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("web")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	ctx := context.Background()
	var id, name, img, status, raw, sum, ver string
	var labels map[string]string
	state.GetAttribute(ctx, path.Root("id"), &id)
	state.GetAttribute(ctx, path.Root("name"), &name)
	state.GetAttribute(ctx, path.Root("image_id"), &img)
	state.GetAttribute(ctx, path.Root("labels"), &labels)
	state.GetAttribute(ctx, path.Root("inspect").AtName("state").AtName("status"), &status)
	state.GetAttribute(ctx, path.Root("inspect_json"), &raw)
	state.GetAttribute(ctx, path.Root("content_sha256"), &sum)
	state.GetAttribute(ctx, path.Root("docker_server_version"), &ver)
	if id != "c1" || name != "web" || img != "sha256:img1" || labels["app"] != "shop" || status != "running" {
		t.Errorf("id=%q name=%q image_id=%q labels=%v status=%q", id, name, img, labels, status)
	}
	if !strings.Contains(raw, `"SECRET=x"`) || len(sum) != 64 || ver != "29.0.0" {
		t.Errorf("inspect_json=%q sha=%q version=%q", raw, sum, ver)
	}
}

func TestCopContainerDataSourceMissing(t *testing.T) {
	_, pd := copFake(t)
	_, diags := p3eReadDS(t, NewContainerDataSource(), pd, map[string]tftypes.Value{"id": p3eStr("nope")})
	if !diags.HasError() {
		t.Fatal("expected an error for a missing container")
	}
}

func TestCopContainerChanges(t *testing.T) {
	f, pd := copFake(t)
	f.json("GET /containers/c1/changes", []container.FilesystemChange{{Path: "/tmp/x", Kind: 1}, {Path: "/etc", Kind: 0}})
	state, diags := p3eReadDS(t, NewContainerChangesDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	ctx := context.Background()
	var id, p0 string
	var k0 float64
	state.GetAttribute(ctx, path.Root("container_id"), &id)
	state.GetAttribute(ctx, path.Root("changes").AtListIndex(0).AtName("path"), &p0)
	state.GetAttribute(ctx, path.Root("changes").AtListIndex(0).AtName("kind"), &k0)
	if id != "c1" || p0 != "/tmp/x" || k0 != 1 {
		t.Errorf("container_id=%q changes[0]=%q/%v", id, p0, k0)
	}
}

func TestCopContainerStats(t *testing.T) {
	f, pd := copFake(t)
	var q string
	f.on("GET /containers/c1/stats", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"name":"/web","id":"c1","pids_stats":{"current":3},"memory_stats":{"usage":1024}}` + "\n"))
	})
	state, diags := p3eReadDS(t, NewContainerStatsDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	ctx := context.Background()
	var id, name, raw string
	state.GetAttribute(ctx, path.Root("id"), &id)
	state.GetAttribute(ctx, path.Root("name"), &name)
	state.GetAttribute(ctx, path.Root("stats_json"), &raw)
	if id != "c1" || name != "web" || strings.HasSuffix(raw, "\n") || !strings.Contains(raw, `"usage":1024`) {
		t.Errorf("id=%q name=%q stats_json=%q", id, name, raw)
	}
	if !strings.Contains(q, "stream=false") || !strings.Contains(q, "one-shot=true") {
		t.Errorf("stats query %q is not a single sample", q)
	}
}

func TestCopContainerStatsBadBody(t *testing.T) {
	f, pd := copFake(t)
	f.on("GET /containers/c1/stats", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not json")) })
	_, diags := p3eReadDS(t, NewContainerStatsDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web")})
	if !diags.HasError() {
		t.Fatal("expected an error for a non-JSON stats body")
	}
}

func TestCopContainerTop(t *testing.T) {
	f, pd := copFake(t)
	var q string
	f.on("GET /containers/c1/top", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("ps_args")
		p3eWriteJSON(w, http.StatusOK, map[string]any{"Titles": []string{"PID", "CMD"}, "Processes": [][]string{{"1", "sleep"}}})
	})
	state, diags := p3eReadDS(t, NewContainerTopDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web"), "ps_args": p3eStr("aux")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var titles []string
	var procs [][]string
	state.GetAttribute(context.Background(), path.Root("top").AtName("titles"), &titles)
	state.GetAttribute(context.Background(), path.Root("top").AtName("processes"), &procs)
	if strings.Join(titles, ",") != "PID,CMD" || len(procs) != 1 || procs[0][1] != "sleep" || q != "aux" {
		t.Errorf("titles=%v processes=%v ps_args=%q", titles, procs, q)
	}
}

func TestCopContainerFile(t *testing.T) {
	f, pd := copFake(t)
	st := container.PathStat{Name: "app.conf", Size: 5, Mode: 0o644, Mtime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	hdr := copStatHeader(t, st)
	f.on("HEAD /containers/c1/archive", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", hdr)
	})
	f.on("GET /containers/c1/archive", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", hdr)
		tw := tar.NewWriter(w)
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "app.conf", Size: 5, Mode: 0o644})
		_, _ = tw.Write([]byte("hello"))
		_ = tw.Close()
	})
	state, diags := p3eReadDS(t, NewContainerFileDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web"), "path": p3eStr("/etc/app.conf")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	ctx := context.Background()
	var content, name, mtime, sha, statSha string
	var size, mode float64
	state.GetAttribute(ctx, path.Root("content_base64"), &content)
	state.GetAttribute(ctx, path.Root("stat").AtName("name"), &name)
	state.GetAttribute(ctx, path.Root("stat").AtName("size"), &size)
	state.GetAttribute(ctx, path.Root("stat").AtName("mode"), &mode)
	state.GetAttribute(ctx, path.Root("stat").AtName("mtime"), &mtime)
	state.GetAttribute(ctx, path.Root("content_base64_sha256"), &sha)
	state.GetAttribute(ctx, path.Root("stat_json_sha256"), &statSha)
	if content != base64.StdEncoding.EncodeToString([]byte("hello")) || name != "app.conf" || size != 5 || mode != 0o644 || mtime != "2026-01-02T03:04:05Z" {
		t.Errorf("content=%q stat=%q/%v/%v/%q", content, name, size, mode, mtime)
	}
	if len(sha) != 64 || len(statSha) != 64 {
		t.Errorf("hashes %q %q", sha, statSha)
	}
}

func TestCopContainerFileDirectory(t *testing.T) {
	f, pd := copFake(t)
	hdr := copStatHeader(t, container.PathStat{Name: "etc", Mode: fs.ModeDir | 0o755})
	f.on("HEAD /containers/c1/archive", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", hdr)
	})
	state, diags := p3eReadDS(t, NewContainerFileDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web"), "path": p3eStr("/etc")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var content *string
	state.GetAttribute(context.Background(), path.Root("content_base64"), &content)
	if content != nil {
		t.Errorf("content_base64 of a directory = %q, want null", *content)
	}
}

func TestCopContainerFileTooLarge(t *testing.T) {
	f, pd := copFake(t)
	hdr := copStatHeader(t, container.PathStat{Name: "big", Size: copFileMaxBytes + 1, Mode: 0o644})
	f.on("HEAD /containers/c1/archive", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", hdr)
	})
	_, diags := p3eReadDS(t, NewContainerFileDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("web"), "path": p3eStr("/big")})
	if !diags.HasError() || !strings.Contains(diags[0].Summary(), "too large") {
		t.Fatalf("diags = %v", diags)
	}
}

func TestCopContainerVerbs(t *testing.T) {
	for _, tc := range []struct {
		new   func() action.Action
		verb  string
		extra map[string]tftypes.Value
		query []string
	}{
		{func() action.Action { return NewContainerStartAction() }, "start", nil, nil},
		{func() action.Action { return NewContainerStopAction() }, "stop", map[string]tftypes.Value{"signal": actTfStr("SIGINT"), "timeout": actTfNum(5)}, []string{"signal=SIGINT", "t=5"}},
		{func() action.Action { return NewContainerRestartAction() }, "restart", map[string]tftypes.Value{"timeout": actTfNum(0)}, []string{"t=0"}},
		{func() action.Action { return NewContainerKillAction() }, "kill", map[string]tftypes.Value{"signal": actTfStr("SIGHUP")}, []string{"signal=SIGHUP"}},
		{func() action.Action { return NewContainerPauseAction() }, "pause", nil, nil},
		{func() action.Action { return NewContainerUnpauseAction() }, "unpause", nil, nil},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			f, pd := copFake(t)
			var mu sync.Mutex
			var q string
			f.on("POST /containers/c1/"+tc.verb, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				q = r.URL.RawQuery
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			})
			out := filepath.Join(t.TempDir(), "r.json")
			cfg := map[string]tftypes.Value{"container_id": actTfStr("web"), "result_path": actTfStr(out)}
			for k, v := range tc.extra {
				cfg[k] = v
			}
			res := actInvokePD(t, pd, tc.new(), cfg)
			if res.Diags.HasError() {
				t.Fatal(res.errText())
			}
			for _, want := range tc.query {
				if !strings.Contains(q, want) {
					t.Errorf("query %q lacks %s", q, want)
				}
			}
			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			r, _ := m["result"].(map[string]any)
			st, _ := r["state"].(map[string]any)
			if m["container_id"] != "c1" || st["status"] != "running" || m["docker_server_version"] != "29.0.0" {
				t.Errorf("result file = %s", b)
			}
		})
	}
}

func TestCopContainerVerbError(t *testing.T) {
	f, pd := copFake(t)
	f.on("POST /containers/c1/pause", func(w http.ResponseWriter, r *http.Request) {
		p3eWriteJSON(w, http.StatusConflict, map[string]string{"message": "container c1 is not running"})
	})
	res := actInvokePD(t, pd, NewContainerPauseAction(), map[string]tftypes.Value{"container_id": actTfStr("web")})
	if !strings.Contains(res.errText(), "is not running") {
		t.Fatalf("got %q", res.errText())
	}
	res = actInvokePD(t, pd, NewContainerStartAction(), map[string]tftypes.Value{"container_id": actTfStr("gone")})
	if !strings.Contains(res.errText(), "cannot find container") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestCopContainerWait(t *testing.T) {
	f, pd := copFake(t)
	var cond string
	f.on("POST /containers/c1/wait", func(w http.ResponseWriter, r *http.Request) {
		cond = r.URL.Query().Get("condition")
		p3eWriteJSON(w, http.StatusOK, container.WaitResponse{StatusCode: 3})
	})
	out := filepath.Join(t.TempDir(), "w.json")
	res := actInvokePD(t, pd, NewContainerWaitAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"), "condition": actTfStr("next-exit"), "result_path": actTfStr(out),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	b, _ := os.ReadFile(out)
	if cond != "next-exit" || !strings.Contains(string(b), `"status_code": 3`) {
		t.Errorf("condition=%q result=%s", cond, b)
	}
	res = actInvokePD(t, pd, NewContainerWaitAction(), map[string]tftypes.Value{"container_id": actTfStr("web"), "expect_exit_code": actTfNum(0)})
	if !strings.Contains(res.errText(), "exited with code 3, expected 0") {
		t.Fatalf("got %q", res.errText())
	}
	if d := actValidate(t, NewContainerWaitAction(), map[string]tftypes.Value{"container_id": actTfStr("web"), "timeout": actTfStr("x")}); !d.HasError() {
		t.Error("timeout = x should fail validation")
	}
}

func TestCopContainerCpContent(t *testing.T) {
	f, pd := copFake(t)
	var dest string
	var hdr *tar.Header
	var body []byte
	f.on("PUT /containers/c1/archive", func(w http.ResponseWriter, r *http.Request) {
		dest = r.URL.Query().Get("path")
		tr := tar.NewReader(r.Body)
		h, err := tr.Next()
		if err == nil {
			hdr = h
			body, _ = io.ReadAll(tr)
		}
		w.WriteHeader(http.StatusOK)
	})
	f.on("HEAD /containers/c1/archive", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Docker-Container-Path-Stat", copStatHeader(t, container.PathStat{Name: "app.conf", Size: 2, Mode: 0o600}))
	})
	out := filepath.Join(t.TempDir(), "cp.json")
	res := actInvokePD(t, pd, NewContainerCpAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"), "path": actTfStr("/etc/app.conf"), "content": actTfStr("hi"),
		"mode": actTfStr("0600"), "uid": actTfNum(1000), "gid": actTfNum(2000), "result_path": actTfStr(out),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if dest != "/etc" || hdr == nil || hdr.Name != "app.conf" || hdr.Mode != 0o600 || hdr.Uid != 1000 || hdr.Gid != 2000 || string(body) != "hi" {
		t.Errorf("dest=%q header=%+v body=%q", dest, hdr, body)
	}
	b, _ := os.ReadFile(out)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	r, _ := m["result"].(map[string]any)
	ps, _ := r["path_stat"].(map[string]any)
	if m["path"] != "/etc/app.conf" || len(m["archive_sha256"].(string)) != 64 || ps["name"] != "app.conf" {
		t.Errorf("result file = %s", b)
	}
}

func TestCopContainerCpValidate(t *testing.T) {
	for name, cfg := range map[string]map[string]tftypes.Value{
		"none": {"container_id": actTfStr("web"), "path": actTfStr("/x")},
		"two":  {"container_id": actTfStr("web"), "path": actTfStr("/x"), "content": actTfStr("a"), "source": actTfStr("b")},
		"b64":  {"container_id": actTfStr("web"), "path": actTfStr("/x"), "content_base64": actTfStr("!!!")},
	} {
		if d := actValidate(t, NewContainerCpAction(), cfg); !d.HasError() {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestCopTarSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	mode := fs.FileMode(0o640)
	b, err := copTarSource(dir, "conf", copTarOptions{UID: 1, GID: 2, Mode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(b))
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
		if h.Name == "conf/sub/a.txt" && (h.Mode != 0o640 || h.Uid != 1 || h.Gid != 2) {
			t.Errorf("a.txt header %+v", h)
		}
	}
	if strings.Join(names, ",") != "conf/,conf/sub/,conf/sub/a.txt" {
		t.Errorf("entries = %v", names)
	}
}

func TestCopContainerExport(t *testing.T) {
	f, pd := copFake(t)
	f.on("GET /containers/c1/export", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("TARDATA")) })
	out := filepath.Join(t.TempDir(), "sub", "web.tar")
	rp := filepath.Join(t.TempDir(), "r.json")
	res := actInvokePD(t, pd, NewContainerExportAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"), "output": actTfStr(out), "result_path": actTfStr(rp),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "TARDATA" {
		t.Fatalf("file: %q %v", b, err)
	}
	b, _ := os.ReadFile(rp)
	// sha256("TARDATA")
	if !strings.Contains(string(b), `"archive_size": 7`) || !strings.Contains(string(b), `"archive_sha256": "`) || !strings.Contains(string(b), `"container_id": "c1"`) {
		t.Errorf("result file = %s", b)
	}
	left, _ := os.ReadDir(filepath.Dir(out))
	if len(left) != 1 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

func TestCopContainerExportError(t *testing.T) {
	f, pd := copFake(t)
	f.on("GET /containers/c1/export", func(w http.ResponseWriter, r *http.Request) {
		p3eWriteJSON(w, http.StatusInternalServerError, map[string]string{"message": "export broke"})
	})
	out := filepath.Join(t.TempDir(), "web.tar")
	res := actInvokePD(t, pd, NewContainerExportAction(), map[string]tftypes.Value{"container_id": actTfStr("web"), "output": actTfStr(out)})
	if !strings.Contains(res.errText(), "export broke") {
		t.Fatalf("got %q", res.errText())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("no file should be written on error")
	}
}

func TestCopIdentityOutputs(t *testing.T) {
	ctx := context.Background()
	for id, d := range map[string]datasource.DataSource{
		"engine:container-data":    NewContainerDataSource(),
		"engine:container-changes": NewContainerChangesDataSource(),
		"engine:container-file":    NewContainerFileDataSource(),
		"engine:container-stats":   NewContainerStatsDataSource(),
		"engine:container-top":     NewContainerTopDataSource(),
		"engine:container-logs":    NewContainerLogsDataSource(),
		"engine:containers":        NewContainersDataSource(),
	} {
		var sr datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &sr)
		for _, n := range contractIdentityOutputNames(id) {
			if _, ok := sr.Schema.Attributes[n]; !ok {
				t.Errorf("%s: identity output %q missing from the schema", id, n)
			}
		}
	}
	var er ephemeral.SchemaResponse
	NewExecOutputEphemeralResource().Schema(ctx, ephemeral.SchemaRequest{}, &er)
	for _, n := range contractIdentityOutputNames("engine:exec-output") {
		if _, ok := er.Schema.Attributes[n]; !ok {
			t.Errorf("engine:exec-output: identity output %q missing from the schema", n)
		}
	}
}

func TestCopContainerDataSourceExactlyOne(t *testing.T) {
	vs := NewContainerDataSource().(datasource.DataSourceWithConfigValidators).ConfigValidators(context.Background())
	if len(vs) != 1 {
		t.Fatalf("want one config validator, got %d", len(vs))
	}
}
