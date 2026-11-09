package provider

// Compose list data sources, answered from the Engine by the
// com.docker.compose.* labels like the CLI does:
//   - data.docker_compose_containers (compose:cli-ps, Q13)
//   - data.docker_compose_projects   (compose:cli-ls, Q13)
//   - data.docker_compose_volumes    (compose:cli-volumes, Q14)
//   - data.docker_compose_images     (compose:cli-images)

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// composeDSBase is the shared Configure of the compose data sources.
type composeDSBase struct{ pd *providerData }

func (d *composeDSBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *composeDSBase) ready(resp *datasource.ReadResponse) bool {
	if d.pd == nil || d.pd.Client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured before the data source was read.")
		return false
	}
	return true
}

func composeDSProjectAttr() dschema.StringAttribute {
	return dschema.StringAttribute{Required: true,
		MarkdownDescription: "The compose project name (`com.docker.compose.project` label, CLI `--project-name`)."}
}

func composeDSServicesAttr() dschema.ListAttribute {
	return dschema.ListAttribute{ElementType: types.StringType, Optional: true,
		MarkdownDescription: "Only these services (default: every service of the project)."}
}

func composeContainerIDsAttr() dschema.MapAttribute {
	return dschema.MapAttribute{ElementType: types.ListType{ElemType: types.StringType}, Computed: true,
		MarkdownDescription: "Map of service name to the IDs of its selected containers, in container-number order."}
}

// ---------------------------------------------------------------------------
// docker_compose_containers

// NewComposeContainersDataSource returns data.docker_compose_containers.
func NewComposeContainersDataSource() datasource.DataSource { return &composeContainersDataSource{} }

type composeContainersDataSource struct{ composeDSBase }

type composeContainersModel struct {
	contractComposeCliPs
	Name         types.String `tfsdk:"name"`
	Services     types.List   `tfsdk:"services"`
	All          types.Bool   `tfsdk:"all"`
	ContainerIDs types.Map    `tfsdk:"container_ids"`
}

func (d *composeContainersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_containers"
}

func (d *composeContainersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Lists the containers of a compose project, like `docker compose ps`, by the `com.docker.compose.project` " +
			"label (one-off `run` containers excluded). Works for projects created by `docker_compose` and by the compose CLI.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"name":     composeDSProjectAttr(),
			"services": composeDSServicesAttr(),
			"all": dschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Include stopped containers (CLI `--all`). Default `false`: running containers only."},
			"container_ids": composeContainerIDsAttr(),
		}, "compose:cli-ps"),
	}
}

func (d *composeContainersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composeContainersModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	cs, err := composeContainers(ctx, d.pd, composeSelector{
		Project: m.Name.ValueString(), Services: stringListFrom(ctx, m.Services, &resp.Diagnostics), All: m.All.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list compose containers", err.Error())
		return
	}
	list := make([]any, 0, len(cs))
	for _, c := range cs {
		list = append(list, composePlain(c, map[string]any{
			"service": c.Labels[composeLabelService],
			"number":  c.Labels[composeLabelNumber],
		}))
	}
	m.ContainerIDs = composeStringListMap(composeContainerIDs(cs))
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Structured: map[string]any{"containers": list},
		Raw:        map[string][]byte{"containers_json": composeJSON(cs)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// composeJSON is json.Marshal that never fails the caller (null on error),
// with a nil slice written as [].
func composeJSON(v any) []byte {
	b, err := composeMarshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// ---------------------------------------------------------------------------
// docker_compose_projects

// NewComposeProjectsDataSource returns data.docker_compose_projects.
func NewComposeProjectsDataSource() datasource.DataSource { return &composeProjectsDataSource{} }

type composeProjectsDataSource struct{ composeDSBase }

type composeProjectsModel struct {
	contractComposeCliLs
	All   types.Bool `tfsdk:"all"`
	Names types.List `tfsdk:"names"`
}

func (d *composeProjectsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_projects"
}

func (d *composeProjectsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Lists the compose projects on the daemon, like `docker compose ls`: containers grouped by the " +
			"`com.docker.compose.project` label, each with a status such as `running(2), exited(1)` and its config files.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"all": dschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Include projects whose containers are all stopped (CLI `--all`). Default `false`."},
			"names": dschema.ListAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "The project names, sorted."},
		}, "compose:cli-ls"),
	}
}

// composeProjectStatus formats per-state counts like the CLI: "running(2), exited(1)".
func composeProjectStatus(states map[string]int) string {
	keys := composeSortedKeys(states)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s(%d)", k, states[k]))
	}
	return strings.Join(parts, ", ")
}

