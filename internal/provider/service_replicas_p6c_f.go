package provider

// Pass 6c Lane F (open finding 2): docker_service.replicas is null in state
// for every mode but replicated. With a schema default of 1 it was 1 for a
// global service too, so the configuration `terraform plan
// -generate-config-out` wrote for an imported global service (and `terraform
// query -generate-config-out` for a listed one) carried `replicas = 1` and
// then failed ValidateConfig (svcReplicasInvalidMode, replicas with a
// non-replicated mode is an error, as in the docker CLI).
//
// docker_stack keeps the plain default: its service blocks ignore replicas
// for the other modes and have no such validation.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// svcReplicasAttribute is the replicas attribute of docker_service
// (topLevel) or of a docker_stack service block.
func svcReplicasAttribute(topLevel bool, desc string) schema.Int64Attribute {
	if !topLevel {
		return schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1), MarkdownDescription: desc}
	}
	return schema.Int64Attribute{
		Optional: true, Computed: true,
		MarkdownDescription: desc + " Null in state for the other modes.",
		PlanModifiers:       []planmodifier.Int64{svcReplicasDefault{}},
	}
}

// svcReplicasDefault plans an unset replicas as 1 for mode = "replicated",
// null for the other modes and unknown while the mode is unknown.
type svcReplicasDefault struct{}

func (svcReplicasDefault) Description(context.Context) string {
	return "1 for mode = \"replicated\", null for the other modes"
}

func (d svcReplicasDefault) MarkdownDescription(ctx context.Context) string {
	return d.Description(ctx)
}

func (svcReplicasDefault) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.ConfigValue.IsNull() {
		return
	}
	var mode types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("mode"), &mode)...)
	switch {
	case resp.Diagnostics.HasError():
	case mode.IsUnknown():
		resp.PlanValue = types.Int64Unknown()
	case mode.IsNull() || mode.ValueString() == "replicated":
		resp.PlanValue = types.Int64Value(1)
	default:
		resp.PlanValue = types.Int64Null()
	}
}
