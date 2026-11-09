package provider

// docker_registry_manifest_list (DECISIONS Q19): assemble an OCI image
// index or Docker manifest list from images already in a registry and push
// it under a tag, through the registry v2 HTTP API (the job of
// `docker manifest create/annotate/push` and `docker buildx imagetools create`).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// regMLItem is the inventory item of docker_registry_manifest_list.
const regMLItem = "cli-only:manifest-list"

// regMLPrivatePushed is the private-state key holding the digest the
// provider last pushed (JSON string), to detect a tag moved outside
// Terraform.
const regMLPrivatePushed = "pushed_digest"

var (
	_ resource.Resource                = (*regManifestListResource)(nil)
	_ resource.ResourceWithConfigure   = (*regManifestListResource)(nil)
	_ resource.ResourceWithImportState = (*regManifestListResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*regManifestListResource)(nil)
)

// NewRegistryManifestListResource returns docker_registry_manifest_list.
func NewRegistryManifestListResource() resource.Resource { return &regManifestListResource{} }

type regManifestListResource struct {
	pd *providerData
}

type regMLModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Manifests          types.List   `tfsdk:"manifests"`
	Annotations        types.Map    `tfsdk:"annotations"`
	KeepRemotely       types.Bool   `tfsdk:"keep_remotely"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	ImageRef           types.String `tfsdk:"image_ref"`
	ImageDigestRef     types.String `tfsdk:"image_digest_ref"`
	Digest             types.String `tfsdk:"digest"`
	MediaType          types.String `tfsdk:"media_type"`
	Platforms          types.List   `tfsdk:"platforms"`
	ManifestDigests    types.Map    `tfsdk:"manifest_digests"`
	contractCliOnlyManifestList
}

type regMLEntryModel struct {
	Image        types.String `tfsdk:"image"`
	OS           types.String `tfsdk:"os"`
	Architecture types.String `tfsdk:"architecture"`
	Variant      types.String `tfsdk:"variant"`
	OSVersion    types.String `tfsdk:"os_version"`
	OSFeatures   types.List   `tfsdk:"os_features"`
	Annotations  types.Map    `tfsdk:"annotations"`
}

var regMLEntryAttrTypes = map[string]attr.Type{
	"image":        types.StringType,
	"os":           types.StringType,
	"architecture": types.StringType,
	"variant":      types.StringType,
	"os_version":   types.StringType,
	"os_features":  types.ListType{ElemType: types.StringType},
	"annotations":  types.MapType{ElemType: types.StringType},
}

func (r *regManifestListResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_manifest_list"
}

func (r *regManifestListResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	opt := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, MarkdownDescription: desc}
	}
	out := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Pushes a multi-platform image index (OCI image index or Docker manifest list) under a tag, assembled from images that are already in the same registry, " +
			"like `docker manifest create` + `annotate` + `push` or `docker buildx imagetools create`. " +
			"Everything goes through the registry v2 HTTP API with the provider's `registry_auth`; nothing is pulled and the daemon is not involved. " +
			"Images from other repositories of the registry are copied in by cross-repository blob mount. " +
			"The index is a Docker manifest list when every entry is a Docker schema 2 manifest and no annotations are set, otherwise an OCI image index. " +
			"A change to the entries pushes a new index under the same tag (in place); destroy deletes the index unless `keep_remotely = true`.",
		Attributes: contractAddResourceAttributes(map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The tag reference, the same as `name`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Reference the index is pushed under, with registry and tag, e.g. `registry.example.com/team/app:1.2` " +
					"(no tag means `latest`; a digest is not allowed). Forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"manifests": schema.ListNestedAttribute{
				Required: true,
				MarkdownDescription: "Entries of the index, in order. Each `image` is a reference in the same registry as `name`. " +
					"An image manifest becomes one entry, its platform read from its image config; the platform attributes override what the config says (`docker manifest annotate`). " +
					"An index or manifest list contributes all its entries, attestation manifests included, and takes no platform overrides.",
				Validators: []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"image": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "Source reference, e.g. `registry.example.com/team/app:1.2-arm64` or `...@sha256:...`. Pin a digest to make the index reproducible.",
					},
					"os":           opt("Operating system to record for this entry, e.g. `linux` (`docker manifest annotate --os`)."),
					"architecture": opt("CPU architecture to record, e.g. `arm64` (`--arch`)."),
					"variant":      opt("CPU variant to record, e.g. `v8` (`--variant`)."),
					"os_version":   opt("OS version to record, e.g. a Windows build number (`--os-version`)."),
					"os_features": schema.ListAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "OS features to record (`--os-features`).",
					},
					"annotations": schema.MapAttribute{
						ElementType:         types.StringType,
						Optional:            true,
						MarkdownDescription: "Annotations on this entry's descriptor (OCI index only).",
					},
				}},
			},
			"annotations": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Annotations on the index itself, e.g. `org.opencontainers.image.source` (forces an OCI image index).",
			},
			"keep_remotely": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When false (default), destroy deletes the index from the registry (`DELETE /v2/<repo>/manifests/<digest>`, which removes the tag; " +
					"the registry must allow deletes). When true the index is left in place.",
			},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "Skip TLS certificate verification of the registry. Default `false`.",
			},
			"image_ref":        out("The tag the index was pushed under, normalized to `<registry>/<repository>:<tag>`."),
			"image_digest_ref": out("`<registry>/<repository>@<digest>` of the pushed index."),
			"digest":           out("Digest of the pushed index."),
			"media_type":       out("Media type of the index: `application/vnd.oci.image.index.v1+json` or `application/vnd.docker.distribution.manifest.list.v2+json`."),
			"platforms": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Platforms of the entries as `os/arch[/variant]`, attestation manifests excluded.",
			},
			"manifest_digests": schema.MapAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Platform (`os/arch[/variant]`) to the digest of its entry.",
			},
		}, regMLItem),
	}
}

func (r *regManifestListResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// regMLOutputsUnknown marks every computed output unknown.
func regMLOutputsUnknown(m *regMLModel) {
	m.ImageRef, m.ImageDigestRef, m.Digest, m.MediaType = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	m.Platforms = types.ListUnknown(types.StringType)
	m.ManifestDigests = types.MapUnknown(types.StringType)
	m.contractSetUnknown()
}

// regMLCopyOutputs copies the computed outputs of src into dst.
func regMLCopyOutputs(dst, src *regMLModel) {
	dst.ID, dst.ImageRef, dst.ImageDigestRef, dst.Digest, dst.MediaType = src.ID, src.ImageRef, src.ImageDigestRef, src.Digest, src.MediaType
	dst.Platforms, dst.ManifestDigests = src.Platforms, src.ManifestDigests
	dst.contractCliOnlyManifestList = src.contractCliOnlyManifestList
}

// ModifyPlan keeps the outputs when only keep_remotely or
// insecure_skip_verify change, and plans a re-push when the tag no longer
// points at the index the provider pushed.
func (r *regManifestListResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || len(resp.RequiresReplace) > 0 {
		return
	}
	var plan, st regMLModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() || !plan.Name.Equal(st.Name) {
		return
	}
	var pushedDigest string
	if req.Private != nil {
		pushed, d := req.Private.GetKey(ctx, regMLPrivatePushed)
		resp.Diagnostics.Append(d...)
		_ = json.Unmarshal(pushed, &pushedDigest)
	}
	resp.Diagnostics.Append(regMLPlanOutputs(&plan, &st, pushedDigest)...)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// regMLPlanOutputs decides the planned outputs of an update: the state's
// when the entries are unchanged and the tag still holds the pushed index
// (pushedDigest, "" when unknown), unknown otherwise.
func regMLPlanOutputs(plan, st *regMLModel, pushedDigest string) diag.Diagnostics {
	var diags diag.Diagnostics
	switch {
	case plan.Manifests.IsUnknown() || plan.Annotations.IsUnknown():
		regMLOutputsUnknown(plan)
	case plan.Manifests.Equal(st.Manifests) && plan.Annotations.Equal(st.Annotations):
		if pushedDigest != "" && pushedDigest != st.Digest.ValueString() {
			diags.AddWarning("Manifest list changed outside Terraform",
				fmt.Sprintf("%s now points at %s, not at the index Terraform pushed (%s); the index is pushed again.", st.Name.ValueString(), st.Digest.ValueString(), pushedDigest))
			regMLOutputsUnknown(plan)
		} else {
			regMLCopyOutputs(plan, st)
		}
	default:
		regMLOutputsUnknown(plan)
	}
	plan.ID = st.ID
	return diags
}

func (r *regManifestListResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan regMLModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	digest, d := r.regMLPush(ctx, &plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Private != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, regMLPrivatePushed, regMLJSONString(digest))...)
	}
}

func (r *regManifestListResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st regMLModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Digest.IsUnknown() && !plan.Digest.IsNull() {
		// Only keep_remotely / insecure_skip_verify changed.
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	digest, d := r.regMLPush(ctx, &plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Private != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, regMLPrivatePushed, regMLJSONString(digest))...)
	}
}

// regMLJSONString encodes s as a JSON string (private state values are JSON).
func regMLJSONString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

func (r *regManifestListResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st regMLModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := st.Name.ValueString()
	if name == "" {
		name = st.ID.ValueString() // import
		st.Name = types.StringValue(name)
	}
	p, err := parseImageRef(name)
	if err != nil {
		resp.Diagnostics.AddError("Invalid image reference", err.Error())
		return
	}
	reg := newP3Registry(r.pd, p.Registry, p.Repository, st.InsecureSkipVerify.ValueBool())
	top, err := regGetManifest(ctx, reg, p.Tag)
	if err != nil {
		if errors.Is(err, regErrNotFound) {
			tflog.Warn(ctx, "manifest list gone from registry; removing from state", map[string]any{"name": name})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Cannot read manifest list from registry", fmt.Sprintf("%s: %v", name, err))
		return
	}
	if !top.isIndex() {
		resp.Diagnostics.AddWarning("Tag no longer holds an index",
			fmt.Sprintf("%s now points at a single image manifest (%s); the index is pushed again on the next apply.", name, top.Digest))
	}
	if st.Manifests.IsNull() {
		// Import: rebuild the entries from the index as digest references.
		st.Manifests = regMLEntriesFromIndex(p, top)
		if len(top.Doc.Annotations) > 0 {
			st.Annotations = stringMapValue(top.Doc.Annotations, false)
		} else {
			st.Annotations = types.MapNull(types.StringType)
		}
	}
	if st.KeepRemotely.IsNull() {
		st.KeepRemotely = types.BoolValue(false)
	}
	if st.InsecureSkipVerify.IsNull() {
		st.InsecureSkipVerify = types.BoolValue(false)
	}
	st.ID = types.StringValue(name)
	resp.Diagnostics.Append(regMLFillOutputs(ctx, &st, p, top)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// regMLEntriesFromIndex turns the non-attestation entries of an index into
// `manifests` values pinned by digest (import).
func regMLEntriesFromIndex(p imageRefParts, top *regFetched) types.List {
	vals := []attr.Value{}
	for _, d := range top.Doc.Manifests {
		if regIsAttestation(d) {
			continue
		}
		ann := types.MapNull(types.StringType)
		if len(d.Annotations) > 0 {
			ann = stringMapValue(d.Annotations, false)
		}
		vals = append(vals, types.ObjectValueMust(regMLEntryAttrTypes, map[string]attr.Value{
			"image":        types.StringValue(p.Registry + "/" + p.Repository + "@" + d.Digest),
			"os":           types.StringNull(),
			"architecture": types.StringNull(),
			"variant":      types.StringNull(),
			"os_version":   types.StringNull(),
			"os_features":  types.ListNull(types.StringType),
			"annotations":  ann,
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: regMLEntryAttrTypes}, vals)
}

// regMLFillOutputs sets the computed outputs from the index in the registry.
func regMLFillOutputs(ctx context.Context, m *regMLModel, p imageRefParts, top *regFetched) diag.Diagnostics {
	repo := p.Registry + "/" + p.Repository
	m.ImageRef = types.StringValue(repo + ":" + p.Tag)
	m.ImageDigestRef = types.StringValue(repo + "@" + top.Digest)
	m.Digest = types.StringValue(top.Digest)
	m.MediaType = types.StringValue(top.MediaType)
	var plats []string
	digests := map[string]attr.Value{}
	for _, d := range top.Doc.Manifests {
		if regIsAttestation(d) {
			continue
		}
		pl := regPlatformOf(d)
		plats = append(plats, pl)
		digests[pl] = types.StringValue(d.Digest)
	}
	m.Platforms = regStringList(plats)
	m.ManifestDigests = types.MapValueMust(types.StringType, digests)
	return m.contractFill(ctx, nil, contractFillInput{Raw: map[string][]byte{"manifest_json": top.Body}})
}

// regMLEntries decodes the manifests attribute.
func regMLEntries(ctx context.Context, l types.List) ([]regMLEntryModel, diag.Diagnostics) {
	var out []regMLEntryModel
	d := l.ElementsAs(ctx, &out, false)
	return out, d
}

// regMLPush assembles the index from the plan, pushes it and fills the
// plan's outputs. It returns the pushed digest.
func (r *regManifestListResource) regMLPush(ctx context.Context, m *regMLModel) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	name := m.Name.ValueString()
	p, err := parseImageRef(name)
	if err != nil {
		diags.AddAttributeError(path.Root("name"), "Invalid image reference", err.Error())
		return "", diags
	}
	if p.Digest != "" {
		diags.AddAttributeError(path.Root("name"), "Digest not allowed", "name must be a tag reference; the index digest is an output.")
		return "", diags
	}
	entries, d := regMLEntries(ctx, m.Manifests)
	diags.Append(d...)
	annotations := stringMapFrom(ctx, m.Annotations, &diags)
	if diags.HasError() {
		return "", diags
	}
	insecure := m.InsecureSkipVerify.ValueBool()
	target := newP3Registry(r.pd, p.Registry, p.Repository, insecure)
	doc, err := regMLAssemble(ctx, r.pd, target, p, entries, annotations, insecure)
	if err != nil {
		diags.AddError("Cannot assemble manifest list", fmt.Sprintf("%s: %v", name, err))
		return "", diags
	}
	body, err := regMarshal(doc)
	if err != nil {
		diags.AddError("Cannot encode manifest list", err.Error())
		return "", diags
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	scope := "repository:" + p.Repository + ":pull,push"
	if err := regPutManifest(ctx, target, p.Tag, doc.MediaType, body, scope); err != nil {
		diags.AddError("Cannot push manifest list", fmt.Sprintf("%s: %v", name, err))
		return "", diags
	}
	m.ID = types.StringValue(name)
	top := &regFetched{Body: body, Digest: digest, MediaType: doc.MediaType, Doc: *doc}
	diags.Append(regMLFillOutputs(ctx, m, p, top)...)
	return digest, diags
}

// regMLAssemble builds the index document. Entries from another repository
// of the registry are copied into the target repository first.
func regMLAssemble(ctx context.Context, pd *providerData, target *p3Registry, p imageRefParts, entries []regMLEntryModel, annotations map[string]string, insecure bool) (*regManifestDoc, error) {
	var descs []regDescriptor
	seen := map[string]string{}
	for i, e := range entries {
		src, err := parseImageRef(e.Image.ValueString())
		if err != nil {
			return nil, fmt.Errorf("manifests[%d].image: %w", i, err)
		}
		if normalizeRegistry(src.Registry) != normalizeRegistry(p.Registry) {
			return nil, fmt.Errorf("manifests[%d].image %s is in registry %s, not %s; copy it into %s first", i, e.Image.ValueString(), src.Registry, p.Registry, p.Registry)
		}
		ref := src.Digest
		if ref == "" {
			ref = src.Tag
		}
		sreg := newP3Registry(pd, src.Registry, src.Repository, insecure)
		f, err := regGetManifest(ctx, sreg, ref)
		if err != nil {
			return nil, fmt.Errorf("manifests[%d].image %s: %w", i, e.Image.ValueString(), err)
		}
		var add []regDescriptor
		if f.isIndex() {
			if regMLHasOverride(e) {
				return nil, fmt.Errorf("manifests[%d].image %s is an index; platform overrides apply to single image manifests only", i, e.Image.ValueString())
			}
			for _, d := range f.Doc.Manifests {
				if src.Repository != p.Repository {
					if err := regMLCopyManifest(ctx, sreg, target, src.Repository, p.Repository, d.Digest); err != nil {
						return nil, fmt.Errorf("manifests[%d]: copying %s: %w", i, d.Digest, err)
					}
				}
				add = append(add, d)
			}
		} else {
			d := regDescriptor{MediaType: f.MediaType, Digest: f.Digest, Size: int64(len(f.Body))}
			plat := &ocispec.Platform{}
			if f.Doc.Config != nil && f.Doc.Config.Digest != "" {
				blob, err := regGetBlob(ctx, sreg, f.Doc.Config.Digest)
				if err != nil {
					return nil, fmt.Errorf("manifests[%d]: reading image config: %w", i, err)
				}
				_ = json.Unmarshal(blob, plat)
			}
			regMLApplyOverrides(ctx, plat, e)
			if plat.OS == "" || plat.Architecture == "" {
				return nil, fmt.Errorf("manifests[%d].image %s: platform unknown; set os and architecture", i, e.Image.ValueString())
			}
			d.Platform = plat
			if !e.Annotations.IsNull() {
				var ann map[string]string
				_ = e.Annotations.ElementsAs(ctx, &ann, false)
				if len(ann) > 0 {
					d.Annotations = ann
				}
			}
			if src.Repository != p.Repository {
				if err := regMLCopyBody(ctx, sreg, target, src.Repository, p.Repository, f); err != nil {
					return nil, fmt.Errorf("manifests[%d]: copying %s: %w", i, f.Digest, err)
				}
			}
			add = append(add, d)
		}
		for _, d := range add {
			if regIsAttestation(d) {
				descs = append(descs, d)
				continue
			}
			pl := regPlatformOf(d)
			if prev, dup := seen[pl]; dup {
				return nil, fmt.Errorf("platform %s appears twice (%s and %s)", pl, prev, d.Digest)
			}
			seen[pl] = d.Digest
			descs = append(descs, d)
		}
	}
	return regMLDoc(descs, annotations)
}

// regMLDoc picks the index media type and builds the document: a Docker
// manifest list when every entry is a Docker schema 2 manifest and nothing
// carries annotations, otherwise an OCI image index.
func regMLDoc(descs []regDescriptor, annotations map[string]string) (*regManifestDoc, error) {
	docker := len(annotations) == 0
	for _, d := range descs {
		if d.MediaType != p3MediaDockerManif2 || len(d.Annotations) > 0 {
			docker = false
		}
	}
	doc := &regManifestDoc{SchemaVersion: 2, MediaType: p3MediaOCIIndex, Manifests: descs}
	if docker {
		doc.MediaType = p3MediaDockerList
	}
	if len(annotations) > 0 {
		doc.Annotations = annotations
	}
	return doc, nil
}

// regMLHasOverride reports whether any platform override is set.
func regMLHasOverride(e regMLEntryModel) bool {
	for _, v := range []types.String{e.OS, e.Architecture, e.Variant, e.OSVersion} {
		if v.ValueString() != "" {
			return true
		}
	}
	return !e.OSFeatures.IsNull() && len(e.OSFeatures.Elements()) > 0
}

// regMLApplyOverrides applies the platform attributes of e to plat.
func regMLApplyOverrides(ctx context.Context, plat *ocispec.Platform, e regMLEntryModel) {
	if v := e.OS.ValueString(); v != "" {
		plat.OS = v
	}
	if v := e.Architecture.ValueString(); v != "" {
		plat.Architecture = v
	}
	if v := e.Variant.ValueString(); v != "" {
		plat.Variant = v
	}
	if v := e.OSVersion.ValueString(); v != "" {
		plat.OSVersion = v
	}
	if !e.OSFeatures.IsNull() && !e.OSFeatures.IsUnknown() {
		var f []string
		_ = e.OSFeatures.ElementsAs(ctx, &f, false)
		sort.Strings(f)
		plat.OSFeatures = f
	}
}

// regMLCopyManifest copies manifest digest (and, for an index, its
// children) from repository from into to.
func regMLCopyManifest(ctx context.Context, src, dst *p3Registry, from, to, digest string) error {
	f, err := regGetManifest(ctx, src, digest)
	if err != nil {
		return err
	}
	if f.isIndex() {
		for _, d := range f.Doc.Manifests {
			if err := regMLCopyManifest(ctx, src, dst, from, to, d.Digest); err != nil {
				return err
			}
		}
	}
	return regMLCopyBody(ctx, src, dst, from, to, f)
}

// regMLCopyBody mounts the blobs of image manifest f into to and PUTs f
// there by digest.
func regMLCopyBody(ctx context.Context, _ *p3Registry, dst *p3Registry, from, to string, f *regFetched) error {
	scope := "repository:" + to + ":pull,push repository:" + from + ":pull"
	var blobs []string
	if f.Doc.Config != nil && f.Doc.Config.Digest != "" {
		blobs = append(blobs, f.Doc.Config.Digest)
	}
	for _, l := range f.Doc.Layers {
		if len(l.URLs) == 0 { // foreign layers stay where they are
			blobs = append(blobs, l.Digest)
		}
	}
	for _, b := range blobs {
		if err := regMountBlob(ctx, dst, b, from, scope); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(f.Digest, "sha256:") {
		return fmt.Errorf("unexpected digest %q", f.Digest)
	}
	return regPutManifest(ctx, dst, f.Digest, f.MediaType, f.Body, scope)
}

func (r *regManifestListResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st regMLModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() || st.KeepRemotely.ValueBool() {
		return
	}
	name := st.Name.ValueString()
	p, err := parseImageRef(name)
	if err != nil {
		resp.Diagnostics.AddError("Invalid image reference", err.Error())
		return
	}
	user, pass := r.pd.p3RegistryCreds(p.Registry)
	if err := registryImageDeleteRemote(ctx, name, st.Digest.ValueString(), user, pass, st.InsecureSkipVerify.ValueBool()); err != nil {
		resp.Diagnostics.AddWarning("Manifest list not deleted from registry",
			fmt.Sprintf("%s was removed from Terraform state, but deleting %s from the registry failed: %v. "+
				"Delete it manually or set keep_remotely = true.", name, st.Digest.ValueString(), err))
	}
}

func (r *regManifestListResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
