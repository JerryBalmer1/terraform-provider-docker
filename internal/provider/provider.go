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

	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const defaultDockerHost = "unix:///var/run/docker.sock"

var _ provider.Provider = (*dockerProvider)(nil)

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
	Host         types.String        `tfsdk:"host"`
	APIVersion   types.String        `tfsdk:"api_version"`
	TLS          *tlsModel           `tfsdk:"tls"`
	RegistryAuth []registryAuthModel `tfsdk:"registry_auth"`
}

// tlsModel is shared by the provider block and by resources that open their
// own endpoint (docker_swarm_node). Each value is PEM content or a file path.
type tlsModel struct {
	CA   types.String `tfsdk:"ca"`
	Cert types.String `tfsdk:"cert"`
	Key  types.String `tfsdk:"key"`
}

type registryAuthModel struct {
	Address    types.String `tfsdk:"address"`
	Username   types.String `tfsdk:"username"`
	Password   types.String `tfsdk:"password"`
	ConfigFile types.String `tfsdk:"config_file"`
}

// providerData is handed to every resource and data source as
// ResourceData/DataSourceData. Use providerDataFrom to unpack it.
type providerData struct {
	Client       *client.Client
	Endpoint     endpointConfig
	RegistryAuth []registryCredential
	Version      string
}

func (p *dockerProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "docker"
	resp.Version = p.version
}

// tlsBlockSchema is the provider's tls block. docker_swarm_node builds the same
// shape for its own endpoint from resource/schema types.
func tlsBlockSchema() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		MarkdownDescription: "TLS client material for a `tcp://` host. Each value is PEM content or a path to a PEM file. " +
			"When the block is absent and `DOCKER_TLS_VERIFY` is set, `ca.pem`, `cert.pem` and `key.pem` are read from `DOCKER_CERT_PATH`.",
		Attributes: map[string]schema.Attribute{
			"ca":   schema.StringAttribute{Optional: true, MarkdownDescription: "CA certificate (PEM or path)."},
			"cert": schema.StringAttribute{Optional: true, MarkdownDescription: "Client certificate (PEM or path)."},
			"key":  schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Client private key (PEM or path)."},
		},
	}
}

func (p *dockerProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Owns the whole Docker chain through the Docker Engine API: image build and push, " +
			"single-host networks, volumes and containers, Swarm init and node join, and Swarm secrets, configs, services and stacks. " +
			"No compose files and no docker CLI are involved.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Docker daemon address, e.g. `unix:///var/run/docker.sock`, `npipe:////./pipe/docker_engine` or `tcp://10.0.0.5:2376`. " +
					"Environment: `DOCKER_HOST`. Default `" + defaultDockerHost + "`.",
			},
			"api_version": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Engine API version to pin, e.g. `1.47`. Environment: `DOCKER_API_VERSION`. Default: negotiated with the daemon.",
			},
		},
		Blocks: map[string]schema.Block{
			"tls": tlsBlockSchema(),
			"registry_auth": schema.ListNestedBlock{
				MarkdownDescription: "Credentials for registries, used for pulls and pushes. Give `username`/`password`, or `config_file` " +
					"(a docker `config.json`; its `auths` entry for `address` is used).",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"address":     schema.StringAttribute{Required: true, MarkdownDescription: "Registry host, e.g. `registry-1.docker.io` or `ghcr.io`."},
						"username":    schema.StringAttribute{Optional: true, MarkdownDescription: "Registry user."},
						"password":    schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Registry password or token."},
						"config_file": schema.StringAttribute{Optional: true, MarkdownDescription: "Path to a docker `config.json`. `~` expands to the home directory."},
					},
				},
			},
		},
	}
}

// resolveEndpoint applies config value, then environment, then default.
func resolveEndpoint(m providerModel, getenv func(string) string, diags *diag.Diagnostics) endpointConfig {
	str := func(v types.String, attr, env, def string) string {
		if v.IsUnknown() {
			diags.AddAttributeError(path.Root(attr), "Unknown docker provider argument",
				fmt.Sprintf("%s is not known until apply. Set it to a literal or use the %s environment variable.", attr, env))
			return ""
		}
		if !v.IsNull() && v.ValueString() != "" {
			return v.ValueString()
		}
		if e := getenv(env); e != "" {
			return e
		}
		return def
	}
	ep := endpointConfig{
		Host:       str(m.Host, "host", "DOCKER_HOST", defaultDockerHost),
		APIVersion: str(m.APIVersion, "api_version", "DOCKER_API_VERSION", ""),
	}
	if m.TLS != nil {
		ep.CA = m.TLS.CA.ValueString()
		ep.Cert = m.TLS.Cert.ValueString()
		ep.Key = m.TLS.Key.ValueString()
	} else if getenv("DOCKER_TLS_VERIFY") != "" && getenv("DOCKER_CERT_PATH") != "" {
		dir := getenv("DOCKER_CERT_PATH")
		ep.CA = filepath.Join(dir, "ca.pem")
		ep.Cert = filepath.Join(dir, "cert.pem")
		ep.Key = filepath.Join(dir, "key.pem")
	}
	return ep
}

func (p *dockerProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ep := resolveEndpoint(m, os.Getenv, &resp.Diagnostics)
	creds := resolveRegistryAuth(m.RegistryAuth, &resp.Diagnostics)
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
	pd := &providerData{Client: c, Endpoint: ep, RegistryAuth: creds, Version: p.version}
	resp.ResourceData = pd
	resp.DataSourceData = pd
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
	return r
}

func (p *dockerProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	var d []func() datasource.DataSource
	d = append(d, imageLaneDataSources()...)    // lane 1
	d = append(d, composeLaneDataSources()...)  // lane 2
	d = append(d, swarmLaneDataSources()...)    // lane 3
	d = append(d, workloadLaneDataSources()...) // lane 4
	return d
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