func (d *composeProjectsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composeProjectsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	res, err := d.pd.Client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", composeLabelProject)})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list compose projects", err.Error())
		return
	}
	type proj struct {
		states  map[string]int
		files   string
		running bool
	}
	byName := map[string]*proj{}
	for _, c := range res.Items {
		name := c.Labels[composeLabelProject]
		p := byName[name]
		if p == nil {
			p = &proj{states: map[string]int{}}
			byName[name] = p
		}
		p.states[string(c.State)]++
		if c.State == container.StateRunning {
			p.running = true
		}
		if f := c.Labels[composeLabelConfigFiles]; f != "" && p.files == "" {
			p.files = f
		}
	}
	var names []string
	var list []map[string]any
	for _, name := range composeSortedKeys(byName) {
		p := byName[name]
		if !p.running && !m.All.ValueBool() {
			continue
		}
		names = append(names, name)
		list = append(list, map[string]any{"Name": name, "Status": composeProjectStatus(p.states), "ConfigFiles": p.files})
	}
	if list == nil {
		list = []map[string]any{}
	}
	m.Names = stringListValue(names, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw: map[string][]byte{"projects_json": composeJSON(list)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_compose_volumes

// NewComposeVolumesDataSource returns data.docker_compose_volumes.
func NewComposeVolumesDataSource() datasource.DataSource { return &composeVolumesDataSource{} }

type composeVolumesDataSource struct{ composeDSBase }

type composeVolumesModel struct {
	contractComposeCliVolumes
	Name  types.String `tfsdk:"name"`
	Names types.List   `tfsdk:"names"`
}

func (d *composeVolumesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_volumes"
}

func (d *composeVolumesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Lists the volumes of a compose project, like `docker compose volumes`, by the `com.docker.compose.project` label.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"name": composeDSProjectAttr(),
			"names": dschema.ListAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "The volume names, sorted."},
		}, "compose:cli-volumes"),
	}
}

func (d *composeVolumesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composeVolumesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	res, err := d.pd.Client.VolumeList(ctx, client.VolumeListOptions{Filters: client.Filters{}.Add("label", composeLabelProject+"="+m.Name.ValueString())})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list compose volumes", err.Error())
		return
	}
	vols := res.Items
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	names := make([]string, 0, len(vols))
	for _, v := range vols {
		names = append(names, v.Name)
	}
	m.Names = stringListValue(names, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw: map[string][]byte{"volumes_json": composeJSON(vols)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_compose_images

// NewComposeImagesDataSource returns data.docker_compose_images.
func NewComposeImagesDataSource() datasource.DataSource { return &composeImagesDataSource{} }

type composeImagesDataSource struct{ composeDSBase }

type composeImagesModel struct {
	contractComposeCliImages
	Name            types.String `tfsdk:"name"`
	Services        types.List   `tfsdk:"services"`
	ImageRefs       types.Map    `tfsdk:"image_refs"`
	ImageDigestRefs types.Map    `tfsdk:"image_digest_refs"`
	ImageIDs        types.Map    `tfsdk:"image_ids"`
}

func (d *composeImagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_images"
}

func (d *composeImagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Lists the images the containers of a compose project use, like `docker compose images`: one entry per " +
			"container (stopped ones included) with the image inspect fields.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"name":     composeDSProjectAttr(),
			"services": composeDSServicesAttr(),
			"image_refs": dschema.MapAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Map of service name to the image reference its containers were created from."},
			"image_digest_refs": dschema.MapAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Map of service name to the image's first repo digest (`repo@sha256:...`), when it has one."},
			"image_ids": dschema.MapAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Map of service name to the image ID."},
		}, "compose:cli-images"),
	}
}

func (d *composeImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composeImagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	cs, err := composeContainers(ctx, d.pd, composeSelector{Project: m.Name.ValueString(), Services: stringListFrom(ctx, m.Services, &resp.Diagnostics), All: true})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list compose containers", err.Error())
		return
	}
	refs, digests, ids := map[string]string{}, map[string]string{}, map[string]string{}
	list := []any{}
	cache := map[string]map[string]any{}
	for _, c := range cs {
		svc := c.Labels[composeLabelService]
		img, ok := cache[c.ImageID]
		if !ok {
			res, err := d.pd.Client.ImageInspect(ctx, c.ImageID)
			if err != nil {
				resp.Diagnostics.AddWarning("Cannot inspect image", fmt.Sprintf("container %s image %s: %v", composeSummaryName(c), c.ImageID, err))
				img = map[string]any{"Id": c.ImageID}
			} else {
				img = composePlain(res.InspectResponse, nil)
				if len(res.RepoDigests) > 0 {
					digests[svc] = res.RepoDigests[0]
				}
			}
			cache[c.ImageID] = img
		}
		refs[svc] = c.Image
		ids[svc] = c.ImageID
		entry := map[string]any{}
		for k, v := range img {
			entry[k] = v
		}
		entry["service"] = svc
		entry["container_name"] = composeSummaryName(c)
		list = append(list, entry)
	}
	m.ImageRefs = stringMapValue(refs, false)
	m.ImageDigestRefs = stringMapValue(digests, false)
	m.ImageIDs = stringMapValue(ids, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Structured: map[string]any{"images": list},
		Raw:        map[string][]byte{"images_json": composeJSON(list)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
