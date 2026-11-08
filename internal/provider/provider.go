// Package provider implements the docker Terraform provider on
// terraform-plugin-framework. It owns the whole Docker chain: image build and
// push, single-host primitives (networks, volumes, containers), Swarm topology
// (init, node join) and Swarm workload (secrets, configs, services, stacks).
package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/list"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/client"
)

const defaultDockerHost = "unix:///var/run/docker.sock"

// daemonCheckTimeout bounds the Configure-time ping (plus the SSH connect
// timeout for ssh:// hosts).
const daemonCheckTimeout = 5 * time.Second

var (
	_ provider.Provider                       = (*dockerProvider)(nil)
	_ provider.ProviderWithActions            = (*dockerProvider)(nil)
	_ provider.ProviderWithEphemeralResources = (*dockerProvider)(nil)
	_ provider.ProviderWithFunctions          = (*dockerProvider)(nil)
	_ provider.ProviderWithListResources      = (*dockerProvider)(nil)
	_ provider.ProviderWithValidateConfig     = (*dockerProvider)(nil)
)

type dockerProvider struct {
	version string
}

// New returns a provider factory for providerserver.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &dockerProvider{version: version}
	}
}

type providerModel struct {
	Host                     types.String        `tfsdk:"host"`
	APIVersion               types.String        `tfsdk:"api_version"`
	Context                  types.String        `tfsdk:"context"`
	CertPath                 types.String        `tfsdk:"cert_path"`
	DisableDockerDaemonCheck types.Bool          `tfsdk:"disable_docker_daemon_check"`
	AllowCredentialHelpers   types.Bool          `tfsdk:"allow_credential_helpers"`
	MinAPIVersion            types.String        `tfsdk:"min_api_version"`
	TLS                      *tlsModel           `tfsdk:"tls"`
	SSH                      *sshModel           `tfsdk:"ssh"`
	RegistryAuth             []registryAuthModel `tfsdk:"registry_auth"`
}

// tlsModel is shared by the provider block and by resources that open their
// own endpoint (docker_swarm_node). Each value is PEM content or a file path.
type tlsModel struct {
	CA   types.String `tfsdk:"ca"`
	Cert types.String `tfsdk:"cert"`
	Key  types.String `tfsdk:"key"`
}

type registryAuthModel struct {
	Address           types.String `tfsdk:"address"`
	Username          types.String `tfsdk:"username"`
	Password          types.String `tfsdk:"password"`
	PasswordWO        types.String `tfsdk:"password_wo"`
	ConfigFile        types.String `tfsdk:"config_file"`
	ConfigFileContent types.String `tfsdk:"config_file_content"`
	AuthDisabled      types.Bool   `tfsdk:"auth_disabled"`
}

// providerData is handed to every resource, data source, action, ephemeral
// and list resource in Configure. Use providerDataFrom to unpack it, and
// APIVersion (api_version.go) for the negotiated Engine API version.
type providerData struct {
	Client       *client.Client
	Endpoint     endpointConfig
	RegistryAuth []registryCredential
	Version      string
	// MinAPIVersion is min_api_version (default DefaultMinAPIVersion); ""
	// (test fixtures) means no floor.
	MinAPIVersion string

	// apiMu guards apiVersion, the negotiated version APIVersion caches.
	apiMu      sync.Mutex
	apiVersion string
}

func (p *dockerProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "docker"
	resp.Version = p.version
}

// tlsBlockSchema is the provider's tls block. docker_swarm_node builds the same
// shape for its own endpoint from resource/schema types.
func tlsBlockSchema() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: "TLS client material for a `tcp://` host. Each value is PEM content or a path to a PEM file. Conflicts with `cert_path`. " +
			"When neither is set, TLS material comes from the Docker context, else (with `DOCKER_TLS_VERIFY` set) `ca.pem`, `cert.pem` and " +
			"`key.pem` are read from `DOCKER_CERT_PATH`.",
		Attributes: map[string]schema.Attribute{
			"ca":   schema.StringAttribute{Optional: true, MarkdownDescription: "CA certificate (PEM or path)."},
			"cert": schema.StringAttribute{Optional: true, MarkdownDescription: "Client certificate (PEM or path)."},
			"key":  schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Client private key (PEM or path)."},
		},
	}
}

