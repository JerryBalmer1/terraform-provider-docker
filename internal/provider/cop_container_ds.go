package provider

// Pass 5 Lane A (WP cop): read-only container data sources:
// docker_container (inspect), docker_container_changes (diff),
// docker_container_stats (one sample), docker_container_top (ps) and
// docker_container_file (archive stat + content).

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSourceWithConfigure        = (*copContainerDS)(nil)
	_ datasource.DataSourceWithConfigValidators = (*copContainerDS)(nil)
	_ datasource.DataSourceWithConfigure        = (*copContainerChangesDS)(nil)
	_ datasource.DataSourceWithConfigure        = (*copContainerStatsDS)(nil)
	_ datasource.DataSourceWithConfigure        = (*copContainerTopDS)(nil)
	_ datasource.DataSourceWithConfigure        = (*copContainerFileDS)(nil)
)

// copFileMaxBytes caps the file content docker_container_file returns.
const copFileMaxBytes = 32 << 20

// ---------------------------------------------------------------------------
// data docker_container

// NewContainerDataSource returns the docker_container data source.
func NewContainerDataSource() datasource.DataSource { return &copContainerDS{} }

type copContainerDS struct{ copDSBase }

type copContainerDSModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	ImageID types.String `tfsdk:"image_id"`
	Labels  types.Map    `tfsdk:"labels"`
	contractEngineContainerData
}

func (d *copContainerDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container"
}

func (d *copContainerDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{Optional: true, Computed: true,
			MarkdownDescription: "Container ID (a unique prefix works). Set exactly one of `id` and `name`; the other is filled in."},
		"name": schema.StringAttribute{Optional: true, Computed: true,
			MarkdownDescription: "Container name, without the leading `/`. Set exactly one of `id` and `name`; the other is filled in."},
		"image_id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the image the container was created from."},
		"labels":   schema.MapAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Container labels (`Config.Labels`)."},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one container on the provider's Docker host by ID or name, like `docker container inspect`. " +
			"Use it for containers Terraform does not manage; the full inspect response is in `inspect` and `inspect_json`.",
		Attributes: contractAddDataSourceAttributes(attrs, "engine:container-data"),
	}
}

func (d *copContainerDS) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *copContainerDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m copContainerDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.ID.ValueString()
	if ref == "" {
		ref = m.Name.ValueString()
	}
	insp, raw, err := copInspect(ctx, d.pd.Client, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "data docker_container", ref, err)
		return
	}
	m.ID = types.StringValue(insp.ID)
	m.Name = types.StringValue(copName(insp.Name))
	m.ImageID = types.StringValue(insp.Image)
	var labels map[string]string
	if insp.Config != nil {
		labels = insp.Config.Labels
	}
	m.Labels = stringMapValue(labels, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// data docker_container_changes

// NewContainerChangesDataSource returns the docker_container_changes data source.
func NewContainerChangesDataSource() datasource.DataSource { return &copContainerChangesDS{} }

type copContainerChangesDS struct{ copDSBase }

type copContainerChangesModel struct {
	Container   types.String `tfsdk:"container"`
	ContainerID types.String `tfsdk:"container_id"`
	contractEngineContainerChanges
}

func (d *copContainerChangesDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_changes"
}

func (d *copContainerChangesDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the filesystem changes of a container against its image, like `docker container diff`. " +
			"Each entry of `changes` has a `path` and a `kind`: 0 modified, 1 added, 2 deleted.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"container":    copContainerInputAttr(),
			"container_id": copContainerIDAttr("the changes were read from"),
		}, "engine:container-changes"),
	}
}

func (d *copContainerChangesDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m copContainerChangesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.Container.ValueString()
	insp, _, err := copInspect(ctx, d.pd.Client, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "data docker_container_changes", ref, err)
		return
	}
	res, err := d.pd.Client.ContainerDiff(ctx, insp.ID, client.ContainerDiffOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot read container changes", fmt.Sprintf("container %q: %v", ref, err))
		return
	}
	// Re-encoded (ContainerDiff keeps no body) in canonical form, sorted by
	// path so the order does not depend on the snapshotter's walk.
	sorted, err := rSortJSONArray(res.Changes, "Path")
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode container changes", err.Error())
		return
	}
	raw, err := rCanonicalList(sorted)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode container changes", err.Error())
		return
	}
	m.ContainerID = types.StringValue(insp.ID)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"changes_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// data docker_container_stats

