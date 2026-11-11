package provider

// Pass 6b Lane Z: a network created with config_from takes its IPAM,
// IPv4/IPv6 settings and labels from the config-only network, and the
// Engine refuses any of its own ("user-specified configurations are not
// supported if the network depends on a configuration network", Engine
// 29.9.3 daemon/libnetwork/network.go:410-419). Reject them at validate time.

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func networkValidateConfigFromP6bZ2(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cf types.String
	var labels, ipamOpts types.Map
	var v4, v6 types.Bool
	var pools []networkIPAMConfigModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("config_from"), &cf)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("labels"), &labels)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ipam_options"), &ipamOpts)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("enable_ipv4"), &v4)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("enable_ipv6"), &v6)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ipam_config"), &pools)...)
	if resp.Diagnostics.HasError() || cf.IsNull() || (containerKnown(cf) && cf.ValueString() == "") {
		return
	}
	for _, at := range networkConfigFromConflicts(labels, ipamOpts, v4, v6, len(pools)) {
		resp.Diagnostics.AddAttributeError(path.Root(at), "Not supported with config_from",
			fmt.Sprintf("%s cannot be set on a network created with config_from: the network takes it from the config-only "+
				"network, and the Engine refuses its own (\"user-specified configurations are not supported if the network "+
				"depends on a configuration network\"). Set it on the config-only network instead.", at))
	}
}

// networkConfigFromConflicts names the arguments a config_from network
// sets that the Engine refuses.
func networkConfigFromConflicts(labels, ipamOpts types.Map, v4, v6 types.Bool, pools int) []string {
	var out []string
	if containerKnown(labels) && len(labels.Elements()) > 0 {
		out = append(out, "labels")
	}
	if containerKnown(ipamOpts) && len(ipamOpts.Elements()) > 0 {
		out = append(out, "ipam_options")
	}
	if pools > 0 {
		out = append(out, "ipam_config")
	}
	if containerKnown(v4) && !v4.ValueBool() {
		out = append(out, "enable_ipv4")
	}
	if containerKnown(v6) && v6.ValueBool() {
		out = append(out, "enable_ipv6")
	}
	return out
}
