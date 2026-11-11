package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*volumeResource)(nil)
	_ resource.ResourceWithConfigure   = (*volumeResource)(nil)
	_ resource.ResourceWithImportState = (*volumeResource)(nil)
	_ resource.ResourceWithIdentity    = (*volumeResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*volumeResource)(nil)
)

// NewVolumeResource is the docker_volume constructor.
func NewVolumeResource() resource.Resource { return &volumeResource{} }

type volumeResource struct {
	pd *providerData
}

type volumeResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Driver     types.String `tfsdk:"driver"`
	DriverOpts types.Map    `tfsdk:"driver_opts"`
	Labels     types.Map    `tfsdk:"labels"`
	Mountpoint types.String `tfsdk:"mountpoint"`
	// ClusterVolumeSpec is the Swarm cluster (CSI) volume spec; ClusterVolumeID its swarm object ID.
	ClusterVolumeSpec types.Object `tfsdk:"cluster_volume_spec"`
	ClusterVolumeID   types.String `tfsdk:"cluster_volume_id"`
	Adopt             types.Bool   `tfsdk:"adopt"`
	contractEngineVolume
}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Docker volume (`VolumeCreate`). Volumes cannot be changed after creation, so every argument except " +
			"`cluster_volume_spec.availability` forces a new volume (and its data is lost). Destroy fails while a container still uses the volume. Import by volume name. " +
			"Swarm cluster (CSI) volumes are created with `cluster_volume_spec` (their `availability` updates in place). The raw `inspect_json` output is sensitive because driver options can hold credentials.",
		Attributes: contractAddResourceAttributes(map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Volume name (volumes have no separate ID).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Volume name. Unset lets the daemon generate one. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"driver": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("local"),
				MarkdownDescription: "Volume driver. Default `local`. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"driver_opts": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Driver options, e.g. `{ type = \"nfs\", o = \"addr=10.0.0.1,rw\", device = \":/export\" }` for `local`. " +
					"Changing this forces a new resource.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"labels": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Volume labels. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"cluster_volume_spec": objClusterVolumeSpecAttribute(),
			"adopt":               ownerAdoptAttribute(),
			"cluster_volume_id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Swarm object ID of a cluster volume (`ClusterVolume.ID`); null for a local volume.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"mountpoint": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Host path of the volume's data, as reported by the driver.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		}, "engine:volume"),
	}
}

func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan checks the owner label of an existing volume.
func (r *volumeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var name types.String
	var adopt types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("adopt"), &adopt)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(ownerPlanCheck(ctx, r.pd, req.Private, "volume", name.ValueString(), adopt)...)
}

// volumeFromInspect maps VolumeInspect output onto prior.
func volumeFromInspect(v volume.Volume, prior volumeResourceModel) volumeResourceModel {
	out := prior
	out.ID = types.StringValue(v.Name)
	out.Name = types.StringValue(v.Name)
	out.Driver = volumeDriverValue(prior.Driver, v.Driver)
	out.DriverOpts = containerMapValue(prior.DriverOpts, v.Options)
	out.Labels = containerMapValue(prior.Labels, volumeStripAnonymous(ownerStrip(userLabels(v.Labels, false)), prior.Labels))
	out.Mountpoint = types.StringValue(v.Mountpoint)
	out.ClusterVolumeSpec, out.ClusterVolumeID = objClusterVolumeState(prior.ClusterVolumeSpec, v.ClusterVolume)
	return out
}

// volumeDriverValue keeps the configured driver when the daemon reports
// the same one in another spelling. A managed plugin driver is reported
// with its tag ("sshfs" configured, VolumeInspect says "sshfs:latest"),
// as the plugin store names plugins by their normalized reference; both
// name the same plugin (pluginFamiliar), so the prior value stays and
// apply is consistent. Any other difference is drift and is read.
func volumeDriverValue(prior types.String, reported string) types.String {
	if p, ok := knownString(prior); ok && p != reported && pluginFamiliar(p) == pluginFamiliar(reported) {
		return prior
	}
	return types.StringValue(reported)
}

// volumeAnonymousLabel is the label the Engine adds to a volume created
// without a name (POST /volumes/create with Name ""): an anonymous volume.
const volumeAnonymousLabel = "com.docker.volume.anonymous"

// volumeStripAnonymous drops the Engine's anonymous-volume label unless the
// configuration (prior) sets it. Pass 6b Lane W pairwise finding: with name
// unset, apply failed with "Provider produced inconsistent result after
// apply: .labels: new element \"com.docker.volume.anonymous\" has appeared".
func volumeStripAnonymous(labels map[string]string, prior types.Map) map[string]string {
	if _, ok := labels[volumeAnonymousLabel]; !ok {
		return labels
	}
	if !prior.IsNull() && !prior.IsUnknown() {
		if _, set := prior.Elements()[volumeAnonymousLabel]; set {
			return labels
		}
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k != volumeAnonymousLabel {
			out[k] = v
		}
	}
	return out
}

