package provider

// Pass 3 Lane C owns this file. Return every constructor this lane implements.

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func p3ContainerResources() []func() resource.Resource {
	return []func() resource.Resource{
		NewTagResource,           // docker_tag
		NewPluginResource,        // docker_plugin
		NewBuildxBuilderResource, // docker_buildx_builder
	}
}

func p3ContainerDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewPluginDataSource, // docker_plugin
	}
}
