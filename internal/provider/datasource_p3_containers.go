package provider

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*p3ContainersDS)(nil)
	_ datasource.DataSourceWithConfigure = (*p3ContainersDS)(nil)
)

// NewP3ContainersDataSource returns the docker_containers data source.
func NewP3ContainersDataSource() datasource.DataSource { return &p3ContainersDS{} }

type p3ContainersDS struct {
	pd *providerData
}

type p3ContainersModel struct {
	Name       types.String `tfsdk:"name"`
	Labels     types.Map    `tfsdk:"labels"`
	Status     types.String `tfsdk:"status"`
	Ancestor   types.String `tfsdk:"ancestor"`
	All        types.Bool   `tfsdk:"all"`
	Containers types.List   `tfsdk:"containers"`
}

var p3ContainerPortAttrTypes = map[string]attr.Type{
	"ip":           types.StringType,
	"private_port": types.Int64Type,
	"public_port":  types.Int64Type,
	"type":         types.StringType,
}

var p3ContainerAttrTypes = map[string]attr.Type{
	"id":       types.StringType,
	"name":     types.StringType,
	"names":    types.ListType{ElemType: types.StringType},
	"image":    types.StringType,
	"image_id": types.StringType,
	"command":  types.StringType,
	"created":  types.StringType,
	"state":    types.StringType,
	"status":   types.StringType,
	"labels":   types.MapType{ElemType: types.StringType},
	"networks": types.ListType{ElemType: types.StringType},
	"ports":    types.ListType{ElemType: types.ObjectType{AttrTypes: p3ContainerPortAttrTypes}},
}

func (d *p3ContainersDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_containers"
}

func (d *p3ContainersDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists containers on the provider's Docker host (`GET /containers/json`), sorted by name. " +
			"Every container is listed, running or not, unless `all = false`; the other arguments are Engine API filters.",
		Attributes: map[string]schema.Attribute{
			"name":     schema.StringAttribute{Optional: true, MarkdownDescription: "Only containers whose name matches (Engine `name` filter, a substring/regex match)."},
			"labels":   schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Only containers carrying every one of these labels; an empty value matches any value."},
			"ancestor": schema.StringAttribute{Optional: true, MarkdownDescription: "Only containers created from this image or a descendant of it."},
			"status": schema.StringAttribute{
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf("created", "restarting", "running", "removing", "paused", "exited", "dead")},
				MarkdownDescription: "Only containers in this state: `created`, `restarting`, `running`, `removing`, `paused`, `exited` or `dead`.",
			},
			"all": schema.BoolAttribute{Optional: true, MarkdownDescription: "Include stopped containers. Default `true`; `false` lists only running ones, like `docker ps`."},
			"containers": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The containers, sorted by name then ID.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":       str("Container ID."),
					"name":     str("Primary name, without the leading `/`."),
					"names":    schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Every name, without the leading `/`."},
					"image":    str("Image the container was created from, as given."),
					"image_id": str("Image ID."),
					"command":  str("Command line the container runs."),
					"created":  str("RFC 3339 creation time."),
					"state":    str("State, e.g. `running` or `exited`."),
					"status":   str("Human-readable status, e.g. `Up 2 hours`."),
					"labels":   schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Container labels."},
					"networks": schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Names of attached networks, sorted."},
					"ports": schema.ListNestedAttribute{
						Computed:            true,
						MarkdownDescription: "Published and exposed ports.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"ip":           str("Host IP the port is published on, empty when only exposed."),
							"private_port": schema.Int64Attribute{Computed: true, MarkdownDescription: "Container port."},
							"public_port":  schema.Int64Attribute{Computed: true, MarkdownDescription: "Host port, 0 when only exposed."},
							"type":         str("`tcp`, `udp` or `sctp`."),
						}},
					},
				}},
			},
		},
	}
}

func (d *p3ContainersDS) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *p3ContainersDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var m p3ContainersModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	args := client.Filters{}
	for k, v := range map[string]types.String{"name": m.Name, "status": m.Status, "ancestor": m.Ancestor} {
		if v.ValueString() != "" {
			args.Add(k, v.ValueString())
		}
	}
	p3AddLabelFilters(args, "label", stringMapFrom(ctx, m.Labels, &resp.Diagnostics))
	all := m.All.IsNull() || m.All.ValueBool()
	listRes, err := d.pd.Client.ContainerList(ctx, client.ContainerListOptions{All: all, Filters: args})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list containers", err.Error())
		return
	}
	list := listRes.Items
	m.Containers = p3ContainersValue(list)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func p3ContainersValue(list []container.Summary) types.List {
	type row struct {
		name string
		s    container.Summary
	}
	rows := make([]row, 0, len(list))
	for _, s := range list {
		name := ""
		if len(s.Names) > 0 {
			name = strings.TrimPrefix(s.Names[0], "/")
		}
		rows = append(rows, row{name, s})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].name != rows[j].name {
			return rows[i].name < rows[j].name
		}
		return rows[i].s.ID < rows[j].s.ID
	})
	objType := types.ObjectType{AttrTypes: p3ContainerAttrTypes}
	portType := types.ObjectType{AttrTypes: p3ContainerPortAttrTypes}
	vals := make([]attr.Value, 0, len(rows))
	for _, r := range rows {
		s := r.s
		names := make([]attr.Value, 0, len(s.Names))
		for _, n := range s.Names {
			names = append(names, types.StringValue(strings.TrimPrefix(n, "/")))
		}
		var nets []string
		if s.NetworkSettings != nil {
			for n := range s.NetworkSettings.Networks {
				nets = append(nets, n)
			}
		}
		sort.Strings(nets)
		netVals := make([]attr.Value, 0, len(nets))
		for _, n := range nets {
			netVals = append(netVals, types.StringValue(n))
		}
		ports := make([]attr.Value, 0, len(s.Ports))
		for _, p := range s.Ports {
			ports = append(ports, types.ObjectValueMust(p3ContainerPortAttrTypes, map[string]attr.Value{
				"ip":           types.StringValue(addrString(p.IP)),
				"private_port": types.Int64Value(int64(p.PrivatePort)),
				"public_port":  types.Int64Value(int64(p.PublicPort)),
				"type":         types.StringValue(p.Type),
			}))
		}
		vals = append(vals, types.ObjectValueMust(p3ContainerAttrTypes, map[string]attr.Value{
			"id":       types.StringValue(s.ID),
			"name":     types.StringValue(r.name),
			"names":    types.ListValueMust(types.StringType, names),
			"image":    types.StringValue(s.Image),
			"image_id": types.StringValue(s.ImageID),
			"command":  types.StringValue(s.Command),
			"created":  types.StringValue(time.Unix(s.Created, 0).UTC().Format(time.RFC3339)),
			"state":    types.StringValue(string(s.State)),
			"status":   types.StringValue(s.Status),
			"labels":   stringMapValue(s.Labels, false),
			"networks": types.ListValueMust(types.StringType, netVals),
			"ports":    types.ListValueMust(portType, ports),
		}))
	}
	return types.ListValueMust(objType, vals)
}
