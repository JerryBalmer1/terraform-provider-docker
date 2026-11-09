package provider

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/plugin"
	"github.com/moby/moby/api/types/volume"
)

// objFakePrune wires every prune endpoint on an objFake and records each
// request's query.
func objFakePrune(t *testing.T) (*p3eFake, *providerData, map[string]string) {
	t.Helper()
	f, pd := objFake(t)
	got := map[string]string{}
	rec := func(k string, r *http.Request) { got[k] = r.URL.RawQuery }
	f.on("POST /containers/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("containers", r)
		p3eWriteJSON(w, 200, container.PruneReport{ContainersDeleted: []string{"c1", "c2"}, SpaceReclaimed: 1000})
	})
	f.on("POST /networks/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("networks", r)
		p3eWriteJSON(w, 200, network.PruneReport{NetworksDeleted: []string{"n1"}})
	})
	f.on("POST /volumes/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("volumes", r)
		p3eWriteJSON(w, 200, volume.PruneReport{VolumesDeleted: []string{"v1"}, SpaceReclaimed: 2000})
	})
	f.on("POST /images/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("images", r)
		p3eWriteJSON(w, 200, image.PruneReport{ImagesDeleted: []image.DeleteResponse{{Deleted: "sha256:x"}, {Untagged: "a:1"}}, SpaceReclaimed: 3000})
	})
	f.on("POST /build/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("build", r)
		p3eWriteJSON(w, 200, build.CachePruneReport{CachesDeleted: []string{"b1"}, SpaceReclaimed: 4000})
	})
	return f, pd, got
}

// objReadResult reads a result_path file.
func objReadResult(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return out
}

