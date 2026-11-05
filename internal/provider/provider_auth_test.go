package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/credentials"
	clitypes "github.com/docker/cli/cli/config/types"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// fakeHelperStore stands in for docker-credential-<helper>.
type fakeHelperStore struct {
	helper string
	creds  map[string]clitypes.AuthConfig
	calls  *[]string
}

func (f fakeHelperStore) Get(addr string) (clitypes.AuthConfig, error) {
	*f.calls = append(*f.calls, f.helper+":"+addr)
	if c, ok := f.creds[f.helper+":"+addr]; ok {
		return c, nil
	}
	return clitypes.AuthConfig{}, errors.New("credentials not found in native keychain")
}
func (fakeHelperStore) Erase(string) error                              { return errors.New("read-only") }
func (fakeHelperStore) GetAll() (map[string]clitypes.AuthConfig, error) { return nil, nil }
func (fakeHelperStore) Store(clitypes.AuthConfig) error                 { return errors.New("read-only") }

func fakeHelpers(t *testing.T, creds map[string]clitypes.AuthConfig) *[]string {
	t.Helper()
	calls := &[]string{}
	old := newCredentialHelperStore
	newCredentialHelperStore = func(_ *configfile.ConfigFile, helper string) credentials.Store {
		return fakeHelperStore{helper: helper, creds: creds, calls: calls}
	}
	t.Cleanup(func() { newCredentialHelperStore = old })
	return calls
}

func authEntry(addr string) registryAuthModel {
	return registryAuthModel{
		Address: types.StringValue(addr), Username: types.StringNull(), Password: types.StringNull(), PasswordWO: types.StringNull(),
		ConfigFile: types.StringNull(), ConfigFileContent: types.StringNull(), AuthDisabled: types.BoolNull(),
	}
}

func resolveAuth(t *testing.T, in []registryAuthModel, env map[string]string, allow bool) ([]registryCredential, diag.Diagnostics) {
	t.Helper()
	var diags diag.Diagnostics
	out := resolveRegistryAuth(in, registryAuthOptions{Getenv: func(k string) string { return env[k] }, AllowHelpers: allow}, &diags)
	return out, diags
}

const helperConfig = `{
  "auths": {"https://index.docker.io/v1/": {}, "quay.io": {"auth": "cXVzZXI6cXBhc3M="}},
  "credsStore": "desktop",
  "credHelpers": {"ghcr.io": "gh", "quay.io": "quayhelper"}
}`

func TestRegistryAuthExplicitAndEnv(t *testing.T) {
	home := hermeticSSH(t)
	_ = home
	e := authEntry("ghcr.io")
	e.Username = types.StringValue("u")
	e.PasswordWO = types.StringValue("wo")
	out, diags := resolveAuth(t, []registryAuthModel{e}, nil, false)
	if diags.HasError() || len(out) != 1 || out[0].Username != "u" || out[0].Password != "wo" {
		t.Fatalf("password_wo: %+v %v", out, diags)
	}
	// Env defaults apply when nothing else is set.
	out, diags = resolveAuth(t, []registryAuthModel{authEntry("ghcr.io")},
		map[string]string{"DOCKER_REGISTRY_USER": "envu", "DOCKER_REGISTRY_PASS": "envp"}, false)
	if diags.HasError() || len(out) != 1 || out[0].Username != "envu" || out[0].Password != "envp" {
		t.Fatalf("env: %+v %v", out, diags)
	}
	// Username without any password is an error.
	e = authEntry("ghcr.io")
	e.Username = types.StringValue("u")
	if _, diags = resolveAuth(t, []registryAuthModel{e}, nil, false); !diags.HasError() {
		t.Error("username without password should fail")
	}
	// auth_disabled yields no credential and needs nothing.
	e = authEntry("ghcr.io")
	e.AuthDisabled = types.BoolValue(true)
	out, diags = resolveAuth(t, []registryAuthModel{e}, nil, false)
	if diags.HasError() || len(out) != 0 {
		t.Errorf("auth_disabled: %+v %v", out, diags)
	}
	// Unknown values are errors.
	e = authEntry("ghcr.io")
	e.Password = types.StringUnknown()
	if _, diags = resolveAuth(t, []registryAuthModel{e}, nil, false); !diags.HasError() {
		t.Error("unknown password should fail")
	}
}

