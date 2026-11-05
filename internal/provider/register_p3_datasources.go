package provider

// Pass 3 Lane E owns this file: data source parity with kreuzwerker v4.6.0.

import "github.com/hashicorp/terraform-plugin-framework/datasource"

func p3ParityDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewP3ContainersDataSource,
		NewP3LogsDataSource,
		NewP3RegistryImageManifestsDataSource,
		NewP3RegistryImageTagsDataSource,
		NewP3ServicesDataSource,
		NewP3SecretsDataSource,
		NewP3ConfigsDataSource,
	}
}
