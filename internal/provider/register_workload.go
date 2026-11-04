package provider

// Lane 4 owns this file. Return every constructor this lane implements.

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func workloadLaneResources() []func() resource.Resource {
	return []func() resource.Resource{
		NewSecretResource,
		NewDockerConfigResource,
		NewServiceResource,
		NewStackResource,
	}
}

func workloadLaneDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