// NewContainerStatsDataSource returns the docker_container_stats data source.
func NewContainerStatsDataSource() datasource.DataSource { return &copContainerStatsDS{} }

type copContainerStatsDS struct{ copDSBase }

type copContainerStatsModel struct {
	Container             types.String `tfsdk:"container"`
	IncludePreviousSample types.Bool   `tfsdk:"include_previous_sample"`
	ID                    types.String `tfsdk:"id"`
	Name                  types.String `tfsdk:"name"`
	contractEngineContainerStats
}

func (d *copContainerStatsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_stats"
}

func (d *copContainerStatsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads one resource usage sample of a container (CPU, memory, network, block I/O, PIDs), like `docker container stats --no-stream`. " +
			"The sample is taken at read time and changes on every plan.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"container": copContainerInputAttr(),
			"include_previous_sample": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Let the daemon take a second sample one second earlier so `precpu_stats` is filled and CPU percentages can be computed (Engine `one-shot=false`). Default `false`: one immediate sample."},
			"id":   schema.StringAttribute{Computed: true, MarkdownDescription: "Container ID."},
			"name": schema.StringAttribute{Computed: true, MarkdownDescription: "Container name, without the leading `/`."},
		}, "engine:container-stats"),
	}
}

func (d *copContainerStatsDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m copContainerStatsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.Container.ValueString()
	insp, _, err := copInspect(ctx, d.pd.Client, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "data docker_container_stats", ref, err)
		return
	}
	res, err := d.pd.Client.ContainerStats(ctx, insp.ID, client.ContainerStatsOptions{Stream: false, IncludePreviousSample: m.IncludePreviousSample.ValueBool()})
	if err != nil {
		resp.Diagnostics.AddError("Cannot read container stats", fmt.Sprintf("container %q: %v", ref, err))
		return
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		resp.Diagnostics.AddError("Cannot read container stats", err.Error())
		return
	}
	var probe json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		resp.Diagnostics.AddError("Cannot read container stats", fmt.Sprintf("the daemon returned no JSON object: %v", err))
		return
	}
	m.ID = types.StringValue(insp.ID)
	m.Name = types.StringValue(copName(insp.Name))
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"stats_json": rEngineBody(raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// data docker_container_top

// NewContainerTopDataSource returns the docker_container_top data source.
func NewContainerTopDataSource() datasource.DataSource { return &copContainerTopDS{} }

type copContainerTopDS struct{ copDSBase }

type copContainerTopModel struct {
	Container   types.String `tfsdk:"container"`
	PsArgs      types.String `tfsdk:"ps_args"`
	ContainerID types.String `tfsdk:"container_id"`
	contractEngineContainerTop
}

func (d *copContainerTopDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_top"
}

func (d *copContainerTopDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the processes running in a container, like `docker container top`. " +
			"`top.titles` holds the column names and `top.processes` one row per process; the container must be running.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"container":    copContainerInputAttr(),
			"ps_args":      schema.StringAttribute{Optional: true, MarkdownDescription: "Arguments for `ps` on the host (Engine `ps_args`), e.g. `aux`. Default `-ef`."},
			"container_id": copContainerIDAttr("the processes were read from"),
		}, "engine:container-top"),
	}
}

func (d *copContainerTopDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m copContainerTopModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := m.Container.ValueString()
	insp, _, err := copInspect(ctx, d.pd.Client, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "data docker_container_top", ref, err)
		return
	}
	var args []string
	if s := m.PsArgs.ValueString(); s != "" {
		args = []string{s}
	}
	res, err := d.pd.Client.ContainerTop(ctx, insp.ID, client.ContainerTopOptions{Arguments: args})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list container processes", fmt.Sprintf("container %q: %v", ref, err))
		return
	}
	if res.Titles == nil {
		res.Titles = []string{}
	}
	if res.Processes == nil {
		res.Processes = [][]string{}
	}
	// Re-encoded (ContainerTop keeps no body) in canonical form; the
	// process rows keep the order ps printed them in.
	raw, err := rCanonicalJSON(map[string]any{"Titles": res.Titles, "Processes": res.Processes})
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode container processes", err.Error())
		return
	}
	m.ContainerID = types.StringValue(insp.ID)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"top_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// data docker_container_file

