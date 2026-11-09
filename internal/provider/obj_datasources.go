package provider

// Pass 5 Lane A (WP obj): the thin Engine API list and system data sources
// docker_networks, docker_volumes, docker_plugins, docker_info,
// docker_version and docker_system_df. Each one makes one Engine call,
// re-encodes the response as its `*_json` raw output and lets the contract
// fill the structured, hash and version outputs from it.

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSourceWithConfigure = (*objNetworksDS)(nil)
	_ datasource.DataSourceWithConfigure = (*objVolumesDS)(nil)
	_ datasource.DataSourceWithConfigure = (*objPluginsDS)(nil)
	_ datasource.DataSourceWithConfigure = (*objInfoDS)(nil)
	_ datasource.DataSourceWithConfigure = (*objVersionDS)(nil)
	_ datasource.DataSourceWithConfigure = (*objSystemDfDS)(nil)
)

func objStrIn(desc string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, MarkdownDescription: desc}
}

func objLabelsIn(what string) schema.MapAttribute {
	return schema.MapAttribute{ElementType: types.StringType, Optional: true,
		MarkdownDescription: "Only " + what + " carrying every one of these labels (Engine `label` filter); an empty value matches any value."}
}

func objStrListOut(desc string) schema.ListAttribute {
	return schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: desc}
}

func objStrOut(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
}

// ---------------------------------------------------------------------------
// docker_networks

// NewNetworksDataSource returns the docker_networks data source.
func NewNetworksDataSource() datasource.DataSource { return &objNetworksDS{} }

type objNetworksDS struct{ objDSBase }

type objNetworksModel struct {
	Name     types.String `tfsdk:"name"`
	ID       types.String `tfsdk:"id"`
	Driver   types.String `tfsdk:"driver"`
	Scope    types.String `tfsdk:"scope"`
	Type     types.String `tfsdk:"type"`
	Labels   types.Map    `tfsdk:"labels"`
	Dangling types.Bool   `tfsdk:"dangling"`
	IDs      types.List   `tfsdk:"ids"`
	Names    types.List   `tfsdk:"names"`
	contractEngineNetworks
}

func (d *objNetworksDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_networks"
}

func (d *objNetworksDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"name":   objStrIn("Only networks whose name matches (Engine `name` filter, a substring match)."),
		"id":     objStrIn("Only networks whose ID starts with this value (Engine `id` filter)."),
		"driver": objStrIn("Only networks using this driver, e.g. `bridge` or `overlay`."),
		"scope": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.OneOf("local", "swarm", "global")},
			MarkdownDescription: "Only networks of this scope: `local`, `swarm` or `global`."},
		"type": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.OneOf("custom", "builtin")},
			MarkdownDescription: "`custom` lists only user-defined networks, `builtin` only the predefined `bridge`, `host` and `none`."},
		"labels":   objLabelsIn("networks"),
		"dangling": schema.BoolAttribute{Optional: true, MarkdownDescription: "`true` lists only networks no container uses, `false` only used ones."},
		"ids":      objStrListOut("IDs of the listed networks, in the order of `networks`."),
		"names":    objStrListOut("Names of the listed networks, in the order of `networks`."),
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the networks on the provider's Docker host (`GET /networks`), like `docker network ls`. " +
			"The optional arguments are Engine API filters; the list is sorted by name, then ID.",
		Attributes: contractAddDataSourceAttributes(attrs, "engine:networks"),
	}
}

func (d *objNetworksDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !objReady(d.pd, &resp.Diagnostics) {
		return
	}
	var m objNetworksModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.pd.gateItemAPI(ctx, "engine:networks", &resp.Diagnostics) {
		return
	}
	args := client.Filters{}
	objAddFilter(args, "name", m.Name)
	objAddFilter(args, "id", m.ID)
	objAddFilter(args, "driver", m.Driver)
	objAddFilter(args, "scope", m.Scope)
	objAddFilter(args, "type", m.Type)
	objAddBoolFilter(args, "dangling", m.Dangling)
	p3AddLabelFilters(args, "label", stringMapFrom(ctx, m.Labels, &resp.Diagnostics))
	res, err := d.pd.Client.NetworkList(ctx, client.NetworkListOptions{Filters: args})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list networks", err.Error())
		return
	}
	items := res.Items
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	ids, names := make([]string, 0, len(items)), make([]string, 0, len(items))
	for _, n := range items {
		ids, names = append(ids, n.ID), append(names, n.Name)
	}
	m.IDs, m.Names = stringListValue(ids, false), stringListValue(names, false)
	raw := objJSON(items, &resp.Diagnostics)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"networks_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_volumes

