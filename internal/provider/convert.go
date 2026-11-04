package provider

// Conversions between framework types and Go values, shared by every lane.

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// stringMapFrom converts a map(string) to a Go map. Null and unknown give nil.
func stringMapFrom(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out
}

// stringMapValue converts a Go map to map(string). An empty or nil map gives
// null when nullIfEmpty, so an unset optional attribute stays unset.
func stringMapValue(in map[string]string, nullIfEmpty bool) types.Map {
	if len(in) == 0 && nullIfEmpty {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]attr.Value, len(in))
	for k, v := range in {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

// stringListFrom converts a list(string) to a slice. Null and unknown give nil.
func stringListFrom(ctx context.Context, l types.List, diags *diag.Diagnostics) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

// stringListValue converts a slice to list(string); nil/empty gives null
// when nullIfEmpty.
func stringListValue(in []string, nullIfEmpty bool) types.List {
	if len(in) == 0 && nullIfEmpty {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, len(in))
	for i, v := range in {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
}

// stringSetFrom converts a set(string) to a sorted slice.
func stringSetFrom(ctx context.Context, s types.Set, diags *diag.Diagnostics) []string {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(s.ElementsAs(ctx, &out, false)...)
	sort.Strings(out)
	return out
}

// stringSetValue converts a slice to set(string); nil/empty gives null when
// nullIfEmpty.
func stringSetValue(in []string, nullIfEmpty bool) types.Set {
	if len(in) == 0 && nullIfEmpty {
		return types.SetNull(types.StringType)
	}
	elems := make([]attr.Value, len(in))
	for i, v := range in {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

// stringOrNull returns null for "".
func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// knownString returns the value and true when v is known and not null.
func knownString(v types.String) (string, bool) {
	if v.IsNull() || v.IsUnknown() {
		return "", false
	}
	return v.ValueString(), true
}

func knownInt64(v types.Int64) (int64, bool) {
	if v.IsNull() || v.IsUnknown() {
		return 0, false
	}
	return v.ValueInt64(), true
}

func knownBool(v types.Bool) (bool, bool) {
	if v.IsNull() || v.IsUnknown() {
		return false, false
	}
	return v.ValueBool(), true
}
