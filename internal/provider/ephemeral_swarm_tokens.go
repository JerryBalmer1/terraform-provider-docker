package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ ephemeral.EphemeralResource              = (*swarmTokensEphemeral)(nil)
	_ ephemeral.EphemeralResourceWithConfigure = (*swarmTokensEphemeral)(nil)
)

// NewSwarmTokensEphemeralResource returns the docker_swarm_tokens ephemeral resource.
func NewSwarmTokensEphemeralResource() ephemeral.EphemeralResource { return &swarmTokensEphemeral{} }

type swarmTokensEphemeral struct {
	pd *providerData
}

func (e *swarmTokensEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_swarm_tokens"
}

func (e *swarmTokensEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the worker and manager join tokens of the swarm the provider's Docker host manages, as ephemeral values that are never written to plan or state. " +
			"Use it (Terraform 1.10+) instead of the `docker_swarm_tokens` data source when the tokens may only flow into write-only arguments (such as `docker_secret.data_wo`), provider blocks or other ephemeral resources. The host must be a manager.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, MarkdownDescription: "Swarm cluster ID."},
			"worker":  schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Token that joins a node as a worker."},
			"manager": schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Token that joins a node as a manager."},
		},
	}
}

func (e *swarmTokensEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	e.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (e *swarmTokensEphemeral) Open(ctx context.Context, _ ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	if e.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	sw, err := e.pd.Client.SwarmInspect(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect swarm", "The provider's Docker host must be a swarm manager: "+err.Error())
		return
	}
	resp.Diagnostics.Append(resp.Result.Set(ctx, &swarmTokensModel{
		ID:      types.StringValue(sw.ID),
		Worker:  types.StringValue(sw.JoinTokens.Worker),
		Manager: types.StringValue(sw.JoinTokens.Manager),
	})...)
}
