package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/image"
)

var (
	_ datasource.DataSource              = (*imageDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*imageDataSource)(nil)
)

// imgItemImageData is the inventory item of data docker_image.
const imgItemImageData = "engine:image-data"

// NewImageDataSource returns the docker_image data source.
func NewImageDataSource() datasource.DataSource { return &imageDataSource{} }

type imageDataSource struct {
	pd *providerData
}

type imageDataSourceModel struct {
	Name           types.String `tfsdk:"name"`
	ID             types.String `tfsdk:"id"`
	ImageRef       types.String `tfsdk:"image_ref"`
	ImageDigestRef types.String `tfsdk:"image_digest_ref"`
	Digest         types.String `tfsdk:"digest"`
	Platforms      types.List   `tfsdk:"platforms"`
	RepoTags       types.List   `tfsdk:"repo_tags"`
	Labels         types.Map    `tfsdk:"labels"`
	contractEngineImageData
}

func (d *imageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (d *imageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"name": schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Image name, tag or ID to inspect, e.g. `nginx:1.27` or `sha256:...`.",
		},
		"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Image ID (`sha256:...`, `ImageInspect.Id`)."},
		"image_ref": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Normalized `registry/repository:tag` form of `name` (implicit `latest` added); null when `name` is an ID or a digest reference.",
		},
		"image_digest_ref": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "Repository digest reference (`repo@sha256:...`) from `RepoDigests` for the repository of `name`, if known to the daemon; " +
				"null for a local-only image. Every entry is in `inspect.repo_digests`.",
		},
		"digest": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Manifest or index digest: `ImageInspect.Descriptor.digest` on the containerd store, else the digest part of `image_digest_ref`.",
		},
		"platforms": schema.ListAttribute{
			Computed: true, ElementType: types.StringType,
			MarkdownDescription: "Available platforms as `os/arch[/variant]` (`Manifests[].ImageData.Platform` on the containerd store), else the image's single platform.",
		},
		"repo_tags": schema.ListAttribute{
			Computed: true, ElementType: types.StringType,
			MarkdownDescription: "Every tag referencing this image on the daemon.",
		},
		"labels": schema.MapAttribute{
			Computed: true, ElementType: types.StringType,
			MarkdownDescription: "Labels from the image config (`Config.Labels`).",
		},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an image already present on the provider's daemon, without pulling it (`GET /images/{name}/json`). " +
			"Fails if the image does not exist locally. Per-platform `manifests` are filled on daemons with the containerd image store (API 1.48+).",
		Attributes: contractAddDataSourceAttributes(attrs, imgItemImageData),
	}
}

func (d *imageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *imageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var m imageDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	insp, raw, err := imgInspectRaw(ctx, d.pd, m.Name.ValueString(), true)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect image", fmt.Sprintf("%s: %v", m.Name.ValueString(), err))
		return
	}
	imgDataSourceApply(&m, insp)
	resp.Diagnostics.Append(imgDataSourceFill(ctx, d.pd, &m, insp, raw)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// imgDataSourceApply fills the identity attributes from an inspect result.
func imgDataSourceApply(m *imageDataSourceModel, insp image.InspectResponse) {
	m.ID = types.StringValue(insp.ID)
	id := imgIdentityOf(m.Name.ValueString(), insp)
	m.ImageRef, m.ImageDigestRef, m.Digest, m.Platforms, m.Labels = id.ImageRef, id.ImageDigestRef, id.Digest, id.Platforms, id.Labels
	m.RepoTags = stringListValue(insp.RepoTags, false)
}

// imgDataSourceFill fills the contract outputs.
func imgDataSourceFill(ctx context.Context, pd *providerData, m *imageDataSourceModel, insp image.InspectResponse, raw []byte) diag.Diagnostics {
	structured := map[string]any{"manifests": imgManifestsValue(insp)}
	if insp.Config != nil {
		structured["config"] = insp.Config
	}
	var created any
	if insp.Created != "" {
		created = insp.Created
	}
	return m.contractFill(ctx, pd, contractFillInput{
		Raw:        map[string][]byte{"inspect_json": raw},
		Structured: structured,
		Result:     map[string]any{"created": created},
	})
}
