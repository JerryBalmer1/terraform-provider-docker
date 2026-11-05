package provider

// docker_registry_image_manifests and docker_registry_image_tags: read a
// repository straight from its registry over the v2 HTTP API.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type p3Platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	OSVersion    string `json:"os.version"`
	Variant      string `json:"variant"`
}

type p3Descriptor struct {
	MediaType string      `json:"mediaType"`
	Digest    string      `json:"digest"`
	Size      int64       `json:"size"`
	Platform  *p3Platform `json:"platform"`
}

type p3Manifest struct {
	MediaType string         `json:"mediaType"`
	Manifests []p3Descriptor `json:"manifests"`
	Config    *p3Descriptor  `json:"config"`
}

// p3ManifestEntry is one per-platform image manifest.
type p3ManifestEntry struct {
	p3Descriptor
	Platform p3Platform
}

// p3FetchManifests reads ref of the registry's repository. For an index or
// manifest list it returns its entries; for a single image manifest it
// returns that manifest, with its platform read from the image config blob.
func p3FetchManifests(ctx context.Context, r *p3Registry, ref string) (digest, mediaType string, entries []p3ManifestEntry, err error) {
	resp, err := r.get(ctx, "/v2/"+r.repo+"/manifests/"+ref, p3ManifestAccept...)
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", "", nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("GET manifest %s:%s: %s: %s", r.repo, ref, resp.Status, strings.TrimSpace(string(body)))
	}
	var m p3Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return "", "", nil, fmt.Errorf("decoding manifest: %w", err)
	}
	mediaType = m.MediaType
	if mediaType == "" {
		mediaType, _, _ = strings.Cut(resp.Header.Get("Content-Type"), ";")
	}
	digest = resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		digest = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	}
	switch mediaType {
	case p3MediaOCIIndex, p3MediaDockerList:
		for _, d := range m.Manifests {
			e := p3ManifestEntry{p3Descriptor: d}
			if d.Platform != nil {
				e.Platform = *d.Platform
			}
			entries = append(entries, e)
		}
		return digest, mediaType, entries, nil
	case p3MediaOCIManifest, p3MediaDockerManif2:
		e := p3ManifestEntry{p3Descriptor: p3Descriptor{MediaType: mediaType, Digest: digest, Size: int64(len(body))}}
		if m.Config != nil && m.Config.Digest != "" {
			var plat p3Platform
			if _, err := r.getJSON(ctx, "/v2/"+r.repo+"/blobs/"+m.Config.Digest, &plat); err != nil {
				return "", "", nil, fmt.Errorf("reading image config: %w", err)
			}
			e.Platform = plat
		}
		return digest, mediaType, []p3ManifestEntry{e}, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported manifest media type %q", mediaType)
	}
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

// NewP3RegistryImageManifestsDataSource returns docker_registry_image_manifests.
func NewP3RegistryImageManifestsDataSource() datasource.DataSource { return &p3RegistryManifestsDS{} }

type p3RegistryManifestsDS struct {
	pd *providerData
}

type p3RegistryManifestsModel struct {
	Name               types.String `tfsdk:"name"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	Digest             types.String `tfsdk:"digest"`
	MediaType          types.String `tfsdk:"media_type"`
	Manifests          types.List   `tfsdk:"manifests"`
}

func (d *p3RegistryManifestsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_image_manifests"
}

func (d *p3RegistryManifestsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	c := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the per-platform manifests of a multi-platform image straight from its registry, with the provider's `registry_auth`. " +
			"For a manifest list or OCI index every entry is listed (attestation manifests appear with platform `unknown`); " +
			"a single-platform image gives one entry whose platform comes from its config blob. Nothing is pulled.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Image reference, e.g. `nginx:1.27` or `ghcr.io/org/app@sha256:...` (no tag means `latest`)."},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Skip TLS certificate verification of the registry.",
			},
			"digest":     c("Digest of the top-level manifest (index, list or image manifest)."),
			"media_type": c("Media type of the top-level manifest."),
			"manifests": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Image manifests, in registry order.",
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
		},
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
	digest, mediaType, entries, err := p3FetchManifests(ctx, r, ref)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read image manifests from registry", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
		return
	}
	vals := make([]attr.Value, 0, len(entries))
	for _, e := range entries {
		vals = append(vals, types.ObjectValueMust(p3ManifestAttrTypes, map[string]attr.Value{
			"digest":       types.StringValue(e.Digest),
			"media_type":   types.StringValue(e.MediaType),
			"size":         types.Int64Value(e.Size),
			"os":           types.StringValue(e.Platform.OS),
			"architecture": types.StringValue(e.Platform.Architecture),
			"variant":      types.StringValue(e.Platform.Variant),
			"os_version":   types.StringValue(e.Platform.OSVersion),
		}))
	}
	m.Digest = types.StringValue(digest)
	m.MediaType = types.StringValue(mediaType)
	m.Manifests = types.ListValueMust(types.ObjectType{AttrTypes: p3ManifestAttrTypes}, vals)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_registry_image_tags

var (
	_ datasource.DataSource              = (*p3RegistryTagsDS)(nil)
	_ datasource.DataSourceWithConfigure = (*p3RegistryTagsDS)(nil)
)

// NewP3RegistryImageTagsDataSource returns docker_registry_image_tags.
func NewP3RegistryImageTagsDataSource() datasource.DataSource { return &p3RegistryTagsDS{} }

type p3RegistryTagsDS struct {
	pd *providerData
}

type p3RegistryTagsModel struct {
	Name               types.String `tfsdk:"name"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	PageSize           types.Int64  `tfsdk:"page_size"`
	Repository         types.String `tfsdk:"repository"`
	Tags               types.List   `tfsdk:"tags"`
}

func (d *p3RegistryTagsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_image_tags"
}

func (d *p3RegistryTagsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every tag of a repository straight from its registry (`GET /v2/<name>/tags/list`), following pagination, with the provider's `registry_auth`. " +
			"Tags come back in the registry's order, which for distribution-based registries is lexical.",
		Attributes: map[string]schema.Attribute{
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
		},
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