// NewVolumesDataSource returns the docker_volumes data source.
func NewVolumesDataSource() datasource.DataSource { return &objVolumesDS{} }

type objVolumesDS struct{ objDSBase }

type objVolumesModel struct {
	Name     types.String `tfsdk:"name"`
	Driver   types.String `tfsdk:"driver"`
	Labels   types.Map    `tfsdk:"labels"`
	Dangling types.Bool   `tfsdk:"dangling"`
	Names    types.List   `tfsdk:"names"`
	contractEngineVolumes
}

func (d *objVolumesDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volumes"
}

func (d *objVolumesDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"name":     objStrIn("Only volumes whose name matches (Engine `name` filter, a substring match)."),
		"driver":   objStrIn("Only volumes using this driver, e.g. `local`."),
		"labels":   objLabelsIn("volumes"),
		"dangling": schema.BoolAttribute{Optional: true, MarkdownDescription: "`true` lists only volumes no container uses, `false` only used ones."},
		"names":    objStrListOut("Names of the listed volumes, sorted."),
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the volumes on the provider's Docker host (`GET /volumes`), like `docker volume ls`. " +
			"The optional arguments are Engine API filters; `volumes.volumes` is sorted by name. " +
			"The raw `volumes_json` output is sensitive because driver options can hold credentials.",
		Attributes: contractAddDataSourceAttributes(attrs, "engine:volumes"),
	}
}

func (d *objVolumesDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !objReady(d.pd, &resp.Diagnostics) {
		return
	}
	var m objVolumesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.pd.gateItemAPI(ctx, "engine:volumes", &resp.Diagnostics) {
		return
	}
	args := client.Filters{}
	objAddFilter(args, "name", m.Name)
	objAddFilter(args, "driver", m.Driver)
	objAddBoolFilter(args, "dangling", m.Dangling)
	p3AddLabelFilters(args, "label", stringMapFrom(ctx, m.Labels, &resp.Diagnostics))
	res, err := d.pd.Client.VolumeList(ctx, client.VolumeListOptions{Filters: args})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list volumes", err.Error())
		return
	}
	items := res.Items
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	names := make([]string, 0, len(items))
	for _, v := range items {
		names = append(names, v.Name)
	}
	m.Names = stringListValue(names, false)
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	raw := objJSON(map[string]any{"Volumes": items, "Warnings": warnings}, &resp.Diagnostics)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"volumes_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_plugins

// NewPluginsDataSource returns the docker_plugins data source.
func NewPluginsDataSource() datasource.DataSource { return &objPluginsDS{} }

type objPluginsDS struct{ objDSBase }

