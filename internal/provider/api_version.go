package provider

// Engine API version policy: the min_api_version floor, the negotiated
// version on providerData, and the apiSince schema annotation with its gate.
//
// The annotation lives in the description text, not in a side table:
// apiSince("1.52") returns the standalone sentence " Needs Engine API >= 1.52."
// that a schema appends to an attribute's, block's or whole type's
// MarkdownDescription. That one string is the single source of truth:
//   - tfplugindocs publishes it as part of the description;
//   - gateAPISince finds it by walking the request's schema at plan/read time;
//   - tools/genskill (which imports this package and already reads every
//     description) gets the version back with APISinceVersion.
// No registry has to be kept in sync with the schemas.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
)

// DefaultMinAPIVersion is the default min_api_version: the oldest Engine API
// the provider supports (Docker Engine 25.0).
const DefaultMinAPIVersion = "1.44"

// apiVersionPattern is the "1.NN" form of min_api_version and apiSince.
var apiVersionPattern = regexp.MustCompile(`^1\.[0-9]{1,3}$`)

// APISincePrefix starts the sentence apiSince appends to a description.
const APISincePrefix = "Needs Engine API >= "

var apiSincePattern = regexp.MustCompile(`Needs Engine API >= (1\.[0-9]{1,3})\.`)

// apiSince marks an attribute, block or whole type as needing a daemon that
// speaks at least Engine API version v (a "1.NN" string; anything else
// panics at schema build time, which every unit test exercises). It returns
// the standalone sentence to append to the MarkdownDescription:
//
//	MarkdownDescription: "Foo bar." + apiSince("1.52"),
//
// The type's ModifyPlan (resources), Read (data sources), ModifyPlan or
// Invoke (actions), Open (ephemeral resources) or List (list resources) then
// calls pd.gateAPISince, which fails when the config sets an annotated value
// and the daemon speaks an older version.
func apiSince(v string) string {
	if !apiVersionPattern.MatchString(v) {
		panic(fmt.Sprintf("apiSince(%q): want a version of the form 1.NN", v))
	}
	return " " + APISincePrefix + v + "."
}

// APISinceVersion returns the Engine API version an apiSince sentence in
// description names, or "" when it has none. tools/genskill uses it to show
// the requirement in the generated reference tables.
func APISinceVersion(description string) string {
	if m := apiSincePattern.FindStringSubmatch(description); m != nil {
		return m[1]
	}
	return ""
}

// ---------------------------------------------------------------------------
// Negotiated version and floor

// apiFloorError is a daemon (or pinned api_version) below min_api_version.
type apiFloorError struct {
	Host    string
	Version string
	Floor   string
}

func (e *apiFloorError) Error() string {
	return fmt.Sprintf("the Docker daemon at %s speaks Engine API %s, below min_api_version %s. "+
		"Upgrade the daemon, or lower min_api_version in the provider block (features that need a newer API then fail with their own error)",
		e.Host, e.Version, e.Floor)
}

// checkAPIFloor returns an *apiFloorError when version is below floor
// ("" floor means no check).
func checkAPIFloor(host, version, floor string) error {
	if floor == "" || version == "" || !versions.LessThan(version, floor) {
		return nil
	}
	return &apiFloorError{Host: host, Version: version, Floor: floor}
}

// apiFloorHook is a client request hook that refuses every versioned request
// (/v1.NN/...) below floor. The client negotiates on its first request, so
// this enforces min_api_version at the first daemon call even when
// Configure skipped the ping (disable_docker_daemon_check, unreachable
// daemon at plan time). /_ping is unversioned and passes.
func apiFloorHook(host, floor string) client.RequestHook {
	return func(req *http.Request) error {
		p := strings.TrimPrefix(req.URL.Path, "/")
		if !strings.HasPrefix(p, "v") {
			return nil
		}
		v, _, _ := strings.Cut(p[1:], "/")
		if !apiVersionPattern.MatchString(v) {
			return nil
		}
		return checkAPIFloor(host, v, floor)
	}
}

