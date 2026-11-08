package provider

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/moby/moby/client"
)

// testAccProtoV6ProviderFactories serves the provider in-process under the
// bare name "docker"; test configs carry no required_providers block.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"docker": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccPreCheck skips acceptance tests unless TF_ACC and DOCKER_HOST are
// both set. Call it first in every TestAcc*.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set; skipping acceptance test")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set; skipping acceptance test (see test/harness)")
	}
}

// testAccDockerClient returns a client for DOCKER_HOST, for Check and
// CheckDestroy functions.
func testAccDockerClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := newDockerClient(endpointConfig{Host: os.Getenv("DOCKER_HOST")})
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	return c
}

// TestProviderSchema fetches the full provider schema over protocol 6. The
// framework validates every resource and data source schema here, so a bad
// attribute definition in any lane fails this test.
func TestProviderSchema(t *testing.T) {
	srv, err := testAccProtoV6ProviderFactories["docker"]()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		t.Errorf("%s: %s: %s", d.Severity, d.Summary, d.Detail)
	}
	if resp.Provider == nil {
		t.Fatal("no provider schema")
	}
	t.Logf("%d resources, %d data sources", len(resp.ResourceSchemas), len(resp.DataSourceSchemas))
}

func TestResolveEndpoint(t *testing.T) {
	// An empty DOCKER_CONFIG dir keeps the real currentContext out of the test.
	env := map[string]string{"DOCKER_CONFIG": t.TempDir()}
	getenv := func(k string) string { return env[k] }
	var diags diag.Diagnostics

	ep := resolveEndpoint(providerModel{Host: types.StringNull(), APIVersion: types.StringNull()}, getenv, &diags)
	if ep.Host != defaultDockerHost || ep.APIVersion != "" {
		t.Errorf("defaults: got %+v", ep)
	}
	env["DOCKER_HOST"] = "tcp://env:2375"
	env["DOCKER_API_VERSION"] = "1.45"
	ep = resolveEndpoint(providerModel{Host: types.StringNull(), APIVersion: types.StringNull()}, getenv, &diags)
	if ep.Host != "tcp://env:2375" || ep.APIVersion != "1.45" {
		t.Errorf("env: got %+v", ep)
	}
	ep = resolveEndpoint(providerModel{Host: types.StringValue("tcp://cfg:2376"), APIVersion: types.StringNull()}, getenv, &diags)
	if ep.Host != "tcp://cfg:2376" {
		t.Errorf("config should win over env: got %+v", ep)
	}
	env["DOCKER_TLS_VERIFY"] = "1"
	env["DOCKER_CERT_PATH"] = "certs"
	ep = resolveEndpoint(providerModel{Host: types.StringNull(), APIVersion: types.StringNull()}, getenv, &diags)
	if ep.CA == "" || ep.Cert == "" || ep.Key == "" {
		t.Errorf("DOCKER_CERT_PATH not applied: %+v", ep)
	}
	if diags.HasError() {
		t.Fatal(diags)
	}
	if _, err := newDockerClient(endpointConfig{Host: "tcp://127.0.0.1:2375"}); err != nil {
		t.Errorf("newDockerClient: %v", err)
	}
}

