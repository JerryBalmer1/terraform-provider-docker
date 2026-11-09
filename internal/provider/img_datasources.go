package provider

// Pass 5 Lane A (WP img): data docker_image_history, docker_image_search,
// docker_image_attestations and docker_images. Thin wrappers over one Engine
// API call each; the outputs follow the contract (identity attributes, the
// `*_json` raw output, its structured twin, content_sha256 and the versions).

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

// imgDSBase is shared by the image list data sources.
type imgDSBase struct {
	pd *providerData
}

func (d *imgDSBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// ready reports whether the provider is configured.
func (d *imgDSBase) ready(diags *diag.Diagnostics) bool {
	if d.pd == nil || d.pd.Client == nil {
		diags.AddError("Provider not configured", "The docker provider was not configured.")
		return false
	}
	return true
}

// imgMarshal returns the canonical JSON of v (rCanonicalJSON), with an
// empty list for a nil slice.
func imgMarshal(v any) ([]byte, error) {
	b, err := rCanonicalJSON(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return []byte("[]"), nil
	}
	return b, nil
}

func imgPlatformDSAttr(api string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true,
		MarkdownDescription: "Platform of a multi-platform image to read, `os[/arch[/variant]]`. Default: the daemon's platform. " +
			"Needs API " + api + "+ (containerd image store).",
	}
}

// ---------------------------------------------------------------------------
// docker_image_history

var (
	_ datasource.DataSource              = (*imgHistoryDS)(nil)
	_ datasource.DataSourceWithConfigure = (*imgHistoryDS)(nil)
)

// NewImageHistoryDataSource returns the docker_image_history data source.
func NewImageHistoryDataSource() datasource.DataSource { return &imgHistoryDS{} }

type imgHistoryDS struct{ imgDSBase }

type imgHistoryModel struct {
	Name     types.String `tfsdk:"name"`
	Platform types.String `tfsdk:"platform"`
	ID       types.String `tfsdk:"id"`
	contractEngineImageHistory
}

func (d *imgHistoryDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_history"
}

func (d *imgHistoryDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the layer history of a local image, like `docker image history` (`GET /images/{name}/history`). " +
			"Entries come newest first, as the daemon returns them; each one is the instruction that created a layer.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name":     schema.StringAttribute{Required: true, MarkdownDescription: "Image name, tag or ID, e.g. `nginx:1.27`."},
			"platform": imgPlatformDSAttr("1.48"),
			"id":       schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the image (the first history entry)."},
		}, "engine:image-history"),
	}
}

func (d *imgHistoryDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m imgHistoryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(&resp.Diagnostics) {
		return
	}
	var opts []client.ImageHistoryOption
	if p := m.Platform.ValueString(); p != "" {
		opts = append(opts, client.ImageHistoryWithPlatform(platformFromString(p)))
	}
	res, err := d.pd.Client.ImageHistory(ctx, m.Name.ValueString(), opts...)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read image history", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
		return
	}
	raw, err := imgMarshal(res.Items)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode image history", err.Error())
		return
	}
	m.ID = types.StringNull()
	if len(res.Items) > 0 {
		m.ID = stringOrNull(res.Items[0].ID)
	}
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"history_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_image_search

var (
	_ datasource.DataSource              = (*imgSearchDS)(nil)
	_ datasource.DataSourceWithConfigure = (*imgSearchDS)(nil)
)

// NewImageSearchDataSource returns the docker_image_search data source.
func NewImageSearchDataSource() datasource.DataSource { return &imgSearchDS{} }

type imgSearchDS struct{ imgDSBase }

type imgSearchModel struct {
	Term       types.String `tfsdk:"term"`
	Limit      types.Int64  `tfsdk:"limit"`
	IsOfficial types.Bool   `tfsdk:"is_official"`
	Stars      types.Int64  `tfsdk:"stars"`
	Names      types.List   `tfsdk:"names"`
	contractEngineImageSearch
}

func (d *imgSearchDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_search"
}

