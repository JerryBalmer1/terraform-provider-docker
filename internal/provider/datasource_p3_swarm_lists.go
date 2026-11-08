package provider

// docker_services, docker_secrets and docker_configs: plural swarm data
// sources sharing one implementation.

import (
	"context"
	"encoding/base64"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"sort"
	"time"
)

// p3SwarmListDS is a plural swarm data source: filters in, a sorted list of
// objects out.
type p3SwarmListDS struct {
	typeSuffix  string
	listAttr    string
	description string
	extraInputs map[string]schema.Attribute
	itemAttrs   map[string]schema.Attribute
	itemTypes   map[string]attr.Type
	// fetch lists objects; it returns each as (sort key, attribute values).
	fetch func(ctx context.Context, c *client.Client, args client.Filters) ([]p3SwarmRow, error)
	pd    *providerData
}

type p3SwarmRow struct {
	key  string
	vals map[string]attr.Value
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
		"name":   schema.StringAttribute{Optional: true, MarkdownDescription: "Only objects whose name matches (Engine `name` filter, a prefix/substring match)."},
		"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Only objects carrying every one of these labels; an empty value matches any value."},
		d.listAttr: schema.ListNestedAttribute{
			Computed:            true,
			MarkdownDescription: "The matching objects, sorted by name then ID.",
			NestedObject:        schema.NestedAttributeObject{Attributes: d.itemAttrs},
		},
	}
	for k, v := range d.extraInputs {
		attrs[k] = v
	}
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
	args := client.Filters{}
	var name types.String
	var labels types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("labels"), &labels)...)
	for k := range d.extraInputs {
		var v types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(k), &v)...)
		if v.ValueString() != "" {
			args.Add(k, v.ValueString())
		}
	}
	if name.ValueString() != "" {
		args.Add("name", name.ValueString())
	}
	p3AddLabelFilters(args, "label", stringMapFrom(ctx, labels, &resp.Diagnostics))
	if resp.Diagnostics.HasError() {
		return
	}
	rows, err := d.fetch(ctx, d.pd.Client, args)
	if err != nil {
		resp.Diagnostics.AddError("Cannot list docker"+d.typeSuffix, "The provider's Docker host must be a swarm manager: "+err.Error())
		return
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	objType := types.ObjectType{AttrTypes: d.itemTypes}
	vals := make([]attr.Value, 0, len(rows))
	for _, r := range rows {
		obj, diags := types.ObjectValue(d.itemTypes, r.vals)
		resp.Diagnostics.Append(diags...)
		vals = append(vals, obj)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("labels"), labels)...)
	for k := range d.extraInputs {
		var v types.String
		req.Config.GetAttribute(ctx, path.Root(k), &v)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(d.listAttr), types.ListValueMust(objType, vals))...)
}

func p3Time(t time.Time) attr.Value {
	if t.IsZero() {
		return types.StringValue("")
	}
	return types.StringValue(t.UTC().Format(time.RFC3339))
}

func p3c(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
}

// p3MetaAttrs / p3MetaTypes are the fields every swarm object row has.
func p3MetaAttrs(kind string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id":         p3c(kind + " ID."),
		"name":       p3c(kind + " name."),
		"labels":     schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: kind + " labels."},
		"created_at": p3c("RFC 3339 creation time."),
		"updated_at": p3c("RFC 3339 time of the last update."),
	}
}

func p3MetaTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"id": types.StringType, "name": types.StringType, "labels": types.MapType{ElemType: types.StringType},
		"created_at": types.StringType, "updated_at": types.StringType,
	}
}

func p3MetaVals(id string, meta swarm.Meta, ann swarm.Annotations) map[string]attr.Value {
	return map[string]attr.Value{
		"id": types.StringValue(id), "name": types.StringValue(ann.Name), "labels": stringMapValue(ann.Labels, false),
		"created_at": p3Time(meta.CreatedAt), "updated_at": p3Time(meta.UpdatedAt),
	}
}

