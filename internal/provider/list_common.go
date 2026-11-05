package provider

// Shared machinery for the pass 3 list resources that back `terraform query`.
// Each list resource carries the type name of a managed resource (docker_container,
// docker_secret, ...). Terraform pairs the two: results carry the managed
// resource's identity ({id}) and, when the query asks for it, the full resource
// object. The object is produced by the managed resource's own ImportState and
// Read, exactly as `terraform import` would, so list results never drift from
// what the resource itself reports.

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/list"
	listschema "github.com/hashicorp/terraform-plugin-framework/list/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// listIDIdentitySchema is the identity schema every listed managed resource
// uses: a single `id`, the same value as the resource's `id` attribute.
// Managed resources return it from IdentitySchema (resource.ResourceWithIdentity).
func listIDIdentitySchema() identityschema.Schema {
	return identityschema.Schema{
		Attributes: map[string]identityschema.Attribute{
			"id": identityschema.StringAttribute{
				RequiredForImport: true,
				Description:       "The object's ID (for docker_volume, its name), the same value as the resource's id attribute.",
			},
		},
	}
}

// listSetIDIdentity writes id into identity when the framework supplied one
// (it does whenever the resource has an identity schema).
func listSetIDIdentity(ctx context.Context, identity *tfsdk.ResourceIdentity, id string) diag.Diagnostics {
	if identity == nil {
		return nil
	}
	return identity.SetAttribute(ctx, path.Root("id"), id)
}

// listIDFromImport returns the import ID, or the identity's id when the import
// came from an `import { identity = { id = ... } }` block.
func listIDFromImport(ctx context.Context, req resource.ImportStateRequest, diags *diag.Diagnostics) string {
	if req.ID != "" || req.Identity == nil {
		return req.ID
	}
	var id types.String
	diags.Append(req.Identity.GetAttribute(ctx, path.Root("id"), &id)...)
	return id.ValueString()
}

type listFilterKind int

const (
	listFilterString listFilterKind = iota
	listFilterBool
	listFilterLabels
)

// listFilter is one config attribute of a list resource. Key is the Engine
// API filter it feeds ("" when fetch reads it from listQuery itself).
type listFilter struct {
	Attr        string
	Key         string
	Kind        listFilterKind
	Description string
	OneOf       []string
}

// listQuery is the decoded list config.
type listQuery struct {
	Filters filters.Args
	Strings map[string]string
	Bools   map[string]bool
}

// listItem is one object found by fetch. ImportID is what ImportState gets
// when the full resource object is requested.
type listItem struct {
	ID          string
	DisplayName string
	ImportID    string
}

// dockerListResource is the list resource implementation shared by every
// listed type; per-type behaviour is the filters and fetch.
type dockerListResource struct {
	typeSuffix  string
	description string
	filters     []listFilter
	newResource func() resource.Resource
	fetch       func(ctx context.Context, c *client.Client, q listQuery) ([]listItem, error)
	pd          *providerData
}

var (
	_ list.ListResource              = (*dockerListResource)(nil)
	_ list.ListResourceWithConfigure = (*dockerListResource)(nil)
)

func (l *dockerListResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + l.typeSuffix
}

func (l *dockerListResource) ListResourceConfigSchema(_ context.Context, _ list.ListResourceSchemaRequest, resp *list.ListResourceSchemaResponse) {
	attrs := map[string]listschema.Attribute{}
	for _, f := range l.filters {
		switch f.Kind {
		case listFilterString:
			a := listschema.StringAttribute{Optional: true, MarkdownDescription: f.Description}
			if len(f.OneOf) > 0 {
				a.Validators = []validator.String{stringvalidator.OneOf(f.OneOf...)}
			}
			attrs[f.Attr] = a
		case listFilterBool:
			attrs[f.Attr] = listschema.BoolAttribute{Optional: true, MarkdownDescription: f.Description}
		case listFilterLabels:
			attrs[f.Attr] = listschema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: f.Description}
		}
	}
	resp.Schema = listschema.Schema{MarkdownDescription: l.description, Attributes: attrs}
}