func (d *imgSearchDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Searches Docker Hub (or the registry named in `term`) through the daemon, like `docker search` (`GET /images/search`). " +
			"Results are in the registry's order; the provider's `registry_auth` for that registry is sent.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"term": schema.StringAttribute{Required: true, MarkdownDescription: "Search term, e.g. `nginx` or `registry.example.com/team`."},
			"limit": schema.Int64Attribute{
				Optional: true, Validators: []validator.Int64{int64validator.Between(1, 100)},
				MarkdownDescription: "Maximum number of results (`--limit`), 1 to 100. Default: the registry's (25 on Docker Hub).",
			},
			"is_official": schema.BoolAttribute{Optional: true, MarkdownDescription: "Only official images (`true`) or only non-official ones (`false`) (`--filter is-official`)."},
			"stars": schema.Int64Attribute{
				Optional: true, Validators: []validator.Int64{int64validator.AtLeast(0)},
				MarkdownDescription: "Only images with at least this many stars (`--filter stars`).",
			},
			"names": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Repository names found, in result order."},
		}, "engine:image-search"),
	}
}

func (d *imgSearchDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m imgSearchModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(&resp.Diagnostics) {
		return
	}
	f := client.Filters{}
	if !m.IsOfficial.IsNull() {
		f.Add("is-official", fmt.Sprint(m.IsOfficial.ValueBool()))
	}
	if !m.Stars.IsNull() {
		f.Add("stars", fmt.Sprint(m.Stars.ValueInt64()))
	}
	term := m.Term.ValueString()
	auth, _ := d.pd.registryAuthFor(term)
	res, err := d.pd.Client.ImageSearch(ctx, term, client.ImageSearchOptions{RegistryAuth: auth, Filters: f, Limit: int(m.Limit.ValueInt64())})
	if err != nil {
		resp.Diagnostics.AddError("Cannot search images", fmt.Sprintf("%s: %v", term, err))
		return
	}
	raw, err := imgMarshal(res.Items)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode search results", err.Error())
		return
	}
	names := make([]string, 0, len(res.Items))
	for _, r := range res.Items {
		names = append(names, r.Name)
	}
	m.Names = stringListValue(names, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"results_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_image_attestations

var (
	_ datasource.DataSource              = (*imgAttestationsDS)(nil)
	_ datasource.DataSourceWithConfigure = (*imgAttestationsDS)(nil)
)

// NewImageAttestationsDataSource returns the docker_image_attestations data source.
func NewImageAttestationsDataSource() datasource.DataSource { return &imgAttestationsDS{} }

type imgAttestationsDS struct{ imgDSBase }

type imgAttestationsModel struct {
	Name             types.String `tfsdk:"name"`
	Platform         types.String `tfsdk:"platform"`
	PredicateTypes   types.List   `tfsdk:"predicate_types"`
	IncludeStatement types.Bool   `tfsdk:"include_statement"`
	ImageRef         types.String `tfsdk:"image_ref"`
	Digests          types.List   `tfsdk:"digests"`
	contractEngineImageAttestations
}

const imgItemAttestations = "engine:image-attestations"

func (d *imgAttestationsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_attestations"
}

func (d *imgAttestationsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the in-toto attestations (SBOM, provenance) stored with a local image (`GET /images/{name}/attestations`, Engine API 1.55+). " +
			"The image must be in the containerd image store with its attestation manifests, e.g. pulled from a registry or built with `provenance` / `sbom`.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name":     schema.StringAttribute{Required: true, MarkdownDescription: "Image name, tag or ID, e.g. `ghcr.io/org/app:1.0`."},
			"platform": imgPlatformDSAttr("1.55"),
			"predicate_types": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Only attestations with one of these in-toto predicate types, e.g. `https://spdx.dev/Document` or `https://slsa.dev/provenance/v1`.",
			},
			"include_statement": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Include each in-toto statement itself (`attestations[*].statement`, JSON). Default `false`: descriptors and predicate types only.",
			},
			"image_ref": schema.StringAttribute{Computed: true, MarkdownDescription: "The image reference whose attestations were read (`name`, normalized when it is a tag)."},
			"digests": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Digests of the attestation manifests (`Descriptor.digest`), in response order.",
			},
		}, imgItemAttestations),
	}
}

