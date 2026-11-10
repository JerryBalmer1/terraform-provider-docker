package provider

// Pass 5.3 Lane S: the {name, options} driver object of the Engine API
// (definition Driver: SecretSpec.Driver, SecretSpec.Templating,
// ConfigSpec.Templating, SwarmSpec.TaskDefaults.LogDriver), shared by
// docker_secret, docker_config and docker_swarm_init.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
)

// swarmDriverModel is an Engine API Driver object.
type swarmDriverModel struct {
	Name    types.String `tfsdk:"name"`
	Options types.Map    `tfsdk:"options"`
}

// swarmDriverAttribute is an optional {name, options} object that forces
// replacement (secrets and configs are immutable apart from their labels).
// since, when set, is the Engine API version that introduced it.
func swarmDriverAttribute(desc, since string) schema.SingleNestedAttribute {
	d := desc + " Forces replacement."
	if since != "" {
		d += apiSince(since)
	}
	return schema.SingleNestedAttribute{
		Optional:            true,
		MarkdownDescription: d,
		PlanModifiers:       []planmodifier.Object{objectplanmodifier.RequiresReplace()},
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Driver name (`Name`)."},
			"options": schema.MapAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Driver-specific options (`Options`)."},
		},
	}
}

// swarmDriverValue reads a daemon Driver back: null when the daemon has none,
// keeping a configured empty options map.
func swarmDriverValue(prior *swarmDriverModel, d *swarm.Driver) *swarmDriverModel {
	if d == nil || (d.Name == "" && len(d.Options) == 0) {
		return nil
	}
	p := types.MapNull(types.StringType)
	if prior != nil {
		p = prior.Options
	}
	return &swarmDriverModel{Name: types.StringValue(d.Name), Options: svcMapValue(p, d.Options)}
}

// secretDriverDataConfigValidators: data and data_wo conflict, data_wo and
// data_wo_version go together, and without a driver one of data and data_wo
// is required (secretDriverNeedsData).
func secretDriverDataConfigValidators() []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.Conflicting(path.MatchRoot("data"), path.MatchRoot("data_wo")),
		resourcevalidator.RequiredTogether(path.MatchRoot("data_wo"), path.MatchRoot("data_wo_version")),
		secretDriverNeedsData{},
	}
}

// secretDriverNeedsData requires data or data_wo unless driver is set.
type secretDriverNeedsData struct{}

func (secretDriverNeedsData) Description(context.Context) string {
	return "one of data and data_wo is required unless driver is set"
}

func (v secretDriverNeedsData) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (secretDriverNeedsData) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data, wo types.String
	var drv types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("data"), &data)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("data_wo"), &wo)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("driver"), &drv)...)
	if resp.Diagnostics.HasError() || data.IsUnknown() || wo.IsUnknown() || drv.IsUnknown() {
		return
	}
	if data.IsNull() && wo.IsNull() && drv.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("data"), "Missing secret content",
			"Set exactly one of data and data_wo, or a driver that provides the secret value.")
	}
}
