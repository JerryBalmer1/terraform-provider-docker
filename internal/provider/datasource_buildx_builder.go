package provider

// Pass 5 Lane E: data docker_buildx_builder (`docker buildx inspect`) and
// data docker_buildx_builders (`docker buildx ls`), Q24. Builders are the
// ones visible on the provider's daemon (buildkit_conn.go): the daemon's
// own `default` builder and docker-container builders' buildx_buildkit_*
// node containers. buildx's client-side store is not read.

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	bxdsBuilderItemID  = "buildx:builder-inspect"
	bxdsBuildersItemID = "buildx:builders"
)

var (
	_ datasource.DataSourceWithConfigure = (*buildxBuilderDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*buildxBuildersDataSource)(nil)
)

// ---------------------------------------------------------------------------
// docker_buildx_builder

// NewBuildxBuilderDataSource is the data docker_buildx_builder constructor.
func NewBuildxBuilderDataSource() datasource.DataSource { return &buildxBuilderDataSource{} }

type buildxBuilderDataSource struct{ pd *providerData }

type bxdsBuilderModel struct {
	contractBuildxBuilderInspect
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Driver    types.String `tfsdk:"driver"`
	Endpoint  types.String `tfsdk:"endpoint"`
	Platforms types.List   `tfsdk:"platforms"`
	Labels    types.Map    `tfsdk:"labels"`
}

func (d *buildxBuilderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_builder"
}

func (d *buildxBuilderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *buildxBuilderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"name": schema.StringAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Builder to inspect, as for `docker buildx inspect <name>`. Default `default`, the daemon's built-in BuildKit " +
				"(buildx's `docker` driver); any other name is a docker-container builder whose node containers `buildx_buildkit_<name><n>` " +
				"run on the provider's daemon.",
		},
		"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Builder id (the builder name)."},
		"driver": schema.StringAttribute{
			Computed: true, MarkdownDescription: "Builder driver: `docker` for the daemon's builder, `docker-container` otherwise.",
		},
		"endpoint": schema.StringAttribute{
			Computed: true, MarkdownDescription: "Endpoint of the first node: `default`, or `docker-container://buildx_buildkit_<node>`.",
		},
		"platforms": schema.ListAttribute{
			ElementType: types.StringType, Computed: true,
			MarkdownDescription: "Platforms the builder can build for, the union over its nodes (empty for nodes that are not running).",
		},
		"labels": schema.MapAttribute{
			ElementType: types.StringType, Computed: true,
			MarkdownDescription: "BuildKit worker labels of the first node (`org.mobyproject.buildkit.worker.*`).",
		},
	}
	contractAddDataSourceAttributes(attrs, bxdsBuilderItemID)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads one buildx builder, like `docker buildx inspect`: driver, nodes with endpoint, status, buildkitd flags, " +
			"driver options, BuildKit version, platforms, worker labels and GC policy. The provider talks to BuildKit itself (the daemon's " +
			"`/grpc` endpoint, or `buildctl dial-stdio` in a node container); it never runs buildx and does not read buildx's client-side " +
			"builder store, so only builders on the provider's daemon are found, and `last_activity` is the newest node container's creation time.",
		Attributes: attrs,
	}
}

func (d *buildxBuilderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m bxdsBuilderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.pd == nil || d.pd.Client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured. This is a provider bug.")
		return
	}
	name := bkDefaultBuilder
	if v, ok := knownString(m.Name); ok && v != "" {
		name = v
	}
	view, err := bxDescribeBuilder(ctx, d.pd, name)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Cannot inspect buildx builder", err.Error())
		return
	}
	first := view.first()
	m.ID, m.Name, m.Driver = types.StringValue(view.Name), types.StringValue(view.Name), types.StringValue(view.Driver)
	m.Endpoint = types.StringValue(first.Endpoint)
	m.Platforms = stringListValue(view.platforms(), false)
	m.Labels = stringMapValue(bxStringMap(first.Labels), false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw:             map[string][]byte{"inspect_json": view.json()},
		Structured:      map[string]any{"builder": view.structured()},
		BuildKitVersion: first.Version,
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_buildx_builders

// NewBuildxBuildersDataSource is the data docker_buildx_builders constructor.
func NewBuildxBuildersDataSource() datasource.DataSource { return &buildxBuildersDataSource{} }

type buildxBuildersDataSource struct{ pd *providerData }

type bxdsBuildersModel struct {
	contractBuildxBuilders
	Names     types.List `tfsdk:"names"`
	Endpoints types.Map  `tfsdk:"endpoints"`
}

func (d *buildxBuildersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_builders"
}

func (d *buildxBuildersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *buildxBuildersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"names": schema.ListAttribute{
			ElementType: types.StringType, Computed: true,
			MarkdownDescription: "Builder names, `default` first.",
		},
		"endpoints": schema.MapAttribute{
			ElementType: types.StringType, Computed: true,
			MarkdownDescription: "Map of builder name to the endpoint of its first node.",
		},
	}
	contractAddDataSourceAttributes(attrs, bxdsBuildersItemID)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the buildx builders on the provider's daemon, like `docker buildx ls`: the daemon's `default` builder and " +
			"every docker-container builder (its `buildx_buildkit_*` node containers), each with its nodes, status, BuildKit version and " +
			"platforms. Running nodes are queried over BuildKit's API; a node that cannot be reached carries its error instead. " +
			"buildx's client-side store and its current-builder marker are not read.",
		Attributes: attrs,
	}
}

func (d *buildxBuildersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m bxdsBuildersModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.pd == nil || d.pd.Client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured. This is a provider bug.")
		return
	}
	nodes, err := bkListNodes(ctx, d.pd)
	if err != nil {
		resp.Diagnostics.AddError("Cannot list buildx builders", err.Error())
		return
	}
	var views []bxBuilderView
	var structured []any
	names := []string{}
	endpoints := map[string]string{}
	for _, g := range bxGroupBuilders(nodes) {
		v := bxBuilderViewOf(ctx, d.pd, g, nil)
		views = append(views, v)
		structured = append(structured, v.structured())
		names = append(names, v.Name)
		endpoints[v.Name] = v.first().Endpoint
	}
	raw, _ := json.Marshal(views)
	m.Names = stringListValue(names, false)
	m.Endpoints = stringMapValue(endpoints, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw:        map[string][]byte{"builders_json": raw},
		Structured: map[string]any{"builders": structured},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