func conflictsWithSibling(name string) []validator.String {
	return []validator.String{stringvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName(name))}
}

func (p *dockerProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Owns the whole Docker chain through the Docker Engine API: image build and push, " +
			"single-host networks, volumes and containers, Swarm init and node join, and Swarm secrets, configs, services and stacks. " +
			"Compose files are parsed in-process (docker_compose) and never handed to the compose plugin; no docker CLI is involved; SSH hosts are reached in pure Go. The one external program the provider " +
			"can run is a docker credential helper, and only with `allow_credential_helpers = true`.\n\n" +
			"Endpoint precedence: `host`, then the `ssh` block's `host`, then `context`, then `DOCKER_HOST`, then `DOCKER_CONTEXT`, " +
			"then `currentContext` in the docker `config.json`, then `" + defaultDockerHost + "`.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Docker daemon address, e.g. `unix:///var/run/docker.sock`, `npipe:////./pipe/docker_engine`, `tcp://10.0.0.5:2376` " +
					"or `ssh://user@10.0.0.5[:port]` (see the `ssh` block). Environment: `DOCKER_HOST`. Wins over `context`. " +
					"Default: the Docker context, else `" + defaultDockerHost + "`.",
			},
			"api_version": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Engine API version to pin, e.g. `1.47`. Environment: `DOCKER_API_VERSION`. Default: negotiated with the daemon.",
			},
			"context": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Name of a Docker CLI context (as created by `docker context create`), read from `contexts/` under " +
					"`DOCKER_CONFIG` (default `~/.docker`). It supplies the host, TLS material and `skip-tls-verify`; an `ssh://` context host " +
					"uses the pure-Go `ssh` transport (the `ssh` block can add credentials). An explicitly set `host` wins. `default` means " +
					"no context. Environment: `DOCKER_CONTEXT` (applies only when `DOCKER_HOST` is unset), then `currentContext` in `config.json`.",
			},
			"cert_path": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Directory holding `ca.pem`, `cert.pem` and `key.pem` for a TLS `tcp://` host: a shortcut for the `tls` " +
					"block, and mutually exclusive with it (checked at plan time). `~` expands to the home directory.",
			},
			"disable_docker_daemon_check": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Skip the daemon reachability check. When `false` (default), Configure pings the daemon " +
					"(`GET /_ping`, " + daemonCheckTimeout.String() + " timeout, plus `ssh.connect_timeout` for SSH hosts) and reports a " +
					"*warning* if it does not answer; it never fails validate or plan. The client itself is lazy either way: no other " +
					"daemon call happens until a resource or data source needs one. Set `true` to skip the ping (no delay when the " +
					"daemon is expected to be absent at plan time).",
			},
			"min_api_version": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Oldest Docker Engine API version the provider accepts from the daemon, in the form `1.NN`. " +
					"Default: `" + DefaultMinAPIVersion + "` (Docker Engine 25.0). It cannot be above `" + client.MaxAPIVersion + "`, the newest " +
					"version this provider speaks. When Configure pings the daemon and the negotiated version is older, configuration fails with " +
					"an error naming both versions; with `disable_docker_daemon_check = true` (or an unreachable daemon) the same error comes " +
					"from the first operation that asks for the version. A pinned `api_version` below it is rejected. There is no environment " +
					"variable: the docker CLI has no API floor setting to mirror.",
			},
			"allow_credential_helpers": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Let `registry_auth` run the docker credential helpers (`docker-credential-<name>`) named by " +
					"`credsStore` / `credHelpers` in a docker `config.json`. Default `false`: the provider never starts another program, " +
					"and a registry whose credentials only live in a helper is an error naming this attribute. This is the provider's one " +
					"documented exception to running nothing external.",
			},
		},
		Blocks: map[string]schema.Block{
			"tls": tlsBlockSchema(),
			"ssh": sshProviderBlockSchema(),
			"registry_auth": schema.ListNestedBlock{
				MarkdownDescription: "Credentials for registries, used for pulls and pushes. Sources, first match wins: `username` with " +
					"`password`/`password_wo`; `DOCKER_REGISTRY_USER`/`DOCKER_REGISTRY_PASS` (when no config file is named); " +
					"`config_file_content`; `config_file`; the default config (`DOCKER_CONFIG`, a directory or a file, else " +
					"`~/.docker/config.json`). In a config file, `credHelpers` and `credsStore` are honored like the docker CLI " +
					"(needs `allow_credential_helpers`), falling back to `auths` entries.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"address": schema.StringAttribute{Required: true, MarkdownDescription: "Registry host, e.g. `registry-1.docker.io` or `ghcr.io`."},
						"username": schema.StringAttribute{Optional: true,
							MarkdownDescription: "Registry user. Environment: `DOCKER_REGISTRY_USER` (when no config file is named)."},
						"password": schema.StringAttribute{Optional: true, Sensitive: true, Validators: conflictsWithSibling("password_wo"),
							MarkdownDescription: "Registry password or token. Environment: `DOCKER_REGISTRY_PASS`. Conflicts with `password_wo`."},
						"password_wo": schema.StringAttribute{Optional: true, Sensitive: true, Validators: conflictsWithSibling("password"),
							MarkdownDescription: "Registry password or token, the write-only spelling of `password` (conflicts with it). Provider " +
								"configuration is never stored in state, so both are equally ephemeral here; Terraform's write-only marker only " +
								"exists for resource schemas."},
						"config_file": schema.StringAttribute{Optional: true, Validators: conflictsWithSibling("config_file_content"),
							MarkdownDescription: "Path to a docker `config.json`. `~` expands to the home directory. Default: `DOCKER_CONFIG` " +
								"(directory or file), else `~/.docker/config.json`. Conflicts with `config_file_content`."},
						"config_file_content": schema.StringAttribute{Optional: true, Sensitive: true, Validators: conflictsWithSibling("config_file"),
							MarkdownDescription: "Content of a docker `config.json` (e.g. `file(\"~/.docker/config.json\")` or a secret). Conflicts with `config_file`."},
						"auth_disabled": schema.BoolAttribute{Optional: true,
							MarkdownDescription: "Access this registry anonymously: no credentials are looked up or sent. Default `false`."},
					},
				},
			},
		},
	}
}

