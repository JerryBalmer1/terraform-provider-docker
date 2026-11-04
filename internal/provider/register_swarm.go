package provider

// Lane 3 owns this file. Return every constructor this lane implements.

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func swarmLaneResources() []func() resource.Resource {
	return []func() resource.Resource{
		NewSwarmInitResource,
		NewSwarmNodeResource,
	}
}

func swarmLaneDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewSwarmTokensDataSource,
		NewNodeDataSource,
		NewNodesDataSource,
	}
}
