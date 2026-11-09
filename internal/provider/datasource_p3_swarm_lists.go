package provider

// docker_services, docker_secrets, docker_configs, docker_nodes and
// docker_tasks: plural swarm data sources sharing one implementation.
// Following the outputs contract (Q36, Q39) each returns the full Engine
// list objects as the structured `<type>` list (services, secrets, ...),
// its raw `<type>_json`, content_sha256, the `ids` / `names` lists and the
// version outputs.

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// p3SwarmFilter is one optional string input sent as an Engine list filter.
type p3SwarmFilter struct {
	attr, key, desc string
	oneOf           []string
}

// p3SwarmListDS is a plural swarm data source: filters in, the sorted
// Engine objects out.
type p3SwarmListDS struct {
	typeSuffix  string
	listAttr    string
	itemID      string
	description string
	// labelKey is the filter key the labels input maps to ("label", or
	// "node.label" for node labels).
	labelKey string
	filters  []p3SwarmFilter
	// fetch lists objects, sorted, with their IDs and names in that order.
	fetch func(ctx context.Context, c *client.Client, args client.Filters) (items any, ids, names []string, err error)
	pd    *providerData
}

var (
	_ datasource.DataSource              = (*p3SwarmListDS)(nil)
	_ datasource.DataSourceWithConfigure = (*p3SwarmListDS)(nil)
)

func (d *p3SwarmListDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.typeSuffix
}

func (d *p3SwarmListDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"name": schema.StringAttribute{Optional: true, MarkdownDescription: "Only objects whose name matches (Engine `name` filter, a prefix/substring match)."},
		"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Only objects carrying every one of these labels (Engine `" + d.labelKey + "` filter); an empty value matches any value."},
		"ids": schema.ListAttribute{ElementType: types.StringType, Computed: true,
			MarkdownDescription: "IDs of the matching objects, in the order of `" + d.listAttr + "`."},
		"names": schema.ListAttribute{ElementType: types.StringType, Computed: true,
			MarkdownDescription: "Names of the matching objects, in the order of `" + d.listAttr + "`."},
	}
	for _, f := range d.filters {
		a := schema.StringAttribute{Optional: true, MarkdownDescription: f.desc}
		if len(f.oneOf) > 0 {
			a.Validators = []validator.String{stringvalidator.OneOf(f.oneOf...)}
		}
		attrs[f.attr] = a
	}
	contractAddDataSourceAttributes(attrs, d.itemID)
	resp.Schema = schema.Schema{MarkdownDescription: d.description, Attributes: attrs}
}

func (d *p3SwarmListDS) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *p3SwarmListDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	if !d.pd.gateItemAPI(ctx, d.itemID, &resp.Diagnostics) {
		return
	}
	args := client.Filters{}
	var name types.String
	var labels types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("labels"), &labels)...)
	inputs := map[string]types.String{}
	for _, f := range d.filters {
		var v types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(f.attr), &v)...)
		inputs[f.attr] = v
		if v.ValueString() != "" {
			args.Add(f.key, v.ValueString())
		}
	}
	if name.ValueString() != "" {
		args.Add("name", name.ValueString())
	}
	p3AddLabelFilters(args, d.labelKey, stringMapFrom(ctx, labels, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	items, ids, names, err := d.fetch(ctx, d.pd.Client, args)
	if err != nil {
		resp.Diagnostics.AddError("Cannot list docker"+d.typeSuffix, "The provider's Docker host must be a swarm manager: "+err.Error())
		return
	}
	vals, diags := contractValues(ctx, d.pd, d.itemID, swarmListInput(d.listAttr, items))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	idList, nameList := swarmListNames(ids, names)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("labels"), labels)...)
	for k, v := range inputs {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ids"), idList)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("names"), nameList)...)
	for k, v := range vals {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}

// p3Reorder returns items sorted by name then ID, plus their IDs and names
// in that order.
func p3Reorder[T any](items []T, name, id func(T) string) ([]T, []string, []string) {
	out := append([]T(nil), items...)
	sort.SliceStable(out, func(a, b int) bool {
		if name(out[a]) != name(out[b]) {
			return name(out[a]) < name(out[b])
		}
		return id(out[a]) < id(out[b])
	})
	ids := make([]string, 0, len(out))
	names := make([]string, 0, len(out))
	for _, it := range out {
		ids = append(ids, id(it))
		names = append(names, name(it))
	}
	if out == nil {
		out = []T{}
	}
	return out, ids, names
}

// NewServicesDataSource returns the docker_services data source.
func NewServicesDataSource() datasource.DataSource {
	return &p3SwarmListDS{
		typeSuffix: "_services",
		listAttr:   "services",
		itemID:     "swarm:services",
		labelKey:   "label",
		description: "Lists the swarm's services (`GET /services`), sorted by name; the provider's host must be a swarm manager. " +
			"`services` holds each Engine service object field by field (`services[0].spec.task_template.container_spec.image`), " +
			"`services_json` the raw list. Filter by `name`, `labels` and `mode`.",
		filters: []p3SwarmFilter{{attr: "mode", key: "mode", oneOf: []string{"replicated", "global"},
			desc: "Only services in this mode: `replicated` or `global`."}},
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) (any, []string, []string, error) {
			res, err := c.ServiceList(ctx, client.ServiceListOptions{Filters: args, Status: true})
			if err != nil {
				return nil, nil, nil, err
			}
			items, ids, names := p3Reorder(res.Items, func(s swarm.Service) string { return s.Spec.Name }, func(s swarm.Service) string { return s.ID })
			return items, ids, names, nil
		},
	}
}