func TestResolveNameOrID(t *testing.T) {
	objs := []namedObject{{ID: "abc123", Name: "web"}, {ID: "abd456", Name: "db"}, {ID: "web", Name: "trap"}}
	cases := map[string]string{"abc123": "abc123", "db": "abd456", "abc": "abc123", "web": "web"}
	for ref, want := range cases {
		got, err := resolveNameOrID("network", ref, objs)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := resolveNameOrID("network", "ab", objs); err == nil {
		t.Error("ambiguous prefix should fail")
	}
	if _, err := resolveNameOrID("network", "zzz", objs); !isNotFound(err) {
		t.Errorf("missing should be not-found, got %v", err)
	}
}

func TestRegistryHelpers(t *testing.T) {
	pd := &providerData{RegistryAuth: []registryCredential{
		{Address: "https://index.docker.io/v1/", Username: "u", Password: "p"},
		{Address: "ghcr.io", Username: "g", Password: "q"},
	}}
	for ref, want := range map[string]string{"nginx:1": "u", "library/nginx": "u", "ghcr.io/a/b:1": "g", "quay.io/x/y": ""} {
		cfg, ok, err := pd.registryAuthConfigFor(ref)
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Username; got != want || ok != (want != "") {
			t.Errorf("%s: got %q ok=%v, want %q", ref, got, ok, want)
		}
	}
	dir := t.TempDir()
	f := dir + "/config.json"
	if err := os.WriteFile(f, []byte(`{"auths":{"ghcr.io":{"auth":"dXNlcjpwYXNz"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	u, p, err := credentialFromConfigFile(f, "https://ghcr.io")
	if err != nil || u != "user" || p != "pass" {
		t.Errorf("config file: %q %q %v", u, p, err)
	}
}

func TestLabelHelpers(t *testing.T) {
	m := mergeLabels(map[string]string{"a": "1", labelManagedBy: labelManagedByValue}, nil, map[string]string{"a": "2", labelStackNamespace: "s"})
	if m["a"] != "2" || len(m) != 3 {
		t.Errorf("merge: %v", m)
	}
	u := userLabels(m, true)
	if len(u) != 1 || u["a"] != "2" {
		t.Errorf("userLabels: %v", u)
	}
	if got := filterGet(labelFilter(map[string]string{"b": "2", "a": "1"}), "label"); len(got) != 2 {
		t.Errorf("labelFilter: %v", got)
	}
}

func TestValidateProviderModelCertPath(t *testing.T) {
	var diags diag.Diagnostics
	m := providerModel{CertPath: types.StringValue("/certs"), TLS: &tlsModel{CA: types.StringValue("ca.pem")}}
	validateProviderModel(m, &diags)
	if !diags.HasError() {
		t.Fatal("cert_path with a tls block should fail validation")
	}
	diags = nil
	validateProviderModel(providerModel{CertPath: types.StringValue("/certs")}, &diags)
	validateProviderModel(providerModel{TLS: &tlsModel{}}, &diags)
	if diags.HasError() {
		t.Fatalf("either alone is fine: %v", diags)
	}
	validateProviderModel(providerModel{SSH: &sshModel{ConnectTimeout: types.StringValue("later")}}, &diags)
	validateProviderModel(providerModel{SSH: &sshModel{ProxyJump: types.StringValue("u@:1")}}, &diags)
	if len(diags) != 2 {
		t.Errorf("want 2 ssh validation errors, got %v", diags)
	}

	// cert_path maps onto the three files.
	diags = nil
	ep := resolveEndpoint(providerModel{Host: types.StringValue("tcp://h:2376"), CertPath: types.StringValue("certs")},
		func(k string) string { return map[string]string{"DOCKER_CONFIG": t.TempDir()}[k] }, &diags)
	if ep.CA != filepath.Join("certs", "ca.pem") || ep.Cert != filepath.Join("certs", "cert.pem") || ep.Key != filepath.Join("certs", "key.pem") {
		t.Errorf("cert_path: %+v", ep)
	}
}

func TestCheckDockerDaemon(t *testing.T) {
	eng := fakeEngine(t)
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + eng.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	var diags diag.Diagnostics
	checkDockerDaemon(context.Background(), c, endpointConfig{Host: "fake"}, DefaultMinAPIVersion, &diags)
	if len(diags) != 0 {
		t.Fatalf("reachable daemon: %v", diags)
	}
	// Unreachable: a warning, never an error.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	c2, err := newDockerClient(endpointConfig{Host: "tcp://" + addr, APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	checkDockerDaemon(context.Background(), c2, endpointConfig{Host: "tcp://" + addr}, DefaultMinAPIVersion, &diags)
	if diags.HasError() || diags.WarningsCount() != 1 {
		t.Fatalf("unreachable daemon should warn only: %v", diags)
	}
}