// APIVersion returns the Engine API version the provider's client speaks to
// the daemon: the version negotiated with it (the lower of the daemon's and
// client.MaxAPIVersion, 1.56), or the pinned api_version. The first call
// negotiates (GET /_ping) unless an earlier request already did; the result
// is cached. It fails when the daemon cannot be reached or speaks a version
// below min_api_version (an *apiFloorError).
//
// Resources, data sources, actions, ephemeral and list resources reach it
// through the *providerData they get in Configure.
func (pd *providerData) APIVersion(ctx context.Context) (string, error) {
	pd.apiMu.Lock()
	defer pd.apiMu.Unlock()
	if pd.apiVersion != "" {
		return pd.apiVersion, nil
	}
	if _, err := pd.Client.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return "", fmt.Errorf("negotiating the Engine API version with %s: %w", pd.Endpoint.Host, err)
	}
	v := pd.Client.ClientVersion()
	if err := checkAPIFloor(pd.Endpoint.Host, v, pd.MinAPIVersion); err != nil {
		return "", err
	}
	pd.apiVersion = v
	return v, nil
}

// APIVersionAtLeast reports whether the negotiated version is at least want.
func (pd *providerData) APIVersionAtLeast(ctx context.Context, want string) (bool, string, error) {
	v, err := pd.APIVersion(ctx)
	if err != nil {
		return false, "", err
	}
	return !versions.LessThan(v, want), v, nil
}

// validateMinAPIVersion checks a min_api_version value: the 1.NN form, and
// not above the newest version this provider's client speaks.
func validateMinAPIVersion(v string) error {
	if !apiVersionPattern.MatchString(v) {
		return fmt.Errorf("%q is not an Engine API version of the form 1.NN, e.g. %s", v, DefaultMinAPIVersion)
	}
	if versions.GreaterThan(v, client.MaxAPIVersion) {
		return fmt.Errorf("%s is above Engine API %s, the newest version this provider speaks, so no daemon could satisfy it", v, client.MaxAPIVersion)
	}
	return nil
}

// ---------------------------------------------------------------------------
// apiSince gate

// apiUse is one annotated part of a config that is set.
type apiUse struct {
	Path    path.Path // empty for a whole-type annotation
	Version string
}

// gateAPISince fails when cfg sets an attribute or block annotated with
// apiSince (or uses a type annotated as a whole) and the daemon speaks an
// older Engine API. typeName (e.g. "docker_container") names the type in the
// whole-type error. It is a no-op on a nil providerData (before Configure),
// a null config (destroy) and when nothing annotated is set; it only
// contacts the daemon when something annotated is set. A daemon that cannot
// be reached becomes a warning: the operation itself will report it.
//
//	func (r *fooResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
//		r.pd.gateAPISince(ctx, "docker_foo", req.Config, &resp.Diagnostics)
//	}
func (pd *providerData) gateAPISince(ctx context.Context, typeName string, cfg tfsdk.Config, diags *diag.Diagnostics) {
	if pd == nil || cfg.Schema == nil || cfg.Raw.IsNull() || !cfg.Raw.IsKnown() {
		return
	}
	uses := apiSinceUses(cfg)
	if len(uses) == 0 {
		return
	}
	v, err := pd.APIVersion(ctx)
	if err != nil {
		var floor *apiFloorError
		if errors.As(err, &floor) {
			diags.AddError("Docker daemon too old", err.Error())
			return
		}
		diags.AddWarning("Cannot check Engine API requirements",
			fmt.Sprintf("%s uses features that need a newer Engine API than min_api_version, but the daemon version is unknown: %v", typeName, err))
		return
	}
	for _, u := range uses {
		if !versions.LessThan(v, u.Version) {
			continue
		}
		if len(u.Path.Steps()) == 0 {
			diags.AddError("Engine API version too old",
				fmt.Sprintf("%s needs Engine API >= %s, daemon speaks %s.", typeName, u.Version, v))
			continue
		}
		diags.AddAttributeError(u.Path, "Engine API version too old",
			fmt.Sprintf("%s needs Engine API >= %s, daemon speaks %s.", u.Path, u.Version, v))
	}
}

