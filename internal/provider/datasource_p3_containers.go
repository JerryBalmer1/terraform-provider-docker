package provider

import (
	"context"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// NewContainersDataSource returns the docker_containers data source.
func NewContainersDataSource() datasource.DataSource { return &p3ContainersDS{} }

type p3ContainersDS struct {
	pd *providerData
}

type p3ContainersModel struct {
	Name     types.String `tfsdk:"name"`
	Labels   types.Map    `tfsdk:"labels"`
	Status   types.String `tfsdk:"status"`
	Ancestor types.String `tfsdk:"ancestor"`
	All      types.Bool   `tfsdk:"all"`
	IDs      types.List   `tfsdk:"ids"`
	Names    types.List   `tfsdk:"names"`
	contractEngineContainers
}

func (d *p3ContainersDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_containers"
}

func (d *p3ContainersDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists containers on the provider's Docker host (`GET /containers/json`), like `docker container ls --all`, sorted by name. " +
			"Every container is listed, running or not, unless `all = false`; the other arguments are Engine API filters. " +
			"`containers` holds the Engine's list entries field by field, `ids` and `names` the IDs and primary names in the same order.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name":     schema.StringAttribute{Optional: true, MarkdownDescription: "Only containers whose name matches (Engine `name` filter, a substring/regex match)."},
			"labels":   schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Only containers carrying every one of these labels; an empty value matches any value."},
			"ancestor": schema.StringAttribute{Optional: true, MarkdownDescription: "Only containers created from this image or a descendant of it."},
			"status": schema.StringAttribute{
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf("created", "restarting", "running", "removing", "paused", "exited", "dead")},
				MarkdownDescription: "Only containers in this state: `created`, `restarting`, `running`, `removing`, `paused`, `exited` or `dead`.",
			},
			"all":   schema.BoolAttribute{Optional: true, MarkdownDescription: "Include stopped containers. Default `true`; `false` lists only running ones, like `docker ps`."},
			"ids":   schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "IDs of the listed containers, in the order of `containers`."},
			"names": schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Primary names (without the leading `/`) of the listed containers, in the order of `containers`."},
		}, "engine:containers"),
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
	list := p3SortContainers(listRes.Items)
	ids := make([]string, 0, len(list))
	names := make([]string, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
		names = append(names, p3ContainerPrimaryName(s))
	}
	// ContainerList keeps no body: containers_json is the list re-encoded in
	// canonical form (rCanonicalJSON), in the name-then-ID order above.
	raw, err := rCanonicalList(list)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode container list", err.Error())
		return
	}
	m.IDs = copStringList(ids)
	m.Names = copStringList(names)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"containers_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// p3ContainerPrimaryName is a list entry's first name without the leading /.
func p3ContainerPrimaryName(s container.Summary) string {
	if len(s.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(s.Names[0], "/")
}

// p3SortContainers orders list entries by primary name, then ID.
func p3SortContainers(list []container.Summary) []container.Summary {
	out := append([]container.Summary{}, list...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := p3ContainerPrimaryName(out[i]), p3ContainerPrimaryName(out[j])
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// copStringList builds a list(string) value.
func copStringList(in []string) types.List {
	v, _ := types.ListValueFrom(context.Background(), types.StringType, in)
	return v
}