type objPluginsModel struct {
	Capability types.String `tfsdk:"capability"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	IDs        types.List   `tfsdk:"ids"`
	Names      types.List   `tfsdk:"names"`
	contractEnginePlugins
}

func (d *objPluginsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugins"
}

func (d *objPluginsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"capability": objStrIn("Only plugins with this capability (Engine `capability` filter), e.g. `volumedriver` or `networkdriver`."),
		"enabled":    schema.BoolAttribute{Optional: true, MarkdownDescription: "`true` lists only enabled plugins, `false` only disabled ones."},
		"ids":        objStrListOut("IDs of the listed plugins, in the order of `plugins`."),
		"names":      objStrListOut("Names of the listed plugins (e.g. `vieux/sshfs:latest`), in the order of `plugins`."),
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the managed plugins installed on the provider's Docker host (`GET /plugins`), like `docker plugin ls`. " +
			"The list is sorted by name; the raw `plugins_json` output is sensitive because plugin settings can hold credentials.",
		Attributes: contractAddDataSourceAttributes(attrs, "engine:plugins"),
	}
}

func (d *objPluginsDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !objReady(d.pd, &resp.Diagnostics) {
		return
	}
	var m objPluginsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.pd.gateItemAPI(ctx, "engine:plugins", &resp.Diagnostics) {
		return
	}
	args := client.Filters{}
	objAddFilter(args, "capability", m.Capability)
	objAddBoolFilter(args, "enabled", m.Enabled)
	res, err := d.pd.Client.PluginList(ctx, client.PluginListOptions{Filters: args})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list plugins", err.Error())
		return
	}
	items := res.Items
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	ids, names := make([]string, 0, len(items)), make([]string, 0, len(items))
	for _, p := range items {
		ids, names = append(ids, p.ID), append(names, p.Name)
	}
	m.IDs, m.Names = stringListValue(ids, false), stringListValue(names, false)
	raw := objJSON(items, &resp.Diagnostics)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"plugins_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_info

// NewInfoDataSource returns the docker_info data source.
func NewInfoDataSource() datasource.DataSource { return &objInfoDS{} }

type objInfoDS struct{ objDSBase }

type objInfoModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
	contractEngineInfo
}

func (d *objInfoDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_info"
}

func (d *objInfoDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id":   objStrOut("Daemon ID (`ID` in `GET /info`)."),
		"name": objStrOut("Host name of the daemon (`Name` in `GET /info`)."),
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads daemon and host information from the provider's Docker host (`GET /info`), like `docker info`. " +
			"Use `info` for fields such as the swarm state, storage driver, CPU count and registry configuration.",
		Attributes: contractAddDataSourceAttributes(attrs, "engine:info"),
	}
}

func (d *objInfoDS) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !objReady(d.pd, &resp.Diagnostics) {
		return
	}
	res, err := d.pd.Client.Info(ctx, client.InfoOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot read daemon info", err.Error())
		return
	}
	var m objInfoModel
	m.ID, m.Name = types.StringValue(res.Info.ID), types.StringValue(res.Info.Name)
	raw := objJSON(res.Info, &resp.Diagnostics)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"info_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_version

// NewVersionDataSource returns the docker_version data source.
func NewVersionDataSource() datasource.DataSource { return &objVersionDS{} }

type objVersionDS struct{ objDSBase }

type objVersionModel struct {
	ID           types.String `tfsdk:"id"`
	PlatformName types.String `tfsdk:"platform_name"`
	contractEngineVersion
}

func (d *objVersionDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_version"
}

func (d *objVersionDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id":            objStrOut("ID of the daemon that answered (`ID` in `GET /info`)."),
		"platform_name": objStrOut("Platform name of the daemon, e.g. `Docker Engine - Community`."),
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the Docker Engine version, API version range and component versions of the provider's Docker host " +
			"(`GET /version`), like the server half of `docker version`.",
		Attributes: contractAddDataSourceAttributes(attrs, "engine:version"),
	}
}

// objVersionBody rebuilds the GET /version body from the client's result:
// the client folds GitCommit, GoVersion, KernelVersion and BuildTime into the
// Engine component's details, so they are read back from there.
func objVersionBody(v client.ServerVersionResult) map[string]any {
	body := map[string]any{
		"Platform":      map[string]any{"Name": v.Platform.Name},
		"Components":    v.Components,
		"Version":       v.Version,
		"ApiVersion":    v.APIVersion,
		"MinAPIVersion": v.MinAPIVersion,
		"Os":            v.Os,
		"Arch":          v.Arch,
		"Experimental":  v.Experimental,
	}
	for _, c := range v.Components {
		if c.Name != "Engine" {
			continue
		}
		for _, k := range []string{"GitCommit", "GoVersion", "KernelVersion", "BuildTime"} {
			if s, ok := c.Details[k]; ok {
				body[k] = s
			}
		}
	}
	return body
}

func (d *objVersionDS) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !objReady(d.pd, &resp.Diagnostics) {
		return
	}
	v, err := d.pd.Client.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot read daemon version", err.Error())
		return
	}
	var m objVersionModel
	m.PlatformName = types.StringValue(v.Platform.Name)
	m.ID = types.StringNull()
	if info, err := d.pd.Client.Info(ctx, client.InfoOptions{}); err == nil {
		m.ID = types.StringValue(info.Info.ID)
	} else {
		resp.Diagnostics.AddWarning("Cannot read daemon ID", "GET /info failed, so `id` is null: "+err.Error())
	}
	raw := objJSON(objVersionBody(v), &resp.Diagnostics)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"version_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_system_df

// NewSystemDfDataSource returns the docker_system_df data source.
func NewSystemDfDataSource() datasource.DataSource { return &objSystemDfDS{} }

type objSystemDfDS struct{ objDSBase }

type objSystemDfModel struct {
	Verbose types.Bool   `tfsdk:"verbose"`
	ID      types.String `tfsdk:"id"`
	contractEngineSystemDf
}

func (d *objSystemDfDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_df"
}

func (d *objSystemDfDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"verbose": schema.BoolAttribute{Optional: true,
			MarkdownDescription: "Include the per-object `items` lists (`docker system df -v`). Default `false`: only the counts and sizes."},
		"id": objStrOut("ID of the daemon the usage was read from (`ID` in `GET /info`)."),
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads disk usage by images, containers, volumes and build cache on the provider's Docker host " +
			"(`GET /system/df`), like `docker system df`.",
		Attributes: contractAddDataSourceAttributes(attrs, "engine:system-df"),
	}
}

// objDfUsage is one usage block of the GET /system/df body (API 1.52+ shape).
func objDfUsage(active, total, reclaimable, size int64, items any, verbose bool) map[string]any {
	u := map[string]any{"ActiveCount": active, "TotalCount": total, "Reclaimable": reclaimable, "TotalSize": size}
	if verbose {
		// The Engine builds these lists from maps; sort them (by ID, or
		// Name for volumes) so two reads give the same df_json.
		if sorted, err := rSortJSONArray(items, "ID", "Id", "Name"); err == nil {
			u["Items"] = sorted
		} else {
			u["Items"] = items
		}
	}
	return u
}

func objDfBody(du client.DiskUsageResult, verbose bool) map[string]any {
	return map[string]any{
		"ImageUsage":      objDfUsage(du.Images.ActiveCount, du.Images.TotalCount, du.Images.Reclaimable, du.Images.TotalSize, du.Images.Items, verbose),
		"ContainerUsage":  objDfUsage(du.Containers.ActiveCount, du.Containers.TotalCount, du.Containers.Reclaimable, du.Containers.TotalSize, du.Containers.Items, verbose),
		"VolumeUsage":     objDfUsage(du.Volumes.ActiveCount, du.Volumes.TotalCount, du.Volumes.Reclaimable, du.Volumes.TotalSize, du.Volumes.Items, verbose),
		"BuildCacheUsage": objDfUsage(du.BuildCache.ActiveCount, du.BuildCache.TotalCount, du.BuildCache.Reclaimable, du.BuildCache.TotalSize, du.BuildCache.Items, verbose),
	}
}

func (d *objSystemDfDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !objReady(d.pd, &resp.Diagnostics) {
		return
	}
	var m objSystemDfModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	verbose := m.Verbose.ValueBool()
	du, err := d.pd.Client.DiskUsage(ctx, client.DiskUsageOptions{Containers: true, Images: true, Volumes: true, BuildCache: true, Verbose: verbose})
	if err != nil {
		resp.Diagnostics.AddError("Cannot read disk usage", err.Error())
		return
	}
	m.ID = types.StringNull()
	if info, err := d.pd.Client.Info(ctx, client.InfoOptions{}); err == nil {
		m.ID = types.StringValue(info.Info.ID)
	} else {
		resp.Diagnostics.AddWarning("Cannot read daemon ID", "GET /info failed, so `id` is null: "+err.Error())
	}
	raw := objJSON(objDfBody(du, verbose), &resp.Diagnostics)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"df_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
