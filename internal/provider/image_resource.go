package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = (*imageResource)(nil)
	_ resource.ResourceWithConfigure      = (*imageResource)(nil)
	_ resource.ResourceWithImportState    = (*imageResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*imageResource)(nil)
	_ resource.ResourceWithValidateConfig = (*imageResource)(nil)
)

// NewImageResource returns the docker_image resource.
func NewImageResource() resource.Resource { return &imageResource{} }

type imageResource struct {
	pd *providerData
}

type imageResourceModel struct {
	ID            types.String     `tfsdk:"id"`
	Name          types.String     `tfsdk:"name"`
	ImageID       types.String     `tfsdk:"image_id"`
	RepoDigest    types.String     `tfsdk:"repo_digest"`
	Platform      types.String     `tfsdk:"platform"`
	KeepLocally   types.Bool       `tfsdk:"keep_locally"`
	ForceRemove   types.Bool       `tfsdk:"force_remove"`
	Triggers      types.Map        `tfsdk:"triggers"`
	ContextDigest types.String     `tfsdk:"context_digest"`
	Build         *imageBuildModel `tfsdk:"build"`
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
}

func (r *imageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (r *imageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Docker image on the provider's daemon. Without a `build` block the image `name` is pulled from its registry " +
			"(credentials from the provider's `registry_auth`); with a `build` block the image is built from a local context directory " +
			"through the Engine API (classic builder, the context is tarred by the provider honouring `.dockerignore`) and tagged `name`. " +
			"On destroy the tag is removed (and the image deleted when no other tag references it) unless `keep_locally` is true. " +
			"Edits to files in the build context change `context_digest` at plan time and force a rebuild.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Image ID (`sha256:...`), same as `image_id`.",
				PlanModifiers:       keep,
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Image reference. Without `build`, the reference to pull (e.g. `nginx:1.27`, `ghcr.io/org/app:1.0`; no tag means `latest`). " +
					"With `build`, the tag given to the built image. Changing this forces a new resource.",
				PlanModifiers: replace,
			},
			"image_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Content-addressable image ID (`sha256:...`) reported by the daemon.",
				PlanModifiers:       keep,
			},
			"repo_digest": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Repository digest (`repo@sha256:...`) of the image for `name`'s repository, when the daemon knows one " +
					"(pulled images, or built images after a push). Null for a built image that was never pushed.",
				PlanModifiers: keep,
			},
			"platform": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Platform to pull or build, `os[/arch[/variant]]` such as `linux/amd64`. Used for the build too unless " +
					"`build.platform` is set. Default: the daemon's platform. Changing this forces a new resource.",
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
					"(e.g. a timestamp) or to tie the image to other resources. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"context_digest": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "SHA-256 over the build context as it would be sent (paths, modes, symlink targets and file contents after " +
					"`.dockerignore`; not mtimes) plus the Dockerfile. Computed at plan time; a change forces a rebuild. Null without `build`.",
			},
		},
		Blocks: map[string]schema.Block{
			"build": schema.SingleNestedBlock{
				MarkdownDescription: "Build the image from a local directory instead of pulling it. Changing anything in this block forces a new resource.",
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"context": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "**Required inside `build`.** Path of the build context directory, relative to the directory Terraform runs in " +
							"(use `${path.module}/...`). Files matched by its `.dockerignore` are not sent; the Dockerfile and `.dockerignore` always are.",
					},
					"dockerfile": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Dockerfile path relative to `context` (or absolute). Default `Dockerfile`. A Dockerfile outside the context " +
							"is sent under a generated name, like the docker CLI does.",
					},
					"args": schema.MapAttribute{
						Optional:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Build arguments (`--build-arg`).",
					},
					"target": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Multi-stage target to build (`--target`).",
					},
					"platform": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Build platform, e.g. `linux/arm64`. Default: the top-level `platform`, else the daemon's.",
					},
					"labels": schema.MapAttribute{
						Optional:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Labels set on the built image. The provider also adds `" + labelManagedBy + "=" + labelManagedByValue + "`.",
					},
					"no_cache": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Do not use the build cache (`--no-cache`).",
					},
					"pull": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Always attempt to pull newer versions of base images (`--pull`).",
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
	if m.Build.Context.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("build").AtName("context"), "Missing build context",
			"build.context is required when a build block is present.")
	}
}

// ModifyPlan computes context_digest so file edits in the build context
// force a rebuild.
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
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("context_digest"), digest)...)
	if req.State.Raw.IsNull() {
		return
	}
	var prior types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("context_digest"), &prior)...)
	if prior.IsNull() || plan.Build == nil {
		return
	}
	if digest.IsUnknown() || digest.ValueString() != prior.ValueString() {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("context_digest"))
	}
}

