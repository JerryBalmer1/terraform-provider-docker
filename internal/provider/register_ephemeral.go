package provider

// Pass 3 Lane E owns this file. Return every ephemeral resource constructor.

import "github.com/hashicorp/terraform-plugin-framework/ephemeral"

func laneEphemeralResources() []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewSwarmTokensEphemeralResource,
		NewRegistryTokenEphemeralResource,
		NewExecOutputEphemeralResource,
	}
}
