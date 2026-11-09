package provider

import (
	"context"
	"encoding/binary"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// composeCFake is a p3eFake answering for project "shop": web (1 running
// container), worker (1 exited) and a one-off run container.
func composeCFake(t *testing.T) (*p3eFake, *providerData) {
	t.Helper()
	f, pd := newP3eFake(t)
	f.on("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, 200, map[string]any{"Version": "28.5.0", "ApiVersion": "1.47"})
	})
	lbl := func(svc, n, oneoff string) map[string]any {
		return map[string]any{composeLabelProject: "shop", composeLabelService: svc, composeLabelNumber: n, composeLabelOneoff: oneoff,
			composeLabelConfigFiles: "/p/compose.yml"}
	}
	f.on("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		all := []map[string]any{
			{"Id": "c1", "Names": []string{"/shop-web-1"}, "Image": "nginx:1.27", "ImageID": "sha256:img1", "State": "running", "Status": "Up 1 minute",
				"Labels": lbl("web", "1", "False"), "Ports": []map[string]any{{"IP": "0.0.0.0", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"}}},
			{"Id": "c2", "Names": []string{"/shop-worker-1"}, "Image": "busybox:1.36", "ImageID": "sha256:img2", "State": "exited", "Status": "Exited (0) 1 minute ago",
				"Labels": lbl("worker", "1", "False")},
			{"Id": "c3", "Names": []string{"/shop-worker-run-abc"}, "Image": "busybox:1.36", "ImageID": "sha256:img2", "State": "exited",
				"Labels": lbl("worker", "", "True")},
		}
		var out []map[string]any
		for _, c := range all {
			if r.URL.Query().Get("all") != "1" && c["State"] != "running" {
				continue
			}
			out = append(out, c)
		}
		p3eWriteJSON(w, 200, out)
	})
	f.json("GET /containers/c1/json", map[string]any{"Id": "c1", "Name": "/shop-web-1", "Config": map[string]any{"Tty": false},
		"State":           map[string]any{"Status": "running", "Running": true},
		"NetworkSettings": map[string]any{"Ports": map[string]any{"80/tcp": []map[string]any{{"HostIp": "0.0.0.0", "HostPort": "8080"}}}}})
	f.json("GET /containers/c2/json", map[string]any{"Id": "c2", "Name": "/shop-worker-1", "Config": map[string]any{"Tty": true},
		"State": map[string]any{"Status": "exited"}})
	f.on("GET /containers/c1/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(composeCFrame(1, "2026-01-01T00:00:02.000000000Z GET / 200\n"))
		_, _ = w.Write(composeCFrame(2, "2026-01-01T00:00:03.000000000Z warn: slow\n"))
	})
	f.on("GET /containers/c2/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2026-01-01T00:00:01.000000000Z worker done\n"))
	})
	f.json("GET /containers/c1/top", map[string]any{"Titles": []string{"PID", "CMD"}, "Processes": [][]string{{"1", "nginx"}}})
	f.json("GET /images/sha256:img1/json", map[string]any{"Id": "sha256:img1", "RepoTags": []string{"nginx:1.27"},
		"RepoDigests": []string{"nginx@sha256:" + strings.Repeat("a", 64)}, "Os": "linux", "Architecture": "amd64", "Size": 1000, "Created": "2026-01-01T00:00:00Z"})
	f.json("GET /images/sha256:img2/json", map[string]any{"Id": "sha256:img2", "RepoTags": []string{"busybox:1.36"}, "Os": "linux", "Architecture": "amd64"})
	f.json("GET /volumes", map[string]any{"Volumes": []map[string]any{
		{"Name": "shop_data", "Driver": "local", "Mountpoint": "/var/lib/docker/volumes/shop_data/_data", "Scope": "local", "Labels": map[string]string{composeLabelProject: "shop"}},
	}})
	return f, pd
}

// composeCFrame is one multiplexed log frame.
func composeCFrame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

// composeCRead reads d and fails on errors and on any contract warning.
func composeCRead(t *testing.T, d datasource.DataSource, pd *providerData, cfg map[string]tftypes.Value) tfsdk.State {
	t.Helper()
	st, diags := p3eReadDS(t, d, pd, cfg)
	composeCNoDiags(t, diags)
	return st
}

func composeCNoDiags(t *testing.T, diags diag.Diagnostics) {
	t.Helper()
	for _, d := range diags {
		t.Errorf("%s: %s: %s", d.Severity(), d.Summary(), d.Detail())
	}
}

func composeCAttr(t *testing.T, st tfsdk.State, p path.Path) attr.Value {
	t.Helper()
	var v attr.Value
	if d := st.GetAttribute(context.Background(), p, &v); d.HasError() {
		t.Fatalf("%s: %v", p, d)
	}
	return v
}

func composeCStrings(v ...string) tftypes.Value {
	var vals []tftypes.Value
	for _, s := range v {
		vals = append(vals, p3eStr(s))
	}
	return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, vals)
}

