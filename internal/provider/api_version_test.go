package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/client"
)

// fakeEngineAPI is a fake daemon whose /_ping and /version answer with
// Engine API version apiVersion.
func fakeEngineAPI(t *testing.T, apiVersion string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", apiVersion)
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = io.WriteString(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/version"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"Version":"fake","ApiVersion":"`+apiVersion+`","Os":"linux"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// closedTCPAddr returns a loopback address nothing listens on.
func closedTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// configureProvider runs the provider's Configure against a config built
// from its own schema: every attribute and block null except attrs.
func configureProvider(t *testing.T, attrs map[string]tftypes.Value) (*fwprovider.ConfigureResponse, *providerData) {
	t.Helper()
	// Keep the developer's docker environment out of the test.
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_API_VERSION", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_REGISTRY_USER", "")
	t.Setenv("DOCKER_REGISTRY_PASS", "")

	ctx := context.Background()
	p := New("test")()
	var sresp fwprovider.SchemaResponse
	p.Schema(ctx, fwprovider.SchemaRequest{}, &sresp)
	objType, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("provider schema is not an object")
	}
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if v, ok := attrs[name]; ok {
			vals[name] = v
			continue
		}
		vals[name] = tftypes.NewValue(typ, nil)
	}
	for name := range attrs {
		if _, ok := objType.AttributeTypes[name]; !ok {
			t.Fatalf("no provider attribute %q", name)
		}
	}
	cfg := tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(objType, vals)}
	resp := &fwprovider.ConfigureResponse{}
	p.Configure(ctx, fwprovider.ConfigureRequest{Config: cfg}, resp)
	pd, _ := resp.ResourceData.(*providerData)
	return resp, pd
}

func tfStr(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

// hasAttrError reports whether diags has an error on attribute name.
func hasAttrError(diags diag.Diagnostics, name string) bool {
	for _, d := range diags.Errors() {
		if wp, ok := d.(diag.DiagnosticWithPath); ok && wp.Path().Equal(path.Root(name)) {
			return true
		}
	}
	return false
}

func TestMinAPIVersionSchema(t *testing.T) {
	var sresp fwprovider.SchemaResponse
	New("test")().Schema(context.Background(), fwprovider.SchemaRequest{}, &sresp)
	a, ok := sresp.Schema.Attributes["min_api_version"]
	if !ok {
		t.Fatal("provider schema has no min_api_version")
	}
	if !a.IsOptional() || a.IsRequired() || a.IsComputed() {
		t.Errorf("min_api_version should be Optional only")
	}
	d := a.GetMarkdownDescription()
	for _, want := range []string{"`" + DefaultMinAPIVersion + "`", client.MaxAPIVersion, "1.NN"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q: %s", want, d)
		}
	}
}

func TestConfigureMinAPIVersionDefault(t *testing.T) {
	eng := fakeEngineAPI(t, "1.47")
	resp, pd := configureProvider(t, map[string]tftypes.Value{"host": tfStr("tcp://" + eng.Listener.Addr().String())})
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 0 {
		t.Fatalf("diags: %v", resp.Diagnostics)
	}
	if pd == nil || pd.MinAPIVersion != DefaultMinAPIVersion {
		t.Fatalf("MinAPIVersion: got %+v, want %s", pd, DefaultMinAPIVersion)
	}
	// The Configure ping seeds the negotiated version cache.
	if pd.apiVersion != "1.47" {
		t.Errorf("negotiated version not cached: %q", pd.apiVersion)
	}
	if v, err := pd.APIVersion(context.Background()); err != nil || v != "1.47" {
		t.Errorf("APIVersion: %q, %v", v, err)
	}
}

