package provider

// docker_volume cluster_volume_spec: Swarm cluster (CSI) volumes
// (VolumeCreate ClusterVolumeSpec, VolumeUpdate for availability).

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/volume"
)

var objCVMountAttrTypes = map[string]attr.Type{
	"fs_type":     types.StringType,
	"mount_flags": types.ListType{ElemType: types.StringType},
}

var objCVAccessAttrTypes = map[string]attr.Type{
	"scope":        types.StringType,
	"sharing":      types.StringType,
	"mount_volume": types.ObjectType{AttrTypes: objCVMountAttrTypes},
	"block_volume": types.BoolType,
}

var objCVTopologyType = types.ListType{ElemType: types.MapType{ElemType: types.StringType}}

var objCVAccessibilityAttrTypes = map[string]attr.Type{
	"requisite": objCVTopologyType,
	"preferred": objCVTopologyType,
}

var objCVCapacityAttrTypes = map[string]attr.Type{
	"required_bytes": types.Int64Type,
	"limit_bytes":    types.Int64Type,
}

var objCVSecretAttrTypes = map[string]attr.Type{
	"key":    types.StringType,
	"secret": types.StringType,
}

var objCVSpecAttrTypes = map[string]attr.Type{
	"group":                      types.StringType,
	"access_mode":                types.ObjectType{AttrTypes: objCVAccessAttrTypes},
	"accessibility_requirements": types.ObjectType{AttrTypes: objCVAccessibilityAttrTypes},
	"capacity_range":             types.ObjectType{AttrTypes: objCVCapacityAttrTypes},
	"secrets":                    types.ListType{ElemType: types.ObjectType{AttrTypes: objCVSecretAttrTypes}},
	"availability":               types.StringType,
}

// objClusterVolumeSpecAttribute is the optional cluster_volume_spec
// argument of docker_volume.
func objClusterVolumeSpecAttribute() schema.SingleNestedAttribute {
	topo := func(desc string) schema.ListAttribute {
		return schema.ListAttribute{Optional: true, ElementType: types.MapType{ElemType: types.StringType}, MarkdownDescription: desc}
	}
	return schema.SingleNestedAttribute{
		Optional: true,
		MarkdownDescription: "Swarm cluster (CSI) volume specification (`ClusterVolumeSpec`, API 1.42+); setting it creates a cluster volume " +
			"through the Swarm manager instead of a local volume, so the provider must point at a manager and `driver` must name an installed CSI plugin. " +
			"Changing anything except `availability` forces replacement.",
		PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplaceIf(objCVRequiresReplace,
			"Changing anything except availability forces replacement.", "Changing anything except `availability` forces replacement.")},
		Attributes: map[string]schema.Attribute{
			"group": schema.StringAttribute{Optional: true, MarkdownDescription: "Volume group; services can request any volume of a group (`group:<name>`). Forces replacement."},
			"access_mode": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "How the volume may be used by tasks (`AccessMode`). Forces replacement.",
				Attributes: map[string]schema.Attribute{
					"scope": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.OneOf("single", "multi")},
						MarkdownDescription: "`single` (one node at a time) or `multi` (several nodes). Forces replacement."},
					"sharing": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.OneOf("none", "readonly", "onewriter", "all")},
						MarkdownDescription: "Sharing between tasks: `none`, `readonly`, `onewriter` or `all`. Forces replacement."},
					"mount_volume": schema.SingleNestedAttribute{
						Optional:            true,
						MarkdownDescription: "Use the volume as a mounted filesystem (`MountVolume`). Forces replacement.",
						Attributes: map[string]schema.Attribute{
							"fs_type":     schema.StringAttribute{Optional: true, MarkdownDescription: "Filesystem type, e.g. `ext4`. Forces replacement."},
							"mount_flags": schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Mount flags. Forces replacement."},
						},
					},
					"block_volume": schema.BoolAttribute{Optional: true, MarkdownDescription: "Use the volume as a raw block device (`BlockVolume`) instead of a mount. Forces replacement."},
				},
			},
			"accessibility_requirements": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Topology constraints on where the volume is created (`AccessibilityRequirements`). Forces replacement.",
				Attributes: map[string]schema.Attribute{
					"requisite": topo("Topologies (segment maps) the volume must be accessible from. Forces replacement."),
					"preferred": topo("Topologies (segment maps) preferred for the volume, in order. Forces replacement."),
				},
			},
			"capacity_range": schema.SingleNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Size bounds passed to the CSI plugin (`CapacityRange`). Forces replacement.",
				Attributes: map[string]schema.Attribute{
					"required_bytes": schema.Int64Attribute{Optional: true, MarkdownDescription: "Minimum size in bytes. Forces replacement."},
					"limit_bytes":    schema.Int64Attribute{Optional: true, MarkdownDescription: "Maximum size in bytes. Forces replacement."},
				},
			},
			"secrets": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Swarm secrets passed to the CSI plugin. Forces replacement.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"key":    schema.StringAttribute{Required: true, MarkdownDescription: "Key the plugin receives the secret under. Forces replacement."},
					"secret": schema.StringAttribute{Required: true, MarkdownDescription: "Name or ID of the Swarm secret. Forces replacement."},
				}},
			},
			"availability": schema.StringAttribute{
				Optional: true, Computed: true,
				Validators:          []validator.String{stringvalidator.OneOf("active", "pause", "drain")},
				MarkdownDescription: "Scheduling availability: `active`, `pause` or `drain` (default `active`). Updated in place with `VolumeUpdate`.",
			},
		},
	}
}

