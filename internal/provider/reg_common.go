package provider

// Shared helpers of the Lane A registry types (WP reg).

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// regStringOrNull is a string value, null when s is empty.
func regStringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// regPlatformString renders os/arch[/variant].
func regPlatformString(os, arch, variant string) string {
	s := os + "/" + arch
	if variant != "" {
		s += "/" + variant
	}
	return s
}

// regOCIPlatforms renders platforms as os/arch[/variant], keeping order.
func regOCIPlatforms(ps []ocispec.Platform) types.List {
	vals := make([]attr.Value, 0, len(ps))
	for _, p := range ps {
		vals = append(vals, types.StringValue(regPlatformString(p.OS, p.Architecture, p.Variant)))
	}
	return types.ListValueMust(types.StringType, vals)
}

// regStringList is a list(string) value.
func regStringList(ss []string) types.List {
	vals := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		vals = append(vals, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, vals)
}

// regRefs returns the normalized repo:tag (image_ref) and repo@digest
// (image_digest_ref) of name; digest may be empty.
func regRefs(name, digest string) (imageRef, digestRef string) {
	p, err := parseImageRef(name)
	if err != nil {
		return name, ""
	}
	repo := p.Registry + "/" + p.Repository
	if p.Tag != "" {
		imageRef = repo + ":" + p.Tag
	} else {
		imageRef = repo + "@" + p.Digest
	}
	if digest == "" {
		digest = p.Digest
	}
	if digest != "" {
		digestRef = repo + "@" + digest
	}
	return imageRef, digestRef
}

// regMarshal is json.Marshal without HTML escaping and without the
// trailing newline.
func regMarshal(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}

// regSortedKeys returns the keys of m, sorted.
func regSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