func TestConfigureMinAPIVersionExplicit(t *testing.T) {
	eng := fakeEngineAPI(t, "1.47")
	resp, pd := configureProvider(t, map[string]tftypes.Value{
		"host":            tfStr("tcp://" + eng.Listener.Addr().String()),
		"min_api_version": tfStr("1.45"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("diags: %v", resp.Diagnostics)
	}
	if pd == nil || pd.MinAPIVersion != "1.45" {
		t.Fatalf("MinAPIVersion: got %+v, want 1.45", pd)
	}
}

func TestConfigureMinAPIVersionInvalid(t *testing.T) {
	above := "1.999"
	for _, v := range []string{"1.4x", "2.0", "1.", "", above} {
		t.Run(v, func(t *testing.T) {
			resp, pd := configureProvider(t, map[string]tftypes.Value{
				"host":                        tfStr("tcp://" + closedTCPAddr(t)),
				"disable_docker_daemon_check": tftypes.NewValue(tftypes.Bool, true),
				"min_api_version":             tfStr(v),
			})
			if !hasAttrError(resp.Diagnostics, "min_api_version") {
				t.Fatalf("want an error on min_api_version, got %v", resp.Diagnostics)
			}
			if pd != nil {
				t.Error("provider data handed out despite the error")
			}
		})
	}
	// Above client.MaxAPIVersion names that version.
	resp, _ := configureProvider(t, map[string]tftypes.Value{
		"host":                        tfStr("tcp://" + closedTCPAddr(t)),
		"disable_docker_daemon_check": tftypes.NewValue(tftypes.Bool, true),
		"min_api_version":             tfStr(above),
	})
	if errs := resp.Diagnostics.Errors(); len(errs) != 1 || !strings.Contains(errs[0].Detail(), client.MaxAPIVersion) {
		t.Errorf("above-max error should name %s: %v", client.MaxAPIVersion, resp.Diagnostics)
	}
}

func TestConfigureMinAPIVersionUnknown(t *testing.T) {
	resp, _ := configureProvider(t, map[string]tftypes.Value{
		"host":                        tfStr("tcp://" + closedTCPAddr(t)),
		"disable_docker_daemon_check": tftypes.NewValue(tftypes.Bool, true),
		"min_api_version":             tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
	if !hasAttrError(resp.Diagnostics, "min_api_version") {
		t.Fatalf("want an unknown-value error on min_api_version, got %v", resp.Diagnostics)
	}
}

func TestConfigureDaemonBelowFloor(t *testing.T) {
	eng := fakeEngineAPI(t, "1.41")
	resp, pd := configureProvider(t, map[string]tftypes.Value{
		"host":            tfStr("tcp://" + eng.Listener.Addr().String()),
		"min_api_version": tfStr("1.44"),
	})
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("want one error, got %v", resp.Diagnostics)
	}
	if d := errs[0].Detail(); !strings.Contains(d, "1.41") || !strings.Contains(d, "1.44") {
		t.Errorf("error should name both versions: %s", d)
	}
	if pd != nil {
		t.Error("provider data handed out despite the error")
	}
	// Lowering the floor accepts the same daemon.
	resp, pd = configureProvider(t, map[string]tftypes.Value{
		"host":            tfStr("tcp://" + eng.Listener.Addr().String()),
		"min_api_version": tfStr("1.41"),
	})
	if resp.Diagnostics.HasError() || pd == nil || pd.apiVersion != "1.41" {
		t.Fatalf("floor 1.41: %v, %+v", resp.Diagnostics, pd)
	}
}

func TestConfigureDaemonCheckDisabledFloorIsLazy(t *testing.T) {
	eng := fakeEngineAPI(t, "1.41")
	resp, pd := configureProvider(t, map[string]tftypes.Value{
		"host":                        tfStr("tcp://" + eng.Listener.Addr().String()),
		"disable_docker_daemon_check": tftypes.NewValue(tftypes.Bool, true),
	})
	if resp.Diagnostics.HasError() || pd == nil {
		t.Fatalf("no ping, so no floor error at Configure: %v", resp.Diagnostics)
	}
	_, err := pd.APIVersion(context.Background())
	var floor *apiFloorError
	if !errors.As(err, &floor) || floor.Version != "1.41" || floor.Floor != DefaultMinAPIVersion {
		t.Fatalf("first APIVersion call should hit the floor: %v", err)
	}
}

func TestConfigureUnreachableDaemonWarnsOnly(t *testing.T) {
	resp, pd := configureProvider(t, map[string]tftypes.Value{"host": tfStr("tcp://" + closedTCPAddr(t))})
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("unreachable daemon should warn only: %v", resp.Diagnostics)
	}
	if pd == nil || pd.MinAPIVersion != DefaultMinAPIVersion || pd.apiVersion != "" {
		t.Fatalf("provider data: %+v", pd)
	}
}

func TestConfigurePinnedAPIVersionBelowFloor(t *testing.T) {
	resp, _ := configureProvider(t, map[string]tftypes.Value{
		"host":                        tfStr("tcp://" + closedTCPAddr(t)),
		"disable_docker_daemon_check": tftypes.NewValue(tftypes.Bool, true),
		"api_version":                 tfStr("1.41"),
	})
	if !hasAttrError(resp.Diagnostics, "api_version") {
		t.Fatalf("want an error on api_version, got %v", resp.Diagnostics)
	}
	// Same pin through DOCKER_API_VERSION: a general error naming it.
	t.Run("env", func(t *testing.T) {
		var diags diag.Diagnostics
		checkPinnedAPIFloor(providerModel{}, endpointConfig{Host: "h", APIVersion: "1.41"}, "1.44", &diags)
		if len(diags.Errors()) != 1 || !strings.Contains(diags.Errors()[0].Detail(), "DOCKER_API_VERSION") {
			t.Fatalf("env pin: %v", diags)
		}
	})
	// A pin at or above the floor is fine.
	resp, pd := configureProvider(t, map[string]tftypes.Value{
		"host":                        tfStr("tcp://" + closedTCPAddr(t)),
		"disable_docker_daemon_check": tftypes.NewValue(tftypes.Bool, true),
		"api_version":                 tfStr("1.44"),
	})
	if resp.Diagnostics.HasError() || pd == nil {
		t.Fatalf("pin 1.44: %v", resp.Diagnostics)
	}
}

func TestCheckAPIFloor(t *testing.T) {
	if err := checkAPIFloor("h", "1.41", "1.44"); err == nil || !strings.Contains(err.Error(), "1.41") || !strings.Contains(err.Error(), "1.44") {
		t.Errorf("below: %v", err)
	}
	for _, c := range [][2]string{{"1.44", "1.44"}, {"1.47", "1.44"}, {"1.41", ""}, {"", "1.44"}} {
		if err := checkAPIFloor("h", c[0], c[1]); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	for _, v := range []string{"1.44", "1.24", client.MaxAPIVersion} {
		if err := validateMinAPIVersion(v); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
}