func (d *imgAttestationsDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m imgAttestationsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(&resp.Diagnostics) {
		return
	}
	if !d.pd.gateItemAPI(ctx, imgItemAttestations, &resp.Diagnostics) {
		return
	}
	var opts []client.ImageAttestationsOption
	if p := m.Platform.ValueString(); p != "" {
		opts = append(opts, client.ImageAttestationsWithPlatform(platformFromString(p)))
	}
	if pts := stringListFrom(ctx, m.PredicateTypes, &resp.Diagnostics); len(pts) > 0 {
		opts = append(opts, client.ImageAttestationsWithPredicateTypes(pts...))
	}
	if m.IncludeStatement.ValueBool() {
		opts = append(opts, client.ImageAttestationsWithStatement())
	}
	name := m.Name.ValueString()
	res, err := d.pd.Client.ImageAttestations(ctx, name, opts...)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read image attestations", fmt.Sprintf("%s: %v", name, err))
		return
	}
	raw, err := imgMarshal(res.Items)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode attestations", err.Error())
		return
	}
	digests := make([]string, 0, len(res.Items))
	for _, a := range res.Items {
		digests = append(digests, a.Descriptor.Digest.String())
	}
	ref := imgNormalizedRef(name)
	if ref == "" {
		ref = name
	}
	m.ImageRef = types.StringValue(ref)
	m.Digests = stringListValue(digests, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"attestations_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_images

var (
	_ datasource.DataSource              = (*imgImagesDS)(nil)
	_ datasource.DataSourceWithConfigure = (*imgImagesDS)(nil)
)

// NewImagesDataSource returns the docker_images data source.
func NewImagesDataSource() datasource.DataSource { return &imgImagesDS{} }

type imgImagesDS struct{ imgDSBase }

type imgImagesModel struct {
	All       types.Bool   `tfsdk:"all"`
	Reference types.String `tfsdk:"reference"`
	Labels    types.Map    `tfsdk:"labels"`
	Dangling  types.Bool   `tfsdk:"dangling"`
	Before    types.String `tfsdk:"before"`
	Since     types.String `tfsdk:"since"`
	Until     types.String `tfsdk:"until"`
	IDs       types.List   `tfsdk:"ids"`
	contractEngineImages
}

func (d *imgImagesDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imgImagesDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the images in the provider's daemon image store, like `docker image ls` (`GET /images/json`), sorted by ID. " +
			"The optional arguments are Engine API filters.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"all":       schema.BoolAttribute{Optional: true, MarkdownDescription: "Also list intermediate images (`--all`). Default `false`."},
			"reference": str("Only images whose reference matches this pattern, e.g. `nginx` or `ghcr.io/org/*:1.*` (`reference` filter)."),
			"labels": schema.MapAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Only images carrying every one of these labels; an empty value matches any value (`label` filter).",
			},
			"dangling": schema.BoolAttribute{Optional: true, MarkdownDescription: "`true` lists only untagged images, `false` only tagged ones (`dangling` filter)."},
			"before":   str("Only images created before this image (name or ID) (`before` filter)."),
			"since":    str("Only images created after this image (name or ID) (`since` filter)."),
			"until":    str("Only images created before this time: a timestamp or a Go duration such as `24h` (`until` filter)."),
			"ids":      schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "IDs of the listed images, sorted."},
		}, "engine:images"),
	}
}

func (d *imgImagesDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m imgImagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(&resp.Diagnostics) {
		return
	}
	f := client.Filters{}
	for k, v := range map[string]types.String{"reference": m.Reference, "before": m.Before, "since": m.Since, "until": m.Until} {
		if v.ValueString() != "" {
			f.Add(k, v.ValueString())
		}
	}
	if !m.Dangling.IsNull() {
		f.Add("dangling", fmt.Sprint(m.Dangling.ValueBool()))
	}
	p3AddLabelFilters(f, "label", stringMapFrom(ctx, m.Labels, &resp.Diagnostics))
	res, err := d.pd.Client.ImageList(ctx, client.ImageListOptions{All: m.All.ValueBool(), Filters: f})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list images", err.Error())
		return
	}
	items := res.Items
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	raw, err := imgMarshal(items)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode image list", err.Error())
		return
	}
	ids := make([]string, 0, len(items))
	for _, s := range items {
		ids = append(ids, s.ID)
	}
	m.IDs = stringListValue(ids, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"images_json": raw}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