// NewContainerFileDataSource returns the docker_container_file data source.
func NewContainerFileDataSource() datasource.DataSource { return &copContainerFileDS{} }

type copContainerFileDS struct{ copDSBase }

type copContainerFileModel struct {
	Container   types.String `tfsdk:"container"`
	Path        types.String `tfsdk:"path"`
	ContainerID types.String `tfsdk:"container_id"`
	contractEngineContainerFile
}

func (d *copContainerFileDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_file"
}

func (d *copContainerFileDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a path inside a container: its stat (name, size, mode, mtime, link target) and, for a regular file, its content as base64. " +
			"It uses the Engine archive API (`HEAD` and `GET /containers/{id}/archive`), like `docker container cp CONTAINER:PATH -`, and works on stopped containers too. " +
			"For a directory or symlink only `stat` is filled and `content_base64` is null. Files larger than 32 MiB are an error. " +
			"Decode the content with `base64decode()` (UTF-8 text) or pass it on as is.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"container":    copContainerInputAttr(),
			"path":         schema.StringAttribute{Required: true, MarkdownDescription: "Absolute path inside the container, e.g. `/etc/nginx/nginx.conf`."},
			"container_id": copContainerIDAttr("the file was read from"),
		}, "engine:container-file"),
	}
}

func (d *copContainerFileDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m copContainerFileModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !d.pd.gateItemAPI(ctx, "engine:container-file", &resp.Diagnostics) {
		return
	}
	ref, p := m.Container.ValueString(), m.Path.ValueString()
	insp, _, err := copInspect(ctx, d.pd.Client, ref)
	if err != nil {
		copInspectErr(&resp.Diagnostics, "data docker_container_file", ref, err)
		return
	}
	st, err := d.pd.Client.ContainerStatPath(ctx, insp.ID, client.ContainerStatPathOptions{Path: p})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("path"), "Cannot stat container path", fmt.Sprintf("%s in container %q: %v", p, ref, err))
		return
	}
	// The X-Docker-Container-Path-Stat header, decoded by the client and
	// re-encoded in canonical form.
	statJSON, err := rCanonicalJSON(st.Stat)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode path stat", err.Error())
		return
	}
	in := contractFillInput{Raw: map[string][]byte{"stat_json": statJSON}}
	if st.Stat.Mode.IsRegular() {
		if st.Stat.Size > copFileMaxBytes {
			resp.Diagnostics.AddAttributeError(path.Root("path"), "File too large",
				fmt.Sprintf("%s in container %q is %d bytes; docker_container_file reads at most %d bytes.", p, ref, st.Stat.Size, copFileMaxBytes))
			return
		}
		cp, err := d.pd.Client.CopyFromContainer(ctx, insp.ID, client.CopyFromContainerOptions{SourcePath: p})
		if err != nil {
			resp.Diagnostics.AddError("Cannot read container file", fmt.Sprintf("%s in container %q: %v", p, ref, err))
			return
		}
		content, err := copFirstTarFile(cp.Content, copFileMaxBytes)
		cp.Content.Close()
		if err != nil {
			resp.Diagnostics.AddError("Cannot read container file", fmt.Sprintf("%s in container %q: %v", p, ref, err))
			return
		}
		in.Raw["content_base64"] = []byte(base64.StdEncoding.EncodeToString(content))
	}
	m.ContainerID = types.StringValue(insp.ID)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, in)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// copFirstTarFile returns the content of the first regular file in a tar
// stream, reading at most max bytes of it.
func copFirstTarFile(r io.Reader, max int64) ([]byte, error) {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the archive holds no regular file")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > max {
			return nil, fmt.Errorf("file is %d bytes, more than the %d byte limit", h.Size, max)
		}
		return io.ReadAll(io.LimitReader(tr, max))
	}
}
