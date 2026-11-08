package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*registryImageDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*registryImageDataSource)(nil)
)

// NewRegistryImageDataSource returns the docker_registry_image data source.
func NewRegistryImageDataSource() datasource.DataSource { return &registryImageDataSource{} }

type registryImageDataSource struct {
	pd *providerData
}

type registryImageDataSourceModel struct {
	Name         types.String `tfsdk:"name"`
	ID           types.String `tfsdk:"id"`
	Sha256Digest types.String `tfsdk:"sha256_digest"`
}

func (d *registryImageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_image"
}

func (d *registryImageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the current manifest digest of an image tag from its registry, through the daemon's distribution " +
			"endpoint (`GET /distribution/<name>/json`) with the provider's `registry_auth`. Nothing is pulled. " +
			"Feed `sha256_digest` into `docker_image.triggers` to re-pull when a moving tag changes.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Image reference in the registry, e.g. `nginx:1.27` or `ghcr.io/org/app:main` (no tag means `latest`).",
			},
			"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "`<name>@<sha256_digest>`."},
			"sha256_digest": schema.StringAttribute{Computed: true, MarkdownDescription: "Manifest (or manifest list / index) digest, `sha256:...`."},
		},
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
	m.Sha256Digest = types.StringValue(digest)
	m.ID = types.StringValue(name + "@" + digest)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
