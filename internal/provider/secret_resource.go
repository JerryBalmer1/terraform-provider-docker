package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                     = (*secretResource)(nil)
	_ resource.ResourceWithConfigure        = (*secretResource)(nil)
	_ resource.ResourceWithImportState      = (*secretResource)(nil)
	_ resource.ResourceWithIdentity         = (*secretResource)(nil)
	_ resource.ResourceWithConfigValidators = (*secretResource)(nil)
)

// NewSecretResource returns the docker_secret resource.
func NewSecretResource() resource.Resource { return &secretResource{} }

type secretResource struct {
	pd *providerData
}

type secretResourceModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Data          types.String `tfsdk:"data"`
	DataWO        types.String `tfsdk:"data_wo"`
	DataWOVersion types.Int64  `tfsdk:"data_wo_version"`
	Labels        types.Map    `tfsdk:"labels"`
	VersionIndex  types.Int64  `tfsdk:"version_index"`
	contractSwarmSecret
}

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Swarm secret: a blob the Swarm managers store encrypted and mount into service tasks " +
			"under `/run/secrets/<file_name>`. Requires the provider to point at a Swarm manager. The content of a swarm secret cannot be " +
			"changed after creation, so a content or name change replaces the secret, while labels are updated in place (`SecretUpdate`); a secret still referenced by a service cannot be " +
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
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Secret content, base64-encoded (use `base64encode()`); it must decode. The daemon never " +
					"returns secret data, so it is kept from state on refresh and ignored on import. Changing this forces a new resource. " +
					"Exactly one of `data` and `data_wo` is required.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{secretBase64Validator{}},
			},
			"data_wo":         secretDataWOAttribute("Secret"),
			"data_wo_version": secretDataWOVersionAttribute(),
			"labels": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "User labels on the secret. Updated in place (`SecretUpdate`); the content cannot change, so only labels can.",
			},
			"version_index": schema.Int64Attribute{Computed: true,
				MarkdownDescription: "The secret object's `Version.Index`; it changes when the labels are updated."},
		},
	}
	contractAddResourceAttributes(resp.Schema.Attributes, "swarm:secret")
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *secretResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = listIDIdentitySchema()
}

func (r *secretResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return secretDataConfigValidators()
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan secretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.pd != nil && !r.pd.gateItemAPI(ctx, "swarm:secret", &resp.Diagnostics) {
		return
	}
	data, ok := secretPayload(ctx, req.Config, plan.Data, &resp.Diagnostics)
	if !ok {
		return
	}
	spec := swarm.SecretSpec{
		Annotations: swarm.Annotations{
			Name:   plan.Name.ValueString(),
			Labels: mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue}),
		},
		Data: data,
	}
	out, err := r.pd.Client.SecretCreate(ctx, client.SecretCreateOptions{Spec: spec})
	if err != nil {
		resp.Diagnostics.AddError("Cannot create secret", err.Error())
		return
	}
	plan.ID = types.StringValue(out.ID)
	plan.DataWO = types.StringNull() // write-only: never persisted
	plan.VersionIndex = types.Int64Null()
	plan.contractSetNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, out.ID)...)
	r.read(ctx, &plan, &resp.Diagnostics, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// read refreshes m from the daemon; it reports gone=true when the secret no
// longer exists.
func (r *secretResource) read(ctx context.Context, m *secretResourceModel, diags *diag.Diagnostics, gone *bool) {
	sRes, err := r.pd.Client.SecretInspect(ctx, m.ID.ValueString(), client.SecretInspectOptions{})
	if err != nil {
		if isNotFound(err) && gone != nil {
			*gone = true
			return
		}
		diags.AddError("Cannot read secret", err.Error())
		return
	}
	s := sRes.Secret
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Spec.Name)
	m.Labels = svcMapValue(m.Labels, userLabels(s.Spec.Labels, false))
	m.VersionIndex = swarmVersionIndex(s.Version)
	// Data is never returned by the daemon: keep the state value.
	diags.Append(m.contractFill(ctx, r.pd, swarmInspectInput(sRes.Raw, s))...)
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
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, state.ID.ValueString())...)
}