func TestObjSystemPruneResult(t *testing.T) {
	_, pd, got := objFakePrune(t)
	rp := filepath.Join(t.TempDir(), "out", "prune.json")
	res := actInvokePD(t, pd, NewSystemPruneAction(), map[string]tftypes.Value{
		"all": actTfBool(true), "volumes": actTfBool(true), "result_path": actTfStr(rp),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if !strings.Contains(got["images"], "dangling") || !strings.Contains(got["build"], "all=1") {
		t.Errorf("all = true must prune every unused image and all cache: %v", got)
	}
	out := objReadResult(t, rp)
	r, _ := out["result"].(map[string]any)
	if r == nil || r["space_reclaimed"] != float64(10000) || len(r["images_deleted"].([]any)) != 2 || len(r["volumes_deleted"].([]any)) != 1 {
		t.Errorf("result %v", out)
	}
	if ids, _ := out["containers_deleted"].([]any); len(ids) != 2 || out["docker_server_version"] != "29.0.0" {
		t.Errorf("identity/version %v", out)
	}
	if !actHasProgress(res, "docker_system_prune result:") {
		t.Errorf("no result progress event: %v", res.Progress)
	}
}

func TestObjSystemPruneDefaults(t *testing.T) {
	_, pd, got := objFakePrune(t)
	res := actInvokePD(t, pd, NewSystemPruneAction(), nil)
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if _, ok := got["volumes"]; ok || strings.Contains(got["images"], "dangling") {
		t.Errorf("defaults: %v", got)
	}
	if !actHasProgress(res, "removed 2 containers, 1 networks, 2 images, 1 build cache entries") {
		t.Errorf("progress %v", res.Progress)
	}
}

func TestObjImagePruneResult(t *testing.T) {
	_, pd, _ := objFakePrune(t)
	rp := filepath.Join(t.TempDir(), "img.json")
	res := actInvokePD(t, pd, NewImagePruneAction(), map[string]tftypes.Value{"result_path": actTfStr(rp)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	out := objReadResult(t, rp)
	if ids, _ := out["images_deleted"].([]any); len(ids) != 1 || ids[0] != "sha256:x" {
		t.Errorf("images_deleted %v", out)
	}
}

func TestObjObjectPruneActions(t *testing.T) {
	_, pd, got := objFakePrune(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		name, key, deleted string
		vals               map[string]tftypes.Value
		n                  int
	}{
		{"container", "containers", "containers_deleted", map[string]tftypes.Value{"filters": actTfMap(map[string]string{"until": "1h"})}, 2},
		{"network", "networks", "networks_deleted", map[string]tftypes.Value{"filters": actTfMap(map[string]string{"label": "a=b"})}, 1},
		{"volume", "volumes", "volumes_deleted", map[string]tftypes.Value{"all": actTfBool(true)}, 1},
	} {
		rp := filepath.Join(dir, tc.name+".json")
		tc.vals["result_path"] = actTfStr(rp)
		var res actResult
		switch tc.name {
		case "container":
			res = actInvokePD(t, pd, NewContainerPruneAction(), tc.vals)
		case "network":
			res = actInvokePD(t, pd, NewNetworkPruneAction(), tc.vals)
		case "volume":
			res = actInvokePD(t, pd, NewVolumePruneAction(), tc.vals)
		}
		if res.Diags.HasError() {
			t.Fatalf("%s: %s", tc.name, res.errText())
		}
		if _, ok := got[tc.key]; !ok {
			t.Errorf("%s: endpoint not called", tc.name)
		}
		out := objReadResult(t, rp)
		if ids, _ := out[tc.deleted].([]any); len(ids) != tc.n {
			t.Errorf("%s: %v", tc.name, out)
		}
	}
	if !strings.Contains(got["containers"], "until") || !strings.Contains(got["networks"], "label") || !strings.Contains(got["volumes"], "all") {
		t.Errorf("queries %v", got)
	}
}

func TestObjObjectPruneValidate(t *testing.T) {
	d := actValidate(t, NewVolumePruneAction(), map[string]tftypes.Value{"filters": actTfMap(map[string]string{"until": "1h"})})
	if !d.HasError() {
		t.Error("volume prune must reject until")
	}
	if d := actValidate(t, NewContainerPruneAction(), map[string]tftypes.Value{"filters": actTfMap(map[string]string{"until": "1h"})}); d.HasError() {
		t.Errorf("container prune until: %v", d)
	}
}

func TestObjObjectPruneError(t *testing.T) {
	f, pd, _ := objFakePrune(t)
	f.on("POST /networks/prune", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 409, map[string]string{"message": "a prune operation is already running"})
	})
	res := actInvokePD(t, pd, NewNetworkPruneAction(), nil)
	if !strings.Contains(res.errText(), "already running") {
		t.Errorf("got %q", res.errText())
	}
}

// objFakeBuildxPrune records the seam call.
type objFakeBuildxPrune struct {
	req seamBuildxPruneRequest
	err error
}

func (f *objFakeBuildxPrune) Prune(_ context.Context, _ *providerData, req seamBuildxPruneRequest) (seamBuildxPruneResult, error) {
	f.req = req
	return seamBuildxPruneResult{CachesDeleted: []string{"x", "y"}, SpaceReclaimed: 77}, f.err
}

func TestObjBuilderPrune(t *testing.T) {
	_, pd, got := objFakePrune(t)
	rp := filepath.Join(t.TempDir(), "b.json")
	res := actInvokePD(t, pd, NewBuilderPruneAction(), map[string]tftypes.Value{
		"all": actTfBool(true), "filters": actTfMap(map[string]string{"until": "24h"}), "result_path": actTfStr(rp),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if !strings.Contains(got["build"], "all=1") || !strings.Contains(got["build"], "until") {
		t.Errorf("engine query %q", got["build"])
	}
	out := objReadResult(t, rp)
	if r, _ := out["result"].(map[string]any); r == nil || r["space_reclaimed"] != float64(4000) {
		t.Errorf("result %v", out)
	}

	// keep_storage needs a builder.
	res = actInvokePD(t, pd, NewBuilderPruneAction(), map[string]tftypes.Value{"keep_storage": actTfNum(5)})
	if !res.Diags.HasError() {
		t.Error("keep_storage without builder must fail")
	}

	// With builder: the default seam is not implemented -> a clear error.
	res = actInvokePD(t, pd, NewBuilderPruneAction(), map[string]tftypes.Value{"builder": actTfStr("bk")})
	if seamBuildxPruneImpl == (seamBuildxPrune)(seamBuildxPruneStub{}) && !strings.Contains(res.errText(), "not implemented") {
		t.Errorf("stub seam: %q", res.errText())
	}

	// With a seam implementation.
	old := seamBuildxPruneImpl
	fake := &objFakeBuildxPrune{}
	seamBuildxPruneImpl = fake
	t.Cleanup(func() { seamBuildxPruneImpl = old })
	delete(got, "build")
	res = actInvokePD(t, pd, NewBuilderPruneAction(), map[string]tftypes.Value{
		"builder": actTfStr("bk"), "keep_storage": actTfNum(5), "filters": actTfMap(map[string]string{"type": "regular"}), "result_path": actTfStr(rp),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if fake.req.Builder != "bk" || fake.req.KeepStorage != 5 || fake.req.Filters["type"][0] != "regular" {
		t.Errorf("seam request %+v", fake.req)
	}
	if _, ok := got["build"]; ok {
		t.Error("engine endpoint called with builder set")
	}
	if ids, _ := objReadResult(t, rp)["caches_deleted"].([]any); len(ids) != 2 {
		t.Errorf("caches_deleted")
	}
	fake.err = errors.New("builder bk not found")
	res = actInvokePD(t, pd, NewBuilderPruneAction(), map[string]tftypes.Value{"builder": actTfStr("bk")})
	if !strings.Contains(res.errText(), "builder bk not found") {
		t.Errorf("seam error %q", res.errText())
	}
}

func TestObjTarDir(t *testing.T) {
	dir := t.TempDir()
	if err := objCheckPluginDir(dir); err == nil {
		t.Error("empty dir accepted")
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"Description":"x"}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "rootfs", "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "rootfs", "bin", "p"), []byte("bin"), 0o755)
	if err := objCheckPluginDir(dir); err != nil {
		t.Fatal(err)
	}
	buf, err := objTarDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	if j := strings.Join(names, ","); j != "config.json,rootfs/,rootfs/bin/,rootfs/bin/p" {
		t.Errorf("tar entries %s", j)
	}
}

func TestObjPluginCreateAction(t *testing.T) {
	f, pd := objFake(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "rootfs"), 0o755)
	var gotName string
	var gotTar int
	f.on("POST /plugins/create", func(w http.ResponseWriter, r *http.Request) {
		gotName = r.URL.Query().Get("name")
		b, _ := io.ReadAll(r.Body)
		gotTar = len(b)
		w.WriteHeader(204)
	})
	enabled := false
	f.on("GET /plugins/my/plugin:1/json", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 200, plugin.Plugin{ID: "pid9", Name: "my/plugin:1", Enabled: enabled})
	})
	f.on("POST /plugins/pid9/enable", func(w http.ResponseWriter, _ *http.Request) { enabled = true; w.WriteHeader(200) })
	rp := filepath.Join(t.TempDir(), "c.json")
	res := actInvokePD(t, pd, NewPluginCreateAction(), map[string]tftypes.Value{
		"name": actTfStr("my/plugin:1"), "context_dir": actTfStr(dir), "enable": actTfBool(true), "result_path": actTfStr(rp),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if gotName != "my/plugin:1" || gotTar == 0 || !enabled {
		t.Errorf("name=%q tar=%d enabled=%v", gotName, gotTar, enabled)
	}
	out := objReadResult(t, rp)
	if out["id"] != "pid9" || out["result"].(map[string]any)["enabled"] != true {
		t.Errorf("result %v", out)
	}
	res = actInvokePD(t, pd, NewPluginCreateAction(), map[string]tftypes.Value{"name": actTfStr("x"), "context_dir": actTfStr(t.TempDir())})
	if !strings.Contains(res.errText(), "config.json") {
		t.Errorf("bad dir: %q", res.errText())
	}
}

func TestObjPluginPushAction(t *testing.T) {
	f, pd := objFake(t)
	dg := "sha256:" + strings.Repeat("ab", 32)
	f.on("POST /plugins/reg.local/p:1/push", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"status":"The push refers to repository [reg.local/p]"}`+"\n"+`{"status":"1: digest: `+dg+` size: 1234"}`+"\n")
	})
	rp := filepath.Join(t.TempDir(), "p.json")
	res := actInvokePD(t, pd, NewPluginPushAction(), map[string]tftypes.Value{"name": actTfStr("reg.local/p:1"), "result_path": actTfStr(rp)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	out := objReadResult(t, rp)
	if out["digest"] != dg || !strings.Contains(out["result"].(map[string]any)["push_status"].(string), "size: 1234") {
		t.Errorf("result %v", out)
	}
	f.on("POST /plugins/reg.local/p:1/push", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"errorDetail":{"message":"denied: requested access to the resource is denied"},"error":"denied"}`+"\n")
	})
	res = actInvokePD(t, pd, NewPluginPushAction(), map[string]tftypes.Value{"name": actTfStr("reg.local/p:1")})
	if !strings.Contains(res.errText(), "requested access to the resource is denied") {
		t.Errorf("push error %q", res.errText())
	}
}

func TestObjBuilderPruneDurationFilter(t *testing.T) {
	_, pd, _ := objFakePrune(t)
	old := seamBuildxPruneImpl
	fake := &objFakeBuildxPrune{}
	seamBuildxPruneImpl = fake
	t.Cleanup(func() { seamBuildxPruneImpl = old })
	res := actInvokePD(t, pd, NewBuilderPruneAction(), map[string]tftypes.Value{
		"builder": actTfStr("bk"), "filters": actTfMap(map[string]string{"until": "yesterday"}),
	})
	if !strings.Contains(res.errText(), "Go duration") {
		t.Errorf("bad until must fail: %q", res.errText())
	}
	res = actInvokePD(t, pd, NewBuilderPruneAction(), map[string]tftypes.Value{
		"builder": actTfStr("bk"), "filters": actTfMap(map[string]string{"unused-for": "90m", "description": "~cache"}),
	})
	if res.Diags.HasError() || fake.req.Filters["unused-for"][0] != "90m" || fake.req.Filters["description"][0] != "~cache" {
		t.Errorf("%q %+v", res.errText(), fake.req)
	}
}