func TestComposeContainersDataSourceC(t *testing.T) {
	_, pd := composeCFake(t)
	st := composeCRead(t, NewComposeContainersDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop"), "all": tftypes.NewValue(tftypes.Bool, true)})
	if got := composeCAttr(t, st, path.Root("container_ids")).String(); !strings.Contains(got, `"web":["c1"]`) || !strings.Contains(got, `"worker":["c2"]`) || strings.Contains(got, "c3") {
		t.Errorf("container_ids = %s", got)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("containers").AtListIndex(0).AtName("service"))); got != "web" {
		t.Errorf("containers[0].service = %q", got)
	}
	if got := composeCAttr(t, st, path.Root("containers").AtListIndex(1).AtName("number")).String(); got != "1" {
		t.Errorf("containers[1].number = %s", got)
	}
	if p3eAttrString(composeCAttr(t, st, path.Root("engine_api_version"))) != "1.47" || p3eAttrString(composeCAttr(t, st, path.Root("docker_server_version"))) != "28.5.0" {
		t.Error("versions not filled")
	}
	// Running only.
	st = composeCRead(t, NewComposeContainersDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop"), "services": composeCStrings("web", "worker")})
	if got := composeCAttr(t, st, path.Root("container_ids")).String(); strings.Contains(got, "c2") {
		t.Errorf("all=false listed stopped containers: %s", got)
	}
}

func TestComposeProjectsDataSourceC(t *testing.T) {
	_, pd := composeCFake(t)
	st := composeCRead(t, NewComposeProjectsDataSource(), pd, nil)
	if got := composeCAttr(t, st, path.Root("names")).String(); got != `["shop"]` {
		t.Errorf("names = %s", got)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("projects").AtListIndex(0).AtName("status"))); got != "exited(2), running(1)" {
		t.Errorf("status = %q", got)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("projects").AtListIndex(0).AtName("config_files"))); got != "/p/compose.yml" {
		t.Errorf("config_files = %q", got)
	}
	if composeProjectStatus(map[string]int{}) != "" {
		t.Error("empty status")
	}
}

func TestComposeVolumesImagesDataSourcesC(t *testing.T) {
	_, pd := composeCFake(t)
	st := composeCRead(t, NewComposeVolumesDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop")})
	if got := composeCAttr(t, st, path.Root("names")).String(); got != `["shop_data"]` {
		t.Errorf("volume names = %s", got)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("volumes").AtListIndex(0).AtName("driver"))); got != "local" {
		t.Errorf("volumes[0].driver = %q", got)
	}
	st = composeCRead(t, NewComposeImagesDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop")})
	if got := composeCAttr(t, st, path.Root("image_digest_refs")).String(); !strings.Contains(got, `"web":"nginx@sha256:aaaa`) {
		t.Errorf("image_digest_refs = %s", got)
	}
	if got := composeCAttr(t, st, path.Root("image_refs")).String(); !strings.Contains(got, `"worker":"busybox:1.36"`) {
		t.Errorf("image_refs = %s", got)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("images").AtListIndex(0).AtName("container_name"))); got != "shop-web-1" {
		t.Errorf("images[0].container_name = %q", got)
	}
}

func TestComposeLogsPortTopDataSourcesC(t *testing.T) {
	_, pd := composeCFake(t)
	st := composeCRead(t, NewComposeLogsDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop"), "tail": tftypes.NewValue(tftypes.Number, 10)})
	logs := p3eAttrString(composeCAttr(t, st, path.Root("logs")))
	want := "2026-01-01T00:00:01.000000000Z combined worker.shop-worker-1 worker done\n" +
		"2026-01-01T00:00:02.000000000Z stdout web.shop-web-1 GET / 200\n" +
		"2026-01-01T00:00:03.000000000Z stderr web.shop-web-1 warn: slow"
	if logs != want {
		t.Errorf("logs =\n%s\nwant\n%s", logs, want)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("log_entries").AtListIndex(1).AtName("source"))); got != "web.shop-web-1" {
		t.Errorf("source = %q", got)
	}
	if p3eAttrString(composeCAttr(t, st, path.Root("content_sha256"))) != composeSHA256Hex([]byte(want)) {
		t.Error("content_sha256")
	}

	st = composeCRead(t, NewComposePortDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop"), "service": p3eStr("web"), "private_port": tftypes.NewValue(tftypes.Number, 80)})
	if got := composeCAttr(t, st, path.Root("published_port")).String(); got != "8080" {
		t.Errorf("published_port = %s", got)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("bindings").AtListIndex(0).AtName("host_port"))); got != "8080" {
		t.Errorf("bindings[0].host_port = %q", got)
	}
	if _, diags := p3eReadDS(t, NewComposePortDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop"), "service": p3eStr("web"),
		"private_port": tftypes.NewValue(tftypes.Number, 80), "index": tftypes.NewValue(tftypes.Number, 2)}); !diags.HasError() {
		t.Error("index 2: no error")
	}

	st = composeCRead(t, NewComposeTopDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop")})
	if got := p3eAttrString(composeCAttr(t, st, path.Root("processes").AtMapKey("shop-web-1").AtName("service"))); got != "web" {
		t.Errorf("processes service = %q", got)
	}
	if got := composeCAttr(t, st, path.Root("processes").AtMapKey("shop-web-1").AtName("processes")).String(); got != `[["1","nginx"]]` {
		t.Errorf("processes = %s", got)
	}
}

