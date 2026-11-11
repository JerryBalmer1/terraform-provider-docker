package provider

// Pass 6b Lane W, pairwise finding: `replicas` set together with a mode
// other than `replicated` was accepted and silently ignored (pairwise configs
// pw-service-03/04/10/13/17/18: replicas = 1 or 2 with mode = "global"
// applied cleanly while the service spec had no replica count).
//
// Pass 6b Lane Z: ValidateConfig now rejects the combination, as the docker
// CLI does. docker/cli v29.9.3 cli/command/service/opts.go:630 (global) and
// :666 (global-job) return "replicas can only be used with replicated or
// replicated-job mode". The CLI maps --replicas onto ReplicatedJob's
// TotalCompletions for replicated-job; this provider has a dedicated
// total_completions attribute for that, so replicas is rejected there too and
// the message points at total_completions.

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithValidateConfig = (*serviceResource)(nil)

// svcReplicasModeCLIError is the docker CLI's message for --replicas with a
// non-replicated mode (docker/cli v29.9.3 cli/command/service/opts.go:630).
const svcReplicasModeCLIError = "replicas can only be used with replicated or replicated-job mode"

// ValidateConfig rejects replicas configured for a non-replicated mode.
func (r *serviceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var mode types.String
	var replicas types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("mode"), &mode)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("replicas"), &replicas)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m, ok := svcReplicasInvalidMode(mode, replicas); ok {
		hint := "Remove replicas, or use mode = \"replicated\"."
		if m == "replicated_job" {
			hint = "For a replicated job, set total_completions (and max_concurrent) instead of replicas."
		}
		resp.Diagnostics.AddAttributeError(path.Root("replicas"), "replicas is not valid for this mode",
			fmt.Sprintf("%s (docker CLI). replicas only applies to mode = \"replicated\"; mode is %q. %s",
				svcReplicasModeCLIError, m, hint))
	}
}

// svcReplicasInvalidMode returns the mode when replicas is configured and
// mode is known and not replicated.
func svcReplicasInvalidMode(mode types.String, replicas types.Int64) (string, bool) {
	if replicas.IsNull() || replicas.IsUnknown() || mode.IsNull() || mode.IsUnknown() {
		return "", false
	}
	if m := mode.ValueString(); m != "" && m != "replicated" {
		return m, true
	}
	return "", false
}