func (l *dockerListResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	l.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// query decodes the list config into Engine filters and raw values.
func (l *dockerListResource) query(ctx context.Context, cfg tfsdk.Config) (listQuery, diag.Diagnostics) {
	var diags diag.Diagnostics
	q := listQuery{Filters: filters.NewArgs(), Strings: map[string]string{}, Bools: map[string]bool{}}
	if cfg.Raw.IsNull() {
		return q, diags
	}
	for _, f := range l.filters {
		switch f.Kind {
		case listFilterString:
			var v types.String
			diags.Append(cfg.GetAttribute(ctx, path.Root(f.Attr), &v)...)
			if s := v.ValueString(); s != "" {
				q.Strings[f.Attr] = s
				if f.Key != "" {
					q.Filters.Add(f.Key, s)
				}
			}
		case listFilterBool:
			var v types.Bool
			diags.Append(cfg.GetAttribute(ctx, path.Root(f.Attr), &v)...)
			if !v.IsNull() && !v.IsUnknown() {
				q.Bools[f.Attr] = v.ValueBool()
				if f.Key != "" {
					q.Filters.Add(f.Key, strconv.FormatBool(v.ValueBool()))
				}
			}
		case listFilterLabels:
			var v types.Map
			diags.Append(cfg.GetAttribute(ctx, path.Root(f.Attr), &v)...)
			p3AddLabelFilters(q.Filters, f.Key, stringMapFrom(ctx, v, &diags))
		}
	}
	return q, diags
}

func (l *dockerListResource) List(ctx context.Context, req list.ListRequest, stream *list.ListResultsStream) {
	var diags diag.Diagnostics
	if l.pd == nil {
		diags.AddError("Provider not configured", "The docker provider was not configured; list resources need a configured provider.")
		stream.Results = list.ListResultsStreamDiagnostics(diags)
		return
	}
	if req.ResourceIdentitySchema == nil {
		diags.AddError("Managed resource has no identity",
			fmt.Sprintf("docker%s has no resource identity schema, so it cannot be listed. This is a provider bug: the managed resource "+
				"must implement resource.ResourceWithIdentity with listIDIdentitySchema().", l.typeSuffix))
		stream.Results = list.ListResultsStreamDiagnostics(diags)
		return
	}
	q, d := l.query(ctx, req.Config)
	diags.Append(d...)
	if diags.HasError() {
		stream.Results = list.ListResultsStreamDiagnostics(diags)
		return
	}
	items, err := l.fetch(ctx, l.pd.Client, q)
	if err != nil {
		diags.AddError("Cannot list docker"+l.typeSuffix, err.Error())
		stream.Results = list.ListResultsStreamDiagnostics(diags)
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].DisplayName != items[j].DisplayName {
			return items[i].DisplayName < items[j].DisplayName
		}
		return items[i].ID < items[j].ID
	})
	stream.Results = func(push func(list.ListResult) bool) {
		var n int64
		for _, it := range items {
			if req.Limit > 0 && n >= req.Limit {
				return
			}
			res := req.NewListResult(ctx)
			res.DisplayName = it.DisplayName
			res.Diagnostics.Append(listSetIDIdentity(ctx, res.Identity, it.ID)...)
			if req.IncludeResource {
				obj, gone, d := listReadResource(ctx, l.newResource(), l.pd, req, it.ImportID)
				if gone {
					continue // removed between list and inspect
				}
				res.Diagnostics.Append(d...)
				if obj != nil {
					res.Resource = obj
				}
			}
			n++
			if !push(res) {
				return
			}
		}
	}
}

