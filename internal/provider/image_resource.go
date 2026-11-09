package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                   = (*imageResource)(nil)
	_ resource.ResourceWithConfigure      = (*imageResource)(nil)
	_ resource.ResourceWithImportState    = (*imageResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*imageResource)(nil)
	_ resource.ResourceWithValidateConfig = (*imageResource)(nil)
	_ resource.ResourceWithIdentity       = (*imageResource)(nil)
)

// imgItemImage is the inventory item of docker_image.
const imgItemImage = "engine:image"

// NewImageResource returns the docker_image resource.
func NewImageResource() resource.Resource { return &imageResource{} }

type imageResource struct {
	pd *providerData
}

type imageResourceModel struct {
	ID             types.String     `tfsdk:"id"`
	Name           types.String     `tfsdk:"name"`
	ImageRef       types.String     `tfsdk:"image_ref"`
	ImageDigestRef types.String     `tfsdk:"image_digest_ref"`
	Digest         types.String     `tfsdk:"digest"`
	Platforms      types.List       `tfsdk:"platforms"`
	Labels         types.Map        `tfsdk:"labels"`
	Platform       types.String     `tfsdk:"platform"`
	KeepLocally    types.Bool       `tfsdk:"keep_locally"`
	ForceRemove    types.Bool       `tfsdk:"force_remove"`
	Triggers       types.Map        `tfsdk:"triggers"`
	ContextDigest  types.String     `tfsdk:"context_digest"`
	Build          *imageBuildModel `tfsdk:"build"`
	contractEngineImage
	imgBuildxOutputs
}

type imageBuildModel struct {
	Context    types.String `tfsdk:"context"`
	Dockerfile types.String `tfsdk:"dockerfile"`
	Args       types.Map    `tfsdk:"args"`
	Target     types.String `tfsdk:"target"`
	Platform   types.String `tfsdk:"platform"`
	Labels     types.Map    `tfsdk:"labels"`
	NoCache    types.Bool   `tfsdk:"no_cache"`
	Pull       types.Bool   `tfsdk:"pull"`
	// BuildKit (image_build_buildkit.go, the BuildKit seam).
	Builder   types.String            `tfsdk:"builder"`
	CacheFrom types.List              `tfsdk:"cache_from"`
	Platforms types.List              `tfsdk:"platforms"`
	Secrets   []imageBuildSecretModel `tfsdk:"secrets"`
	// Pass 5 additions (img_build_opts.go).
	AdditionalContexts types.Map    `tfsdk:"additional_contexts"`
	CacheTo            types.List   `tfsdk:"cache_to"`
	CgroupParent       types.String `tfsdk:"cgroup_parent"`
	CPUPeriod          types.Int64  `tfsdk:"cpu_period"`
	CPUQuota           types.Int64  `tfsdk:"cpu_quota"`
	CPUSetCPUs         types.String `tfsdk:"cpuset_cpus"`
	CPUSetMems         types.String `tfsdk:"cpuset_mems"`
	CPUShares          types.Int64  `tfsdk:"cpu_shares"`
	ExtraHosts         types.List   `tfsdk:"extra_hosts"`
	ForceRm            types.Bool   `tfsdk:"force_rm"`
	Isolation          types.String `tfsdk:"isolation"`
	Memory             types.Int64  `tfsdk:"memory"`
	Memswap            types.Int64  `tfsdk:"memswap"`
	Network            types.String `tfsdk:"network"`
	Provenance         types.String `tfsdk:"provenance"`
	Remote             types.String `tfsdk:"remote"`
	SBOM               types.Bool   `tfsdk:"sbom"`
	ShmSize            types.Int64  `tfsdk:"shm_size"`
	Squash             types.Bool   `tfsdk:"squash"`
	Tags               types.List   `tfsdk:"tags"`
	Ulimits            types.Map    `tfsdk:"ulimits"`
	// BuildKit seam fields (img_build_opts.go).
	SSH           []imgBuildSSHModel `tfsdk:"ssh"`
	NoCacheFilter types.List         `tfsdk:"no_cache_filter"`
	Outputs       types.List         `tfsdk:"outputs"`
	Attestations  types.List         `tfsdk:"attestations"`
}

