package provider

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/volume"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*volumeResource)(nil)
	_ resource.ResourceWithConfigure   = (*volumeResource)(nil)
	_ resource.ResourceWithImportState = (*volumeResource)(nil)
	_ resource.ResourceWithIdentity    = (*volumeResource)(nil)
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
}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A local Docker volume (`VolumeCreate`). Volumes cannot be changed after creation, so every argument forces " +
			"a new volume (and its data is lost). Destroy fails while a container still uses the volume. Import by volume name.",
		Attributes: map[string]schema.Attribute{
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
			"mountpoint": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Host path of the volume's data, as reported by the driver.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// volumeFromInspect maps VolumeInspect output onto prior.
func volumeFromInspect(v volume.Volume, prior volumeResourceModel) volumeResourceModel {
	out := prior
	out.ID = types.StringValue(v.Name)
	out.Name = types.StringValue(v.Name)
	out.Driver = types.StringValue(v.Driver)
	out.DriverOpts = containerMapValue(prior.DriverOpts, v.Options)
	out.Labels = containerMapValue(prior.Labels, userLabels(v.Labels, false))
	out.Mountpoint = types.StringValue(v.Mountpoint)
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
	opts := volume.CreateOptions{
		Name:       name,
		Driver:     plan.Driver.ValueString(),
		DriverOpts: stringMapFrom(ctx, plan.DriverOpts, &resp.Diagnostics),
		Labels:     mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue}),
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
	state := volumeFromInspect(v, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior volumeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	v, err := r.pd.Client.VolumeInspect(ctx, prior.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect volume", err.Error())
		return
	}
	state := volumeFromInspect(v, prior)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

// Update is only reached for no-op plans: every argument forces replacement.
func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan volumeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if err := r.pd.Client.VolumeRemove(ctx, state.ID.ValueString(), false); err != nil && !isNotFound(err) {
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