// listReadResource builds the managed resource object for importID by running
// the resource's ImportState then Read, like `terraform import`. gone reports
// that Read removed the resource (the object vanished).
func listReadResource(ctx context.Context, r resource.Resource, pd *providerData, req list.ListRequest, importID string) (*tfsdk.Resource, bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	if rc, ok := r.(resource.ResourceWithConfigure); ok {
		var cr resource.ConfigureResponse
		rc.Configure(ctx, resource.ConfigureRequest{ProviderData: pd}, &cr)
		diags.Append(cr.Diagnostics...)
	}
	imp, ok := r.(resource.ResourceWithImportState)
	if !ok {
		diags.AddError("Resource cannot be imported", fmt.Sprintf("%T does not implement ImportState. This is a provider bug.", r))
		return nil, false, diags
	}
	if diags.HasError() {
		return nil, false, diags
	}
	schemaType := req.ResourceSchema.Type().TerraformType(ctx)
	var identity *tfsdk.ResourceIdentity
	if req.ResourceIdentitySchema != nil {
		identity = &tfsdk.ResourceIdentity{
			Schema: req.ResourceIdentitySchema,
			Raw:    tftypes.NewValue(req.ResourceIdentitySchema.Type().TerraformType(ctx), nil),
		}
	}
	iresp := resource.ImportStateResponse{
		State:    tfsdk.State{Schema: req.ResourceSchema, Raw: tftypes.NewValue(schemaType, nil)},
		Identity: identity,
	}
	listInitPrivate(&iresp)
	imp.ImportState(ctx, resource.ImportStateRequest{ID: importID}, &iresp)
	diags.Append(iresp.Diagnostics...)
	if diags.HasError() {
		return nil, false, diags
	}
	rreq := resource.ReadRequest{State: iresp.State, Identity: iresp.Identity}
	rresp := resource.ReadResponse{State: tfsdk.State{Schema: req.ResourceSchema, Raw: iresp.State.Raw.Copy()}}
	if iresp.Identity != nil {
		rresp.Identity = &tfsdk.ResourceIdentity{Schema: iresp.Identity.Schema, Raw: iresp.Identity.Raw.Copy()}
	}
	listInitPrivate(&rreq)
	listInitPrivate(&rresp)
	r.Read(ctx, rreq, &rresp)
	diags.Append(rresp.Diagnostics...)
	if diags.HasError() {
		return nil, false, diags
	}
	if rresp.State.Raw.IsNull() {
		return nil, true, diags
	}
	return &tfsdk.Resource{Schema: req.ResourceSchema, Raw: rresp.State.Raw}, false, diags
}

// listInitPrivate gives a request/response struct an empty private state.
// The framework's private-state type lives in an internal package, so it is
// allocated by reflection; resources such as docker_swarm_node write to
// resp.Private in ImportState and would otherwise fail on a nil value.
func listInitPrivate(v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	f := rv.Elem().FieldByName("Private")
	if f.IsValid() && f.Kind() == reflect.Pointer && f.IsNil() && f.CanSet() {
		f.Set(reflect.New(f.Type().Elem()))
	}
}

// listNameFilter and listLabelsFilter are the two filters every list resource offers.
func listNameFilter(what string) listFilter {
	return listFilter{Attr: "name", Key: "name", Kind: listFilterString,
		Description: "Only " + what + " whose name matches, using the Engine API `name` filter (substring or prefix match, as the daemon defines it)."}
}

func listLabelsFilter(what, key string) listFilter {
	return listFilter{Attr: "labels", Key: key, Kind: listFilterLabels,
		Description: "Only " + what + " carrying every one of these labels. An empty value matches any value of that label key."}
}

// p3AddLabelFilters adds one key=value filter per label, sorted; an empty
// value adds the bare key (label present with any value).
func p3AddLabelFilters(args filters.Args, key string, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m[k] == "" {
			args.Add(key, k)
		} else {
			args.Add(key, k+"="+m[k])
		}
	}
}

// listShortID shortens a 64-hex ID (with or without `sha256:`) to 12 characters.
func listShortID(id string) string {
	s := id
	if len(s) > 7 && s[:7] == "sha256:" {
		s = s[7:]
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