func (r *imageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (r *imageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Image ID (`sha256:...`, `ImageInspect.Id`).",
			PlanModifiers:       keep,
		},
		"name": schema.StringAttribute{
			Required: true,
			MarkdownDescription: "Image reference. Without `build`, the reference to pull (e.g. `nginx:1.27`, `ghcr.io/org/app:1.0`; no tag means `latest`). " +
				"With `build`, the tag given to the built image. Forces replacement.",
			PlanModifiers: replace,
		},
		"image_ref": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: "Normalized `registry/repository:tag` form of `name` (implicit `latest` added); null when `name` has no tag.",
			PlanModifiers:       keep,
		},
		"image_digest_ref": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "Repository digest reference (`repo@sha256:...`) of the image for `name`'s repository, from `RepoDigests`, when the daemon knows one " +
				"(pulled images, or built images after a push). Null for a built image that was never pushed. Every upstream entry is in `inspect.repo_digests`.",
			PlanModifiers: keep,
		},
		"digest": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "Manifest or index digest (`sha256:...`): `ImageInspect.Descriptor.digest` on the containerd image store, " +
				"else the digest part of `image_digest_ref`; null when neither is known.",
			PlanModifiers: keep,
		},
		"platforms": schema.ListAttribute{
			Computed:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Platforms of the image as `os/arch[/variant]`: one per image manifest on the containerd store, else the image's own platform.",
			PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
		},
		"labels": schema.MapAttribute{
			Computed:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Labels of the resulting image config (`Config.Labels`: base image labels, `LABEL` instructions, `build.labels` and the provider's management label).",
			PlanModifiers:       []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
		},
		"platform": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Platform to pull or build, `os[/arch[/variant]]` such as `linux/amd64`. Used for the build too unless " +
				"`build.platform` is set. Default: the daemon's platform. Forces replacement.",
			PlanModifiers: replace,
		},
		"keep_locally": schema.BoolAttribute{
			Optional:            true,
			Computed:            true,
			Default:             booldefault.StaticBool(false),
			MarkdownDescription: "When true, destroy only removes the image from Terraform state and leaves it on the daemon. Default `false`.",
		},
		"force_remove": schema.BoolAttribute{
			Optional:            true,
			Computed:            true,
			Default:             booldefault.StaticBool(false),
			MarkdownDescription: "Remove the image on destroy even when stopped containers use it (`docker rmi -f`). Default `false`.",
		},
		"triggers": schema.MapAttribute{
			Optional:    true,
			ElementType: types.StringType,
			MarkdownDescription: "Arbitrary string map; any change re-pulls or rebuilds the image. Use it to force a refresh of a moving tag " +
				"(e.g. a timestamp) or to tie the image to other resources. Forces replacement.",
			PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
		},
		"context_digest": schema.StringAttribute{
			Computed: true,
			MarkdownDescription: "SHA-256 over the build context as it would be sent (paths, modes, symlink targets and file contents after " +
				"`.dockerignore`; not mtimes) plus the Dockerfile. Computed at plan time; a change forces a rebuild. Null without `build` or with `build.remote`.",
		},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Docker image on the provider's daemon, pulled from its registry or built from a local context. Without a `build` block the image `name` is pulled " +
			"(credentials from the provider's `registry_auth`); with a `build` block the image is built and tagged `name`: by the classic Engine builder " +
			"(`POST /build`, the default) or by BuildKit with `build.builder = \"buildkit\"` or a buildx builder name (through the provider's BuildKit build path, " +
			"which also fills `metadata_json`, `build_metadata`, `build_ref` and `buildx_version`). " +
			"The context is tarred by the provider honouring `.dockerignore`, or fetched by the daemon from `build.remote`. " +
			"On destroy the tag (and `build.tags`) are removed, and the image deleted when no other tag references it, unless `keep_locally` is true. " +
			"Edits to files in the build context change `context_digest` at plan time and force a rebuild.",
		Attributes: contractAddResourceAttributes(imgBuildxAttributes(attrs), imgItemImage),
		Blocks: map[string]schema.Block{
			"build": imgBuildBlock(),
		},
	}
}

