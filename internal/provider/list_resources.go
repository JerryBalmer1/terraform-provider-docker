package provider

// The eight list resources (terraform query). Each one shares its type name
// with the managed resource it lists; see list_common.go.

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/list"
	"github.com/moby/moby/client"
)

// NewContainerListResource lists containers as docker_container.
func NewContainerListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_container",
		description: "Lists containers on the provider's Docker host as `docker_container` resources, for `terraform query` and bulk import. " +
			"Every container is listed (running or not) unless `all = false`; filters map to the Engine API `GET /containers/json` filters.",
		filters: []listFilter{
			listNameFilter("containers"),
			listLabelsFilter("containers", "label"),
			{Attr: "status", Key: "status", Kind: listFilterString, OneOf: []string{"created", "restarting", "running", "removing", "paused", "exited", "dead"},
				Description: "Only containers in this state: `created`, `restarting`, `running`, `removing`, `paused`, `exited` or `dead`."},
			{Attr: "ancestor", Key: "ancestor", Kind: listFilterString,
				Description: "Only containers created from this image (name, name:tag, ID or a descendant of it)."},
			{Attr: "network", Key: "network", Kind: listFilterString, Description: "Only containers connected to this network (name or ID)."},
			{Attr: "all", Kind: listFilterBool,
				Description: "List stopped containers too. Default `true`; `false` lists only running containers, like `docker ps`."},
		},
		newResource: NewContainerResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			all := true
			if v, ok := q.Bools["all"]; ok {
				all = v
			}
			csRes, err := c.ContainerList(ctx, client.ContainerListOptions{All: all, Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			cs := csRes.Items
			out := make([]listItem, 0, len(cs))
			for _, s := range cs {
				name := listShortID(s.ID)
				if len(s.Names) > 0 {
					name = strings.TrimPrefix(s.Names[0], "/")
				}
				out = append(out, listItem{ID: s.ID, DisplayName: name, ImportID: s.ID})
			}
			return out, nil
		},
	}
}

// NewImageListResource lists local images as docker_image.
func NewImageListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_image",
		description: "Lists images in the provider's Docker host image store as `docker_image` resources, for `terraform query` and bulk import. " +
			"Each image appears once, named by its first tag; filters map to the Engine API `GET /images/json` filters.",
		filters: []listFilter{
			{Attr: "reference", Key: "reference", Kind: listFilterString,
				Description: "Only images whose reference matches this pattern, e.g. `nginx` or `ghcr.io/org/*:1.*`."},
			listLabelsFilter("images", "label"),
			{Attr: "dangling", Key: "dangling", Kind: listFilterBool, Description: "`true` lists only untagged images, `false` only tagged ones."},
		},
		newResource: NewImageResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			imgsRes, err := c.ImageList(ctx, client.ImageListOptions{Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			imgs := imgsRes.Items
			out := make([]listItem, 0, len(imgs))
			for _, s := range imgs {
				it := listItem{ID: s.ID, DisplayName: listShortID(s.ID), ImportID: s.ID}
				for _, t := range s.RepoTags {
					if t != "" && t != "<none>:<none>" {
						it.DisplayName, it.ImportID = t, t
						break
					}
				}
				out = append(out, it)
			}
			return out, nil
		},
	}
}

// NewNetworkListResource lists networks as docker_network.
func NewNetworkListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_network",
		description: "Lists networks on the provider's Docker host as `docker_network` resources, for `terraform query` and bulk import. " +
			"Filters map to the Engine API `GET /networks` filters; the predefined `bridge`, `host` and `none` networks are included unless `type = \"custom\"`.",
		filters: []listFilter{
			listNameFilter("networks"),
			listLabelsFilter("networks", "label"),
			{Attr: "driver", Key: "driver", Kind: listFilterString, Description: "Only networks using this driver, e.g. `bridge` or `overlay`."},
			{Attr: "scope", Key: "scope", Kind: listFilterString, OneOf: []string{"local", "swarm", "global"},
				Description: "Only networks of this scope: `local`, `swarm` or `global`."},
			{Attr: "type", Key: "type", Kind: listFilterString, OneOf: []string{"custom", "builtin"},
				Description: "`custom` lists only user-defined networks, `builtin` only the predefined ones."},
		},
		newResource: NewNetworkResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			nsRes, err := c.NetworkList(ctx, client.NetworkListOptions{Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			ns := nsRes.Items
			out := make([]listItem, 0, len(ns))
			for _, n := range ns {
				out = append(out, listItem{ID: n.ID, DisplayName: n.Name, ImportID: n.ID})
			}
			return out, nil
		},
	}
}

