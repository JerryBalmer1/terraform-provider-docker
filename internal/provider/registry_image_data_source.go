package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*registryImageDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*registryImageDataSource)(nil)
)

// regImageDataItem is the inventory item of data docker_registry_image.
const regImageDataItem = "engine:registry-image-data"

// NewRegistryImageDataSource returns the docker_registry_image data source.
func NewRegistryImageDataSource() datasource.DataSource { return &registryImageDataSource{} }

type registryImageDataSource struct {
	pd *providerData
}

type registryImageDataSourceModel struct {
	Name               types.String `tfsdk:"name"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	ID                 types.String `tfsdk:"id"`
	Digest             types.String `tfsdk:"digest"`
	ImageRef           types.String `tfsdk:"image_ref"`
	ImageDigestRef     types.String `tfsdk:"image_digest_ref"`
	Platforms          types.List   `tfsdk:"platforms"`
	Labels             types.Map    `tfsdk:"labels"`
	contractEngineRegistryImageData
}

func (d *registryImageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_image"
}

func (d *registryImageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the current manifest digest and platforms of an image tag from its registry, through the daemon's distribution " +
			"endpoint (`GET /distribution/<name>/json`) with the provider's `registry_auth`. Nothing is pulled. " +
			"Feed `digest` into `docker_image.triggers` to re-pull when a moving tag changes. The registry side returns no image config, " +
			"so `labels`, `created` and `config` are always null here; use `docker_registry_image_manifests` for the config.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Image reference in the registry, e.g. `nginx:1.27` or `ghcr.io/org/app:main` (no tag means `latest`).",
			},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Accepted for configurations migrated from other providers; it has no effect. " +
					"The read goes through the daemon, which follows its own `insecure-registries` setting.",
			},
			"id":               str("`<name>@<digest>`, the reference as given pinned to the digest."),
			"digest":           str("Manifest (or manifest list / index) digest, `sha256:...` (DistributionInspect `Descriptor.digest`)."),
			"image_ref":        str("Normalized `<registry>/<repository>:<tag>` of `name`."),
			"image_digest_ref": str("Pinned reference `<registry>/<repository>@<digest>`."),
			"platforms": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Platforms of the image as `os/arch[/variant]` (DistributionInspect `Platforms`).",
			},
			"labels": schema.MapAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Always null: the distribution endpoint returns no image config. Present so both image data sources share one shape.",
			},
		}, regImageDataItem),
	}
}

func (d *registryImageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *registryImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var m registryImageDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !d.pd.gateItemAPI(ctx, regImageDataItem, &resp.Diagnostics) {
		return
	}
	name := m.Name.ValueString()
	auth, err := d.pd.registryAuthFor(name)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid image reference", err.Error())
		return
	}
	di, err := d.pd.Client.DistributionInspect(ctx, name, client.DistributionInspectOptions{EncodedRegistryAuth: auth})
	if err != nil {
		resp.Diagnostics.AddError("Cannot read image digest from registry", fmt.Sprintf("%s: %v", name, err))
		return
	}
	digest := di.Descriptor.Digest.String()
	m.Digest = types.StringValue(digest)
	m.ID = types.StringValue(name + "@" + digest)
	m.ImageRef, m.ImageDigestRef = regRefValues(name, digest)
	m.Platforms = regOCIPlatforms(di.Platforms)
	m.Labels = types.MapNull(types.StringType)
	fill, err := regDistributionFill(di.DistributionInspect, "inspect_json")
	if err != nil {
		resp.Diagnostics.AddError("Cannot encode distribution response", err.Error())
		return
	}
	manifests := make([]map[string]any, 0, len(di.Platforms))
	for _, p := range di.Platforms {
		manifests = append(manifests, map[string]any{"platform": p})
	}
	fill.Structured = map[string]any{"manifests": manifests, "config": nil}
	fill.Result = map[string]any{"created": nil}
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, fill)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// regRefValues is regRefs as framework values.
func regRefValues(name, digest string) (types.String, types.String) {
	ref, dref := regRefs(name, digest)
	return regStringOrNull(ref), regStringOrNull(dref)
}

// regDistributionFill serializes a DistributionInspect response as the raw
// output rawName. The Engine client decodes the body without keeping it,
// so the raw value is the re-encoded response (same fields and values) in
// canonical form (rCanonicalJSON: keys sorted, deterministic).
func regDistributionFill(di registry.DistributionInspect, rawName string) (contractFillInput, error) {
	b, err := rCanonicalJSON(di)
	if err != nil {
		return contractFillInput{}, err
	}
	return contractFillInput{Raw: map[string][]byte{rawName: b}}, nil
}