// imgBuildBlock is the `build` block schema.
func imgBuildBlock() schema.SingleNestedBlock {
	attrs := map[string]schema.Attribute{
		"context": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Path of the build context directory, relative to the directory Terraform runs in " +
				"(use `${path.module}/...`); exactly one of `context` and `remote` is required. Files matched by its `.dockerignore` are not sent; " +
				"the Dockerfile and `.dockerignore` always are. Forces replacement.",
		},
		"dockerfile": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Dockerfile path relative to `context` (or absolute). Default `Dockerfile`. A Dockerfile outside the context " +
				"is sent under a generated name, like the docker CLI does. Forces replacement.",
		},
		"args": schema.MapAttribute{
			Optional:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Build arguments (`--build-arg`). Forces replacement.",
		},
		"target": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Multi-stage target to build (`--target`). Forces replacement.",
		},
		"platform": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "Build platform, e.g. `linux/arm64`. Default: the top-level `platform`, else the daemon's. Forces replacement.",
		},
		"labels": schema.MapAttribute{
			Optional:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Labels set on the built image. The provider also adds `" + labelManagedBy + "=" + labelManagedByValue + "`. Forces replacement.",
		},
		"no_cache": schema.BoolAttribute{
			Optional:            true,
			MarkdownDescription: "Do not use the build cache (`--no-cache`). Forces replacement.",
		},
		"pull": schema.BoolAttribute{
			Optional:            true,
			MarkdownDescription: "Always attempt to pull newer versions of base images (`--pull`). Forces replacement.",
		},
		"builder": schema.StringAttribute{
			Optional: true,
			MarkdownDescription: "Builder backend: `classic` (default; the legacy Engine builder, `DOCKER_BUILDKIT=0`), `buildkit` (the daemon's " +
				"embedded BuildKit) or the name of a buildx builder (`docker_buildx_builder`, like `docker buildx build --builder <name>`). " +
				"With BuildKit the build runs with a session (serving `secrets`, `ssh` and `registry_auth` credentials) so Dockerfile " +
				"features such as `RUN --mount=type=secret` and heredocs work, and `cache_to`, `ssh`, `outputs`, `attestations`, `provenance`, " +
				"`sbom` and `no_cache_filter` become available. Requires a daemon with BuildKit enabled (default since Docker 23). Forces replacement.",
			Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
		},
		"cache_from": schema.ListAttribute{
			Optional:    true,
			ElementType: types.StringType,
			MarkdownDescription: "BuildKit only. Cache sources (`--cache-from`): image references carrying inline cache metadata, or buildx cache specs " +
				"such as `type=registry,ref=...`. Forces replacement.",
		},
		"platforms": schema.ListAttribute{
			Optional:    true,
			ElementType: types.StringType,
			MarkdownDescription: "BuildKit only. Target platform list; exactly one element is supported (sent as the build platform, " +
				"like `platform`): the image lands in the daemon's image store, which holds one platform per tag. Use one `docker_image` per platform. " +
				"Conflicts with `build.platform`. Forces replacement.",
		},
	}
	for k, v := range imgBuildGapAttributes() {
		attrs[k] = v
	}
	return schema.SingleNestedBlock{
		MarkdownDescription: "Build the image instead of pulling it, like `docker build`. Changing anything in this block forces a new resource.",
		PlanModifiers:       []planmodifier.Object{objectplanmodifier.RequiresReplace()},
		Attributes:          attrs,
		Blocks: map[string]schema.Block{
			"secrets": schema.ListNestedBlock{
				MarkdownDescription: "BuildKit only. Build secrets exposed to `RUN --mount=type=secret,id=<id>`. Values are read by the " +
					"provider at build time from a file or an environment variable of the provider process and are never stored in state. " +
					"Forces replacement (but changing a secret's value does not trigger a rebuild; use `triggers`).",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Secret ID referenced by `RUN --mount=type=secret,id=...`. Forces replacement.",
						},
						"src": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Path of a file holding the secret (max 500 KiB). Exactly one of `src` or `env`. Forces replacement.",
						},
						"env": schema.StringAttribute{
							Optional:            true,
							MarkdownDescription: "Name of an environment variable of the provider process holding the secret. Exactly one of `src` or `env`. Forces replacement.",
						},
					},
				},
			},
		},
	}
}

