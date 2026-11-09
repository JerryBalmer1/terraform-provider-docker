package provider

// docker_registry_login (DECISIONS Q7): Engine SystemAuth (POST /auth).

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

// regLoginItem is the inventory item of docker_registry_login.
const regLoginItem = "engine:registry-auth"

// regLoginDefaultServer is the server address the docker CLI logs in to
// when none is given (Docker Hub).
const regLoginDefaultServer = "https://index.docker.io/v1/"

var (
	_ ephemeral.EphemeralResource              = (*regLoginEphemeral)(nil)
	_ ephemeral.EphemeralResourceWithConfigure = (*regLoginEphemeral)(nil)
)

// NewRegistryLoginEphemeralResource returns the docker_registry_login
// ephemeral resource.
func NewRegistryLoginEphemeralResource() ephemeral.EphemeralResource {
	return &regLoginEphemeral{}
}

type regLoginEphemeral struct {
	pd *providerData
}

type regLoginModel struct {
	ServerAddress types.String `tfsdk:"server_address"`
	Username      types.String `tfsdk:"username"`
	Password      types.String `tfsdk:"password"`
	contractEngineRegistryAuth
}

func (e *regLoginEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_login"
}

func (e *regLoginEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Validates registry credentials through the daemon (`POST /auth`, like `docker login`) and returns the identity token the registry issues, if any. " +
			"It is ephemeral: the token and the response are never written to plan or state. Nothing is stored on the daemon or in a docker config file. " +
			"Without `username`, the provider's matching `registry_auth` entry is used.",
		Attributes: contractAddEphemeralAttributes(map[string]schema.Attribute{
			"server_address": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Registry to log in to, e.g. `ghcr.io` or `localhost:5000`. Default `" + regLoginDefaultServer + "` (Docker Hub), as for `docker login`.",
			},
			"username": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "User name. Omitted: the user name of the provider's `registry_auth` entry for `server_address`.",
			},
			"password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Password or access token. Omitted: the password of the provider's `registry_auth` entry for `server_address`.",
			},
		}, regLoginItem),
	}
}

func (e *regLoginEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	e.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (e *regLoginEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	if e.pd == nil || e.pd.Client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var m regLoginModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !e.pd.gateItemAPI(ctx, regLoginItem, &resp.Diagnostics) {
		return
	}
	server := m.ServerAddress.ValueString()
	if server == "" {
		server = regLoginDefaultServer
	}
	user, pass := m.Username.ValueString(), m.Password.ValueString()
	if user == "" {
		user, pass = e.pd.p3RegistryCreds(server)
	}
	if user == "" {
		resp.Diagnostics.AddAttributeError(path.Root("username"), "No credentials",
			fmt.Sprintf("Set username and password, or add a provider registry_auth entry for %s.", server))
		return
	}
	res, err := e.pd.Client.RegistryLogin(ctx, client.RegistryLoginOptions{Username: user, Password: pass, ServerAddress: server})
	if err != nil {
		resp.Diagnostics.AddError("Registry login failed", fmt.Sprintf("%s as %s: %v", server, user, err))
		return
	}
	// RegistryLogin keeps no body: auth_json is the decoded /auth response
	// re-encoded in canonical form (rCanonicalJSON).
	raw, err := rCanonicalJSON(res.Auth)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode the login response", err.Error())
		return
	}
	m.ServerAddress = types.StringValue(server)
	m.Username = types.StringValue(user)
	resp.Diagnostics.Append(m.contractFill(ctx, e.pd, contractFillInput{Raw: map[string][]byte{"auth_json": raw}})...)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
}