// ValidateConfig runs at validate and plan time, without a daemon.
func (p *dockerProvider) ValidateConfig(ctx context.Context, req provider.ValidateConfigRequest, resp *provider.ValidateConfigResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateProviderModel(m, &resp.Diagnostics)
}

func validateProviderModel(m providerModel, diags *diag.Diagnostics) {
	if m.TLS != nil && !m.CertPath.IsNull() {
		diags.AddAttributeError(path.Root("cert_path"), "Conflicting TLS configuration",
			"cert_path and the tls block are mutually exclusive: use cert_path for a directory of ca.pem/cert.pem/key.pem, or the tls block for individual files or PEM content.")
	}
	if m.SSH != nil && !m.SSH.ConnectTimeout.IsNull() && !m.SSH.ConnectTimeout.IsUnknown() {
		if d, err := time.ParseDuration(m.SSH.ConnectTimeout.ValueString()); err != nil || d <= 0 {
			diags.AddAttributeError(path.Root("ssh").AtName("connect_timeout"), "Invalid connect_timeout",
				fmt.Sprintf("%q is not a positive Go duration such as 10s or 1m.", m.SSH.ConnectTimeout.ValueString()))
		}
	}
	if m.SSH != nil && !m.SSH.ProxyJump.IsNull() && !m.SSH.ProxyJump.IsUnknown() {
		if _, err := (sshConfig{User: "x", Host: "x", Port: 22, ProxyJump: m.SSH.ProxyJump.ValueString()}).hops(); err != nil {
			diags.AddAttributeError(path.Root("ssh").AtName("proxy_jump"), "Invalid proxy_jump", err.Error())
		}
	}
}