// Update changes the labels in place (SecretUpdate); every other attribute
// forces replacement.
func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state secretResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	labels := mergeLabels(stringMapFrom(ctx, plan.Labels, &resp.Diagnostics), map[string]string{labelManagedBy: labelManagedByValue})
	err := swarmRetryConflict(ctx, func(ctx context.Context) error {
		res, err := r.pd.Client.SecretInspect(ctx, plan.ID.ValueString(), client.SecretInspectOptions{})
		if err != nil {
			return err
		}
		spec := res.Secret.Spec
		spec.Labels = labels
		spec.Data = nil // the Engine only accepts label changes
		_, err = r.pd.Client.SecretUpdate(ctx, plan.ID.ValueString(), client.SecretUpdateOptions{Version: res.Secret.Version, Spec: spec})
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot update secret labels", err.Error())
		return
	}
	plan.DataWO = types.StringNull()
	r.read(ctx, &plan, &resp.Diagnostics, nil)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, plan.ID.ValueString())...)
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
	ref := listIDFromImport(ctx, req, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := resolveSecretID(ctx, r.pd.Client, ref)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import secret", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(listSetIDIdentity(ctx, resp.Identity, id)...)
}

// secretDataWOAttribute is the write-only twin of `data`, shared by
// docker_secret and docker_config. Terraform never stores it; data_wo_version
// is the stored signal that it changed.
func secretDataWOAttribute(kind string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional:  true,
		WriteOnly: true,
		Sensitive: true,
		MarkdownDescription: kind + " content as a write-only argument (Terraform 1.11+), base64-encoded (use `base64encode()`); it is never " +
			"stored in plan or state, so it can come from an ephemeral value. Requires `data_wo_version`; bump the version to " +
			"replace the " + strings.ToLower(kind) + " with new content. Conflicts with `data`.",
		Validators: []validator.String{secretBase64Validator{}},
	}
}

func secretDataWOVersionAttribute() schema.Int64Attribute {
	return schema.Int64Attribute{
		Optional: true,
		MarkdownDescription: "Version of `data_wo`. Terraform cannot see changes to a write-only value, so change this number " +
			"whenever `data_wo` changes; changing it forces a new resource (swarm secrets and configs are immutable).",
		PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
	}
}

// secretDataConfigValidators: exactly one of data / data_wo; data_wo and
// data_wo_version go together.
func secretDataConfigValidators() []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("data"), path.MatchRoot("data_wo")),
		resourcevalidator.RequiredTogether(path.MatchRoot("data_wo"), path.MatchRoot("data_wo_version")),
	}
}

// secretPayload returns the decoded content: data from the plan, or data_wo
// from the config (write-only values are always null in the plan).
func secretPayload(ctx context.Context, cfg tfsdk.Config, data types.String, diags *diag.Diagnostics) ([]byte, bool) {
	attr, raw := "data", data
	if raw.IsNull() {
		var wo types.String
		diags.Append(cfg.GetAttribute(ctx, path.Root("data_wo"), &wo)...)
		if diags.HasError() {
			return nil, false
		}
		attr, raw = "data_wo", wo
	}
	if raw.IsNull() || raw.IsUnknown() {
		diags.AddError("Missing content", "Set exactly one of data and data_wo.")
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(raw.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root(attr), "Invalid base64", err.Error())
		return nil, false
	}
	return b, true
}

// secretRemoveWhenFree removes a secret (or config when isConfig), retrying
// for a while when it is still in use by tasks that are shutting down.
func secretRemoveWhenFree(ctx context.Context, c *client.Client, id string, isConfig bool) error {
	if isConfig {
		return stackRemoveRetry(ctx, "config "+id, func(ctx context.Context) error {
			_, err := c.ConfigRemove(ctx, id, client.ConfigRemoveOptions{})
			return err
		})
	}
	return stackRemoveRetry(ctx, "secret "+id, func(ctx context.Context) error {
		_, err := c.SecretRemove(ctx, id, client.SecretRemoveOptions{})
		return err
	})
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
