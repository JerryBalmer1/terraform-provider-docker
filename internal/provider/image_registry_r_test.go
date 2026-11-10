package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// rActionAttrType is the Terraform type of attribute name in a's schema.
func rActionAttrType(t *testing.T, a action.Action, name string) tftypes.Type {
	t.Helper()
	var sr action.SchemaResponse
	a.Schema(context.Background(), action.SchemaRequest{}, &sr)
	return sr.Schema.Type().TerraformType(context.Background()).(tftypes.Object).AttributeTypes[name]
}

func rTfSet(ss ...string) tftypes.Value {
	vals := make([]tftypes.Value, len(ss))
	for i, s := range ss {
		vals[i] = actTfStr(s)
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, vals)
}

// TestImgActionCommitConfigR sends every ContainerConfig field of the
// commit body and checks the JSON the daemon receives.
func TestImgActionCommitConfigR(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /containers/web/json", map[string]any{"Id": "c0ffee", "Name": "/web"})
	f.json("GET /containers/c0ffee/changes", []container.FilesystemChange{})
	var q string
	var body container.Config
	f.on("POST /commit", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&body)
		p3eWriteJSON(w, http.StatusCreated, map[string]string{"Id": "sha256:new"})
	})
	a := NewImageCommitAction()
	cfgType := rActionAttrType(t, a, "config")
	hcType := cfgType.(tftypes.Object).AttributeTypes["healthcheck"]
	cfg := p3eObject(cfgType, map[string]tftypes.Value{
		"hostname": actTfStr("h"), "domainname": actTfStr("example.com"), "user": actTfStr("1000:1000"),
		"attach_stdin": actTfBool(true), "attach_stdout": actTfBool(true), "attach_stderr": actTfBool(true),
		"exposed_ports": rTfSet("80/tcp", "53/udp"), "tty": actTfBool(true), "open_stdin": actTfBool(true),
		"stdin_once": actTfBool(true), "env": actTfList("A=1"), "cmd": actTfList("serve", "--port=80"),
		"healthcheck": p3eObject(hcType, map[string]tftypes.Value{
			"test": actTfList("CMD", "true"), "interval": actTfStr("30s"), "timeout": actTfStr("5s"),
			"start_period": actTfStr("1m"), "start_interval": actTfStr("2s"), "retries": actTfNum(3),
		}),
		"args_escaped": actTfBool(true), "image": actTfStr("busybox:1.36"), "volumes": rTfSet("/data"),
		"working_dir": actTfStr("/srv"), "entrypoint": actTfList("/bin/app"), "network_disabled": actTfBool(true),
		"on_build": actTfList("RUN echo hi"), "labels": actTfMap(map[string]string{"k": "v"}), "stop_signal": actTfStr("SIGINT"),
		"stop_timeout": actTfNum(7), "shell": actTfList("/bin/sh", "-c"),
	})
	res := actInvokePD(t, pd, a, map[string]tftypes.Value{"container_id": actTfStr("web"), "reference": actTfStr("snap:2"), "config": cfg})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if !strings.Contains(q, "container=c0ffee") || !strings.Contains(q, "repo=docker.io%2Flibrary%2Fsnap&tag=2") {
		t.Errorf("query %s", q)
	}
	b := body
	if b.Hostname != "h" || b.Domainname != "example.com" || b.User != "1000:1000" || !b.AttachStdin || !b.AttachStdout ||
		!b.AttachStderr || !b.Tty || !b.OpenStdin || !b.StdinOnce || len(b.ExposedPorts) != 2 || strings.Join(b.Env, ",") != "A=1" ||
		strings.Join(b.Cmd, " ") != "serve --port=80" || !b.ArgsEscaped || b.Image != "busybox:1.36" || len(b.Volumes) != 1 ||
		b.WorkingDir != "/srv" || strings.Join(b.Entrypoint, "") != "/bin/app" || !b.NetworkDisabled ||
		strings.Join(b.OnBuild, "") != "RUN echo hi" || b.Labels["k"] != "v" || b.StopSignal != "SIGINT" ||
		b.StopTimeout == nil || *b.StopTimeout != 7 || strings.Join(b.Shell, " ") != "/bin/sh -c" {
		t.Errorf("commit body %+v", b)
	}
	if h := b.Healthcheck; h == nil || strings.Join(h.Test, " ") != "CMD true" || h.Interval.String() != "30s" ||
		h.Timeout.String() != "5s" || h.StartPeriod.String() != "1m0s" || h.StartInterval.String() != "2s" || h.Retries != 3 {
		t.Errorf("healthcheck %+v", b.Healthcheck)
	}

	// Without config no body is sent (the daemon uses the container's).
	body = container.Config{Hostname: "unset"}
	res = actInvokePD(t, pd, a, map[string]tftypes.Value{"container_id": actTfStr("web")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if body.Hostname != "unset" && body.Hostname != "" {
		t.Errorf("unexpected body %+v", body)
	}

	// Invalid values fail validation.
	bad := p3eObject(cfgType, map[string]tftypes.Value{"exposed_ports": rTfSet("http"),
		"healthcheck": p3eObject(hcType, map[string]tftypes.Value{"interval": actTfStr("soon")})})
	d := actValidate(t, a, map[string]tftypes.Value{"container_id": actTfStr("web"), "config": bad})
	if len(d.Errors()) != 2 {
		t.Errorf("want 2 validation errors, got %v", d)
	}
}

// TestImageLoadQuietR maps quiet onto the ImageLoad query (default true).
func TestImageLoadQuietR(t *testing.T) {
	f, pd := newP3eFake(t)
	var quiet []string
	f.on("POST /images/load", func(w http.ResponseWriter, r *http.Request) {
		quiet = append(quiet, r.URL.Query().Get("quiet"))
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"status":"Loading layer","progressDetail":{"current":1,"total":2},"id":"abc"}` + "\n" +
			`{"stream":"Loaded image: busybox:1.36\n"}` + "\n"))
	})
	tarPath := filepath.Join(t.TempDir(), "imgs.tar")
	if err := os.WriteFile(tarPath, []byte("TAR"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []map[string]tftypes.Value{
		{"input": actTfStr(tarPath)},
		{"input": actTfStr(tarPath), "quiet": actTfBool(false)},
	} {
		if res := actInvokePD(t, pd, NewImageLoadAction(), v); res.Diags.HasError() {
			t.Fatal(res.errText())
		}
	}
	if strings.Join(quiet, ",") != "1,0" {
		t.Errorf("quiet query values %v, want 1,0", quiet)
	}
}

// TestRegistryImagePushPlatformR sends the platform as the JSON-encoded OCI
// platform query parameter, and only when set; the attribute is gated on
// Engine API 1.46 and forces replacement.
func TestRegistryImagePushPlatformR(t *testing.T) {
	f, pd := newP3eFake(t)
	var queries []string
	f.on("POST /images/reg.example/app/push", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		_, _ = w.Write([]byte(`{"status":"1: digest: sha256:` + strings.Repeat("a", 64) + ` size: 1"}` + "\n"))
	})
	ctx := context.Background()
	for _, pl := range []string{"linux/arm64/v8", ""} {
		rc, err := pd.Client.ImagePush(ctx, "reg.example/app:1", regPushOptions("", pl))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
	if len(queries) != 2 {
		t.Fatalf("pushes %v", queries)
	}
	if !strings.Contains(queries[0], "tag=1") || !strings.Contains(queries[0], "platform=") ||
		!strings.Contains(queries[0], "%22architecture%22%3A%22arm64%22") || !strings.Contains(queries[0], "%22variant%22%3A%22v8%22") {
		t.Errorf("platform query %s", queries[0])
	}
	if strings.Contains(queries[1], "platform") {
		t.Errorf("unexpected platform: %s", queries[1])
	}
	var sr resource.SchemaResponse
	NewRegistryImageResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	desc := sr.Schema.Attributes["platform"].GetMarkdownDescription()
	if APISinceVersion(desc) != "1.46" || !strings.Contains(desc, "Forces replacement.") {
		t.Errorf("platform description %q", desc)
	}
}

// rFakeTLSRegistry serves an index (tfacc/multi:v1) and a single manifest
// with its config blob (tfacc/single:v1) over TLS with a self-signed
// certificate, like the harness's tlsregistry.
func rFakeTLSRegistry(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	index := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("1", 64) + `","size":10,"platform":{"architecture":"amd64","os":"linux"}},` +
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("2", 64) + `","size":10,"platform":{"architecture":"arm64","os":"linux","variant":"v8"}}]}`)
	cfg := []byte(`{"architecture":"riscv64","os":"linux","config":{}}`)
	cfgDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(cfg))
	single := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` +
		cfgDigest + `","size":` + fmt.Sprint(len(cfg)) + `},"layers":[]}`)
	mux := http.NewServeMux()
	serve := func(p string, mt string, b []byte) {
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", mt)
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			_, _ = w.Write(b)
		})
	}
	serve("/v2/tfacc/multi/manifests/v1", "application/vnd.oci.image.index.v1+json", index)
	serve("/v2/tfacc/single/manifests/v1", "application/vnd.oci.image.manifest.v1+json", single)
	serve("/v2/tfacc/single/blobs/"+cfgDigest, "application/octet-stream", cfg)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv, fmt.Sprintf("sha256:%x", sha256.Sum256(index)), fmt.Sprintf("sha256:%x", sha256.Sum256(single))
}

