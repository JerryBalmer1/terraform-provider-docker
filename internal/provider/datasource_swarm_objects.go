package provider

// Singular swarm data sources: docker_node, docker_secret, docker_config,
// docker_service, docker_task and docker_swarm. Each looks one object up
// through the provider's Docker host (a swarm manager) and returns its
// identity values plus the outputs contract (inspect_json, inspect,
// content_sha256, version outputs; published_ports for services,
// join_tokens for the swarm).

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// swarmObjectDS is a singular swarm data source: lane-owned inputs and
// identity attributes, the item's contract attributes, and a read func.
type swarmObjectDS struct {
	suffix, itemID, desc string
	attrs                func() map[string]schema.Attribute
	// oneOf, when set, are the root inputs of which exactly one is required.
	oneOf []string
	read  func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool)
	pd    *providerData
}

var (
	_ datasource.DataSource                     = (*swarmObjectDS)(nil)
	_ datasource.DataSourceWithConfigure        = (*swarmObjectDS)(nil)
	_ datasource.DataSourceWithConfigValidators = (*swarmObjectDS)(nil)
)

func (d *swarmObjectDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.suffix
}

func (d *swarmObjectDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := d.attrs()
	contractAddDataSourceAttributes(attrs, d.itemID)
	resp.Schema = schema.Schema{MarkdownDescription: d.desc, Attributes: attrs}
}

func (d *swarmObjectDS) ConfigValidators(context.Context) []datasource.ConfigValidator {
	if len(d.oneOf) == 0 {
		return nil
	}
	var ps []path.Expression
	for _, a := range d.oneOf {
		ps = append(ps, path.MatchRoot(a))
	}
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(ps...)}
}

func (d *swarmObjectDS) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *swarmObjectDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	if !d.pd.gateItemAPI(ctx, d.itemID, &resp.Diagnostics) {
		return
	}
	vals, in, ok := d.read(ctx, d.pd, req.Config, &resp.Diagnostics)
	if !ok || resp.Diagnostics.HasError() {
		return
	}
	cvals, diags := contractValues(ctx, d.pd, d.itemID, in)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	for k, v := range cvals {
		vals[k] = v
	}
	for k, v := range vals {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}

// swarmDSRef returns the first set root string input among names, and
// its attribute name.
func swarmDSRef(ctx context.Context, cfg tfsdk.Config, diags *diag.Diagnostics, names ...string) (string, string) {
	for _, n := range names {
		var v types.String
		diags.Append(cfg.GetAttribute(ctx, path.Root(n), &v)...)
		if s, ok := knownString(v); ok && s != "" {
			return s, n
		}
	}
	return "", names[0]
}

func swarmDSComputed(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
}

func swarmDSLabels(kind string) schema.MapAttribute {
	return schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Labels on the " + kind + " (`Spec.Labels`)."}
}

func swarmDSVersionIndex(kind string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, MarkdownDescription: "The " + kind + " object's `Version.Index`; it changes on every update."}
}

// ---------------------------------------------------------------------------
// docker_node

// NewNodeDataSource returns the docker_node data source.
func NewNodeDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "node", itemID: "swarm:node-data", oneOf: []string{"id", "hostname"},
		desc: "Looks up one swarm node (`GET /nodes/{id}`) through the provider's Docker host (which must be a manager), " +
			"by `id` (full ID or unique prefix) or `hostname`. Set exactly one. `inspect` holds the whole Engine node object.",
		attrs: func() map[string]schema.Attribute {
			return map[string]schema.Attribute{
				"id": schema.StringAttribute{Optional: true, Computed: true,
					MarkdownDescription: "Node ID or unique ID prefix; resolved to the full ID. Conflicts with `hostname`."},
				"hostname": schema.StringAttribute{Optional: true, Computed: true,
					MarkdownDescription: "Node hostname (or node name). Conflicts with `id`."},
				"name":           swarmDSComputed("Node name (`Spec.Name`), else the hostname."),
				"labels":         swarmDSLabels("node"),
				"version_index":  swarmDSVersionIndex("node"),
				"role":           swarmDSComputed("`worker` or `manager`."),
				"addr":           swarmDSComputed("IP address the node is reached at (`Status.Addr`)."),
				"availability":   swarmDSComputed("`active`, `pause` or `drain`."),
				"state":          swarmDSComputed("Node status: `ready`, `down`, `disconnected` or `unknown`."),
				"engine_version": swarmDSComputed("Docker Engine version on the node."),
				"leader":         schema.BoolAttribute{Computed: true, MarkdownDescription: "True when the node is the current raft leader."},
			}
		},
		read: func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			ref, attrName := swarmDSRef(ctx, cfg, diags, "id", "hostname")
			id, err := resolveNodeID(ctx, pd.Client, ref)
			if err != nil {
				diags.AddAttributeError(path.Root(attrName), "Cannot find swarm node", err.Error())
				return nil, contractFillInput{}, false
			}
			res, err := pd.Client.NodeInspect(ctx, id, client.NodeInspectOptions{})
			if err != nil {
				diags.AddError("Cannot inspect swarm node", err.Error())
				return nil, contractFillInput{}, false
			}
			return swarmNodeIdentity(res.Node), swarmInspectInput(res.Raw, res.Node), true
		},
	}
}