// objCVRequiresReplace replaces the volume when the spec changes other
// than availability (VolumeUpdate only changes availability).
func objCVRequiresReplace(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() != req.PlanValue.IsNull() {
		resp.RequiresReplace = true
		return
	}
	if req.StateValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	s, p := req.StateValue.Attributes(), req.PlanValue.Attributes()
	for k := range objCVSpecAttrTypes {
		if k == "availability" {
			continue
		}
		if !s[k].Equal(p[k]) {
			resp.RequiresReplace = true
			return
		}
	}
}

func objCVStr(v attr.Value) string {
	if s, ok := v.(types.String); ok {
		return s.ValueString()
	}
	return ""
}

func objCVTopologies(ctx context.Context, v attr.Value, diags *diag.Diagnostics) []volume.Topology {
	l, ok := v.(types.List)
	if !ok || l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []volume.Topology
	for _, e := range l.Elements() {
		m, _ := e.(types.Map)
		out = append(out, volume.Topology{Segments: stringMapFrom(ctx, m, diags)})
	}
	return out
}

// objExpandClusterVolumeSpec turns the cluster_volume_spec object into the
// Engine spec. A null object gives nil.
func objExpandClusterVolumeSpec(ctx context.Context, o types.Object, diags *diag.Diagnostics) *volume.ClusterVolumeSpec {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	a := o.Attributes()
	spec := &volume.ClusterVolumeSpec{
		Group:        objCVStr(a["group"]),
		Availability: volume.Availability(objCVStr(a["availability"])),
	}
	if spec.Availability == "" {
		spec.Availability = volume.AvailabilityActive
	}
	if am, ok := a["access_mode"].(types.Object); ok && !am.IsNull() && !am.IsUnknown() {
		x := am.Attributes()
		mode := &volume.AccessMode{Scope: volume.Scope(objCVStr(x["scope"])), Sharing: volume.SharingMode(objCVStr(x["sharing"]))}
		if b, ok := x["block_volume"].(types.Bool); ok && b.ValueBool() {
			mode.BlockVolume = &volume.TypeBlock{}
		}
		if mv, ok := x["mount_volume"].(types.Object); ok && !mv.IsNull() && !mv.IsUnknown() {
			y := mv.Attributes()
			tm := &volume.TypeMount{FsType: objCVStr(y["fs_type"])}
			if fl, ok := y["mount_flags"].(types.List); ok {
				tm.MountFlags = stringListFrom(ctx, fl, diags)
			}
			mode.MountVolume = tm
		}
		if mode.BlockVolume == nil && mode.MountVolume == nil {
			mode.MountVolume = &volume.TypeMount{}
		}
		spec.AccessMode = mode
	}
	if ar, ok := a["accessibility_requirements"].(types.Object); ok && !ar.IsNull() && !ar.IsUnknown() {
		x := ar.Attributes()
		spec.AccessibilityRequirements = &volume.TopologyRequirement{
			Requisite: objCVTopologies(ctx, x["requisite"], diags),
			Preferred: objCVTopologies(ctx, x["preferred"], diags),
		}
	}
	if cr, ok := a["capacity_range"].(types.Object); ok && !cr.IsNull() && !cr.IsUnknown() {
		x := cr.Attributes()
		rb, _ := x["required_bytes"].(types.Int64)
		lb, _ := x["limit_bytes"].(types.Int64)
		spec.CapacityRange = &volume.CapacityRange{RequiredBytes: rb.ValueInt64(), LimitBytes: lb.ValueInt64()}
	}
	if sl, ok := a["secrets"].(types.List); ok && !sl.IsNull() && !sl.IsUnknown() {
		for _, e := range sl.Elements() {
			x := e.(types.Object).Attributes()
			spec.Secrets = append(spec.Secrets, volume.Secret{Key: objCVStr(x["key"]), Secret: objCVStr(x["secret"])})
		}
	}
	return spec
}