func TestRegistryAuthConfigSources(t *testing.T) {
	hermeticSSH(t)
	fakeHelpers(t, nil)
	cfg := `{"auths":{"ghcr.io":{"auth":"dXNlcjpwYXNz"},"registry.example:5000":{"username":"r","password":"s","identitytoken":"tok"}}}`
	// config_file_content.
	e := authEntry("https://ghcr.io")
	e.ConfigFileContent = types.StringValue(cfg)
	out, diags := resolveAuth(t, []registryAuthModel{e}, nil, false)
	if diags.HasError() || out[0].Username != "user" || out[0].Password != "pass" || out[0].Address != "https://ghcr.io" {
		t.Fatalf("content: %+v %v", out, diags)
	}
	// DOCKER_CONFIG as a directory, then as a file.
	dir := t.TempDir()
	f := filepath.Join(dir, "config.json")
	if err := os.WriteFile(f, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dc := range []string{dir, f} {
		out, diags = resolveAuth(t, []registryAuthModel{authEntry("registry.example:5000")}, map[string]string{"DOCKER_CONFIG": dc}, false)
		if diags.HasError() || out[0].Username != "r" || out[0].IdentityToken != "tok" {
			t.Fatalf("DOCKER_CONFIG=%s: %+v %v", dc, out, diags)
		}
	}
	// Explicit config_file beats env user/pass.
	e = authEntry("ghcr.io")
	e.ConfigFile = types.StringValue(f)
	out, diags = resolveAuth(t, []registryAuthModel{e}, map[string]string{"DOCKER_REGISTRY_USER": "envu", "DOCKER_REGISTRY_PASS": "p"}, false)
	if diags.HasError() || out[0].Username != "user" {
		t.Fatalf("config_file: %+v %v", out, diags)
	}
	// Missing entry and missing file are errors.
	if _, diags = resolveAuth(t, []registryAuthModel{authEntry("quay.io")}, map[string]string{"DOCKER_CONFIG": dir}, false); !diags.HasError() {
		t.Error("missing entry should fail")
	}
	e = authEntry("ghcr.io")
	e.ConfigFile = types.StringValue(filepath.Join(dir, "absent.json"))
	if _, diags = resolveAuth(t, []registryAuthModel{e}, nil, false); !diags.HasError() {
		t.Error("missing file should fail")
	}
	// The default path is ~/.docker/config.json.
	home, _ := dockerHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".docker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".docker", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diags = resolveAuth(t, []registryAuthModel{authEntry("ghcr.io")}, nil, false)
	if diags.HasError() || out[0].Username != "user" {
		t.Fatalf("home default: %+v %v", out, diags)
	}
}

func TestRegistryAuthCredentialHelpers(t *testing.T) {
	hermeticSSH(t)
	calls := fakeHelpers(t, map[string]clitypes.AuthConfig{
		"gh:ghcr.io":                  {Username: "ghuser", Password: "ghtoken"},
		"desktop:" + dockerHubAuthKey: {Username: "hub", IdentityToken: "refresh"},
	})
	content := func(addr string) registryAuthModel {
		e := authEntry(addr)
		e.ConfigFileContent = types.StringValue(helperConfig)
		return e
	}

	// Without allow_credential_helpers, a helper-only registry is an error naming the attribute.
	_, diags := resolveAuth(t, []registryAuthModel{content("ghcr.io")}, nil, false)
	if !diags.HasError() || !strings.Contains(diags[0].Detail(), "allow_credential_helpers") {
		t.Fatalf("want allow_credential_helpers error, got %v", diags)
	}
	if len(*calls) != 0 {
		t.Fatalf("helpers ran while disallowed: %v", *calls)
	}
	// ... but an auths entry is still used.
	out, diags := resolveAuth(t, []registryAuthModel{content("quay.io")}, nil, false)
	if diags.HasError() || out[0].Username != "quser" {
		t.Fatalf("auths fallback while disallowed: %+v %v", out, diags)
	}

	// Allowed: credHelpers per registry.
	out, diags = resolveAuth(t, []registryAuthModel{content("ghcr.io")}, nil, true)
	if diags.HasError() || out[0].Username != "ghuser" || out[0].Password != "ghtoken" {
		t.Fatalf("credHelpers: %+v %v", out, diags)
	}
	// credsStore for Docker Hub, any spelling, keyed by the index URL.
	out, diags = resolveAuth(t, []registryAuthModel{content("registry-1.docker.io")}, nil, true)
	if diags.HasError() || out[0].Username != "hub" || out[0].IdentityToken != "refresh" {
		t.Fatalf("credsStore: %+v %v", out, diags)
	}
	// Helper failure falls back to auths.
	out, diags = resolveAuth(t, []registryAuthModel{content("quay.io")}, nil, true)
	if diags.HasError() || out[0].Username != "quser" {
		t.Fatalf("helper miss -> auths: %+v %v", out, diags)
	}
	// Helper failure without auths is an error.
	if _, diags = resolveAuth(t, []registryAuthModel{content("other.example")}, nil, true); !diags.HasError() {
		t.Error("helper miss without auths should fail")
	}
	want := []string{"gh:ghcr.io", "desktop:" + dockerHubAuthKey, "quayhelper:quay.io", "desktop:other.example"}
	if strings.Join(*calls, ",") != strings.Join(want, ",") {
		t.Errorf("helper calls %v, want %v", *calls, want)
	}

	// The identity token reaches the AuthConfig handed to the Engine API.
	pd := &providerData{RegistryAuth: []registryCredential{{Address: "docker.io", Username: "hub", IdentityToken: "refresh"}}}
	ac, ok, err := pd.registryAuthConfigFor("nginx")
	if err != nil || !ok || ac.IdentityToken != "refresh" {
		t.Errorf("registryAuthConfigFor: %+v %v %v", ac, ok, err)
	}
}

func TestCredentialAuthKey(t *testing.T) {
	for in, want := range map[string]string{
		"docker.io": dockerHubAuthKey, "https://index.docker.io/v1/": dockerHubAuthKey, "registry-1.docker.io": dockerHubAuthKey,
		"ghcr.io": "ghcr.io", "https://r.example:5000/v2/": "r.example:5000",
	} {
		if got := credentialAuthKey(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}