// swarmNodeIdentity is the identity (and descriptive) values of a node,
// shared by data.docker_node and docker_node.
func swarmNodeIdentity(n swarm.Node) map[string]attr.Value {
	leader := false
	if n.ManagerStatus != nil {
		leader = n.ManagerStatus.Leader
	}
	return map[string]attr.Value{
		"id":             types.StringValue(n.ID),
		"hostname":       types.StringValue(n.Description.Hostname),
		"name":           types.StringValue(swarmNodeName(n)),
		"labels":         stringMapValue(n.Spec.Labels, false),
		"version_index":  swarmVersionIndex(n.Version),
		"role":           types.StringValue(string(n.Spec.Role)),
		"addr":           types.StringValue(n.Status.Addr),
		"availability":   types.StringValue(string(n.Spec.Availability)),
		"state":          types.StringValue(string(n.Status.State)),
		"engine_version": types.StringValue(n.Description.Engine.EngineVersion),
		"leader":         types.BoolValue(leader),
	}
}

// ---------------------------------------------------------------------------
// docker_secret / docker_config

func swarmBlobDSAttrs(kind string) func() map[string]schema.Attribute {
	return func() map[string]schema.Attribute {
		return map[string]schema.Attribute{
			"id": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "The " + kind + " ID or a unique ID prefix; resolved to the full ID. Conflicts with `name`."},
			"name": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "The " + kind + " name. Conflicts with `id`."},
			"labels":        swarmDSLabels(kind),
			"version_index": swarmDSVersionIndex(kind),
		}
	}
}

// NewSecretDataSource returns the docker_secret data source.
func NewSecretDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "secret", itemID: "swarm:secret-data", oneOf: []string{"id", "name"}, attrs: swarmBlobDSAttrs("secret"),
		desc: "Looks up one swarm secret's metadata (`GET /secrets/{id}`) by `id` or `name`; the provider's host must be a swarm manager. " +
			"The Engine never returns secret data, so `inspect` holds the ID, version, times, name, labels and driver only.",
		read: func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			ref, attrName := swarmDSRef(ctx, cfg, diags, "id", "name")
			id, err := resolveSecretID(ctx, pd.Client, ref)
			if err != nil {
				diags.AddAttributeError(path.Root(attrName), "Cannot find secret", err.Error())
				return nil, contractFillInput{}, false
			}
			res, err := pd.Client.SecretInspect(ctx, id, client.SecretInspectOptions{})
			if err != nil {
				diags.AddError("Cannot inspect secret", err.Error())
				return nil, contractFillInput{}, false
			}
			s := res.Secret
			return map[string]attr.Value{
				"id": types.StringValue(s.ID), "name": types.StringValue(s.Spec.Name),
				"labels": stringMapValue(s.Spec.Labels, false), "version_index": swarmVersionIndex(s.Version),
			}, swarmInspectInput(res.Raw, s), true
		},
	}
}