// TestRegistryImageDataInsecureSkipVerifyR: insecure_skip_verify = true
// reads a self-signed TLS registry directly; false fails with the
// certificate error (no fallback to plain http); unset goes through the
// daemon's distribution endpoint.
func TestRegistryImageDataInsecureSkipVerifyR(t *testing.T) {
	srv, indexDigest, singleDigest := rFakeTLSRegistry(t)
	host := strings.TrimPrefix(srv.URL, "https://")
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47"})
	ds := NewRegistryImageDataSource()

	st := rReadDS(t, ds, pd, map[string]tftypes.Value{"name": actTfStr(host + "/tfacc/multi:v1"), "insecure_skip_verify": actTfBool(true)})
	if got := swarmBStr(t, st, path.Root("digest")); got != indexDigest {
		t.Errorf("index digest %s, want %s", got, indexDigest)
	}
	var plats []string
	st.GetAttribute(context.Background(), path.Root("platforms"), &plats)
	if strings.Join(plats, ",") != "linux/amd64,linux/arm64/v8" {
		t.Errorf("index platforms %v", plats)
	}
	var raw string
	st.GetAttribute(context.Background(), path.Root("inspect_json"), &raw)
	if !strings.Contains(raw, `"mediaType":"application/vnd.oci.image.index.v1+json"`) || !strings.Contains(raw, indexDigest) {
		t.Errorf("inspect_json %s", raw)
	}

	st = rReadDS(t, ds, pd, map[string]tftypes.Value{"name": actTfStr(host + "/tfacc/single:v1"), "insecure_skip_verify": actTfBool(true)})
	plats = nil
	st.GetAttribute(context.Background(), path.Root("platforms"), &plats)
	if swarmBStr(t, st, path.Root("digest")) != singleDigest || strings.Join(plats, ",") != "linux/riscv64" {
		t.Errorf("single manifest: %s %v", swarmBStr(t, st, path.Root("digest")), plats)
	}
	// Only the contract's docker_server_version reads the daemon; the
	// registry itself is never asked through it.
	for _, r := range f.requests() {
		if strings.Contains(r, "/distribution/") {
			t.Errorf("direct reads must not use the daemon's distribution endpoint: %v", r)
		}
	}

	_, diags := swarmBReadDS(t, ds, pd, map[string]tftypes.Value{"name": actTfStr(host + "/tfacc/multi:v1"), "insecure_skip_verify": actTfBool(false)})
	if !diags.HasError() || !strings.Contains(diags.Errors()[0].Detail(), "certificate") {
		t.Fatalf("want a certificate error, got %v", diags)
	}

	f.json("GET /distribution/"+host+"/tfacc/multi:v1/json", map[string]any{
		"Descriptor": map[string]any{"mediaType": ocispec.MediaTypeImageIndex, "digest": "sha256:" + strings.Repeat("d", 64), "size": 1},
		"Platforms":  []ocispec.Platform{{OS: "linux", Architecture: "amd64"}},
	})
	st = rReadDS(t, ds, pd, map[string]tftypes.Value{"name": actTfStr(host + "/tfacc/multi:v1")})
	if got := swarmBStr(t, st, path.Root("digest")); got != "sha256:"+strings.Repeat("d", 64) {
		t.Errorf("daemon path digest %s", got)
	}
}