// resolveEndpoint works out the daemon endpoint from the provider block, the
// environment and Docker contexts. See the provider description for the
// precedence order.
func resolveEndpoint(m providerModel, getenv func(string) string, diags *diag.Diagnostics) endpointConfig {
	str := func(v types.String, attr, env string) string {
		if v.IsUnknown() {
			hint := "Set it to a literal"
			if env != "" {
				hint += " or use the " + env + " environment variable"
			}
			diags.AddAttributeError(path.Root(attr), "Unknown docker provider argument",
				fmt.Sprintf("%s is not known until apply. %s.", attr, hint))
			return ""
		}
		return v.ValueString()
	}
	host := str(m.Host, "host", "DOCKER_HOST")
	api := str(m.APIVersion, "api_version", "DOCKER_API_VERSION")
	ctxName := str(m.Context, "context", "DOCKER_CONTEXT")
	certPath := str(m.CertPath, "cert_path", "DOCKER_CERT_PATH")
	if api == "" {
		api = getenv("DOCKER_API_VERSION")
	}
	sshBlockHost := ""
	if m.SSH != nil {
		sshBlockHost = knownStr(m.SSH.Host)
	}
	ep := endpointConfig{APIVersion: api}

	var dc *dockerContextEndpoint
	if host == "" && sshBlockHost == "" {
		// An explicit context beats DOCKER_HOST, except "default", which
		// is the DOCKER_HOST-or-default-socket context.
		if ctxName == "" || ctxName == "default" {
			host = getenv("DOCKER_HOST")
		}
		if host == "" && ctxName == "" {
			ctxName = getenv("DOCKER_CONTEXT")
			if ctxName == "" {
				ctxName = currentDockerContext(dockerConfigDir(getenv))
			}
		}
		if host == "" {
			var err error
			dc, err = loadDockerContext(dockerConfigDir(getenv), ctxName)
			if err != nil {
				diags.AddAttributeError(path.Root("context"), "Cannot load Docker context", err.Error())
			}
		}
	}
	if dc != nil {
		host = dc.Host
		ep.Context = dc.Name
	}
	if host == "" && sshBlockHost == "" {
		host = defaultDockerHost
	}
	ep.Host = host

	switch {
	case m.TLS != nil:
		ep.CA = knownStr(m.TLS.CA)
		ep.Cert = knownStr(m.TLS.Cert)
		ep.Key = knownStr(m.TLS.Key)
	case certPath != "":
		dir := expandHome(certPath)
		ep.CA = filepath.Join(dir, "ca.pem")
		ep.Cert = filepath.Join(dir, "cert.pem")
		ep.Key = filepath.Join(dir, "key.pem")
	case dc != nil:
		ep.CA, ep.Cert, ep.Key = string(dc.CA), string(dc.Cert), string(dc.Key)
		ep.SkipTLSVerify = dc.SkipTLSVerify
	case getenv("DOCKER_TLS_VERIFY") != "" && getenv("DOCKER_CERT_PATH") != "":
		dir := getenv("DOCKER_CERT_PATH")
		ep.CA = filepath.Join(dir, "ca.pem")
		ep.Cert = filepath.Join(dir, "cert.pem")
		ep.Key = filepath.Join(dir, "key.pem")
	}

	if m.SSH != nil || isSSHHost(host) {
		if host != "" && !isSSHHost(host) {
			diags.AddAttributeError(path.Root("ssh"), "ssh block needs an ssh:// host",
				fmt.Sprintf("The Docker host resolved to %q. With an ssh block, leave host unset (and set ssh.host) or use an ssh://user@host host.", host))
			return ep
		}
		sc, err := sshConfigFrom(m.SSH, host, getenv)
		if err != nil {
			diags.AddAttributeError(path.Root("ssh"), "Invalid SSH endpoint", err.Error())
			return ep
		}
		ep.SSH = sc
		ep.Host = sc.dockerHost()
	}
	return ep
}