// NewConfigDataSource returns the docker_config data source.
func NewConfigDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "config", itemID: "swarm:config-data", oneOf: []string{"id", "name"}, attrs: swarmBlobDSAttrs("config"),
		desc: "Looks up one swarm config (`GET /configs/{id}`) by `id` or `name`; the provider's host must be a swarm manager. " +
			"`inspect.spec.data` is the content, base64-encoded as the Engine returns it.",
		read: func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			ref, attrName := swarmDSRef(ctx, cfg, diags, "id", "name")
			id, err := resolveConfigID(ctx, pd.Client, ref)
			if err != nil {
				diags.AddAttributeError(path.Root(attrName), "Cannot find config", err.Error())
				return nil, contractFillInput{}, false
			}
			res, err := pd.Client.ConfigInspect(ctx, id, client.ConfigInspectOptions{})
			if err != nil {
				diags.AddError("Cannot inspect config", err.Error())
				return nil, contractFillInput{}, false
			}
			s := res.Config
			return map[string]attr.Value{
				"id": types.StringValue(s.ID), "name": types.StringValue(s.Spec.Name),
				"labels": stringMapValue(s.Spec.Labels, false), "version_index": swarmVersionIndex(s.Version),
			}, swarmInspectInput(res.Raw, s), true
		},
	}
}

// ---------------------------------------------------------------------------
// docker_service

// NewServiceDataSource returns the docker_service data source.
func NewServiceDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "service", itemID: "swarm:service-data", oneOf: []string{"id", "name"},
		desc: "Looks up one swarm service (`GET /services/{id}`) by `id` or `name`; the provider's host must be a swarm manager. " +
			"`inspect` holds the whole Engine service object (spec, endpoint, update status), `published_ports` the ports it publishes; " +
			"only the container environment inside it is sensitive.",
		attrs: func() map[string]schema.Attribute {
			return map[string]schema.Attribute{
				"id": schema.StringAttribute{Optional: true, Computed: true,
					MarkdownDescription: "Service ID or a unique ID prefix; resolved to the full ID. Conflicts with `name`."},
				"name": schema.StringAttribute{Optional: true, Computed: true,
					MarkdownDescription: "Service name. Conflicts with `id`."},
				"labels":        swarmDSLabels("service"),
				"version_index": swarmDSVersionIndex("service"),
				"image_digest_ref": swarmDSComputed("Image the tasks run, pinned by digest (`repo@sha256:...`); null when the " +
					"daemon did not pin one."),
				"replicas": schema.Int64Attribute{Computed: true,
					MarkdownDescription: "Wanted task count of a replicated service; null for global and job services."},
			}
		},
		read: func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			ref, attrName := swarmDSRef(ctx, cfg, diags, "id", "name")
			id, err := resolveServiceID(ctx, pd.Client, ref)
			if err != nil {
				diags.AddAttributeError(path.Root(attrName), "Cannot find service", err.Error())
				return nil, contractFillInput{}, false
			}
			res, err := pd.Client.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
			if err != nil {
				diags.AddError("Cannot inspect service", err.Error())
				return nil, contractFillInput{}, false
			}
			svc := res.Service
			digest := types.StringNull()
			if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
				if r := svcImageDigestRef(cs.Image); r != "" {
					digest = types.StringValue(r)
				}
			}
			return map[string]attr.Value{
				"id": types.StringValue(svc.ID), "name": types.StringValue(svc.Spec.Name),
				"labels": stringMapValue(svc.Spec.Labels, false), "version_index": swarmVersionIndex(svc.Version),
				"image_digest_ref": digest, "replicas": svcReplicasValue(svc),
			}, svcContractInput(res.Raw, svc), true
		},
	}
}

// ---------------------------------------------------------------------------
// docker_task

