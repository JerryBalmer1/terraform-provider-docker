package provider

// Registry credential resolution. Precedence for one registry:
//
//  1. a registry_auth block for it: username + password / password_wo, then
//     DOCKER_REGISTRY_USER / DOCKER_REGISTRY_PASS, then the block's docker
//     config (config_file_content, config_file, else the default config);
//  2. no block: the default docker config (DOCKER_CONFIG, else
//     ~/.docker/config.json), see dockerConfigAuth;
//  3. nothing: anonymous.
//
// Inside a docker config the lookup order is the docker CLI's:
// credHelpers[registry], then credsStore, then auths.
//
// Credential helpers (docker-credential-<name>) are external programs, run by
// docker/cli's credentials package (docker-credential-helpers/client) over
// the credential-helper protocol: `docker-credential-<name> get` with the
// server URL on stdin, JSON {"ServerURL","Username","Secret"} on stdout, and
// "credentials not found in native keychain" meaning no entry. That is the
// provider's one documented exception to "nothing shells out", and it only
// happens when the provider sets allow_credential_helpers = true and a docker
// config names the helper.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/credentials"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// dockerHubAuthKey is the key Docker Hub credentials are stored under.
const dockerHubAuthKey = "https://index.docker.io/v1/"

// newCredentialHelperStore runs docker-credential-<helper>. Tests replace it.
var newCredentialHelperStore = func(cf *configfile.ConfigFile, helper string) credentials.Store {
	return credentials.NewNativeStore(cf, helper)
}

// registryAuthOptions carries the provider-level inputs to registry auth.
type registryAuthOptions struct {
	Getenv       func(string) string
	AllowHelpers bool
}

