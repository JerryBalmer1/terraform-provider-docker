package provider

// Pass 3 Lane B owns this file. Return every constructor this lane implements.

import "github.com/hashicorp/terraform-plugin-framework/resource"

func composeFileResources() []func() resource.Resource {
	return []func() resource.Resource{
		NewComposeResource,
	}
}
