package provider

// Pass 3 Lane C: docker_tag. ImageTag on create; refresh compares the
// target tag's image ID with the source image's; destroy untags the target
// reference only (ImageRemove on the tag, never by ID, so the source image
// and its other tags stay).

import (
	"context"
	"fmt"

	"github.com/distribution/reference"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                   = (*tagResource)(nil)
	_ resource.ResourceWithConfigure      = (*tagResource)(nil)
	_ resource.ResourceWithValidateConfig = (*tagResource)(nil)
)

// NewTagResource is the docker_tag constructor.
func NewTagResource() resource.Resource { return &tagResource{} }

type tagResource struct {
	pd *providerData
}

type tagResourceModel struct {
	ID            types.String `tfsdk:"id"`
	SourceImage   types.String `tfsdk:"source_image"`
	TargetImage   types.String `tfsdk:"target_image"`
	SourceImageID types.String `tfsdk:"source_image_id"`
}

func (r *tagResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tag"
}

func (r *tagResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a tag to an image already on the provider's daemon, like `docker tag SOURCE TARGET`. " +
			"Create calls `ImageTag`; refresh inspects `target_image` and plans a re-tag when the tag is gone or now points at a different " +
			"image than `source_image`. Destroy untags `target_image` only (`ImageRemove` on the tag, never by ID); when it is the image's " +
			"only tag it is left in place with a warning, so the source image is never removed. Every argument forces a new resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<source_image_id>.<target_image>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"source_image": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Existing image to tag: a name, `name:tag`, or image ID (e.g. a `docker_image`'s `image_id`). Changing this forces a new resource.",
				PlanModifiers:       replace,
			},
			"target_image": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "New reference, `repository[:tag]` (no tag means `latest`), e.g. `registry.example.com/app:1.0`. Changing this forces a new resource.",
				PlanModifiers:       replace,
			},
			"source_image_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID (`sha256:...`) of the image `source_image` resolved to when the tag was set.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *tagResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *tagResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m tagResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if t, ok := knownString(m.TargetImage); ok {
		if _, err := tagNormalizeTarget(t); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("target_image"), "Invalid target_image", err.Error())
		}
		if s, ok := knownString(m.SourceImage); ok && s == t {
			resp.Diagnostics.AddAttributeError(path.Root("target_image"), "target_image equals source_image",
				"Tagging an image with its own reference does nothing, and destroy would untag the source.")
		}
	}
}

// tagNormalizeTarget validates ref as a taggable reference (no digest) and
// returns its familiar form with the default tag added.
func tagNormalizeTarget(ref string) (string, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", fmt.Errorf("%q: %w", ref, err)
	}
	if _, ok := named.(reference.Digested); ok {
		return "", fmt.Errorf("%q: a tag cannot contain a digest", ref)
	}
	return reference.FamiliarString(reference.TagNameOnly(named)), nil
}

func (r *tagResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tagResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	src, tgt := plan.SourceImage.ValueString(), plan.TargetImage.ValueString()
	insp, err := r.pd.Client.ImageInspect(ctx, src)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("source_image"), "Cannot find source image", fmt.Sprintf("%s: %v", src, err))
		return
	}
	if _, err := r.pd.Client.ImageTag(ctx, client.ImageTagOptions{Source: insp.ID, Target: tgt}); err != nil {
		resp.Diagnostics.AddError("Cannot tag image", fmt.Sprintf("%s -> %s: %v", src, tgt, err))
		return
	}
	plan.SourceImageID = types.StringValue(insp.ID)
	plan.ID = types.StringValue(insp.ID + "." + tgt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// tagCurrent reports whether target still points at the image source names
// (falling back to the recorded ID when source itself is gone).
func tagCurrent(ctx context.Context, pd *providerData, m tagResourceModel) (bool, string, error) {
	want := m.SourceImageID.ValueString()
	if insp, err := pd.Client.ImageInspect(ctx, m.SourceImage.ValueString()); err == nil {
		want = insp.ID
	} else if !isNotFound(err) {
		return false, "", err
	}
	tgt, err := pd.Client.ImageInspect(ctx, m.TargetImage.ValueString())
	if isNotFound(err) {
		return false, want, nil
	}
	if err != nil {
		return false, "", err
	}
	return tgt.ID == want, want, nil
}

func (r *tagResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tagResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	ok, _, err := tagCurrent(ctx, r.pd, state)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect images", err.Error())
		return
	}
	if !ok {
		// Gone or pointing elsewhere: drop from state so the next apply re-tags.
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tagResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every argument forces replacement; nothing to do in place.
	var plan tagResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tagResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tagResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	kept, err := tagRemove(ctx, r.pd, state)
	if err != nil {
		resp.Diagnostics.AddError("Cannot remove tag", err.Error())
		return
	}
	if kept {
		resp.Diagnostics.AddWarning("Tag left in place",
			fmt.Sprintf("%s is the only tag of image %s; removing it would delete the source image, so it was kept. "+
				"Remove it with `docker rmi` if that is intended.", state.TargetImage.ValueString(), state.SourceImageID.ValueString()))
	}
}

// tagRemove untags target_image when it still points at the image this
// resource tagged. It never removes by ID and never touches source_image:
// when target_image is the image's only tag (ImageRemove would then delete
// the image itself) it is kept and kept=true is returned.
func tagRemove(ctx context.Context, pd *providerData, m tagResourceModel) (kept bool, err error) {
	tgt := m.TargetImage.ValueString()
	if tgt == "" || tgt == m.SourceImage.ValueString() {
		return false, nil
	}
	insp, err := pd.Client.ImageInspect(ctx, tgt)
	if isNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if id := m.SourceImageID.ValueString(); id != "" && insp.ID != id {
		return false, nil // someone re-pointed the tag; it is no longer ours
	}
	if !tagHasOtherTag(insp.RepoTags, tgt) {
		return true, nil
	}
	_, err = pd.Client.ImageRemove(ctx, tgt, client.ImageRemoveOptions{Force: false, PruneChildren: false})
	if err != nil && !isNotFound(err) {
		return false, fmt.Errorf("%s: %w", tgt, err)
	}
	return false, nil
}

// tagHasOtherTag reports whether tags holds a reference other than target.
func tagHasOtherTag(tags []string, target string) bool {
	want, err := tagNormalizeTarget(target)
	if err != nil {
		want = target
	}
	for _, t := range tags {
		n, err := tagNormalizeTarget(t)
		if err != nil {
			n = t
		}
		if n != want {
			return true
		}
	}
	return false
}
