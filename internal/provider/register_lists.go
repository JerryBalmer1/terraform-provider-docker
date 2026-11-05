package provider

// Pass 3 Lane E owns this file. Return every list resource constructor.

import "github.com/hashicorp/terraform-plugin-framework/list"

func laneListResources() []func() list.ListResource {
	return []func() list.ListResource{
		NewContainerListResource,
		NewImageListResource,
		NewNetworkListResource,
		NewVolumeListResource,
		NewSwarmNodeListResource,
		NewServiceListResource,
		NewSecretListResource,
		NewConfigListResource,
	}
}
