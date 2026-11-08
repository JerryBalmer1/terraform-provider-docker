package provider

import (
	"context"
	"encoding/base64"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*dockerConfigResource)(nil)
	_ resource.ResourceWithConfigure   = (*dockerConfigResource)(nil)
	_ resource.ResourceWithImportState = (*dockerConfigResource)(nil)
	_ resource.ResourceWithIdentity    = (*dockerConfigResource)(nil)

	_ resource.ResourceWithConfigValidators = (*dockerConfigResource)(nil)
)

// NewDockerConfigResource returns the docker_config resource.
func NewDockerConfigResource() resource.Resource { return &dockerConfigResource{} }

type dockerConfigResource struct {
	pd *providerData
}

type dockerConfigResourceModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Data          types.String `tfsdk:"data"`
	DataWO        types.String `tfsdk:"data_wo"`
	DataWOVersion types.Int64  `tfsdk:"data_wo_version"`
	Labels        types.Map    `tfsdk:"labels"`
}

func (r *dockerConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config"
}

func (r *dockerConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Swarm config: an immutable, non-secret blob mounted into service tasks as a file. Requires the " +
			"provider to point at a Swarm manager. Swarm configs cannot be changed after creation, so every change replaces " +
			"the config; a config still referenced by a service cannot be removed, so change the referencing service in the " +
			"same apply (or use `create_before_destroy` with a new name).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Swarm config ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Config name, unique in the Swarm. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"data": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Config content, base64-encoded (use `base64encode()`); it must decode. Kept from state on " +
					"refresh and filled from the daemon on import. Changing this forces a new resource. " +
					"Exactly one of `data` and `data_wo` is required.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{secretBase64Validator{}},
			},
			"data_wo":         secretDataWOAttribute("Config"),
			"data_wo_version": secretDataWOVersionAttribute(),
			"labels": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "User labels on the config. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *dockerConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *dockerConfigResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

func (r *dockerConfigResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return secretDataConfigValidators()
}

func (r *dockerConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dockerConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data, ok := secretPayload(ctx, req.Config, plan.Data, &resp.Diagnostics)
	if !ok {
		return
	}
	spec := swarm.ConfigSpec{
		Annotations: swarm.Annotations{
			Name:   plan.Name.ValueString(),
			Labels: mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue}),
		},
		Data: data,
	}
	out, err := r.pd.Client.ConfigCreate(ctx, client.ConfigCreateOptions{Spec: spec})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create config", err.Error())
		return
	}
	plan.ID = types.StringValue(out.ID)
	plan.DataWO = types.StringNull() // write-only: never persisted
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, out.ID)...)
	r.read(ctx, &plan, &resp.Diagnostics, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dockerConfigResource) read(ctx context.Context, m *dockerConfigResourceModel, diags *diag.Diagnostics, gone *bool) {
	sRes, err := r.pd.Client.ConfigInspect(ctx, m.ID.ValueString(), client.ConfigInspectOptions{})
	if err != nil {
		if isNotFound(err) && gone != nil {
			*gone = true
			return
		}
		diags.AddError("Cannot read config", err.Error())
		return
	}
	s := sRes.Config
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Spec.Name)
	m.Labels = svcMapValue(m.Labels, userLabels(s.Spec.Labels, false))
	// The daemon does return config data, but import keeps the contract of
	// docker_secret: data stays whatever state holds. Fill it when state has
	// none (import) so plans after import are clean. A config created from
	// data_wo has data_wo_version set and keeps data null.
	if (m.Data.IsNull() || m.Data.IsUnknown()) && m.DataWOVersion.IsNull() {
		m.Data = types.StringValue(base64.StdEncoding.EncodeToString(s.Spec.Data))
	}
}

func (r *dockerConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dockerConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gone := false
	r.read(ctx, &state, &resp.Diagnostics, &gone)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

// Update only persists the plan: every attribute forces replacement.
func (r *dockerConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan dockerConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.DataWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
}

func (r *dockerConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dockerConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := secretRemoveWhenFree(ctx, r.pd.Client, state.ID.ValueString(), true); err != nil {
		resp.Diagnostics.AddError("Cannot remove config", err.Error())
	}
}

func (r *dockerConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := resolveConfigID(ctx, r.pd.Client, ref)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import config", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, id)...)
}