func (p *dockerProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateProviderModel(m, &resp.Diagnostics)
	ep := resolveEndpoint(m, os.Getenv, &resp.Diagnostics)
	minAPI := resolveMinAPIVersion(m, &resp.Diagnostics)
	checkPinnedAPIFloor(m, ep, minAPI, &resp.Diagnostics)
	allowHelpers := m.AllowCredentialHelpers.ValueBool()
	if allowHelpers {
		tflog.Info(ctx, "allow_credential_helpers = true: docker-credential-* helpers named in docker config files may be run for registry_auth")
	}
	creds := resolveRegistryAuth(m.RegistryAuth, registryAuthOptions{Getenv: os.Getenv, AllowHelpers: allowHelpers}, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ep.UserAgent = "terraform-provider-docker/" + p.version
	// The client is lazy: no daemon round trip happens here, so validate and
	// plan work without a reachable daemon.
	c, err := newDockerClient(ep)
	if err != nil {
		resp.Diagnostics.AddError("Cannot build Docker client", fmt.Sprintf("host %q: %v", ep.Host, err))
		return
	}
	// With the check disabled no ping happens here; the floor then applies
	// on the first pd.APIVersion call.
	negotiated := ""
	if !m.DisableDockerDaemonCheck.ValueBool() {
		negotiated = checkDockerDaemon(ctx, c, ep, minAPI, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	tflog.Debug(ctx, "docker provider configured", map[string]any{"host": ep.Host, "context": ep.Context, "ssh": ep.SSH != nil,
		"min_api_version": minAPI, "api_version": negotiated})
	pd := &providerData{Client: c, Endpoint: ep, RegistryAuth: creds, Version: p.version, MinAPIVersion: minAPI, apiVersion: negotiated}
	resp.ResourceData = pd
	resp.DataSourceData = pd
	resp.EphemeralResourceData = pd
	resp.ActionData = pd
	resp.ListResourceData = pd
}

// resolveMinAPIVersion returns min_api_version: DefaultMinAPIVersion when
// unset, "" (with an error on the attribute) when unknown or malformed.
func resolveMinAPIVersion(m providerModel, diags *diag.Diagnostics) string {
	switch {
	case m.MinAPIVersion.IsUnknown():
		diags.AddAttributeError(path.Root("min_api_version"), "Unknown docker provider argument",
			"min_api_version is not known until apply. Set it to a literal.")
		return ""
	case m.MinAPIVersion.IsNull():
		return DefaultMinAPIVersion
	}
	v := m.MinAPIVersion.ValueString()
	if err := validateMinAPIVersion(v); err != nil {
		diags.AddAttributeError(path.Root("min_api_version"), "Invalid min_api_version", err.Error())
		return ""
	}
	return v
}

// checkPinnedAPIFloor rejects a pinned api_version (attribute or
// DOCKER_API_VERSION) below min_api_version: the client would never speak
// anything else, so every daemon call would be below the floor.
func checkPinnedAPIFloor(m providerModel, ep endpointConfig, floor string, diags *diag.Diagnostics) {
	if floor == "" || ep.APIVersion == "" || !apiVersionPattern.MatchString(ep.APIVersion) {
		return
	}
	if checkAPIFloor(ep.Host, ep.APIVersion, floor) == nil {
		return
	}
	msg := fmt.Sprintf("api_version pins Engine API %s, below min_api_version %s. Raise api_version, or lower min_api_version.", ep.APIVersion, floor)
	if knownStr(m.APIVersion) != "" {
		diags.AddAttributeError(path.Root("api_version"), "api_version below min_api_version", msg)
		return
	}
	diags.AddError("DOCKER_API_VERSION below min_api_version",
		fmt.Sprintf("DOCKER_API_VERSION pins Engine API %s, below min_api_version %s. Unset or raise DOCKER_API_VERSION, or lower min_api_version.", ep.APIVersion, floor))
}

// checkDockerDaemon pings the daemon once, negotiating the Engine API
// version, and returns the negotiated version ("" when the ping failed). A
// failed ping is a warning: an unreachable daemon must never fail validate or
// plan. A daemon that answers with a version below floor ("" = no floor) is
// an error naming both versions.
func checkDockerDaemon(ctx context.Context, c *client.Client, ep endpointConfig, floor string, diags *diag.Diagnostics) string {
	timeout := daemonCheckTimeout
	if ep.SSH != nil {
		hops, _ := ep.SSH.hops()
		timeout += ep.SSH.ConnectTimeout * time.Duration(len(hops))
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := c.Ping(pctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		diags.AddWarning("Docker daemon not reachable",
			fmt.Sprintf("Pinging %s failed: %v\n\nPlanning continues; operations that need the daemon will fail until it is reachable. "+
				"Set disable_docker_daemon_check = true to skip this check.", ep.Host, err))
		return ""
	}
	v := c.ClientVersion()
	if err := checkAPIFloor(ep.Host, v, floor); err != nil {
		diags.AddAttributeError(path.Root("min_api_version"), "Docker daemon too old", err.Error())
		return ""
	}
	return v
}

// ===== BEGIN LANE REGISTRATIONS =====
// Each lane owns one register_<lane>.go file and returns its constructors
// from the two functions it defines there. Do not register resources here
// directly; add a new lane function call instead.
func (p *dockerProvider) Resources(_ context.Context) []func() resource.Resource {
	var r []func() resource.Resource
	r = append(r, imageLaneResources()...)    // lane 1: register_image.go
	r = append(r, composeLaneResources()...)  // lane 2: register_compose.go
	r = append(r, swarmLaneResources()...)    // lane 3: register_swarm.go
	r = append(r, workloadLaneResources()...) // lane 4: register_workload.go
	r = append(r, composeFileResources()...)  // pass 3 lane B: register_composefile.go
	r = append(r, p3ContainerResources()...)  // pass 3 lane C: register_p3_container.go
	return r
}

func (p *dockerProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	var d []func() datasource.DataSource
	d = append(d, imageLaneDataSources()...)    // lane 1
	d = append(d, composeLaneDataSources()...)  // lane 2
	d = append(d, swarmLaneDataSources()...)    // lane 3
	d = append(d, workloadLaneDataSources()...) // lane 4
	d = append(d, p3ContainerDataSources()...)  // pass 3 lane C: register_p3_container.go
	d = append(d, p3ParityDataSources()...)     // pass 3 lane E: register_p3_datasources.go
	return d
}

// Pass 3: Terraform 1.14 surface. Same rule: one register file per lane.
func (p *dockerProvider) Actions(_ context.Context) []func() action.Action {
	return laneActions() // pass 3 lane D: register_actions.go
}

func (p *dockerProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return laneEphemeralResources() // pass 3 lane E: register_ephemeral.go
}

func (p *dockerProvider) Functions(_ context.Context) []func() function.Function {
	return laneFunctions() // pass 3 lane E: register_functions.go
}

func (p *dockerProvider) ListResources(_ context.Context) []func() list.ListResource {
	return laneListResources() // pass 3 lane E: register_lists.go
}

// ===== END LANE REGISTRATIONS =====

// providerDataFrom extracts the data handed over by Configure. data is nil
// before Configure runs (validation); callers then return without error.
func providerDataFrom(data any, diags *diag.Diagnostics) *providerData {
	if data == nil {
		return nil
	}
	pd, ok := data.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *providerData, got %T. This is a provider bug.", data))
		return nil
	}
	return pd
}