func (r *volumeResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	name, _ := knownString(plan.Name)
	opts := client.VolumeCreateOptions{
		Name:       name,
		Driver:     plan.Driver.ValueString(),
		DriverOpts: stringMapFrom(ctx, plan.DriverOpts, &resp.Diagnostics),
		Labels:     r.pd.ownerInject(mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue})),

		ClusterVolumeSpec: objExpandClusterVolumeSpec(ctx, plan.ClusterVolumeSpec, &resp.Diagnostics),
	}
	if opts.ClusterVolumeSpec != nil {
		if ok, have, err := r.pd.APIVersionAtLeast(ctx, "1.42"); err == nil && !ok {
			resp.Diagnostics.AddAttributeError(path.Root("cluster_volume_spec"), "Engine API too old",
				fmt.Sprintf("cluster_volume_spec needs Docker Engine API 1.42 or later; the daemon negotiated %s.", have))
			return
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if name != "" {
		// VolumeCreate silently returns an existing volume of the same name;
		// refuse to adopt it so destroy cannot delete someone else's data.
		exists, err := volumeExists(ctx, r.pd.Client, name)
		if err != nil {
			resp.Diagnostics.AddError("Cannot inspect volume", err.Error())
			return
		}
		if exists && r.pd.Owner != "" {
			if ex, err := r.pd.Client.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err == nil {
				if err := ownerCreateCheck(r.pd, "volume", name, ex.Volume.Labels); err != nil {
					resp.Diagnostics.AddAttributeError(path.Root("name"), "Volume owned by another configuration", err.Error())
					return
				}
			}
		}
		if exists {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Volume already exists",
				fmt.Sprintf("A volume named %q already exists. Import it with `terraform import` instead.", name))
			return
		}
	}
	v, err := r.pd.Client.VolumeCreate(ctx, opts)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create volume", err.Error())
		return
	}
	state := volumeFromInspect(v.Volume, plan)
	vRes, err := r.pd.Client.VolumeInspect(ctx, v.Volume.Name, client.VolumeInspectOptions{})
	if err != nil {
		state.contractSetNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
		resp.Diagnostics.AddError("Cannot inspect volume", err.Error())
		return
	}
	state = volumeFromInspect(vRes.Volume, state)
	resp.Diagnostics.Append(state.contractFill(ctx, r.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(vRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior volumeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	vRes, err := r.pd.Client.VolumeInspect(ctx, prior.ID.ValueString(), client.VolumeInspectOptions{})
	v := vRes.Volume
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect volume", err.Error())
		return
	}
	state := volumeFromInspect(v, prior)
	if !ctrNilPrivate(resp.Private) {
		resp.Diagnostics.Append(ownerRecord(ctx, resp.Private, v.Labels)...)
	}
	resp.Diagnostics.Append(state.contractFill(ctx, r.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(vRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

// Update applies a changed cluster_volume_spec.availability (VolumeUpdate,
// the only in-place change; every other argument forces replacement) and
// re-reads the volume so the contract outputs are known.
func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan volumeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	vRes, err := r.pd.Client.VolumeInspect(ctx, plan.ID.ValueString(), client.VolumeInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect volume", err.Error())
		return
	}
	if cv := vRes.Volume.ClusterVolume; cv != nil {
		want := objExpandClusterVolumeSpec(ctx, plan.ClusterVolumeSpec, &resp.Diagnostics)
		if want != nil && want.Availability != cv.Spec.Availability {
			spec := cv.Spec
			spec.Availability = want.Availability
			if _, err := r.pd.Client.VolumeUpdate(ctx, cv.ID, client.VolumeUpdateOptions{Version: cv.Version, Spec: &spec}); err != nil {
				resp.Diagnostics.AddError("Cannot update cluster volume availability", err.Error())
				return
			}
			if vRes, err = r.pd.Client.VolumeInspect(ctx, plan.ID.ValueString(), client.VolumeInspectOptions{}); err != nil {
				resp.Diagnostics.AddError("Cannot inspect volume", err.Error())
				return
			}
		}
	}
	plan = volumeFromInspect(vRes.Volume, plan)
	resp.Diagnostics.Append(plan.contractFill(ctx, r.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(vRes.Raw)}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if _, err := r.pd.Client.VolumeRemove(ctx, state.ID.ValueString(), client.VolumeRemoveOptions{Force: false}); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cannot remove volume", fmt.Sprintf("%s: %v (is a container still using it?)", state.ID.ValueString(), err))
	}
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "Cannot import before the provider is configured.")
		return
	}
	ref := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name, err := resolveVolumeName(ctx, r.pd.Client, ref)
	if err != nil {
		resp.Diagnostics.AddError("Cannot find volume", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, name)...)
}
