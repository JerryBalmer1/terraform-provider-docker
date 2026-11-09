package provider

import (
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
	"github.com/moby/moby/api/types/volume"
)

func TestImageImportActionFile(t *testing.T) {
	f := swarmNewFakeEngine(t)
	src := filepath.Join(t.TempDir(), "rootfs.tar")
	if err := os.WriteFile(src, []byte("ROOTFS"), 0o600); err != nil {
		t.Fatal(err)
	}
	var q map[string][]string
	var body string
	f.handle("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"status":"sha256:abc123"}` + "\n"))
	})
	res := actInvoke(t, f, NewImageImportAction(), map[string]tftypes.Value{
		"source":     actTfStr(src),
		"repository": actTfStr("example/rootfs"),
		"tag":        actTfStr("v1"),
		"changes":    actTfList(`CMD ["/bin/sh"]`, "ENV A=b"),
		"message":    actTfStr("hi"),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if q["fromSrc"][0] != "-" || q["repo"][0] != "example/rootfs" || q["tag"][0] != "v1" || len(q["changes"]) != 2 || q["message"][0] != "hi" {
		t.Errorf("query: %v", q)
	}
	if body != "ROOTFS" {
		t.Errorf("body %q", body)
	}
	if !actHasProgress(res, "example/rootfs:v1 (sha256:abc123)") {
		t.Errorf("progress: %q", res.Progress)
	}
}

func TestImageImportActionURL(t *testing.T) {
	f := swarmNewFakeEngine(t)
	var from string
	f.handle("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		from = r.URL.Query().Get("fromSrc")
		_, _ = w.Write([]byte(`{"status":"Downloading"}` + "\n" + `{"errorDetail":{"message":"404 not found"},"error":"404 not found"}` + "\n"))
	})
	res := actInvoke(t, f, NewImageImportAction(), map[string]tftypes.Value{"source": actTfStr("https://example.com/rootfs.tar.gz")})
	if from != "https://example.com/rootfs.tar.gz" {
		t.Errorf("fromSrc %q", from)
	}
	if !strings.Contains(res.errText(), "404 not found") {
		t.Fatalf("stream error not reported: %q", res.errText())
	}
}

func TestImageImportActionValidate(t *testing.T) {
	d := actValidate(t, NewImageImportAction(), map[string]tftypes.Value{"source": actTfStr("x.tar"), "tag": actTfStr("v1")})
	if d.ErrorsCount() != 1 {
		t.Fatalf("tag without repository: %v", d)
	}
	d = actValidate(t, NewImageImportAction(), map[string]tftypes.Value{"source": actTfStr("x.tar"), "repository": actTfStr("Bad Name")})
	if d.ErrorsCount() != 1 {
		t.Fatalf("bad repository: %v", d)
	}
}