// NewTaskDataSource returns the docker_task data source.
func NewTaskDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "task", itemID: "swarm:task-data",
		desc: "Looks up one swarm task (`GET /tasks/{id}`) by `id`; the provider's host must be a swarm manager. " +
			"`inspect` holds the whole Engine task object (status, container status, network attachments); only the container " +
			"environment inside it is sensitive. List tasks with `docker_tasks`.",
		attrs: func() map[string]schema.Attribute {
			return map[string]schema.Attribute{
				"id":            schema.StringAttribute{Required: true, MarkdownDescription: "Task ID."},
				"name":          swarmDSComputed("Task name as the CLI shows it: `<service>.<slot>`, or `<service>.<node ID>` for global services."),
				"labels":        schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Labels on the task (`Labels`)."},
				"service_id":    swarmDSComputed("ID of the task's service (`ServiceID`)."),
				"node_id":       swarmDSComputed("ID of the node the task is assigned to (`NodeID`); empty while unassigned."),
				"slot":          schema.Int64Attribute{Computed: true, MarkdownDescription: "Slot of a replicated service's task (`Slot`); 0 for global services."},
				"container_id":  swarmDSComputed("ID of the task's container (`Status.ContainerStatus.ContainerID`); empty before it starts."),
				"version_index": swarmDSVersionIndex("task"),
			}
		},
		read: func(ctx context.Context, pd *providerData, cfg tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			ref, _ := swarmDSRef(ctx, cfg, diags, "id")
			res, err := pd.Client.TaskInspect(ctx, ref, client.TaskInspectOptions{})
			if err != nil {
				diags.AddAttributeError(path.Root("id"), "Cannot inspect task", err.Error())
				return nil, contractFillInput{}, false
			}
			t := res.Task
			svcName := ""
			if sres, err := pd.Client.ServiceInspect(ctx, t.ServiceID, client.ServiceInspectOptions{}); err == nil {
				svcName = sres.Service.Spec.Name
			}
			container := ""
			if cs := t.Status.ContainerStatus; cs != nil {
				container = cs.ContainerID
			}
			return map[string]attr.Value{
				"id": types.StringValue(t.ID), "name": types.StringValue(swarmTaskName(t, svcName)),
				"labels": stringMapValue(t.Labels, false), "service_id": types.StringValue(t.ServiceID),
				"node_id": types.StringValue(t.NodeID), "slot": types.Int64Value(int64(t.Slot)),
				"container_id": types.StringValue(container), "version_index": swarmVersionIndex(t.Version),
			}, swarmInspectInput(res.Raw, t), true
		},
	}
}

// ---------------------------------------------------------------------------
// docker_swarm

// swarmContractInput is the contract input of the swarm object: inspect
// (re-encoded; SwarmInspect does not expose the body) and join_tokens.
func swarmContractInput(sw swarm.Swarm) contractFillInput {
	raw, _ := json.Marshal(sw)
	in := swarmInspectInput(raw, sw)
	in.Structured["join_tokens"] = sw.JoinTokens
	return in
}

// NewSwarmDataSource returns the docker_swarm data source.
func NewSwarmDataSource() datasource.DataSource {
	return &swarmObjectDS{
		suffix: "swarm", itemID: "swarm:swarm-data",
		desc: "Reads the swarm the provider's Docker host manages (`GET /swarm`): ID, spec, join tokens, root CA and data path port. " +
			"The host must be a manager. `inspect` holds the whole Engine swarm object; the join tokens are sensitive (`join_tokens`, " +
			"`worker_join_token`, `manager_join_token`), so prefer the `docker_swarm_tokens` ephemeral resource when they only feed other ephemeral values.",
		attrs: func() map[string]schema.Attribute {
			return map[string]schema.Attribute{
				"id":                 swarmDSComputed("Swarm cluster ID (`ClusterInfo.ID`)."),
				"labels":             swarmDSLabels("swarm"),
				"version_index":      swarmDSVersionIndex("swarm"),
				"worker_join_token":  schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Token that joins a node as a worker."},
				"manager_join_token": schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Token that joins a node as a manager."},
			}
		},
		read: func(ctx context.Context, pd *providerData, _ tfsdk.Config, diags *diag.Diagnostics) (map[string]attr.Value, contractFillInput, bool) {
			res, err := pd.Client.SwarmInspect(ctx, client.SwarmInspectOptions{})
			if err != nil {
				diags.AddError("Cannot inspect swarm", "The provider's Docker host must be a swarm manager: "+err.Error())
				return nil, contractFillInput{}, false
			}
			sw := res.Swarm
			return map[string]attr.Value{
				"id": types.StringValue(sw.ID), "labels": stringMapValue(sw.Spec.Labels, false),
				"version_index":     swarmVersionIndex(sw.Version),
				"worker_join_token": types.StringValue(sw.JoinTokens.Worker), "manager_join_token": types.StringValue(sw.JoinTokens.Manager),
			}, swarmContractInput(sw), true
		},
	}
}