func (r *imageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *imageResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m imageResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Build == nil {
		return
	}
	if !m.Build.Context.IsUnknown() && !m.Build.Remote.IsUnknown() && m.Build.Context.IsNull() == m.Build.Remote.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("build").AtName("context"), "Missing or conflicting build context",
			"A build block needs exactly one of build.context (local directory) or build.remote (Git URL or tarball URL the daemon fetches).")
	}
	resp.Diagnostics.Append(imgValidateBuildKit(ctx, imgNormalizedBuilder(m.Build))...)
	resp.Diagnostics.Append(imgValidateBuildGaps(ctx, m.Build)...)
}

// ModifyPlan computes context_digest so file edits in the build context
// force a rebuild, and keeps the computed outputs of an in-place update
// (only keep_locally / force_remove can change in place). A replacement is
// planned again by Terraform with a null prior state, so copying the prior
// values here never leaks into a new image.
func (r *imageResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan imageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	digest := imgPlanContextDigest(plan.Build)
	plan.ContextDigest = digest
	if req.State.Raw.IsNull() {
		plan.contractSetUnknown()
		plan.imgBuildxOutputs = imgBuildxUnknown(plan.Build)
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	var st imageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	imgCopyComputed(&plan, &st)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
	if st.ContextDigest.IsNull() || plan.Build == nil {
		return
	}
	if digest.IsUnknown() || digest.ValueString() != st.ContextDigest.ValueString() {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("context_digest"))
	}
}

// imgCopyComputed copies the daemon-reported values from st into plan.
func imgCopyComputed(plan, st *imageResourceModel) {
	plan.ID, plan.ImageRef, plan.ImageDigestRef, plan.Digest = st.ID, st.ImageRef, st.ImageDigestRef, st.Digest
	plan.Platforms, plan.Labels = st.Platforms, st.Labels
	plan.contractEngineImage = st.contractEngineImage
	plan.imgBuildxOutputs = st.imgBuildxOutputs
}

// imgPlanContextDigest returns the digest for a planned build block: null
// without build or with a remote context, unknown when the context path is
// not known yet or cannot be read at plan time (it may be produced by
// another resource during apply).
func imgPlanContextDigest(b *imageBuildModel) types.String {
	if b == nil || (b.Context.IsNull() && !b.Remote.IsNull() && !b.Remote.IsUnknown()) {
		return types.StringNull()
	}
	dir, ok := knownString(b.Context)
	if !ok || b.Dockerfile.IsUnknown() {
		return types.StringUnknown()
	}
	bc, err := imgCollectContext(dir, b.Dockerfile.ValueString())
	if err != nil {
		return types.StringUnknown()
	}
	d, err := imgContextDigest(bc)
	if err != nil {
		return types.StringUnknown()
	}
	return types.StringValue(d)
}

