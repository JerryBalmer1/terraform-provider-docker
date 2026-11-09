package provider

// docker_registry_image_manifests and docker_registry_image_tags: read a
// repository straight from its registry over the v2 HTTP API.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Media types the manifests data source asks for, index types first.
const (
	p3MediaOCIIndex     = "application/vnd.oci.image.index.v1+json"
	p3MediaDockerList   = "application/vnd.docker.distribution.manifest.list.v2+json"
	p3MediaOCIManifest  = "application/vnd.oci.image.manifest.v1+json"
	p3MediaDockerManif2 = "application/vnd.docker.distribution.manifest.v2+json"
)

var p3ManifestAccept = []string{p3MediaOCIIndex, p3MediaDockerList, p3MediaOCIManifest, p3MediaDockerManif2}

var p3ManifestAttrTypes = map[string]attr.Type{
	"digest":       types.StringType,
	"media_type":   types.StringType,
	"size":         types.Int64Type,
	"os":           types.StringType,
	"architecture": types.StringType,
	"variant":      types.StringType,
	"os_version":   types.StringType,
}

// p3FetchTags lists every tag of the repository, following Link pagination.
func p3FetchTags(ctx context.Context, r *p3Registry, pageSize int64) ([]string, error) {
	next := "/v2/" + r.repo + "/tags/list"
	if pageSize > 0 {
		next += "?n=" + strconv.FormatInt(pageSize, 10)
	}
	tags := []string{}
	seen := map[string]bool{}
	for page := 0; next != ""; page++ {
		if page >= 10000 {
			return nil, fmt.Errorf("tags of %s: more than 10000 pages", r.repo)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		h, err := r.getJSON(ctx, next, &body)
		if err != nil {
			return nil, err
		}
		for _, t := range body.Tags {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
		base := r.base
		next = p3NextLink(h, base)
		// Some registries drop n= from the next link; keep the page size.
		if next != "" && pageSize > 0 && !strings.Contains(next, "n=") {
			if u, err := url.Parse(next); err == nil {
				q := u.Query()
				q.Set("n", strconv.FormatInt(pageSize, 10))
				u.RawQuery = q.Encode()
				next = u.String()
			}
		}
	}
	return tags, nil
}

// ---------------------------------------------------------------------------
// docker_registry_image_manifests

var (
	_ datasource.DataSource              = (*p3RegistryManifestsDS)(nil)
	_ datasource.DataSourceWithConfigure = (*p3RegistryManifestsDS)(nil)
)

// NewRegistryImageManifestsDataSource returns docker_registry_image_manifests.
func NewRegistryImageManifestsDataSource() datasource.DataSource { return &p3RegistryManifestsDS{} }

type p3RegistryManifestsDS struct {
	pd *providerData
}

type p3RegistryManifestsModel struct {
	Name               types.String `tfsdk:"name"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	Attestations       types.Bool   `tfsdk:"attestations"`
	Digest             types.String `tfsdk:"digest"`
	MediaType          types.String `tfsdk:"media_type"`
	ImageRef           types.String `tfsdk:"image_ref"`
	ImageDigestRef     types.String `tfsdk:"image_digest_ref"`
	Platforms          types.List   `tfsdk:"platforms"`
	Manifests          types.List   `tfsdk:"manifests"`
	contractBuildxImagetoolsInspect
}

// regManifestsItem is the primary inventory item of
// docker_registry_image_manifests (cli-only:manifest-inspect shares it).
const regManifestsItem = "buildx:imagetools-inspect"

func (d *p3RegistryManifestsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_image_manifests"
}

func (d *p3RegistryManifestsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	c := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the manifest of an image straight from its registry, with the provider's `registry_auth`, like `docker buildx imagetools inspect` or `docker manifest inspect`. " +
			"For a manifest list or OCI index every entry is listed (attestation manifests appear with platform `unknown/unknown`) and, unless `attestations = false`, " +
			"each platform's image config and its SLSA provenance and SPDX SBOM attestations are read too; " +
			"a single-platform image gives one entry whose platform comes from its config blob. Nothing is pulled.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Image reference, e.g. `nginx:1.27` or `ghcr.io/org/app@sha256:...` (no tag means `latest`)."},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Skip TLS certificate verification of the registry.",
			},
			"attestations": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Read the image config and the attestations of every platform of an index (two to four extra requests per platform). " +
					"Default `true`; `false` reads only the top-level document, and `image_config_json`, `provenance_json` and `sbom_json` stay null for an index.",
			},
			"digest":           c("Digest of the top-level manifest (index, list or image manifest)."),
			"media_type":       c("Media type of the top-level manifest."),
			"image_ref":        c("Normalized reference, e.g. `docker.io/library/alpine:latest`."),
			"image_digest_ref": c("Pinned reference `<registry>/<repository>@sha256:...`, safe to hand to a deploy tool."),
			"platforms": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Platforms as `os/arch[/variant]`, in registry order, excluding attestation manifests.",
			},
			"manifests": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Entries of the top-level document, in registry order (one entry for a single image manifest).",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"digest":       c("Manifest digest; pin a platform with `<repo>@<digest>`."),
					"media_type":   c("Manifest media type."),
					"size":         schema.Int64Attribute{Computed: true, MarkdownDescription: "Manifest size in bytes."},
					"os":           c("Operating system, e.g. `linux`."),
					"architecture": c("CPU architecture, e.g. `amd64` or `arm64`."),
					"variant":      c("CPU variant, e.g. `v8`; empty when none."),
					"os_version":   c("OS version (Windows images); empty when none."),
				}},
			},
		}, regManifestsItem),
	}
}

func (d *p3RegistryManifestsDS) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *p3RegistryManifestsDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m p3RegistryManifestsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := parseImageRef(m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid image reference", err.Error())
		return
	}
	ref := p.Digest
	if ref == "" {
		ref = p.Tag
	}
	r := newP3Registry(d.pd, p.Registry, p.Repository, m.InsecureSkipVerify.ValueBool())
	shallow := !m.Attestations.IsNull() && !m.Attestations.ValueBool()
	in, err := regInspect(ctx, r, ref, shallow)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read image manifests from registry", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
		return
	}
	top := in.Top
	var vals []attr.Value
	addRow := func(dg, mt string, size int64, plat *ocispec.Platform) {
		if plat == nil {
			plat = &ocispec.Platform{}
		}
		vals = append(vals, types.ObjectValueMust(p3ManifestAttrTypes, map[string]attr.Value{
			"digest":       types.StringValue(dg),
			"media_type":   types.StringValue(mt),
			"size":         types.Int64Value(size),
			"os":           types.StringValue(plat.OS),
			"architecture": types.StringValue(plat.Architecture),
			"variant":      types.StringValue(plat.Variant),
			"os_version":   types.StringValue(plat.OSVersion),
		}))
	}
	if top.isIndex() {
		for _, e := range top.Doc.Manifests {
			addRow(e.Digest, e.MediaType, e.Size, e.Platform)
		}
	} else {
		var plat ocispec.Platform
		if in.SingleCfg != nil {
			_ = json.Unmarshal(in.SingleCfg, &plat)
		}
		addRow(top.Digest, top.MediaType, int64(len(top.Body)), &plat)
	}
	m.Digest = types.StringValue(top.Digest)
	m.MediaType = types.StringValue(top.MediaType)
	m.Manifests = types.ListValueMust(types.ObjectType{AttrTypes: p3ManifestAttrTypes}, vals)
	imageRef, digestRef := regRefs(m.Name.ValueString(), top.Digest)
	m.ImageRef = types.StringValue(imageRef)
	m.ImageDigestRef = types.StringValue(digestRef)
	m.Platforms = regStringList(in.Platforms)
	fill, err := regManifestsFillInput(in)
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode registry manifests", err.Error())
		return
	}
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, fill)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// regManifestsFillInput maps an inspection onto the contract outputs.
func regManifestsFillInput(in *regInspection) (contractFillInput, error) {
	top := in.Top
	raw := map[string][]byte{"manifest_json": top.Body}
	var manifest map[string]any
	if err := json.Unmarshal(top.Body, &manifest); err != nil {
		return contractFillInput{}, err
	}
	manifest["digest"] = top.Digest
	manifest["size"] = len(top.Body)
	structured := map[string]any{"manifest": manifest, "image_config": nil}
	switch {
	case !top.isIndex() && in.SingleCfg != nil:
		raw["image_config_json"] = in.SingleCfg
		structured["image_config"] = json.RawMessage(in.SingleCfg)
	case top.isIndex() && len(in.Configs) > 0:
		b, err := regMarshal(in.Configs)
		if err != nil {
			return contractFillInput{}, err
		}
		raw["image_config_json"] = b
		if len(in.Configs) == 1 {
			for _, v := range in.Configs {
				structured["image_config"] = v
			}
		}
	}
	for name, src := range map[string]map[string]any{"provenance_json": in.Provenance, "sbom_json": in.SBOM} {
		if len(src) == 0 {
			continue
		}
		b, err := regMarshal(src)
		if err != nil {
			return contractFillInput{}, err
		}
		raw[name] = b
	}
	return contractFillInput{Raw: raw, Structured: structured}, nil
}

// ---------------------------------------------------------------------------
// docker_registry_image_tags

var (
	_ datasource.DataSource              = (*p3RegistryTagsDS)(nil)
	_ datasource.DataSourceWithConfigure = (*p3RegistryTagsDS)(nil)
)

// regTagsItem is the inventory item of docker_registry_image_tags.
const regTagsItem = "engine:registry-image-tags"

// NewRegistryImageTagsDataSource returns docker_registry_image_tags.
func NewRegistryImageTagsDataSource() datasource.DataSource { return &p3RegistryTagsDS{} }

type p3RegistryTagsDS struct {
	pd *providerData
}

type p3RegistryTagsModel struct {
	Name               types.String `tfsdk:"name"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	PageSize           types.Int64  `tfsdk:"page_size"`
	Repository         types.String `tfsdk:"repository"`
	Tags               types.List   `tfsdk:"tags"`
	contractEngineRegistryImageTags
}

func (d *p3RegistryTagsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_image_tags"
}

func (d *p3RegistryTagsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every tag of a repository straight from its registry (`GET /v2/<name>/tags/list`), following pagination, with the provider's `registry_auth`. " +
			"Tags come back in the registry's order, which for distribution-based registries is lexical. " +
			"`tags_json` holds the pages merged into one `{name, tags}` document.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Repository, e.g. `nginx`, `ghcr.io/org/app` or `localhost:5000/app`. A tag or digest in it is ignored.",
			},
			"insecure_skip_verify": schema.BoolAttribute{Optional: true, MarkdownDescription: "Skip TLS certificate verification of the registry."},
			"page_size": schema.Int64Attribute{
				Optional:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 10000)},
				MarkdownDescription: "Tags requested per page (`n`). Default: the registry's own page size.",
			},
			"repository": schema.StringAttribute{Computed: true, MarkdownDescription: "Normalized `registry/repository`, e.g. `docker.io/library/nginx`."},
			"tags":       schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "The tags."},
		}, regTagsItem),
	}
}

func (d *p3RegistryTagsDS) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *p3RegistryTagsDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m p3RegistryTagsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := parseImageRef(m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid repository", err.Error())
		return
	}
	r := newP3Registry(d.pd, p.Registry, p.Repository, m.InsecureSkipVerify.ValueBool())
	tags, err := p3FetchTags(ctx, r, m.PageSize.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Cannot list image tags from registry", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
		return
	}
	vals := make([]attr.Value, 0, len(tags))
	for _, t := range tags {
		vals = append(vals, types.StringValue(t))
	}
	m.Repository = types.StringValue(p.Registry + "/" + p.Repository)
	m.Tags = types.ListValueMust(types.StringType, vals)
	raw, err := regMarshal(map[string]any{"name": p.Repository, "tags": tags})
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode tags", err.Error())
		return
	}
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Raw: map[string][]byte{"tags_json": raw}})...)
	m.DistributionAPIVersion = regStringOrNull(r.apiVersion)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