func knownStr(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

// defaultDockerConfigFile is DOCKER_CONFIG (a directory holding config.json,
// or a file) when set, else ~/.docker/config.json.
func defaultDockerConfigFile(getenv func(string) string) string {
	if d := getenv("DOCKER_CONFIG"); d != "" {
		d = expandHome(d)
		if st, err := os.Stat(d); err == nil && !st.IsDir() {
			return d
		}
		return filepath.Join(d, "config.json")
	}
	if h := homePath(".docker", "config.json"); h != "" {
		return h
	}
	return ""
}

func resolveRegistryAuth(in []registryAuthModel, opts registryAuthOptions, diags *diag.Diagnostics) []registryCredential {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	var out []registryCredential
	for i, m := range in {
		at := path.Root("registry_auth").AtListIndex(i)
		unknown := false
		for name, v := range map[string]types.String{"address": m.Address, "username": m.Username, "password": m.Password,
			"password_wo": m.PasswordWO, "config_file": m.ConfigFile, "config_file_content": m.ConfigFileContent} {
			if v.IsUnknown() {
				diags.AddAttributeError(at.AtName(name), "Unknown registry_auth argument", name+" is not known until apply; provider configuration must be known at plan time.")
				unknown = true
			}
		}
		if unknown || m.AuthDisabled.IsUnknown() {
			continue
		}
		if m.AuthDisabled.ValueBool() {
			// Anonymous access: no credential is recorded for this registry.
			continue
		}
		c := registryCredential{Address: m.Address.ValueString(), Username: knownStr(m.Username), Password: knownStr(m.Password)}
		if c.Password == "" {
			c.Password = knownStr(m.PasswordWO)
		}
		cfgFile, cfgContent := knownStr(m.ConfigFile), knownStr(m.ConfigFileContent)
		if c.Username == "" && cfgFile == "" && cfgContent == "" {
			c.Username = getenv("DOCKER_REGISTRY_USER")
			if c.Password == "" {
				c.Password = getenv("DOCKER_REGISTRY_PASS")
			}
		}
		if c.Username != "" {
			if c.Password == "" {
				diags.AddAttributeError(at, "Incomplete registry_auth",
					fmt.Sprintf("registry_auth for %q has a username but no password (password, password_wo or DOCKER_REGISTRY_PASS).", c.Address))
				continue
			}
			out = append(out, c)
			continue
		}
		var cf *configfile.ConfigFile
		var err error
		src, attr := "config_file_content", at.AtName("config_file_content")
		switch {
		case cfgContent != "":
			cf, err = config.LoadFromReader(bytes.NewReader([]byte(cfgContent)))
		default:
			file := expandHome(cfgFile)
			attr = at.AtName("config_file")
			if file == "" {
				file = defaultDockerConfigFile(getenv)
				attr = at
			}
			src = file
			cf, err = loadDockerConfigFile(file)
		}
		if err != nil {
			diags.AddAttributeError(attr, "Cannot read registry credentials", fmt.Sprintf("%s: %v", src, err))
			continue
		}
		got, err := credentialFromDockerConfig(cf, c.Address, opts.AllowHelpers)
		if err != nil {
			diags.AddAttributeError(attr, "Cannot resolve registry credentials", fmt.Sprintf("%s: %v", src, err))
			continue
		}
		out = append(out, got)
	}
	return out
}

func loadDockerConfigFile(file string) (*configfile.ConfigFile, error) {
	if file == "" {
		return nil, errors.New("no docker config file: set config_file, config_file_content, DOCKER_CONFIG or username/password")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	cf, err := config.LoadFromReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	cf.Filename = file
	return cf, nil
}

// credentialAuthKey is the key docker stores address under: the historical
// index URL for Docker Hub, the bare host[:port] otherwise.
func credentialAuthKey(address string) string {
	if h := normalizeRegistry(address); h != "docker.io" {
		return h
	}
	return dockerHubAuthKey
}

// credentialFromDockerConfig finds address in cf. Lookup order matches the
// docker CLI: credHelpers[registry], then credsStore, then auths. When a
// helper is configured but helpers are not allowed, a usable auths entry is
// still used; otherwise the error names allow_credential_helpers.
func credentialFromDockerConfig(cf *configfile.ConfigFile, address string, allowHelpers bool) (registryCredential, error) {
	want := normalizeRegistry(address)
	var fileCred registryCredential
	fileOK := false
	for k, ac := range cf.GetAuthConfigs() {
		if normalizeRegistry(k) != want || (ac.Username == "" && ac.IdentityToken == "") {
			continue
		}
		fileCred = registryCredential{Address: address, Username: ac.Username, Password: ac.Password, IdentityToken: ac.IdentityToken}
		fileOK = true
		break
	}
	helper := ""
	for k, h := range cf.CredentialHelpers {
		if normalizeRegistry(k) == want {
			helper = h
		}
	}
	if helper == "" {
		helper = cf.CredentialsStore
	}
	if helper == "" {
		if fileOK {
			return fileCred, nil
		}
		return registryCredential{}, fmt.Errorf("no auths entry for %q", address)
	}
	if !allowHelpers {
		if fileOK {
			return fileCred, nil
		}
		return registryCredential{}, fmt.Errorf("credentials for %q are held by credential helper docker-credential-%s; set "+
			"allow_credential_helpers = true in the provider block to let the provider run it, or give username/password", address, helper)
	}
	ac, err := newCredentialHelperStore(cf, helper).Get(credentialAuthKey(address))
	if err == nil && (ac.Username != "" || ac.IdentityToken != "") {
		return registryCredential{Address: address, Username: ac.Username, Password: ac.Password, IdentityToken: ac.IdentityToken}, nil
	}
	if fileOK {
		return fileCred, nil
	}
	if err != nil {
		return registryCredential{}, fmt.Errorf("credential helper docker-credential-%s: %w", helper, err)
	}
	return registryCredential{}, fmt.Errorf("credential helper docker-credential-%s has no credentials for %q", helper, address)
}

// credentialFromConfigFile reads the credential for address from a docker
// config.json without running credential helpers.
func credentialFromConfigFile(file, address string) (string, string, error) {
	cf, err := loadDockerConfigFile(file)
	if err != nil {
		return "", "", err
	}
	c, err := credentialFromDockerConfig(cf, address, false)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", file, err)
	}
	return c.Username, c.Password, nil
}

// dockerConfigAuth resolves credentials for registries that no
// registry_auth block names, from the default docker config
// (DOCKER_CONFIG, else ~/.docker/config.json), the way the docker CLI does:
// credHelpers[registry], then credsStore (both only with
// allow_credential_helpers), then auths. The file is read on first use and
// each registry's answer is cached for the provider's lifetime. Anything
// that does not produce a credential (no file, no entry, a helper that is
// not allowed or fails) means anonymous access, never an error.
type dockerConfigAuth struct {
	getenv       func(string) string
	allowHelpers bool

	mu     sync.Mutex
	loaded bool
	cf     *configfile.ConfigFile
	byHost map[string]*registryCredential
}

func newDockerConfigAuth(getenv func(string) string, allowHelpers bool) *dockerConfigAuth {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &dockerConfigAuth{getenv: getenv, allowHelpers: allowHelpers, byHost: map[string]*registryCredential{}}
}

// lookup returns the credential for host, if the default config has one.
func (d *dockerConfigAuth) lookup(ctx context.Context, host string) (registryCredential, bool) {
	if d == nil {
		return registryCredential{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := normalizeRegistry(host)
	if c, ok := d.byHost[key]; ok {
		if c == nil {
			return registryCredential{}, false
		}
		return *c, true
	}
	if !d.loaded {
		d.loaded = true
		if file := defaultDockerConfigFile(d.getenv); file != "" {
			if _, err := os.Stat(file); err == nil {
				cf, err := loadDockerConfigFile(file)
				if err != nil {
					tflog.Warn(ctx, "cannot read the default docker config for registry credentials",
						map[string]any{"file": file, "error": err.Error()})
				}
				d.cf = cf
			}
		}
	}
	if d.cf == nil {
		d.byHost[key] = nil
		return registryCredential{}, false
	}
	c, err := credentialFromDockerConfig(d.cf, host, d.allowHelpers)
	if err != nil {
		tflog.Debug(ctx, "no registry credential in the default docker config; using anonymous access",
			map[string]any{"registry": host, "reason": err.Error()})
		d.byHost[key] = nil
		return registryCredential{}, false
	}
	d.byHost[key] = &c
	return c, true
}