func (r *imageResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

// imgBuildOutcome is what a build hands back to Create.
type imgBuildOutcome struct {
	ImageID       string
	ContextDigest string
	Dockerfile    []byte
	// Seam is the BuildKit seam result (nil for a classic build).
	Seam *seamBuildResult
}

func (r *imageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var plan imageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	ref := name
	var dockerfile []byte
	var seamRes *seamBuildResult
	if plan.Build != nil {
		out, diags := r.build(ctx, &plan)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.ContextDigest = stringOrNull(out.ContextDigest)
		if out.ImageID != "" {
			ref = out.ImageID
		}
		dockerfile = out.Dockerfile
		seamRes = out.Seam
	} else {
		resp.Diagnostics.Append(r.pull(ctx, name, plan.Platform.ValueString())...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.ContextDigest = types.StringNull()
	}
	insp, raw, err := imgInspectRaw(ctx, r.pd, ref, true)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect image", fmt.Sprintf("%s: %v", ref, err))
		return
	}
	imgApplyInspect(&plan, insp)
	imgApplySeamResult(&plan, seamRes)
	resp.Diagnostics.Append(imgFillContract(ctx, r.pd, &plan, raw, dockerfile)...)
	resp.Diagnostics.Append(imgFillBuildxOutputs(ctx, r.pd, &plan, seamRes)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

// imgFillContract fills the contract outputs from the inspect body and the
// Dockerfile that was built (nil for a pulled or imported image).
func imgFillContract(ctx context.Context, pd *providerData, m *imageResourceModel, inspectJSON, dockerfile []byte) diag.Diagnostics {
	in := contractFillInput{Raw: map[string][]byte{"inspect_json": inspectJSON}}
	if dockerfile != nil {
		info := imgParseDockerfile(string(dockerfile))
		in.Raw["dockerfile"] = dockerfile
		in.DockerfileSyntax = info.Syntax
		stages := info.Stages
		if stages == nil {
			stages = []map[string]any{}
		}
		in.Structured = map[string]any{"stages": stages}
	}
	diags := m.contractFill(ctx, pd, in)
	if dockerfile == nil {
		// Not built: no Dockerfile, no stages, no syntax.
		m.Dockerfile = types.StringNull()
		m.DockerfileSHA256 = types.StringNull()
		m.DockerfileSyntax = types.StringNull()
	}
	return diags
}

// pull pulls name and drains the progress stream.
func (r *imageResource) pull(ctx context.Context, name, platform string) diag.Diagnostics {
	var diags diag.Diagnostics
	auth, err := r.pd.registryAuthFor(name)
	if err != nil {
		diags.AddAttributeError(path.Root("name"), "Invalid image reference", err.Error())
		return diags
	}
	tflog.Info(ctx, "pulling image", map[string]any{"name": name, "platform": platform})
	rc, err := r.pd.Client.ImagePull(ctx, name, client.ImagePullOptions{RegistryAuth: auth, Platforms: platformsFromString(platform)})
	if err != nil {
		diags.AddError("Cannot pull image", fmt.Sprintf("%s: %v", name, err))
		return diags
	}
	defer rc.Close()
	if _, err := imgDecodeStream(rc, imgLogMessage(ctx)); err != nil {
		diags.AddError("Image pull failed", fmt.Sprintf("%s: %v", name, err))
	}
	return diags
}

// build runs the build the block asks for: BuildKit through the BuildKit
// seam (seamBuildKitImpl, Lane E), or the classic Engine builder. A remote context is sent to
// the daemon as-is (classic builder only).
func (r *imageResource) build(ctx context.Context, plan *imageResourceModel) (imgBuildOutcome, diag.Diagnostics) {
	var diags diag.Diagnostics
	b := plan.Build
	if remote, ok := knownString(b.Remote); ok && remote != "" && b.Context.IsNull() {
		return r.buildRemote(ctx, plan, remote)
	}
	bc, err := imgCollectContext(b.Context.ValueString(), b.Dockerfile.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("build").AtName("context"), "Cannot read build context", err.Error())
		return imgBuildOutcome{}, diags
	}
	digest, err := imgContextDigest(bc)
	if err != nil {
		diags.AddAttributeError(path.Root("build").AtName("context"), "Cannot hash build context", err.Error())
		return imgBuildOutcome{}, diags
	}
	out := imgBuildOutcome{ContextDigest: digest, Dockerfile: imgReadDockerfile(b.Context.ValueString(), bc)}
	opts := imgBuildOptions(ctx, plan, bc.Dockerfile, &diags)
	opts.AuthConfigs = imgAuthConfigs(r.pd.RegistryAuth)
	if diags.HasError() {
		return imgBuildOutcome{}, diags
	}
	if imgUsesBuildKit(b) {
		res, bkDiags := imgBuildViaSeam(ctx, r.pd, plan, opts)
		diags.Append(bkDiags...)
		if res != nil {
			out.ImageID = res.ImageID
			out.Seam = res
		}
		return out, diags
	}
	tflog.Info(ctx, "building image", map[string]any{"name": plan.Name.ValueString(), "context": b.Context.ValueString(), "files": len(bc.Files)})
	tarStream := imgTarReader(bc)
	defer tarStream.Close()
	id, cdiags := imgRunClassicBuild(ctx, r.pd.Client, plan.Name.ValueString(), tarStream, opts)
	diags.Append(cdiags...)
	out.ImageID = id
	return out, diags
}

// buildRemote builds from a remote context (Git URL or tarball URL).
func (r *imageResource) buildRemote(ctx context.Context, plan *imageResourceModel, remote string) (imgBuildOutcome, diag.Diagnostics) {
	var diags diag.Diagnostics
	df := plan.Build.Dockerfile.ValueString()
	if df == "" {
		df = imgDefaultDockerfile
	}
	opts := imgBuildOptions(ctx, plan, df, &diags)
	opts.AuthConfigs = imgAuthConfigs(r.pd.RegistryAuth)
	opts.RemoteContext = remote
	if diags.HasError() {
		return imgBuildOutcome{}, diags
	}
	tflog.Info(ctx, "building image from remote context", map[string]any{"name": plan.Name.ValueString(), "remote": remote})
	id, cdiags := imgRunClassicBuild(ctx, r.pd.Client, plan.Name.ValueString(), nil, opts)
	diags.Append(cdiags...)
	return imgBuildOutcome{ImageID: id}, diags
}

// imgRunClassicBuild runs POST /build and returns the built image ID ("" with
// some daemons; callers then inspect the tag).
func imgRunClassicBuild(ctx context.Context, c *client.Client, name string, body io.Reader, opts client.ImageBuildOptions) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	res, err := c.ImageBuild(ctx, body, opts)
	if err != nil {
		diags.AddError("Cannot start image build", err.Error())
		return "", diags
	}
	defer res.Body.Close()
	out, err := imgDecodeStream(res.Body, imgLogMessage(ctx))
	if err != nil {
		diags.AddError("Image build failed", fmt.Sprintf("%s: %v", name, err))
		return "", diags
	}
	return imgBuiltImageID(out.Aux), diags
}

// imgReadDockerfile returns the Dockerfile text the build sends (nil when it
// cannot be read).
func imgReadDockerfile(contextDir string, bc *imgBuildContext) []byte {
	if bc.extraDockerfile != nil {
		return bc.extraDockerfile
	}
	abs, err := filepath.Abs(contextDir)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(abs, filepath.FromSlash(bc.Dockerfile)))
	if err != nil {
		return nil
	}
	return b
}