// TestImageActionsOwnerLabelR: docker_image_commit and docker_image_import
// carry the owner label when `owner` is set (commit: body Labels with
// `config`, else a LABEL change; import: a LABEL change) and nothing when
// it is unset.
func TestImageActionsOwnerLabelR(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /containers/web/json", map[string]any{"Id": "c0ffee", "Name": "/web"})
	f.json("GET /containers/c0ffee/changes", []container.FilesystemChange{})
	var changes []string
	var body container.Config
	f.on("POST /commit", func(w http.ResponseWriter, r *http.Request) {
		changes = r.URL.Query()["changes"]
		body = container.Config{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p3eWriteJSON(w, http.StatusCreated, map[string]string{"Id": "sha256:new"})
	})
	f.on("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		changes = r.URL.Query()["changes"]
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"status":"sha256:imported"}` + "\n"))
	})
	f.json("GET /images/sha256:imported/json", map[string]any{"Id": "sha256:imported"})
	src := filepath.Join(t.TempDir(), "rootfs.tar")
	if err := os.WriteFile(src, []byte("ROOTFS"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := NewImageCommitAction()
	cfgType := rActionAttrType(t, commit, "config")
	wantChange := `LABEL "` + ownerLabelKey + `"="team-a"`
	has := func(ss []string, s string) bool {
		for _, x := range ss {
			if x == s {
				return true
			}
		}
		return false
	}
	for _, owner := range []string{"team-a", ""} {
		pd.Owner = owner
		// commit without config: LABEL change, user changes kept.
		res := actInvokePD(t, pd, commit, map[string]tftypes.Value{"container_id": actTfStr("web"), "changes": actTfList("ENV A=b")})
		if res.Diags.HasError() {
			t.Fatal(res.errText())
		}
		if !has(changes, "ENV A=b") || has(changes, wantChange) != (owner != "") || (owner == "" && len(changes) != 1) {
			t.Errorf("owner %q: commit changes %v", owner, changes)
		}
		// commit with config: the body's Labels.
		res = actInvokePD(t, pd, commit, map[string]tftypes.Value{"container_id": actTfStr("web"),
			"config": p3eObject(cfgType, map[string]tftypes.Value{"labels": actTfMap(map[string]string{"k": "v"})})})
		if res.Diags.HasError() {
			t.Fatal(res.errText())
		}
		if got, ok := body.Labels[ownerLabelKey]; ok != (owner != "") || got != owner || body.Labels["k"] != "v" || len(changes) != 0 {
			t.Errorf("owner %q: commit body labels %v, changes %v", owner, body.Labels, changes)
		}
		// import: LABEL change.
		res = actInvokePD(t, pd, NewImageImportAction(), map[string]tftypes.Value{"source": actTfStr(src)})
		if res.Diags.HasError() {
			t.Fatal(res.errText())
		}
		if has(changes, wantChange) != (owner != "") || (owner == "" && len(changes) != 0) {
			t.Errorf("owner %q: import changes %v", owner, changes)
		}
	}
}