// NewVolumeListResource lists volumes as docker_volume.
func NewVolumeListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_volume",
		description: "Lists volumes on the provider's Docker host as `docker_volume` resources, for `terraform query` and bulk import. " +
			"A volume's identity `id` is its name; filters map to the Engine API `GET /volumes` filters.",
		filters: []listFilter{
			listNameFilter("volumes"),
			listLabelsFilter("volumes", "label"),
			{Attr: "driver", Key: "driver", Kind: listFilterString, Description: "Only volumes using this driver, e.g. `local`."},
			{Attr: "dangling", Key: "dangling", Kind: listFilterBool, Description: "`true` lists only volumes no container uses, `false` only used ones."},
		},
		newResource: NewVolumeResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			vsRes, err := c.VolumeList(ctx, client.VolumeListOptions{Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			vs := vsRes.Items
			out := make([]listItem, 0, len(vs))
			for _, v := range vs {
				out = append(out, listItem{ID: v.Name, DisplayName: v.Name, ImportID: v.Name})
			}
			return out, nil
		},
	}
}

// NewSwarmNodeListResource lists swarm nodes as docker_swarm_node.
func NewSwarmNodeListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_swarm_node",
		description: "Lists the swarm's nodes as `docker_swarm_node` resources, for `terraform query` and bulk import; the provider's host must be a swarm manager. " +
			"Listed nodes import like `terraform import docker_swarm_node`: set `host` afterwards if Terraform should be able to make the node leave.",
		filters: []listFilter{
			listNameFilter("nodes"),
			listLabelsFilter("nodes", "node.label"),
			{Attr: "role", Key: "role", Kind: listFilterString, OneOf: swarmValidRoles, Description: "Only nodes with this role: `worker` or `manager`."},
			{Attr: "membership", Key: "membership", Kind: listFilterString, OneOf: []string{"accepted", "pending"},
				Description: "Only nodes with this membership: `accepted` or `pending`."},
		},
		newResource: NewSwarmNodeResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			nsRes, err := c.NodeList(ctx, client.NodeListOptions{Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			ns := nsRes.Items
			out := make([]listItem, 0, len(ns))
			for _, n := range ns {
				name := n.Description.Hostname
				if name == "" {
					name = n.ID
				}
				out = append(out, listItem{ID: n.ID, DisplayName: name, ImportID: n.ID})
			}
			return out, nil
		},
	}
}

// NewServiceListResource lists swarm services as docker_service.
func NewServiceListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_service",
		description: "Lists swarm services as `docker_service` resources, for `terraform query` and bulk import; the provider's host must be a swarm manager. " +
			"Filters map to the Engine API `GET /services` filters.",
		filters: []listFilter{
			listNameFilter("services"),
			listLabelsFilter("services", "label"),
			{Attr: "mode", Key: "mode", Kind: listFilterString, OneOf: []string{"replicated", "global"},
				Description: "Only services in this mode: `replicated` or `global`."},
		},
		newResource: NewServiceResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			ssRes, err := c.ServiceList(ctx, client.ServiceListOptions{Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			ss := ssRes.Items
			out := make([]listItem, 0, len(ss))
			for _, s := range ss {
				out = append(out, listItem{ID: s.ID, DisplayName: s.Spec.Name, ImportID: s.ID})
			}
			return out, nil
		},
	}
}

// NewSecretListResource lists swarm secrets as docker_secret.
func NewSecretListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_secret",
		description: "Lists swarm secrets as `docker_secret` resources, for `terraform query` and bulk import; the provider's host must be a swarm manager. " +
			"The daemon never returns secret data, so listed resource objects carry no `data`.",
		filters: []listFilter{
			listNameFilter("secrets"),
			listLabelsFilter("secrets", "label"),
		},
		newResource: NewSecretResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			ssRes, err := c.SecretList(ctx, client.SecretListOptions{Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			ss := ssRes.Items
			out := make([]listItem, 0, len(ss))
			for _, s := range ss {
				out = append(out, listItem{ID: s.ID, DisplayName: s.Spec.Name, ImportID: s.ID})
			}
			return out, nil
		},
	}
}

// NewConfigListResource lists swarm configs as docker_config.
func NewConfigListResource() list.ListResource {
	return &dockerListResource{
		typeSuffix: "_config",
		description: "Lists swarm configs as `docker_config` resources, for `terraform query` and bulk import; the provider's host must be a swarm manager. " +
			"Filters map to the Engine API `GET /configs` filters.",
		filters: []listFilter{
			listNameFilter("configs"),
			listLabelsFilter("configs", "label"),
		},
		newResource: NewConfigResource,
		fetch: func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error) {
			csRes, err := c.ConfigList(ctx, client.ConfigListOptions{Filters: q.Filters})
			if err != nil {
				return nil, err
			}
			cs := csRes.Items
			out := make([]listItem, 0, len(cs))
			for _, s := range cs {
				out = append(out, listItem{ID: s.ID, DisplayName: s.Spec.Name, ImportID: s.ID})
			}
			return out, nil
		},
	}
}