// imgAuthConfigs builds ImageBuild's AuthConfigs map (keyed by registry
// address, Docker Hub also under its legacy index URL) so FROM can pull
// private base images.
func imgAuthConfigs(creds []registryCredential) map[string]registry.AuthConfig {
	out := map[string]registry.AuthConfig{}
	for _, c := range creds {
		ac := registry.AuthConfig{Username: c.Username, Password: c.Password, IdentityToken: c.IdentityToken, ServerAddress: c.Address}
		out[c.Address] = ac
		host := normalizeRegistry(c.Address)
		out[host] = ac
		if host == "docker.io" {
			out[imgDockerHubIndexServer] = ac
		}
	}
	return out
}

// imgLogMessage forwards daemon progress to tflog at debug level.
func imgLogMessage(ctx context.Context) func(jsonstream.Message) {
	return func(m jsonstream.Message) {
		if s := strings.TrimSpace(m.Stream); s != "" {
			tflog.Debug(ctx, s)
		} else if m.Status != "" && m.Progress == nil {
			tflog.Debug(ctx, strings.TrimSpace(m.ID+" "+m.Status))
		}
	}
}

// imgApplyInspect copies daemon-reported fields into the model.
func imgApplyInspect(m *imageResourceModel, insp image.InspectResponse) {
	m.ID = types.StringValue(insp.ID)
	if m.Name.IsNull() || m.Name.ValueString() == "" {
		if len(insp.RepoTags) > 0 {
			m.Name = types.StringValue(insp.RepoTags[0])
		} else {
			m.Name = types.StringValue(insp.ID)
		}
	}
	id := imgIdentityOf(m.Name.ValueString(), insp)
	m.ImageRef, m.ImageDigestRef, m.Digest, m.Platforms, m.Labels = id.ImageRef, id.ImageDigestRef, id.Digest, id.Platforms, id.Labels
	if m.KeepLocally.IsNull() {
		m.KeepLocally = types.BoolValue(false)
	}
	if m.ForceRemove.IsNull() {
		m.ForceRemove = types.BoolValue(false)
	}
	if m.ContextDigest.IsUnknown() {
		m.ContextDigest = types.StringNull()
	}
}