// imgPlanContextDigest returns the digest for a planned build block: null
// without build, unknown when the context path is not known yet or cannot be
// read at plan time (it may be produced by another resource during apply).
func imgPlanContextDigest(b *imageBuildModel) types.String {
	if b == nil {
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
	if plan.Build != nil {
		id, digest, diags := r.build(ctx, &plan)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.ContextDigest = types.StringValue(digest)
		if id != "" {
			ref = id
		}
	} else {
		resp.Diagnostics.Append(r.pull(ctx, name, plan.Platform.ValueString())...)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.ContextDigest = types.StringNull()
	}
	insp, err := r.pd.Client.ImageInspect(ctx, ref)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect image", fmt.Sprintf("%s: %v", ref, err))
		return
	}
	imgApplyInspect(&plan, insp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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
	rc, err := r.pd.Client.ImagePull(ctx, name, image.PullOptions{RegistryAuth: auth, Platform: platform})
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

// build tars the context, runs ImageBuild and returns the built image ID
// (may be "" with some daemons; callers fall back to the tag) and the
// context digest.
func (r *imageResource) build(ctx context.Context, plan *imageResourceModel) (string, string, diag.Diagnostics) {
	var diags diag.Diagnostics
	b := plan.Build
	bc, err := imgCollectContext(b.Context.ValueString(), b.Dockerfile.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("build").AtName("context"), "Cannot read build context", err.Error())
		return "", "", diags
	}
	digest, err := imgContextDigest(bc)
	if err != nil {
		diags.AddAttributeError(path.Root("build").AtName("context"), "Cannot hash build context", err.Error())
		return "", "", diags
	}
	opts := imgBuildOptions(ctx, plan, bc.Dockerfile, &diags)
	opts.AuthConfigs = imgAuthConfigs(r.pd.RegistryAuth)
	if diags.HasError() {
		return "", "", diags
	}
	tflog.Info(ctx, "building image", map[string]any{"name": plan.Name.ValueString(), "context": b.Context.ValueString(), "files": len(bc.Files)})
	tarStream := imgTarReader(bc)
	defer tarStream.Close()
	res, err := r.pd.Client.ImageBuild(ctx, tarStream, opts)
	if err != nil {
		diags.AddError("Cannot start image build", err.Error())
		return "", "", diags
	}
	defer res.Body.Close()
	out, err := imgDecodeStream(res.Body, imgLogMessage(ctx))
	if err != nil {
		diags.AddError("Image build failed", fmt.Sprintf("%s: %v", plan.Name.ValueString(), err))
		return "", "", diags
	}
	return imgBuiltImageID(out.Aux), digest, diags
}

// imgBuildOptions maps the plan to ImageBuildOptions (no auth, no context).
func imgBuildOptions(ctx context.Context, plan *imageResourceModel, dockerfile string, diags *diag.Diagnostics) build.ImageBuildOptions {
	b := plan.Build
	platform := b.Platform.ValueString()
	if platform == "" {
		platform = plan.Platform.ValueString()
	}
	args := map[string]*string{}
	for k, v := range stringMapFrom(ctx, b.Args, diags) {
		args[k] = &v
	}
	return build.ImageBuildOptions{
		Tags:        []string{plan.Name.ValueString()},
		Dockerfile:  dockerfile,
		BuildArgs:   args,
		Target:      b.Target.ValueString(),
		Platform:    platform,
		Labels:      mergeLabels(stringMapFrom(ctx, b.Labels, diags), map[string]string{labelManagedBy: labelManagedByValue}),
		NoCache:     b.NoCache.ValueBool(),
		PullParent:  b.Pull.ValueBool(),
		Remove:      true,
		ForceRemove: true,
		Version:     build.BuilderV1,
	}
}

// imgAuthConfigs builds ImageBuild's AuthConfigs map (keyed by registry
// address, Docker Hub also under its legacy index URL) so FROM can pull
// private base images.
func imgAuthConfigs(creds []registryCredential) map[string]registry.AuthConfig {
	out := map[string]registry.AuthConfig{}
	for _, c := range creds {
		ac := registry.AuthConfig{Username: c.Username, Password: c.Password, ServerAddress: c.Address}
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
func imgLogMessage(ctx context.Context) func(jsonmessage.JSONMessage) {
	return func(m jsonmessage.JSONMessage) {
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
	m.ImageID = types.StringValue(insp.ID)
	if m.Name.IsNull() || m.Name.ValueString() == "" {
		if len(insp.RepoTags) > 0 {
			m.Name = types.StringValue(insp.RepoTags[0])
		} else {
			m.Name = types.StringValue(insp.ID)
		}
	}
	m.RepoDigest = stringOrNull(imgPickRepoDigest(m.Name.ValueString(), insp.RepoDigests))
	if m.KeepLocally.IsNull() {
		m.KeepLocally = types.BoolValue(false)
	}
	if m.ForceRemove.IsNull() {
		m.ForceRemove = types.BoolValue(false)
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
	ref := st.ImageID.ValueString()
	if ref == "" {
		ref = st.ID.ValueString() // import: name or ID
	}
	insp, err := r.pd.Client.ImageInspect(ctx, ref)
	if isNotFound(err) {
		tflog.Warn(ctx, "image gone from daemon; removing from state", map[string]any{"ref": ref})
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect image", fmt.Sprintf("%s: %v", ref, err))
		return
	}
	if st.Name.IsNull() && st.ImageID.IsNull() && !imgIsIDRef(ref, insp.ID) {
		// Imported by name: keep the reference the user gave.
		st.Name = types.StringValue(ref)
	}
	imgApplyInspect(&st, insp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// Update only handles keep_locally / force_remove; everything else replaces.
func (r *imageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st imageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.ImageID, plan.RepoDigest = st.ID, st.ImageID, st.RepoDigest
	if plan.ContextDigest.IsUnknown() {
		plan.ContextDigest = st.ContextDigest
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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
	if err := imgRemove(ctx, r.pd.Client, st.Name.ValueString(), st.ImageID.ValueString(), st.ForceRemove.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Cannot remove image", err.Error())
	}
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
	_, err := c.ImageRemove(ctx, target, image.RemoveOptions{Force: force, PruneChildren: true})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("%s: %w", target, err)
	}
	return nil
}

func (r *imageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// imgDockerHubIndexServer is the legacy key Docker Hub credentials are
// stored under in AuthConfigs.
const imgDockerHubIndexServer = "https://index.docker.io/v1/"