func objCVTopologyValue(in []volume.Topology) types.List {
	if len(in) == 0 {
		return types.ListNull(types.MapType{ElemType: types.StringType})
	}
	vals := make([]attr.Value, 0, len(in))
	for _, t := range in {
		vals = append(vals, stringMapValue(t.Segments, false))
	}
	return types.ListValueMust(types.MapType{ElemType: types.StringType}, vals)
}

func objCVStrOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// objFlattenClusterVolumeSpec maps an Engine spec onto the attribute
// (used for import, where there is no prior configuration).
func objFlattenClusterVolumeSpec(s volume.ClusterVolumeSpec) types.Object {
	access := types.ObjectNull(objCVAccessAttrTypes)
	if am := s.AccessMode; am != nil {
		mv := types.ObjectNull(objCVMountAttrTypes)
		if am.MountVolume != nil && (am.MountVolume.FsType != "" || len(am.MountVolume.MountFlags) > 0) {
			flags := types.ListNull(types.StringType)
			if len(am.MountVolume.MountFlags) > 0 {
				fv := make([]attr.Value, 0, len(am.MountVolume.MountFlags))
				for _, f := range am.MountVolume.MountFlags {
					fv = append(fv, types.StringValue(f))
				}
				flags = types.ListValueMust(types.StringType, fv)
			}
			mv = types.ObjectValueMust(objCVMountAttrTypes, map[string]attr.Value{"fs_type": objCVStrOrNull(am.MountVolume.FsType), "mount_flags": flags})
		}
		block := types.BoolNull()
		if am.BlockVolume != nil {
			block = types.BoolValue(true)
		}
		access = types.ObjectValueMust(objCVAccessAttrTypes, map[string]attr.Value{
			"scope": objCVStrOrNull(string(am.Scope)), "sharing": objCVStrOrNull(string(am.Sharing)),
			"mount_volume": mv, "block_volume": block,
		})
	}
	acc := types.ObjectNull(objCVAccessibilityAttrTypes)
	if ar := s.AccessibilityRequirements; ar != nil {
		acc = types.ObjectValueMust(objCVAccessibilityAttrTypes, map[string]attr.Value{
			"requisite": objCVTopologyValue(ar.Requisite), "preferred": objCVTopologyValue(ar.Preferred),
		})
	}
	capr := types.ObjectNull(objCVCapacityAttrTypes)
	if cr := s.CapacityRange; cr != nil {
		i64 := func(n int64) types.Int64 {
			if n == 0 {
				return types.Int64Null()
			}
			return types.Int64Value(n)
		}
		capr = types.ObjectValueMust(objCVCapacityAttrTypes, map[string]attr.Value{"required_bytes": i64(cr.RequiredBytes), "limit_bytes": i64(cr.LimitBytes)})
	}
	secType := types.ObjectType{AttrTypes: objCVSecretAttrTypes}
	secrets := types.ListNull(secType)
	if len(s.Secrets) > 0 {
		sec := append([]volume.Secret(nil), s.Secrets...)
		sort.SliceStable(sec, func(i, j int) bool { return sec[i].Key < sec[j].Key })
		vals := make([]attr.Value, 0, len(sec))
		for _, x := range sec {
			vals = append(vals, types.ObjectValueMust(objCVSecretAttrTypes, map[string]attr.Value{"key": types.StringValue(x.Key), "secret": types.StringValue(x.Secret)}))
		}
		secrets = types.ListValueMust(secType, vals)
	}
	avail := string(s.Availability)
	if avail == "" {
		avail = string(volume.AvailabilityActive)
	}
	return types.ObjectValueMust(objCVSpecAttrTypes, map[string]attr.Value{
		"group": objCVStrOrNull(s.Group), "access_mode": access, "accessibility_requirements": acc,
		"capacity_range": capr, "secrets": secrets, "availability": types.StringValue(avail),
	})
}

// objClusterVolumeState refreshes cluster_volume_spec and
// cluster_volume_id from an inspected volume. A configured spec keeps the
// configured fields and takes availability from the daemon (the only field
// that can drift in place); with no prior spec (import) the whole spec is
// read back.
func objClusterVolumeState(prior types.Object, cv *volume.ClusterVolume) (types.Object, types.String) {
	if cv == nil {
		if prior.IsUnknown() || prior.IsNull() {
			return types.ObjectNull(objCVSpecAttrTypes), types.StringNull()
		}
		return prior, types.StringNull()
	}
	id := types.StringValue(cv.ID)
	if prior.IsNull() || prior.IsUnknown() {
		return objFlattenClusterVolumeSpec(cv.Spec), id
	}
	attrs := map[string]attr.Value{}
	for k, v := range prior.Attributes() {
		attrs[k] = v
	}
	avail := string(cv.Spec.Availability)
	if avail == "" {
		avail = string(volume.AvailabilityActive)
	}
	attrs["availability"] = types.StringValue(avail)
	return types.ObjectValueMust(objCVSpecAttrTypes, attrs), id
}
