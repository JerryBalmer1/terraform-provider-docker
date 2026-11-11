package provider

// Pass 6b Lane W, pairwise finding: with enable_ipv4 = false the Engine
// accepts an IPv4 pool in IPAM.Config and silently drops it (GET
// /networks/{id} reports no IPv4 pool), so the apply ended in "Provider
// produced inconsistent result after apply: .ipam_config: block count
// changed from 1 to 0". The combination is rejected at validate time instead.

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithValidateConfig = (*networkResource)(nil)

// ValidateConfig rejects IPv4 ipam_config pools on a network with enable_ipv4 = false.
func (r *networkResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var v4 types.Bool
	var pools []networkIPAMConfigModel
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("enable_ipv4"), &v4)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ipam_config"), &pools)...)
	if resp.Diagnostics.HasError() {
		return
	}
	networkValidateConfigFromP6bZ2(ctx, req, resp)
	for _, at := range networkIPv4PoolsWithIPv4Disabled(v4, pools) {
		resp.Diagnostics.AddAttributeError(path.Root("ipam_config").AtListIndex(at).AtName("subnet"), "IPv4 pool on a network with IPv4 disabled",
			fmt.Sprintf("ipam_config[%d].subnet %s is an IPv4 subnet, but enable_ipv4 is false. The Engine drops IPv4 pools when IPv4 is "+
				"disabled, so the network would not have this pool. Remove the pool or set enable_ipv4 to true.", at, pools[at].Subnet.ValueString()))
	}
}

// networkIPv4PoolsWithIPv4Disabled returns the indexes of the pools whose
// known subnet is IPv4 when enable_ipv4 is known to be false.
func networkIPv4PoolsWithIPv4Disabled(v4 types.Bool, pools []networkIPAMConfigModel) []int {
	if v4.IsNull() || v4.IsUnknown() || v4.ValueBool() {
		return nil
	}
	var out []int
	for i, p := range pools {
		if p.Subnet.IsNull() || p.Subnet.IsUnknown() {
			continue
		}
		if pfx, err := netip.ParsePrefix(p.Subnet.ValueString()); err == nil && pfx.Addr().Unmap().Is4() {
			out = append(out, i)
		}
	}
	return out
}
