package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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
		MarkdownDescription: "Reads the current manifest digest, platforms and image config of an image tag from its registry, without pulling it. " +
			"By default the daemon's distribution endpoint (`GET /distribution/<name>/json`, with the provider's `registry_auth`) gives the digest " +
			"and platforms; that endpoint returns no image config, so the provider then reads the image config blob for that digest from the " +
			"registry itself (Registry HTTP API v2, `registry_auth`, TLS verification on; `localhost` and loopback registries fall back to plain " +
			"HTTP like the daemon) to fill `config`, `labels` and `created`. If the provider cannot reach the registry the daemon reached " +
			"(for example a certificate only the daemon trusts through `insecure-registries`), those three stay null and a warning says why; set " +
			"`insecure_skip_verify` to read the registry directly instead. A plain-HTTP registry on a host other than `localhost` or a " +
			"loopback address is never read over plain HTTP by the provider, so its config stays null. For an index or manifest list, `config`, `labels` and `created` " +
			"describe one platform: the daemon's os/arch when an entry matches, else linux/amd64, else the first entry. " +
			"Feed `digest` into `docker_image.triggers` to re-pull when a moving tag changes.",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Image reference in the registry, e.g. `nginx:1.27` or `ghcr.io/org/app:main` (no tag means `latest`).",
			},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Read the registry directly instead of through the daemon, with TLS certificate verification off " +
					"(`true`) or on (`false`). Set either way, the provider itself calls the Registry HTTP API v2 " +
					"(`GET /v2/<repo>/manifests/<ref>`, plus the image config blob) with the " +
					"`registry_auth` username and password, and fills the same outputs from the response. `true` accepts a " +
					"self-signed or otherwise unverifiable certificate; `false` verifies it against the system roots and fails " +
					"with the certificate error. Unset (default): the daemon reads the registry (`GET /distribution/<name>/json`) " +
					"and follows its own `insecure-registries` setting.",
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
				ElementType: types.StringType,
				Computed:    true,
				MarkdownDescription: "Labels of the image config (`config.Labels` of the registry's image config blob, for the platform `config` describes). " +
					"Null when the config blob could not be read (see the warning).",
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
	name := m.Name.ValueString()
	auth, err := d.pd.registryAuthFor(name)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid image reference", err.Error())
		return
	}
	var di client.DistributionInspectResult
	var direct *regInspection
	var directReg *p3Registry
	insecure, isDirect := knownBool(m.InsecureSkipVerify)
	if isDirect {
		di.DistributionInspect, direct, directReg, err = regDirectDistributionInspect(ctx, d.pd, name, insecure)
	} else {
		// Through the daemon: GET /distribution needs Engine API 1.30.
		if !d.pd.gateItemAPI(ctx, regImageDataItem, &resp.Diagnostics) {
			return
		}
		di, err = d.pd.Client.DistributionInspect(ctx, name, client.DistributionInspectOptions{EncodedRegistryAuth: auth})
	}
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
	// The image config comes from the registry's config blob: the daemon
	// path asks the registry itself (the daemon returns no config), the
	// direct path reuses its registry client. Failing to read it is a
	// warning, never an error: digest and platforms are still good.
	var cfg *regImageConfig
	if isDirect {
		cfg, err = regReadImageConfig(ctx, d.pd, directReg, direct.Top, direct.SingleCfg)
	} else {
		cfg, err = regDaemonPathImageConfig(ctx, d.pd, name, digest)
	}
	if err != nil {
		resp.Diagnostics.AddWarning("Image config not read from registry", regImageConfigWarning(name, !isDirect, err))
		cfg = nil
	}
	var config, created any
	if cfg != nil {
		config = cfg.Config
		m.Labels = stringMapValue(cfg.Labels, false)
		if cfg.Created != "" {
			created = cfg.Created
		}
	}
	fill.Structured = map[string]any{"manifests": manifests, "config": config}
	fill.Result = map[string]any{"created": created}
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, fill)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// regDirectDistributionInspect reads name straight from its registry
// (Registry HTTP API v2, TLS verification off when insecure) and returns
// what the daemon's GET /distribution/<name>/json would: the top manifest's
// descriptor and the platforms of an index's entries, or of a single
// manifest's image config.
func regDirectDistributionInspect(ctx context.Context, pd *providerData, name string, insecure bool) (registry.DistributionInspect, *regInspection, *p3Registry, error) {
	p, err := parseImageRef(name)
	if err != nil {
		return registry.DistributionInspect{}, nil, nil, err
	}
	ref := p.Digest
	if ref == "" {
		ref = p.Tag
	}
	if ref == "" {
		ref = "latest"
	}
	r := newP3Registry(pd, p.Registry, p.Repository, insecure)
	in, err := regInspect(ctx, r, ref, true)
	if err != nil {
		return registry.DistributionInspect{}, nil, nil, err
	}
	top := in.Top
	if !regDigestPattern.MatchString(top.Digest) {
		return registry.DistributionInspect{}, nil, nil, fmt.Errorf("registry digest %q is not a sha256 digest", top.Digest)
	}
	var out registry.DistributionInspect
	// Descriptor.Digest is a go-digest type; decoding fills it without a
	// direct dependency on that module.
	desc, _ := json.Marshal(map[string]any{"mediaType": top.MediaType, "digest": top.Digest, "size": len(top.Body)})
	if err := json.Unmarshal(desc, &out.Descriptor); err != nil {
		return registry.DistributionInspect{}, nil, nil, err
	}
	if top.isIndex() {
		for _, e := range top.Doc.Manifests {
			pl := ocispec.Platform{}
			if e.Platform != nil {
				pl = *e.Platform
			}
			out.Platforms = append(out.Platforms, pl)
		}
	} else if in.SingleCfg != nil {
		var pl ocispec.Platform
		if json.Unmarshal(in.SingleCfg, &pl) == nil && (pl.OS != "" || pl.Architecture != "") {
			out.Platforms = append(out.Platforms, pl)
		}
	}
	return out, in, r, nil
}

// regDigestPattern matches a sha256 content digest.
var regDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

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
