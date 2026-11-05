package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	dctx "github.com/docker/cli/cli/context"
	"github.com/docker/cli/cli/context/store"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// writeTestContext creates a context the same way `docker context create`
// does, through docker/cli's store.
func writeTestContext(t *testing.T, configDir, name, host string, skipVerify bool, tlsFiles map[string][]byte) {
	t.Helper()
	s := store.New(filepath.Join(configDir, "contexts"), contextStoreConfig())
	err := s.CreateOrUpdate(store.Metadata{
		Name:      name,
		Metadata:  map[string]any{"Description": "test"},
		Endpoints: map[string]any{"docker": dctx.EndpointMetaBase{Host: host, SkipTLSVerify: skipVerify}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tlsFiles != nil {
		if err := s.ResetEndpointTLSMaterial(name, "docker", &store.EndpointTLSData{Files: tlsFiles}); err != nil {
			t.Fatal(err)
		}
	}
}

func nullProviderModel() providerModel {
	return providerModel{Host: types.StringNull(), APIVersion: types.StringNull(), Context: types.StringNull(), CertPath: types.StringNull()}
}

func TestLoadDockerContext(t *testing.T) {
	dir := t.TempDir()
	writeTestContext(t, dir, "remote", "tcp://ctx.example:2376", true, map[string][]byte{
		"ca.pem": []byte("CA"), "cert.pem": []byte("CERT"), "key.pem": []byte("KEY"),
	})
	c, err := loadDockerContext(dir, "remote")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "tcp://ctx.example:2376" || !c.SkipTLSVerify || string(c.CA) != "CA" || string(c.Cert) != "CERT" || string(c.Key) != "KEY" {
		t.Errorf("got %+v", c)
	}
	for _, n := range []string{"", "default"} {
		if c, err := loadDockerContext(dir, n); c != nil || err != nil {
			t.Errorf("%q: want no-op, got %+v %v", n, c, err)
		}
	}
	if _, err := loadDockerContext(dir, "missing"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing context: %v", err)
	}
}

func TestResolveEndpointContext(t *testing.T) {
	dir := t.TempDir()
	writeTestContext(t, dir, "remote", "tcp://ctx.example:2376", false, map[string][]byte{"ca.pem": []byte("-----BEGIN CA")})
	writeTestContext(t, dir, "current", "tcp://current.example:2375", false, nil)
	writeTestContext(t, dir, "sshctx", "ssh://ops@bastion.example:2200", false, nil)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"currentContext":"current"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DOCKER_CONFIG": dir}
	getenv := func(k string) string { return env[k] }
	resolve := func(m providerModel) endpointConfig {
		t.Helper()
		var diags diag.Diagnostics
		ep := resolveEndpoint(m, getenv, &diags)
		if diags.HasError() {
			t.Fatalf("%v", diags)
		}
		return ep
	}

	// currentContext from config.json.
	if ep := resolve(nullProviderModel()); ep.Host != "tcp://current.example:2375" || ep.Context != "current" {
		t.Errorf("currentContext: %+v", ep)
	}
	// DOCKER_CONTEXT beats currentContext.
	env["DOCKER_CONTEXT"] = "remote"
	if ep := resolve(nullProviderModel()); ep.Host != "tcp://ctx.example:2376" || ep.CA != "-----BEGIN CA" {
		t.Errorf("DOCKER_CONTEXT: %+v", ep)
	}
	// DOCKER_HOST beats DOCKER_CONTEXT and currentContext.
	env["DOCKER_HOST"] = "tcp://env.example:2375"
	if ep := resolve(nullProviderModel()); ep.Host != "tcp://env.example:2375" || ep.Context != "" {
		t.Errorf("DOCKER_HOST: %+v", ep)
	}
	// The context attribute beats DOCKER_HOST ...
	m := nullProviderModel()
	m.Context = types.StringValue("remote")
	if ep := resolve(m); ep.Host != "tcp://ctx.example:2376" {
		t.Errorf("context attr: %+v", ep)
	}
	// ... except "default", which is DOCKER_HOST or the default socket.
	m.Context = types.StringValue("default")
	if ep := resolve(m); ep.Host != "tcp://env.example:2375" {
		t.Errorf("default context: %+v", ep)
	}
	delete(env, "DOCKER_HOST")
	if ep := resolve(m); ep.Host != defaultDockerHost {
		t.Errorf("default context without DOCKER_HOST: %+v", ep)
	}
	// An explicit host wins over the context attribute.
	m = nullProviderModel()
	m.Context = types.StringValue("remote")
	m.Host = types.StringValue("tcp://explicit:2375")
	if ep := resolve(m); ep.Host != "tcp://explicit:2375" || ep.CA != "" {
		t.Errorf("host wins: %+v", ep)
	}
	// The tls block / cert_path beat the context's TLS material.
	m = nullProviderModel()
	m.Context = types.StringValue("remote")
	m.CertPath = types.StringValue(filepath.Join("x", "certs"))
	if ep := resolve(m); ep.CA != filepath.Join("x", "certs", "ca.pem") || ep.Key != filepath.Join("x", "certs", "key.pem") {
		t.Errorf("cert_path over context: %+v", ep)
	}
	// An ssh:// context host maps onto the SSH transport.
	m = nullProviderModel()
	m.Context = types.StringValue("sshctx")
	m.SSH = &sshModel{Password: types.StringValue("pw"), StrictHostKeyChecking: types.BoolValue(false)}
	ep := resolve(m)
	if ep.SSH == nil || ep.SSH.User != "ops" || ep.SSH.Host != "bastion.example" || ep.SSH.Port != 2200 || ep.SSH.Password != "pw" || ep.SSH.Strict {
		t.Errorf("ssh context: %+v / %+v", ep, ep.SSH)
	}
	if ep.Host != "ssh://ops@bastion.example:2200" {
		t.Errorf("ssh context host %q", ep.Host)
	}
	// A missing context is an error.
	m = nullProviderModel()
	m.Context = types.StringValue("nope")
	var diags diag.Diagnostics
	resolveEndpoint(m, getenv, &diags)
	if !diags.HasError() {
		t.Error("missing context should fail")
	}
	// Unknown context is an error.
	m.Context = types.StringUnknown()
	diags = nil
	resolveEndpoint(m, getenv, &diags)
	if !diags.HasError() {
		t.Error("unknown context should fail")
	}
}

func TestCurrentDockerContext(t *testing.T) {
	dir := t.TempDir()
	if got := currentDockerContext(dir); got != "" {
		t.Errorf("no config.json: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := currentDockerContext(dir); got != "" {
		t.Errorf("bad json: %q", got)
	}
	if got := dockerConfigDir(func(string) string { return "" }); got == "" {
		t.Log("no home directory in this environment")
	}
	if got := dockerConfigDir(func(k string) string { return map[string]string{"DOCKER_CONFIG": "/cfg"}[k] }); got != "/cfg" {
		t.Errorf("DOCKER_CONFIG: %q", got)
	}
}