// apiSinceUses walks cfg's schema and value and returns every annotated
// attribute or block the config sets (non-null, known; a block with at least
// one element), plus the whole type when its schema is annotated. Ordered
// by path for stable diagnostics.
func apiSinceUses(cfg tfsdk.Config) []apiUse {
	var out []apiUse
	if v := APISinceVersion(schemaDescription(cfg.Schema)); v != "" {
		out = append(out, apiUse{Path: path.Empty(), Version: v})
	}
	walkAPISince(cfg.Schema, cfg.Raw, path.Empty(), &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path.String() < out[j].Path.String() })
	return out
}

// The framework's schema interfaces live in an internal package, so the
// walk reaches GetAttributes, GetBlocks, GetNestedObject and the
// description getters through reflection. Every resource, data source,
// action, ephemeral and list schema type implements them.

func schemaDescription(node any) string {
	if d := callString(node, "GetMarkdownDescription"); d != "" {
		return d
	}
	return callString(node, "GetDescription")
}

func callString(node any, method string) string {
	m := reflect.ValueOf(node).MethodByName(method)
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 || m.Type().Out(0).Kind() != reflect.String {
		return ""
	}
	return m.Call(nil)[0].String()
}

// callMap calls a no-argument method returning map[string]X.
func callMap(node any, method string) map[string]any {
	m := reflect.ValueOf(node).MethodByName(method)
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 || m.Type().Out(0).Kind() != reflect.Map {
		return nil
	}
	res := m.Call(nil)[0]
	out := make(map[string]any, res.Len())
	iter := res.MapRange()
	for iter.Next() {
		out[iter.Key().String()] = iter.Value().Interface()
	}
	return out
}

// callAny calls a no-argument method returning one value.
func callAny(node any, method string) any {
	m := reflect.ValueOf(node).MethodByName(method)
	if !m.IsValid() || m.Type().NumIn() != 0 || m.Type().NumOut() != 1 {
		return nil
	}
	return m.Call(nil)[0].Interface()
}

// walkAPISince visits the attributes and blocks of one object node (a
// schema or a nested object) against its object value.
func walkAPISince(node any, val tftypes.Value, p path.Path, out *[]apiUse) {
	if val.IsNull() || !val.IsKnown() || !val.Type().Is(tftypes.Object{}) {
		return
	}
	var fields map[string]tftypes.Value
	if err := val.As(&fields); err != nil {
		return
	}
	children := callMap(node, "GetAttributes")
	for name, b := range callMap(node, "GetBlocks") {
		children[name] = b
	}
	for name, child := range children {
		fv, ok := fields[name]
		if !ok || fv.IsNull() || !fv.IsKnown() {
			continue
		}
		cp := p.AtName(name)
		if v := APISinceVersion(schemaDescription(child)); v != "" && valueIsSet(fv) {
			*out = append(*out, apiUse{Path: cp, Version: v})
		}
		if nested := callAny(child, "GetNestedObject"); nested != nil {
			walkNestedAPISince(nested, fv, cp, out)
		}
	}
}

// walkNestedAPISince descends into each element of a nested attribute or
// block value (single object, list, set or map of objects).
func walkNestedAPISince(nested any, val tftypes.Value, p path.Path, out *[]apiUse) {
	switch {
	case val.Type().Is(tftypes.Object{}):
		walkAPISince(nested, val, p, out)
	case val.Type().Is(tftypes.List{}), val.Type().Is(tftypes.Set{}):
		var elems []tftypes.Value
		if err := val.As(&elems); err != nil {
			return
		}
		isList := val.Type().Is(tftypes.List{})
		for i, e := range elems {
			ep := p
			if isList {
				ep = p.AtListIndex(i)
			}
			walkAPISince(nested, e, ep, out)
		}
	case val.Type().Is(tftypes.Map{}):
		var elems map[string]tftypes.Value
		if err := val.As(&elems); err != nil {
			return
		}
		for k, e := range elems {
			walkAPISince(nested, e, p.AtMapKey(k), out)
		}
	}
}