// NewP3ServicesDataSource returns the docker_services data source.
func NewP3ServicesDataSource() datasource.DataSource {
	attrs := p3MetaAttrs("Service")
	attrs["image"] = p3c("Image of the service's tasks, as stored (usually pinned to a digest).")
	attrs["mode"] = p3c("`replicated`, `global`, `replicated-job` or `global-job`.")
	attrs["replicas"] = schema.Int64Attribute{Computed: true, MarkdownDescription: "Desired replicas for `replicated` services; null otherwise."}
	typs := p3MetaTypes()
	typs["image"], typs["mode"], typs["replicas"] = types.StringType, types.StringType, types.Int64Type
	return &p3SwarmListDS{
		typeSuffix: "_services",
		listAttr:   "services",
		description: "Lists the swarm's services (`GET /services`), sorted by name; the provider's host must be a swarm manager. " +
			"Filter by `name`, `labels` and `mode`.",
		extraInputs: map[string]schema.Attribute{
			"mode": schema.StringAttribute{
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf("replicated", "global")},
				MarkdownDescription: "Only services in this mode: `replicated` or `global`.",
			},
		},
		itemAttrs: attrs,
		itemTypes: typs,
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) ([]p3SwarmRow, error) {
			ssRes, err := c.ServiceList(ctx, client.ServiceListOptions{Filters: args})
			if err != nil {
				return nil, err
			}
			ss := ssRes.Items
			rows := make([]p3SwarmRow, 0, len(ss))
			for _, s := range ss {
				v := p3MetaVals(s.ID, s.Meta, s.Spec.Annotations)
				img := ""
				if cs := s.Spec.TaskTemplate.ContainerSpec; cs != nil {
					img = cs.Image
				}
				v["image"] = types.StringValue(img)
				mode, replicas := "", types.Int64Null()
				switch m := s.Spec.Mode; {
				case m.Replicated != nil:
					mode = "replicated"
					if m.Replicated.Replicas != nil {
						replicas = types.Int64Value(int64(*m.Replicated.Replicas))
					}
				case m.Global != nil:
					mode = "global"
				case m.ReplicatedJob != nil:
					mode = "replicated-job"
				case m.GlobalJob != nil:
					mode = "global-job"
				}
				v["mode"], v["replicas"] = types.StringValue(mode), replicas
				rows = append(rows, p3SwarmRow{key: s.Spec.Name + "\x00" + s.ID, vals: v})
			}
			return rows, nil
		},
	}
}

// NewP3SecretsDataSource returns the docker_secrets data source.
func NewP3SecretsDataSource() datasource.DataSource {
	return &p3SwarmListDS{
		typeSuffix: "_secrets",
		listAttr:   "secrets",
		description: "Lists the swarm's secrets (`GET /secrets`), sorted by name; the provider's host must be a swarm manager. " +
			"Secret data is never returned by the daemon, so only metadata is listed.",
		itemAttrs: p3MetaAttrs("Secret"),
		itemTypes: p3MetaTypes(),
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) ([]p3SwarmRow, error) {
			ssRes, err := c.SecretList(ctx, client.SecretListOptions{Filters: args})
			if err != nil {
				return nil, err
			}
			ss := ssRes.Items
			rows := make([]p3SwarmRow, 0, len(ss))
			for _, s := range ss {
				rows = append(rows, p3SwarmRow{key: s.Spec.Name + "\x00" + s.ID, vals: p3MetaVals(s.ID, s.Meta, s.Spec.Annotations)})
			}
			return rows, nil
		},
	}
}

// NewP3ConfigsDataSource returns the docker_configs data source.
func NewP3ConfigsDataSource() datasource.DataSource {
	attrs := p3MetaAttrs("Config")
	attrs["data"] = p3c("Config content, base64-encoded.")
	typs := p3MetaTypes()
	typs["data"] = types.StringType
	return &p3SwarmListDS{
		typeSuffix: "_configs",
		listAttr:   "configs",
		description: "Lists the swarm's configs (`GET /configs`), sorted by name, with their content; the provider's host must be a swarm manager. " +
			"Filter by `name` and `labels`.",
		itemAttrs: attrs,
		itemTypes: typs,
		fetch: func(ctx context.Context, c *client.Client, args client.Filters) ([]p3SwarmRow, error) {
			csRes, err := c.ConfigList(ctx, client.ConfigListOptions{Filters: args})
			if err != nil {
				return nil, err
			}
			cs := csRes.Items
			rows := make([]p3SwarmRow, 0, len(cs))
			for _, s := range cs {
				v := p3MetaVals(s.ID, s.Meta, s.Spec.Annotations)
				v["data"] = types.StringValue(base64.StdEncoding.EncodeToString(s.Spec.Data))
				rows = append(rows, p3SwarmRow{key: s.Spec.Name + "\x00" + s.ID, vals: v})
			}
			return rows, nil
		},
	}
}
