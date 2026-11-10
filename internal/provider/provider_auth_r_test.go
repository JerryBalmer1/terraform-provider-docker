package provider

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// rBuildCredHelper builds testdata/credhelper as docker-credential-tfacc in a
// temp dir, puts that dir first on PATH and returns the call log file.
func rBuildCredHelper(t *testing.T) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH; cannot build the fake credential helper")
	}
	dir := t.TempDir()
	name := "docker-credential-tfacc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cmd := exec.Command(gobin, "build", "-o", filepath.Join(dir, name), "./testdata/credhelper")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the fake credential helper: %v\n%s", err, out)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logFile := filepath.Join(dir, "calls.log")
	t.Setenv("TFACC_CREDHELPER_LOG", logFile)
	return logFile
}

func rHelperCalls(t *testing.T, logFile string) []string {
	t.Helper()
	b, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func rB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// rHelperConfig names the fake helper per registry (credHelpers) and as the
// store (credsStore), with auths entries behind both.
func rHelperConfig() string {
	return `{
  "auths": {
    "helper.example": {"auth": "` + rB64("fileuser:filepw") + `"},
    "auths.example":  {"auth": "` + rB64("authsuser:authspw") + `"}
  },
  "credsStore": "tfacc",
  "credHelpers": {"helper.example": "tfacc"}
}`
}

// TestCredentialHelperProtocolR runs a real helper binary (built from
// testdata/credhelper) through docker/cli's credentials package: server URL
// on stdin, JSON on stdout, "credentials not found" handled, and the
// precedence registry_auth inputs > credHelpers > credsStore > auths.
func TestCredentialHelperProtocolR(t *testing.T) {
	hermeticSSH(t)
	logFile := rBuildCredHelper(t)
	content := func(addr string) registryAuthModel {
		e := authEntry(addr)
		e.ConfigFileContent = types.StringValue(rHelperConfig())
		return e
	}

	// credHelpers[registry] wins over the auths entry for the same registry.
	out, diags := resolveAuth(t, []registryAuthModel{content("helper.example")}, nil, true)
	if diags.HasError() || out[0].Username != "u-helper.example" || out[0].Password != "s3cret" {
		t.Fatalf("credHelpers: %+v %v", out, diags)
	}
	// credsStore: "<token>" turns the secret into an identity token.
	out, diags = resolveAuth(t, []registryAuthModel{content("token.example")}, nil, true)
	if diags.HasError() || out[0].IdentityToken != "refresh-token" || out[0].Password != "" {
		t.Fatalf("credsStore token: %+v %v", out, diags)
	}
	// credsStore answers "credentials not found": auths is used.
	out, diags = resolveAuth(t, []registryAuthModel{content("auths.example")}, nil, true)
	if diags.HasError() || out[0].Username != "authsuser" || out[0].Password != "authspw" {
		t.Fatalf("not found -> auths: %+v %v", out, diags)
	}
	// Not found and no auths entry: an error that names the helper.
	if _, diags = resolveAuth(t, []registryAuthModel{content("nothing.example")}, nil, true); !diags.HasError() ||
		!strings.Contains(diags[0].Detail(), "docker-credential-tfacc") {
		t.Fatalf("not found without auths: %v", diags)
	}
	// registry_auth username/password come first: no helper runs.
	explicit := content("helper.example")
	explicit.Username, explicit.Password = types.StringValue("me"), types.StringValue("pw")
	out, diags = resolveAuth(t, []registryAuthModel{explicit}, nil, true)
	if diags.HasError() || out[0].Username != "me" {
		t.Fatalf("explicit: %+v %v", out, diags)
	}
	want := "helper.example token.example auths.example nothing.example"
	if got := strings.Join(rHelperCalls(t, logFile), " "); got != want {
		t.Errorf("helper calls %q, want %q", got, want)
	}

	// The gate: with allow_credential_helpers false the helper never runs;
	// the auths entry for the same registry is used instead.
	if err := os.Remove(logFile); err != nil {
		t.Fatal(err)
	}
	out, diags = resolveAuth(t, []registryAuthModel{content("helper.example")}, nil, false)
	if diags.HasError() || out[0].Username != "fileuser" {
		t.Fatalf("gate: %+v %v", out, diags)
	}
	if calls := rHelperCalls(t, logFile); len(calls) != 0 {
		t.Errorf("helper ran with allow_credential_helpers = false: %v", calls)
	}
}

// TestDockerConfigAuthDefaultR covers registries without a registry_auth
// block: the default docker config (DOCKER_CONFIG) answers, with the same
// precedence and gate, results are cached, and anything else is anonymous.
func TestDockerConfigAuthDefaultR(t *testing.T) {
	hermeticSSH(t)
	logFile := rBuildCredHelper(t)
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(rHelperConfig()), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DOCKER_CONFIG": cfgDir}
	getenv := func(k string) string { return env[k] }

	pd := &providerData{
		RegistryAuth: []registryCredential{{Address: "token.example", Username: "block", Password: "blockpw"}},
		configAuth:   newDockerConfigAuth(getenv, true),
	}
	for i := 0; i < 2; i++ { // the second round is served from the cache
		ac, ok, err := pd.registryAuthConfigFor("helper.example/team/app:1")
		if err != nil || !ok || ac.Username != "u-helper.example" || ac.Password != "s3cret" {
			t.Fatalf("credHelpers via default config: %+v %v %v", ac, ok, err)
		}
	}
	// A registry_auth block wins over the default config.
	if ac, ok, _ := pd.registryAuthConfigFor("token.example/app"); !ok || ac.Username != "block" {
		t.Errorf("block precedence: %+v %v", ac, ok)
	}
	// credsStore misses, auths answers.
	if ac, ok, _ := pd.registryAuthConfigFor("auths.example/app"); !ok || ac.Username != "authsuser" {
		t.Errorf("auths via default config: %+v %v", ac, ok)
	}
	// Nothing anywhere: anonymous, not an error.
	if ac, ok, err := pd.registryAuthConfigFor("nothing.example/app"); err != nil || ok {
		t.Errorf("anonymous: %+v %v %v", ac, ok, err)
	}
	if got := strings.Join(rHelperCalls(t, logFile), " "); got != "helper.example auths.example nothing.example" {
		t.Errorf("helper calls %q", got)
	}
	// The direct registry client uses the same resolution.
	if u, p := pd.p3RegistryCreds("helper.example"); u != "u-helper.example" || p != "s3cret" {
		t.Errorf("p3RegistryCreds: %q %q", u, p)
	}

	// Gate off: no helper runs, auths entries still count, a helper-only
	// registry is anonymous.
	if err := os.Remove(logFile); err != nil {
		t.Fatal(err)
	}
	pd = &providerData{configAuth: newDockerConfigAuth(getenv, false)}
	if ac, ok, _ := pd.registryAuthConfigFor("helper.example/app"); !ok || ac.Username != "fileuser" {
		t.Errorf("gate off, auths: %+v %v", ac, ok)
	}
	if _, ok := pd.configAuth.lookup(context.Background(), "token.example"); ok {
		t.Error("gate off: helper-only registry must be anonymous")
	}
	if calls := rHelperCalls(t, logFile); len(calls) != 0 {
		t.Errorf("helper ran with the gate off: %v", calls)
	}

	// No config file at all: anonymous.
	pd = &providerData{configAuth: newDockerConfigAuth(func(string) string { return t.TempDir() }, true)}
	if _, ok, err := pd.registryAuthConfigFor("helper.example/app"); ok || err != nil {
		t.Errorf("no config: %v %v", ok, err)
	}
	// Test fixtures without Configure have no default-config lookup.
	if _, ok, _ := (&providerData{}).registryAuthConfigFor("helper.example/app"); ok {
		t.Error("nil configAuth must be anonymous")
	}
}