// valueIsSet reports whether a known, non-null value counts as "set": a
// list or set block with no elements does not.
func valueIsSet(v tftypes.Value) bool {
	if v.Type().Is(tftypes.List{}) || v.Type().Is(tftypes.Set{}) {
		var elems []tftypes.Value
		if err := v.As(&elems); err == nil {
			return len(elems) > 0 || !isBlockLike(v)
		}
	}
	return true
}

// isBlockLike: a list/set of objects is how blocks arrive; an empty one
// means "not written". A list of primitives set to [] is still set.
func isBlockLike(v tftypes.Value) bool {
	switch t := v.Type().(type) {
	case tftypes.List:
		return t.ElementType.Is(tftypes.Object{})
	case tftypes.Set:
		return t.ElementType.Is(tftypes.Object{})
	}
	return false
}

// ---------------------------------------------------------------------------
// Item-level gate
//
// Which gate to use:
//   - pd.gateItemAPI(ctx, itemID, diags): the whole type needs a newer
//     Engine API than the provider floor (the endpoint itself appeared in
//     that version). The minimum comes from the generated contract table
//     (inventory min_api), so new values flow in by regenerating
//     contract_gen.go; nothing is written in the lane file. Call it at the
//     start of Read (data sources), Create/Read (resources), Invoke
//     (actions), Open (ephemeral) or List (list resources).
//   - apiSince("1.NN") in a description + pd.gateAPISince: one attribute or
//     block needs a newer API than the rest of the type (a field added to an
//     existing endpoint). The annotation lives in the schema description.

// gateItemAPI reports whether the daemon is new enough for itemID (an
// inventory id such as "engine:image-attestations"). It returns false after
// adding an error naming the type, the needed and the negotiated version
// when the daemon is older than the item's min_api. It is a no-op (true)
// on a nil providerData (validation, before Configure), for an item with
// no min_api, and when min_api is at or below min_api_version (the floor
// already guarantees it), so it only contacts the daemon when it must. A
// daemon that cannot be reached becomes a warning and true: the operation
// itself will report it. An unknown itemID is a provider bug: an error and
// false.
func (pd *providerData) gateItemAPI(ctx context.Context, itemID string, diags *diag.Diagnostics) bool {
	it, ok := contractLookup(itemID)
	if !ok {
		diags.AddError("Provider bug", fmt.Sprintf("gateItemAPI: unknown inventory item %q (regenerate contract_gen.go?)", itemID))
		return false
	}
	if pd == nil || it.MinAPI == "" {
		return true
	}
	if pd.MinAPIVersion != "" && !versions.LessThan(pd.MinAPIVersion, it.MinAPI) {
		return true
	}
	what := gateItemLabel(it)
	v, err := pd.APIVersion(ctx)
	if err != nil {
		var floor *apiFloorError
		if errors.As(err, &floor) {
			diags.AddError("Docker daemon too old", err.Error())
			return false
		}
		diags.AddWarning("Cannot check Engine API requirements",
			fmt.Sprintf("%s needs Engine API >= %s, but the daemon version is unknown: %v", what, it.MinAPI, err))
		return true
	}
	if versions.LessThan(v, it.MinAPI) {
		diags.AddError("Engine API version too old",
			fmt.Sprintf("%s needs Engine API >= %s; the daemon at %s negotiated %s. Upgrade the Docker Engine.", what, it.MinAPI, pd.Endpoint.Host, v))
		return false
	}
	return true
}

// gateItemLabel names an item for messages: "data source docker_x".
func gateItemLabel(it *contractItem) string {
	kind := strings.ReplaceAll(it.Kind, "_", " ")
	return kind + " " + it.Name
}
