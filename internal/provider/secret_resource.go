package provider

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*secretResource)(nil)
	_ resource.ResourceWithConfigure   = (*secretResource)(nil)
	_ resource.ResourceWithImportState = (*secretResource)(nil)
)

// NewSecretResource returns the docker_secret resource.
func NewSecretResource() resource.Resource { return &secretResource{} }

type secretResource struct {
	pd *providerData
}

type secretResourceModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Data   types.String `tfsdk:"data"`
	Labels types.Map    `tfsdk:"labels"`
}

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Swarm secret: an immutable blob the Swarm managers store encrypted and mount into service tasks " +
			"under `/run/secrets/<file_name>`. Requires the provider to point at a Swarm manager. Swarm secrets cannot be " +
			"changed after creation, so every change replaces the secret; a secret still referenced by a service cannot be " +
			"removed, so change the referencing service in the same apply (or use `create_before_destroy` with a new name).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The Swarm secret ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Secret name, unique in the Swarm. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"data": schema.StringAttribute{
				Required:  true,
				Sensitive: true,
				MarkdownDescription: "Secret content, base64-encoded (use `base64encode()`); it must decode. The daemon never " +
					"returns secret data, so it is kept from state on refresh and ignored on import. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{secretBase64Validator{}},
			},
			"labels": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "User labels on the secret. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data, err := base64.StdEncoding.DecodeString(plan.Data.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("data"), "Invalid base64", err.Error())
		return
	}
	spec := swarm.SecretSpec{
		Annotations: swarm.Annotations{
			Name:   plan.Name.ValueString(),
			Labels: mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue}),
		},
		Data: data,
	}
	out, err := r.pd.Client.SecretCreate(ctx, spec)
	if err != nil {
		resp.Diagnostics.AddError("Cannot create secret", err.Error())
		return
	}
	plan.ID = types.StringValue(out.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	r.read(ctx, &plan, &resp.Diagnostics, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// read refreshes m from the daemon; it reports gone=true when the secret no
// longer exists.
func (r *secretResource) read(ctx context.Context, m *secretResourceModel, diags *diag.Diagnostics, gone *bool) {
	s, _, err := r.pd.Client.SecretInspectWithRaw(ctx, m.ID.ValueString())
	if err != nil {
		if isNotFound(err) && gone != nil {
			*gone = true
			return
		}
		diags.AddError("Cannot read secret", err.Error())
		return
	}
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Spec.Name)
	m.Labels = svcMapValue(m.Labels, userLabels(s.Spec.Labels, false))
	// Data is never returned by the daemon: keep the state value.
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretResourceModel
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
}

// Update is never called with a real change: every attribute forces
// replacement. It just persists the plan.
func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan secretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := secretRemoveWhenFree(ctx, r.pd.Client, state.ID.ValueString(), false); err != nil {
		resp.Diagnostics.AddError("Cannot remove secret", err.Error())
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := resolveSecretID(ctx, r.pd.Client, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import secret", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// secretRemoveWhenFree removes a secret (or config when isConfig), retrying
// for a while when it is still in use by tasks that are shutting down.
func secretRemoveWhenFree(ctx context.Context, c *client.Client, id string, isConfig bool) error {
	if isConfig {
		return stackRemoveRetry(ctx, "config "+id, func(ctx context.Context) error { return c.ConfigRemove(ctx, id) })
	}
	return stackRemoveRetry(ctx, "secret "+id, func(ctx context.Context) error { return c.SecretRemove(ctx, id) })
}

// secretBase64Validator checks a string is valid standard base64. It is shared
// by docker_secret, docker_config and docker_stack's secret/config blocks.
type secretBase64Validator struct{}

func (secretBase64Validator) Description(context.Context) string {
	return "value must be valid standard base64"
}

func (v secretBase64Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (secretBase64Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := secretValidateBase64(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid base64", err.Error()+". Wrap the content in base64encode().")
	}
}

// secretValidateBase64 returns an error when s is not standard base64.
func secretValidateBase64(s string) error {
	if _, err := base64.StdEncoding.DecodeString(s); err != nil {
		return fmt.Errorf("data is not valid base64: %w", err)
	}
	return nil
}
