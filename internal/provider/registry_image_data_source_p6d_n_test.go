package provider

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Unit tests (TF_ACC unset) never let data.docker_registry_image reach a
// public registry for the image config: fake-engine reads of e.g.
// nginx:1.27 get the warning path instead of a Docker Hub request.
func init() {
	if os.Getenv("TF_ACC") == "" {
		regImageDataHostFilter = func(host string) error {
			if registryImageIsLocal(host) {
				return nil
			}
			return fmt.Errorf("unit tests do not reach registry %s", host)
		}
	}
}

// p6dNConfigBlob is an image config blob shaped like nginx:1.27-alpine's.
func p6dNConfigBlob(arch string) string {
	return fmt.Sprintf(`{"architecture":%q,"os":"linux","created":"2026-01-02T03:04:05.123456789Z",
"config":{"User":"","ExposedPorts":{"80/tcp":{}},"Env":["PATH=/usr/sbin:/usr/bin","NGINX_VERSION=1.27.5"],
"Entrypoint":["/docker-entrypoint.sh"],"Cmd":["nginx","-g","daemon off;"],"WorkingDir":"/",
"Labels":{"maintainer":"NGINX Docker Maintainers","arch":%q},"StopSignal":"SIGQUIT","Volumes":{"/cache":{}}},
"rootfs":{"type":"layers","diff_ids":[]},"history":[]}`, arch, arch)
}

// p6dNServe registers body under /v2/<repo>/manifests/<digest> (and
// returns the digest) or under /v2/<repo>/blobs/<digest>.
func p6dNServe(reg *p3eFakeRegistry, repo, kind, mediaType, body string) string {
	dg := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body)))
	reg.handle("/v2/"+repo+"/"+kind+"/"+dg, func(w http.ResponseWriter, _ *http.Request) {
		if mediaType != "" {
			w.Header().Set("Content-Type", mediaType)
		}
		_, _ = w.Write([]byte(body))
	})
	return dg
}