func TestImageLoadAction(t *testing.T) {
	f := swarmNewFakeEngine(t)
	src := filepath.Join(t.TempDir(), "imgs.tar")
	if err := os.WriteFile(src, []byte("IMAGES"), 0o600); err != nil {
		t.Fatal(err)
	}
	var quiet, body string
	f.handle("POST /images/load", func(w http.ResponseWriter, r *http.Request) {
		quiet = r.URL.Query().Get("quiet")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stream":"Loaded image: busybox:1.36\n"}` + "\n"))
	})
	res := actInvoke(t, f, NewImageLoadAction(), map[string]tftypes.Value{"input": actTfStr(src)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if quiet != "1" || body != "IMAGES" {
		t.Errorf("quiet=%q body=%q", quiet, body)
	}
	if !actHasProgress(res, "Loaded image: busybox:1.36") {
		t.Errorf("progress: %q", res.Progress)
	}
}

func TestImageLoadActionMissingFile(t *testing.T) {
	f := swarmNewFakeEngine(t)
	res := actInvoke(t, f, NewImageLoadAction(), map[string]tftypes.Value{"input": actTfStr(filepath.Join(t.TempDir(), "nope.tar"))})
	if !strings.Contains(res.errText(), "cannot open archive") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestImageSaveAction(t *testing.T) {
	f := swarmNewFakeEngine(t)
	var names []string
	f.handle("GET /images/get", func(w http.ResponseWriter, r *http.Request) {
		names = r.URL.Query()["names"]
		_, _ = w.Write([]byte("SAVED"))
	})
	out := filepath.Join(t.TempDir(), "out.tar")
	if err := os.WriteFile(out, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := actInvoke(t, f, NewImageSaveAction(), map[string]tftypes.Value{"images": actTfList("a:1", "b:2"), "output": actTfStr(out)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if strings.Join(names, ",") != "a:1,b:2" {
		t.Errorf("names %v", names)
	}
	if b, _ := os.ReadFile(out); string(b) != "SAVED" {
		t.Errorf("file %q", b)
	}
	d := actValidate(t, NewImageSaveAction(), map[string]tftypes.Value{"images": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{}), "output": actTfStr(out)})
	if d.ErrorsCount() != 1 {
		t.Errorf("empty images: %v", d)
	}
}

// actFakePrune wires every prune endpoint and records each query's filters.
func actFakePrune(t *testing.T, f *swarmFakeEngine) map[string]string {
	t.Helper()
	got := map[string]string{}
	rec := func(k string, r *http.Request) {
		got[k] = r.URL.Query().Get("filters") + "|all=" + r.URL.Query().Get("all")
	}
	f.handle("POST /containers/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("containers", r)
		swarmFakeJSON(w, container.PruneReport{ContainersDeleted: []string{"c1", "c2"}, SpaceReclaimed: 1000})
	})
	f.handle("POST /networks/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("networks", r)
		swarmFakeJSON(w, network.PruneReport{NetworksDeleted: []string{"n1"}})
	})
	f.handle("POST /volumes/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("volumes", r)
		swarmFakeJSON(w, volume.PruneReport{VolumesDeleted: []string{"v1"}, SpaceReclaimed: 2000})
	})
	f.handle("POST /images/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("images", r)
		swarmFakeJSON(w, image.PruneReport{ImagesDeleted: []image.DeleteResponse{{Deleted: "sha256:x"}}, SpaceReclaimed: 3000})
	})
	f.handle("POST /build/prune", func(w http.ResponseWriter, r *http.Request) {
		rec("build", r)
		swarmFakeJSON(w, build.CachePruneReport{CachesDeleted: []string{"b1"}, SpaceReclaimed: 4000})
	})
	return got
}

func TestSystemPruneActionDefaults(t *testing.T) {
	f := swarmNewFakeEngine(t)
	got := actFakePrune(t, f)
	res := actInvoke(t, f, NewSystemPruneAction(), map[string]tftypes.Value{"filters": actTfMap(map[string]string{"until": "24h"})})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if _, ok := got["volumes"]; ok {
		t.Error("volumes pruned without volumes = true")
	}
	for _, k := range []string{"containers", "networks", "images", "build"} {
		if !strings.Contains(got[k], `"until"`) {
			t.Errorf("%s filters %q lack until", k, got[k])
		}
	}
	if strings.Contains(got["images"], "dangling") {
		t.Errorf("images default must stay dangling-only: %q", got["images"])
	}
	// 1000 + 3000 + 4000 bytes.
	if !actHasProgress(res, "reclaimed 8kB") || !actHasProgress(res, "2 containers, 1 networks, 1 images, 1 build cache entries") {
		t.Errorf("summary: %q", res.Progress)
	}
}

func TestSystemPruneActionSelective(t *testing.T) {
	f := swarmNewFakeEngine(t)
	got := actFakePrune(t, f)
	res := actInvoke(t, f, NewSystemPruneAction(), map[string]tftypes.Value{
		"containers":  actTfBool(false),
		"networks":    actTfBool(false),
		"volumes":     actTfBool(true),
		"all":         actTfBool(true),
		"build_cache": actTfBool(false),
		"filters":     actTfMap(map[string]string{"label": "env=test", "until": "1h"}),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if _, ok := got["containers"]; ok {
		t.Error("containers pruned")
	}
	if _, ok := got["build"]; ok {
		t.Error("build cache pruned")
	}
	if strings.Contains(got["volumes"], "until") || !strings.Contains(got["volumes"], "env=test") {
		t.Errorf("volume filters: %q", got["volumes"])
	}
	if !strings.Contains(got["images"], `"dangling":{"false":true}`) {
		t.Errorf("all_images should send dangling=false: %q", got["images"])
	}
	if !actHasProgress(res, "reclaimed 5kB") {
		t.Errorf("summary: %q", res.Progress)
	}
}

func TestSystemPruneActionValidate(t *testing.T) {
	d := actValidate(t, NewSystemPruneAction(), map[string]tftypes.Value{"filters": actTfMap(map[string]string{"label": "a"})})
	if d.ErrorsCount() != 1 {
		t.Errorf("label filter with build cache: %v", d)
	}
	d = actValidate(t, NewSystemPruneAction(), map[string]tftypes.Value{"filters": actTfMap(map[string]string{"dangling": "true"})})
	if d.ErrorsCount() != 1 {
		t.Errorf("unknown filter key: %v", d)
	}
	d = actValidate(t, NewSystemPruneAction(), map[string]tftypes.Value{"build_cache": actTfBool(false), "filters": actTfMap(map[string]string{"label": "a"})})
	if d.HasError() {
		t.Errorf("label filter without build cache: %v", d)
	}
}

func TestSystemPruneActionError(t *testing.T) {
	f := swarmNewFakeEngine(t)
	actFakePrune(t, f)
	f.handle("POST /networks/prune", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusConflict, "a prune operation is already running")
	})
	res := actInvoke(t, f, NewSystemPruneAction(), nil)
	if !strings.Contains(res.errText(), "pruning networks: Error response from daemon: a prune operation is already running (so far: removed 2 containers") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestImagePruneAction(t *testing.T) {
	f := swarmNewFakeEngine(t)
	got := actFakePrune(t, f)
	res := actInvoke(t, f, NewImagePruneAction(), nil)
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if strings.Contains(got["images"], "dangling") {
		t.Errorf("default must be dangling-only: %q", got["images"])
	}
	if !actHasProgress(res, "removed 1 images; reclaimed 3kB") {
		t.Errorf("progress %q", res.Progress)
	}
	res = actInvoke(t, f, NewImagePruneAction(), map[string]tftypes.Value{"dangling": actTfBool(false), "filters": actTfMap(map[string]string{"label!": "keep"})})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if !strings.Contains(got["images"], `"dangling":{"false":true}`) || !strings.Contains(got["images"], `"label!":{"keep":true}`) {
		t.Errorf("filters %q", got["images"])
	}
}
