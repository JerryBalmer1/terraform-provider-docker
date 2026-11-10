package provider

// Pass 5 Lane A (WP img): the buildx:build contract outputs on docker_image
// that do not clash with engine:image's (metadata_json, its hash,
// build_metadata, buildx_version) plus the build_ref identity, filled from
// the BuildKit seam result. dockerfile / dockerfile_sha256 /
// dockerfile_syntax and the engine versions are shared with engine:image
// and come from contractEngineImage.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const imgItemBuildxBuild = "buildx:build"

// imgBuildxOutputNames are the buildx:build outputs docker_image adds
// beside engine:image's.
var imgBuildxOutputNames = []string{"metadata_json", "metadata_json_sha256", "build_metadata", "buildx_version"}

// imgBuildxOutputs is embedded in imageResourceModel.
type imgBuildxOutputs struct {
	MetadataJSON       types.String `tfsdk:"metadata_json"`
	MetadataJSONSHA256 types.String `tfsdk:"metadata_json_sha256"`
	BuildMetadata      types.Object `tfsdk:"build_metadata"`
	BuildxVersion      types.String `tfsdk:"buildx_version"`
	BuildRef           types.String `tfsdk:"build_ref"`
}

// imgBuildxAttributes adds the buildx:build outputs to the resource schema.
func imgBuildxAttributes(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	all := contractResourceAttributes(imgItemBuildxBuild)
	for _, n := range imgBuildxOutputNames {
		attrs[n] = all[n]
	}
	attrs["build_ref"] = schema.StringAttribute{
		Computed: true,
		MarkdownDescription: "BuildKit build record reference (`buildx.build.ref`, builder/node/ref) of a BuildKit build, the input of " +
			"`docker_buildx_history`; null for classic builds, pulls and imports.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	return attrs
}

func imgBuildxFromValues(v map[string]attr.Value, ref types.String) imgBuildxOutputs {
	return imgBuildxOutputs{
		MetadataJSON:       v["metadata_json"].(types.String),
		MetadataJSONSHA256: v["metadata_json_sha256"].(types.String),
		BuildMetadata:      v["build_metadata"].(types.Object),
		BuildxVersion:      v["buildx_version"].(types.String),
		BuildRef:           ref,
	}
}

// imgBuildxNull is the value for an image that was not built by BuildKit.
func imgBuildxNull() imgBuildxOutputs {
	return imgBuildxFromValues(contractNullValues(imgItemBuildxBuild), types.StringNull())
}

// imgBuildxUnknown is the plan-time value of a new image: unknown when it
// is (or may be) built by BuildKit, null otherwise.
func imgBuildxUnknown(b *imageBuildModel) imgBuildxOutputs {
	if b == nil || (!b.Builder.IsUnknown() && !imgUsesBuildKit(b)) {
		return imgBuildxNull()
	}
	return imgBuildxFromValues(contractUnknownValues(imgItemBuildxBuild), types.StringUnknown())
}

// imgFillBuildxOutputs fills the buildx outputs from a seam result (nil:
// not a BuildKit build, all null).
func imgFillBuildxOutputs(ctx context.Context, pd *providerData, m *imageResourceModel, res *seamBuildResult) diag.Diagnostics {
	if res == nil {
		m.imgBuildxOutputs = imgBuildxNull()
		return nil
	}
	meta := res.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	var diags diag.Diagnostics
	raw, err := json.Marshal(meta)
	if err != nil {
		diags.AddWarning("Cannot encode build metadata", err.Error())
		m.imgBuildxOutputs = imgBuildxNull()
		return diags
	}
	vals, d := contractValues(ctx, pd, imgItemBuildxBuild, contractFillInput{Raw: map[string][]byte{"metadata_json": raw}})
	diags.Append(d...)
	if d.HasError() {
		m.imgBuildxOutputs = imgBuildxNull()
		return diags
	}
	ref := types.StringNull()
	if s, ok := meta["buildx.build.ref"].(string); ok && s != "" {
		ref = types.StringValue(s)
	}
	m.imgBuildxOutputs = imgBuildxFromValues(vals, ref)
	return diags
}

// imgApplySeamResult overrides the identity values the seam knows better
// than the local inspect: the pushed manifest digest, its repo@digest
// reference and the platforms built.
func imgApplySeamResult(m *imageResourceModel, res *seamBuildResult) {
	if res == nil {
		return
	}
	if res.Digest != "" {
		m.Digest = types.StringValue(res.Digest)
	}
	if res.ImageDigestRef != "" {
		m.ImageDigestRef = types.StringValue(res.ImageDigestRef)
	}
	if len(res.Platforms) > 0 {
		vals := make([]attr.Value, 0, len(res.Platforms))
		for _, p := range res.Platforms {
			vals = append(vals, types.StringValue(p))
		}
		m.Platforms = types.ListValueMust(types.StringType, vals)
	}
}

// imgUsesBuildKit reports whether the build block selects BuildKit: the
// daemon's embedded BuildKit (`buildkit`) or a named buildx builder.
func imgUsesBuildKit(b *imageBuildModel) bool {
	if b == nil || b.Builder.IsNull() || b.Builder.IsUnknown() {
		return false
	}
	v := b.Builder.ValueString()
	return v != "" && v != imgBuilderClassic
}

// imgSeamBuilder maps build.builder to the seam's builder reference: the
// embedded BuildKit for `buildkit`, else the buildx builder of that name.
func imgSeamBuilder(b *imageBuildModel) seamBuilderRef {
	if v := b.Builder.ValueString(); strings.Contains(v, "://") {
		// A BuildKit address (docker_buildx_builder.endpoint): a remote
		// driver builder, or a docker-container node by container name.
		return seamBuilderRef{Endpoint: v}
	} else if v != imgBuilderBuildKit {
		return seamBuilderRef{Name: v}
	}
	return seamBuilderRef{}
}

// imgNormalizedBuilder returns b with a named buildx builder shown as
// `buildkit`, for validators that only know the two backend names.
func imgNormalizedBuilder(b *imageBuildModel) *imageBuildModel {
	if b == nil || !imgUsesBuildKit(b) || b.Builder.ValueString() == imgBuilderBuildKit {
		return b
	}
	c := *b
	c.Builder = types.StringValue(imgBuilderBuildKit)
	return &c
}

// imgBuildSSHModel is one build.ssh entry.
type imgBuildSSHModel struct {
	ID    types.String `tfsdk:"id"`
	Paths types.List   `tfsdk:"paths"`
}
