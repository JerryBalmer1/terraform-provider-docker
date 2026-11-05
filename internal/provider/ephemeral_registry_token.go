package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ ephemeral.EphemeralResource              = (*registryTokenEphemeral)(nil)
	_ ephemeral.EphemeralResourceWithConfigure = (*registryTokenEphemeral)(nil)
)

// NewRegistryTokenEphemeralResource returns the docker_registry_token ephemeral resource.
func NewRegistryTokenEphemeralResource() ephemeral.EphemeralResource {
	return &registryTokenEphemeral{}
}

type registryTokenEphemeral struct {
	pd *providerData
}

type registryTokenModel struct {
	Registry           types.String `tfsdk:"registry"`
	Scope              types.String `tfsdk:"scope"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	Token              types.String `tfsdk:"token"`
	ExpiresIn          types.Int64  `tfsdk:"expires_in"`
	IssuedAt           types.String `tfsdk:"issued_at"`
	ExpiresAt          types.String `tfsdk:"expires_at"`
	Realm              types.String `tfsdk:"realm"`
	Service            types.String `tfsdk:"service"`
}

func (e *registryTokenEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_token"
}

func (e *registryTokenEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Obtains a short-lived registry bearer token, as an ephemeral value that is never written to plan or state. " +
			"The provider pings `GET /v2/` on the registry, follows its `WWW-Authenticate: Bearer` challenge to the token service and " +
			"authenticates with the matching `registry_auth` entry (anonymous when none matches). Registries that use Basic auth issue no token and are an error.",
		Attributes: map[string]schema.Attribute{
			"registry": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Registry host, e.g. `ghcr.io`, `localhost:5000` or `docker.io` (Docker Hub).",
			},
			"scope": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Token scope, e.g. `repository:org/app:pull,push`; several scopes separated by spaces. " +
					"Omitted: the token carries no repository scope (enough for the catalog on some registries, or to prove the credentials).",
			},
			"insecure_skip_verify": schema.BoolAttribute{Optional: true, MarkdownDescription: "Skip TLS certificate verification of the registry and token service."},
			"token":                schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "The bearer token. Send it as `Authorization: Bearer <token>`."},
			"expires_in":           schema.Int64Attribute{Computed: true, MarkdownDescription: "Token lifetime in seconds (60 when the token service does not say)."},
			"issued_at":            schema.StringAttribute{Computed: true, MarkdownDescription: "RFC 3339 time the token was issued."},
			"expires_at":           schema.StringAttribute{Computed: true, MarkdownDescription: "RFC 3339 time the token expires."},
			"realm":                schema.StringAttribute{Computed: true, MarkdownDescription: "Token service URL from the challenge."},
			"service":              schema.StringAttribute{Computed: true, MarkdownDescription: "Service name from the challenge."},
		},
	}
}

func (e *registryTokenEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	e.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (e *registryTokenEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var m registryTokenModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	reg := m.Registry.ValueString()
	if reg == "" {
		resp.Diagnostics.AddAttributeError(path.Root("registry"), "Missing registry", "registry must be a registry host.")
		return
	}
	r := newP3Registry(e.pd, reg, "", m.InsecureSkipVerify.ValueBool())
	tok, err := p3RegistryToken(ctx, r, m.Scope.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot obtain registry token", fmt.Sprintf("%s: %v", reg, err))
		return
	}
	m.Token = types.StringValue(tok.Token)
	m.ExpiresIn = types.Int64Value(tok.ExpiresIn)
	m.IssuedAt = types.StringValue(tok.IssuedAt.Format(time.RFC3339))
	m.ExpiresAt = types.StringValue(tok.IssuedAt.Add(time.Duration(tok.ExpiresIn) * time.Second).Format(time.RFC3339))
	m.Realm = types.StringValue(tok.Realm)
	m.Service = types.StringValue(tok.Service)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
}