func TestComposeConfigDataSourceC(t *testing.T) {
	st := composeCRead(t, NewComposeConfigDataSource(), nil, map[string]tftypes.Value{
		"name":              p3eStr("cfg"),
		"project_directory": p3eStr(t.TempDir()),
		"env":               p3eStrMap(map[string]string{"TAG": "1.27"}),
		"content":           p3eStr("services:\n  web: {image: \"nginx:${TAG}\"}\n  app: {build: .}\n"),
	})
	if got := p3eAttrString(composeCAttr(t, st, path.Root("project").AtName("services").AtMapKey("web").AtName("image"))); got != "nginx:1.27" {
		t.Errorf("project.services.web.image = %q", got)
	}
	if got := composeCAttr(t, st, path.Root("image_refs")).String(); !strings.Contains(got, `"app":"cfg-app"`) {
		t.Errorf("image_refs = %s", got)
	}
	if got := composeCAttr(t, st, path.Root("service_names")).String(); got != `["app","web"]` {
		t.Errorf("service_names = %s", got)
	}
	y := p3eAttrString(composeCAttr(t, st, path.Root("compose_yaml")))
	if !strings.Contains(y, "nginx:1.27") || p3eAttrString(composeCAttr(t, st, path.Root("content_sha256"))) != composeSHA256Hex([]byte(y)) {
		t.Errorf("compose_yaml / hash: %s", y)
	}
	// Errors: invalid YAML.
	if _, diags := p3eReadDS(t, NewComposeConfigDataSource(), nil, map[string]tftypes.Value{"content": p3eStr("services: [")}); !diags.HasError() {
		t.Error("invalid YAML: no error")
	}
}

func TestStackDataSourcesC(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]any{"Version": "28.5.0", "ApiVersion": "1.47"})
	rep := uint64(2)
	f.json("GET /services", []map[string]any{
		{"ID": "s1", "Spec": map[string]any{"Name": "shop_web", "Labels": map[string]string{labelStackNamespace: "shop"},
			"TaskTemplate": map[string]any{"ContainerSpec": map[string]any{"Image": "nginx:1.27@sha256:" + strings.Repeat("b", 64)}},
			"Mode":         map[string]any{"Replicated": map[string]any{"Replicas": rep}}},
			"Endpoint": map[string]any{"Ports": []map[string]any{{"Protocol": "tcp", "TargetPort": 80, "PublishedPort": 8080}}}},
		{"ID": "s2", "Spec": map[string]any{"Name": "ops_agent", "Labels": map[string]string{labelStackNamespace: "ops"},
			"TaskTemplate": map[string]any{"ContainerSpec": map[string]any{"Image": "busybox"}}, "Mode": map[string]any{"Global": map[string]any{}}}},
	})
	f.json("GET /tasks", []map[string]any{
		{"ID": "t1", "ServiceID": "s1", "NodeID": "n1", "DesiredState": "running", "Status": map[string]any{"State": "running"}},
		{"ID": "t2", "ServiceID": "s1", "NodeID": "n2", "DesiredState": "running", "Status": map[string]any{"State": "starting"}},
		{"ID": "t3", "ServiceID": "s2", "NodeID": "n1", "DesiredState": "running", "Status": map[string]any{"State": "running"}},
	})
	st := composeCRead(t, NewStacksDataSource(), pd, nil)
	if got := composeCAttr(t, st, path.Root("names")).String(); got != `["ops","shop"]` {
		t.Errorf("names = %s", got)
	}
	if got := composeCAttr(t, st, path.Root("stacks").AtListIndex(1).AtName("services_count")).String(); got != "1" {
		t.Errorf("services_count = %s", got)
	}
	if got := p3eAttrString(composeCAttr(t, st, path.Root("stacks").AtListIndex(0).AtName("orchestrator"))); got != "Swarm" {
		t.Errorf("orchestrator = %q", got)
	}
	st = composeCRead(t, NewStackDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("shop")})
	if got := composeCAttr(t, st, path.Root("service_ids")).String(); !strings.Contains(got, `"web":"s1"`) {
		t.Errorf("service_ids = %s", got)
	}
	// The fake ignores the label filter, so both stacks' services come back; pick web.
	if got := p3eAttrString(composeCAttr(t, st, path.Root("services").AtListIndex(1).AtName("replicas"))); got != "1/2" {
		t.Errorf("replicas = %q", got)
	}
	if got := composeCAttr(t, st, path.Root("services").AtListIndex(1).AtName("ports")).String(); got != `["*:8080->80/tcp"]` {
		t.Errorf("ports = %s", got)
	}
	if got := composeCAttr(t, st, path.Root("images")).String(); !strings.Contains(got, `"web":"nginx:1.27"`) {
		t.Errorf("images = %s", got)
	}
}