// p6dNImage serves one platform's manifest + config blob and returns the
// manifest digest.
func p6dNImage(reg *p3eFakeRegistry, repo, arch string) string {
	cfg := p6dNConfigBlob(arch)
	cfgDigest := p6dNServe(reg, repo, "blobs", "", cfg)
	man := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`,
		p3MediaOCIManifest, cfgDigest, len(cfg))
	return p6dNServe(reg, repo, "manifests", p3MediaOCIManifest, man)
}

// p6dNEngine is a fake engine whose distribution endpoint reports digest
// for every name, and whose /version reports os/arch.
func p6dNEngine(t *testing.T, regHost, digest, arch string) *providerData {
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47", "Os": "linux", "Arch": arch})
	f.json("GET /distribution/*", map[string]any{
		"Descriptor": map[string]any{"mediaType": p3MediaOCIIndex, "digest": digest, "size": 1},
		"Platforms":  []map[string]string{{"os": "linux", "architecture": "amd64"}},
	})
	pd.RegistryAuth = []registryCredential{{Address: regHost, Username: "bob", Password: "pw"}}
	return pd
}

func p6dNConfigOf(t *testing.T, st tfsdk.State) types.Object {
	t.Helper()
	var o types.Object
	if d := st.GetAttribute(context.Background(), path.Root("config"), &o); d.HasError() {
		t.Fatal(d)
	}
	return o
}

func TestRegistryImageDataConfigSingleP6dN(t *testing.T) {
	reg := newP3eFakeRegistry(t, "basic")
	dg := p6dNImage(reg, "org/web", "amd64")
	pd := p6dNEngine(t, reg.host(), dg, "amd64")
	st := rReadDS(t, NewRegistryImageDataSource(), pd, map[string]tftypes.Value{"name": p3eStr(reg.host() + "/org/web:1.27")})
	ctx := context.Background()
	if got := swarmBStr(t, st, path.Root("config").AtName("stop_signal")); got != "SIGQUIT" {
		t.Errorf("config.stop_signal = %q", got)
	}
	var ports types.Map
	st.GetAttribute(ctx, path.Root("config").AtName("exposed_ports"), &ports)
	if _, ok := ports.Elements()["80/tcp"]; !ok || len(ports.Elements()) != 1 {
		t.Errorf("config.exposed_ports = %v", ports)
	}
	var labels map[string]string
	st.GetAttribute(ctx, path.Root("labels"), &labels)
	if labels["maintainer"] != "NGINX Docker Maintainers" || labels["arch"] != "amd64" {
		t.Errorf("labels = %v", labels)
	}
	if got := swarmBStr(t, st, path.Root("created")); got != "2026-01-02T03:04:05.123456789Z" {
		t.Errorf("created = %q", got)
	}

	// Shape-equal with data.docker_image for the same config.
	f, ipd := newP3eFake(t)
	f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47"})
	blob, err := regDecodeImageConfig([]byte(p6dNConfigBlob("amd64")))
	if err != nil {
		t.Fatal(err)
	}
	f.json("GET /images/web:1.27/json", map[string]any{"Id": "sha256:abc", "Os": "linux", "Architecture": "amd64",
		"Created": "2026-01-02T03:04:05.123456789Z", "Config": blob.Config})
	ist := rReadDS(t, NewImageDataSource(), ipd, map[string]tftypes.Value{"name": p3eStr("web:1.27")})
	rc, ic := p6dNConfigOf(t, st), p6dNConfigOf(t, ist)
	if rc.IsNull() || !rc.Equal(ic) {
		t.Errorf("config differs between the data sources:\nregistry %v\nimage    %v", rc, ic)
	}
	if got := swarmBStr(t, ist, path.Root("created")); got != swarmBStr(t, st, path.Root("created")) {
		t.Errorf("created differs: image %q", got)
	}
}

func TestRegistryImageDataConfigIndexP6dN(t *testing.T) {
	reg := newP3eFakeRegistry(t, "bearer")
	amd, arm := p6dNImage(reg, "org/web", "amd64"), p6dNImage(reg, "org/web", "arm64")
	att := "sha256:" + strings.Repeat("e", 64)
	index := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[
{"mediaType":%q,"digest":%q,"size":1,"platform":{"os":"unknown","architecture":"unknown"},"annotations":{"vnd.docker.reference.type":"attestation-manifest"}},
{"mediaType":%q,"digest":%q,"size":1,"platform":{"os":"linux","architecture":"arm64","variant":"v8"}},
{"mediaType":%q,"digest":%q,"size":1,"platform":{"os":"linux","architecture":"amd64"}}]}`,
		p3MediaOCIIndex, p3MediaOCIManifest, att, p3MediaOCIManifest, arm, p3MediaOCIManifest, amd)
	idx := p6dNServe(reg, "org/web", "manifests", p3MediaOCIIndex, index)
	for _, c := range []struct{ daemonArch, want string }{
		{"arm64", "arm64"},   // the daemon's platform wins
		{"amd64", "amd64"},   // ... also when it is not the first entry
		{"riscv64", "amd64"}, // no match: linux/amd64
		{"", "amd64"},        // daemon platform unknown: linux/amd64
	} {
		pd := p6dNEngine(t, reg.host(), idx, c.daemonArch)
		st := rReadDS(t, NewRegistryImageDataSource(), pd, map[string]tftypes.Value{"name": p3eStr(reg.host() + "/org/web:1.27")})
		if got := swarmBStr(t, st, path.Root("labels").AtMapKey("arch")); got != c.want {
			t.Errorf("daemon arch %q: picked %q, want %q", c.daemonArch, got, c.want)
		}
	}
	// No amd64 entry and no daemon match: the first image entry.
	only := []regDescriptor{{Digest: att, Annotations: map[string]string{regAnnRefType: regAttestationType}}, {Digest: arm}}
	if d := regPickIndexEntry(only, "", ""); d == nil || d.Digest != arm {
		t.Errorf("fallback pick = %v", d)
	}
	// Direct path (insecure_skip_verify set) picks the same way.
	reg.handle("/v2/org/web/manifests/1.27", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", p3MediaOCIIndex)
		_, _ = w.Write([]byte(index))
	})
	pd := p6dNEngine(t, reg.host(), idx, "arm64")
	st := rReadDS(t, NewRegistryImageDataSource(), pd, map[string]tftypes.Value{
		"name": p3eStr(reg.host() + "/org/web:1.27"), "insecure_skip_verify": tftypes.NewValue(tftypes.Bool, true)})
	if got := swarmBStr(t, st, path.Root("labels").AtMapKey("arch")); got != "arm64" || swarmBStr(t, st, path.Root("digest")) != idx {
		t.Errorf("direct path: picked %q, digest %s", got, swarmBStr(t, st, path.Root("digest")))
	}
}

func TestRegistryImageDataConfigUnreachableP6dN(t *testing.T) {
	// The daemon read the registry; the provider cannot (nothing listens on
	// 127.0.0.1:1): warning, config / labels / created null, no error.
	pd := p6dNEngine(t, "127.0.0.1:1", "sha256:"+strings.Repeat("d", 64), "amd64")
	st, diags := swarmBReadDS(t, NewRegistryImageDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("127.0.0.1:1/org/web:1.27")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	found := false
	for _, w := range diags.Warnings() {
		if w.Summary() == "Image config not read from registry" && strings.Contains(w.Detail(), "insecure_skip_verify") {
			found = true
		}
	}
	if !found {
		t.Errorf("want the config warning, got %v", diags)
	}
	if !p6dNConfigOf(t, st).IsNull() {
		t.Error("config must be null")
	}
	var labels types.Map
	var created types.String
	st.GetAttribute(context.Background(), path.Root("labels"), &labels)
	st.GetAttribute(context.Background(), path.Root("created"), &created)
	if !labels.IsNull() || !created.IsNull() {
		t.Errorf("labels %v, created %v must be null", labels, created)
	}
	if got := swarmBStr(t, st, path.Root("digest")); got != "sha256:"+strings.Repeat("d", 64) {
		t.Errorf("digest %q", got)
	}
}
