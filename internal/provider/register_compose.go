package provider

// Lane 2 owns this file. Return every constructor this lane implements.

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func composeLaneResources() []func() resource.Resource {
	return []func() resource.Resource{
		NewNetworkResource,
		NewVolumeResource,
		NewContainerResource,
	}
}

func composeLaneDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewNetworkDataSource,
		NewVolumeDataSource,
	}
}