func (r *imageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.pd == nil {
		return
	}
	var st imageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := st.ID.ValueString() // the image ID, or the name/ID given to import
	imported := st.Name.IsNull()
	insp, raw, err := imgInspectRaw(ctx, r.pd, ref, true)
	if isNotFound(err) {
		tflog.Warn(ctx, "image gone from daemon; removing from state", map[string]any{"ref": ref})
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect image", fmt.Sprintf("%s: %v", ref, err))
		return
	}
	if imported && !imgIsIDRef(ref, insp.ID) {
		// Imported by name: keep the reference the user gave.
		st.Name = types.StringValue(ref)
	}
	imgApplyInspect(&st, insp)
	var dockerfile []byte
	if !st.Dockerfile.IsNull() && !st.Dockerfile.IsUnknown() {
		dockerfile = []byte(st.Dockerfile.ValueString())
	}
	resp.Diagnostics.Append(imgFillContract(ctx, r.pd, &st, raw, dockerfile)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, st.ID.ValueString())...)
}

// Update only handles keep_locally / force_remove; everything else replaces.
func (r *imageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st imageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	imgCopyComputed(&plan, &st)
	if plan.ContextDigest.IsUnknown() {
		plan.ContextDigest = st.ContextDigest
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

func (r *imageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.pd == nil {
		return
	}
	var st imageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() || st.KeepLocally.ValueBool() {
		return
	}
	if st.Build != nil {
		for _, t := range stringListFrom(ctx, st.Build.Tags, &resp.Diagnostics) {
			if t == st.Name.ValueString() {
				continue
			}
			if err := imgUntag(ctx, r.pd.Client, t, st.ID.ValueString()); err != nil {
				resp.Diagnostics.AddError("Cannot remove image tag", err.Error())
				return
			}
		}
	}
	if err := imgRemove(ctx, r.pd.Client, st.Name.ValueString(), st.ID.ValueString(), st.ForceRemove.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Cannot remove image", err.Error())
	}
}

// imgUntag removes tag when it still points at imageID; a missing or
// re-pointed tag is left alone.
func imgUntag(ctx context.Context, c *client.Client, tag, imageID string) error {
	insp, err := c.ImageInspect(ctx, tag)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", tag, err)
	}
	if imageID != "" && insp.ID != imageID {
		return nil
	}
	if _, err := c.ImageRemove(ctx, tag, client.ImageRemoveOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("%s: %w", tag, err)
	}
	return nil
}

// imgRemove untags name when it still points at imageID (deleting the image
// once no tag is left), otherwise removes imageID directly. Missing images
// are not an error.
func imgRemove(ctx context.Context, c *client.Client, name, imageID string, force bool) error {
	target := imageID
	if name != "" {
		if insp, err := c.ImageInspect(ctx, name); err == nil && (imageID == "" || insp.ID == imageID) {
			target = name
		}
	}
	if target == "" {
		return nil
	}
	_, err := c.ImageRemove(ctx, target, client.ImageRemoveOptions{Force: force, PruneChildren: true})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("%s: %w", target, err)
	}
	return nil
}

// ImportState accepts an image name or ID; Read resolves it to the image ID
// and rewrites both the id attribute and the identity.
func (r *imageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), ref)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, ref)...)
}

// imgDockerHubIndexServer is the legacy key Docker Hub credentials are
// stored under in AuthConfigs.
const imgDockerHubIndexServer = "https://index.docker.io/v1/"