// NewSecretsDataSource returns the docker_secrets data source.
func NewSecretsDataSource() datasource.DataSource {
	return &p3SwarmListDS{
		typeSuffix: "_secrets",
		listAttr:   "secrets",
		itemID:     "swarm:secrets",
		labelKey:   "label",
		description: "Lists the swarm's secrets (`GET /secrets`), sorted by name; the provider's host must be a swarm manager. " +
			"Secret data is never returned by the daemon, so `secrets` holds the metadata (ID, version, times, name, labels, driver).",
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) (any, []string, []string, error) {
			res, err := c.SecretList(ctx, client.SecretListOptions{Filters: args})
			if err != nil {
				return nil, nil, nil, err
			}
			items, ids, names := p3Reorder(res.Items, func(s swarm.Secret) string { return s.Spec.Name }, func(s swarm.Secret) string { return s.ID })
			return items, ids, names, nil
		},
	}
}

// NewConfigsDataSource returns the docker_configs data source.
func NewConfigsDataSource() datasource.DataSource {
	return &p3SwarmListDS{
		typeSuffix: "_configs",
		listAttr:   "configs",
		itemID:     "swarm:configs",
		labelKey:   "label",
		description: "Lists the swarm's configs (`GET /configs`), sorted by name, with their content (`configs[*].spec.data`, base64); " +
			"the provider's host must be a swarm manager. Filter by `name` and `labels`.",
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) (any, []string, []string, error) {
			res, err := c.ConfigList(ctx, client.ConfigListOptions{Filters: args})
			if err != nil {
				return nil, nil, nil, err
			}
			items, ids, names := p3Reorder(res.Items, func(s swarm.Config) string { return s.Spec.Name }, func(s swarm.Config) string { return s.ID })
			return items, ids, names, nil
		},
	}
}

// swarmNodeName is a node's display name: Spec.Name, else its hostname.
func swarmNodeName(n swarm.Node) string {
	if n.Spec.Name != "" {
		return n.Spec.Name
	}
	return n.Description.Hostname
}

// NewNodesDataSource returns the docker_nodes data source.
func NewNodesDataSource() datasource.DataSource {
	return &p3SwarmListDS{
		typeSuffix: "_nodes",
		listAttr:   "nodes",
		itemID:     "swarm:nodes",
		labelKey:   "node.label",
		description: "Lists the swarm's nodes (`GET /nodes`) through the provider's Docker host (which must be a manager), sorted by name " +
			"(the node name, else its hostname) then ID. `nodes` holds each Engine node object (`nodes[0].description.hostname`, " +
			"`nodes[0].status.state`, `nodes[0].manager_status.leader`). Filter by `role`, `name` and node `labels`.",
		filters: []p3SwarmFilter{{attr: "role", key: "role", oneOf: swarmValidRoles,
			desc: "Only list nodes with this role: `worker` or `manager`."}},
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) (any, []string, []string, error) {
			res, err := c.NodeList(ctx, client.NodeListOptions{Filters: args})
			if err != nil {
				return nil, nil, nil, err
			}
			items, ids, names := p3Reorder(res.Items, swarmNodeName, func(n swarm.Node) string { return n.ID })
			return items, ids, names, nil
		},
	}
}

// swarmTaskName is the name the CLI shows for a task:
// "<service>.<slot>" (replicated) or "<service>.<node ID>" (global).
func swarmTaskName(t swarm.Task, serviceName string) string {
	if t.Name != "" {
		return t.Name
	}
	if serviceName == "" {
		serviceName = t.ServiceID
	}
	if t.Slot != 0 {
		return fmt.Sprintf("%s.%d", serviceName, t.Slot)
	}
	return serviceName + "." + t.NodeID
}

// swarmServiceNames maps service IDs to names (empty on error).
func swarmServiceNames(ctx context.Context, c *client.Client) map[string]string {
	out := map[string]string{}
	res, err := c.ServiceList(ctx, client.ServiceListOptions{})
	if err != nil {
		return out
	}
	for _, s := range res.Items {
		out[s.ID] = s.Spec.Name
	}
	return out
}

// NewTasksDataSource returns the docker_tasks data source.
func NewTasksDataSource() datasource.DataSource {
	return &p3SwarmListDS{
		typeSuffix: "_tasks",
		listAttr:   "tasks",
		itemID:     "swarm:tasks",
		labelKey:   "label",
		description: "Lists swarm tasks (`GET /tasks`), like `docker service ps`, `docker node ps` and `docker stack ps`; the provider's host " +
			"must be a swarm manager. Sorted by name (`<service>.<slot>`, or `<service>.<node ID>` for global services) then ID. `tasks` holds " +
			"each Engine task object (`tasks[0].status.state`, `tasks[0].status.container_status.container_id`); only the container environment " +
			"inside it is sensitive. Filter by `service_id`, `node_id`, `desired_state`, `name` and `labels`.",
		filters: []p3SwarmFilter{
			{attr: "service_id", key: "service", desc: "Only tasks of this service (ID or name; Engine `service` filter)."},
			{attr: "node_id", key: "node", desc: "Only tasks on this node (ID or name; Engine `node` filter)."},
			{attr: "desired_state", key: "desired-state", oneOf: []string{"running", "shutdown", "accepted"},
				desc: "Only tasks with this desired state: `running`, `shutdown` or `accepted`."},
		},
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) (any, []string, []string, error) {
			res, err := c.TaskList(ctx, client.TaskListOptions{Filters: args})
			if err != nil {
				return nil, nil, nil, err
			}
			svcNames := swarmServiceNames(ctx, c)
			items, ids, names := p3Reorder(res.Items, func(t swarm.Task) string { return swarmTaskName(t, svcNames[t.ServiceID]) },
				func(t swarm.Task) string { return t.ID })
			return items, ids, names, nil
		},
	}
}
