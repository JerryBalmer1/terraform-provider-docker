package provider

// Lane 1 owns this file. Return every constructor this lane implements.

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func imageLaneResources() []func() resource.Resource {
	return []func() resource.Resource{NewImageResource, NewRegistryImageResource}
}

func imageLaneDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{NewImageDataSource, NewRegistryImageDataSource}
}
