package provider

// Pass 5 Lane A (WP obj): small helpers shared by the network, volume,
// plugin and system data sources and the prune actions.

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

// objDSBase is the configure half every WP obj data source shares.
type objDSBase struct {
	pd *providerData
}

func (d *objDSBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// objReady reports whether the provider is configured, adding an error when not.
func objReady(pd *providerData, diags *diag.Diagnostics) bool {
	if pd == nil || pd.Client == nil {
		diags.AddError("Provider not configured", "The docker provider was not configured.")
		return false
	}
	return true
}

// objJSON marshals an Engine API response for a `*_json` raw output. The
// moby types carry the upstream JSON names, so the result is the daemon's
// response re-encoded (fields the client does not model are not kept), in
// the canonical form of rCanonicalJSON (deterministic: keys sorted).
func objJSON(v any, diags *diag.Diagnostics) []byte {
	b, err := rCanonicalJSON(v)
	if err != nil {
		diags.AddError("Cannot encode Engine API response", err.Error())
		return nil
	}
	return b
}

// objAddFilter adds key=value to args when v is set.
func objAddFilter(args client.Filters, key string, v types.String) {
	if s, ok := knownString(v); ok && s != "" {
		args.Add(key, s)
	}
}

// objAddBoolFilter adds key=true/false to args when v is set.
func objAddBoolFilter(args client.Filters, key string, v types.Bool) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	if v.ValueBool() {
		args.Add(key, "true")
	} else {
		args.Add(key, "false")
	}
}

// objSortedList returns a list value of ss sorted.
func objSortedList(ss []string) types.List {
	out := append([]string{}, ss...)
	sort.Strings(out)
	return stringListValue(out, false)
}
