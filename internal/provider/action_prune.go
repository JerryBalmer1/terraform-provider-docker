package provider

// The prune actions: docker_system_prune, docker_image_prune,
// docker_container_prune, docker_network_prune, docker_volume_prune and
// docker_builder_prune. Each one reports its outcome as one `result` object
// (DECISIONS Q35) through actFinishResult, optionally written to result_path.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*systemPruneAction)(nil)
	_ action.ActionWithValidateConfig = (*systemPruneAction)(nil)
	_ action.ActionWithConfigure      = (*imagePruneAction)(nil)
	_ action.ActionWithValidateConfig = (*imagePruneAction)(nil)
	_ action.ActionWithConfigure      = (*objObjectPruneAction)(nil)
	_ action.ActionWithValidateConfig = (*objObjectPruneAction)(nil)
	_ action.ActionWithConfigure      = (*objBuilderPruneAction)(nil)
)

const actPruneFiltersDoc = "Prune filters, one value per key: `until` (e.g. `24h` or a timestamp; not applied to volumes), " +
	"`label` (`key` or `key=value`) and `label!` (the negation)."

// actPruneTotals accumulates what a prune removed.
type actPruneTotals struct {
	reclaimed uint64
	parts     []string
}

func (t *actPruneTotals) add(what string, n int, reclaimed uint64) {
	t.reclaimed += reclaimed
	t.parts = append(t.parts, fmt.Sprintf("%d %s", n, what))
}

func (t *actPruneTotals) String() string {
	return fmt.Sprintf("removed %s; reclaimed %s", strings.Join(t.parts, ", "), actHumanSize(t.reclaimed))
}

// actPruneImages runs ImagePrune with the dangling switch and filters.
func actPruneImages(ctx context.Context, a *actBase, dangling bool, f map[string]string) (image.PruneReport, error) {
	args := actPruneFilters(f, "label", "label!", "until")
	if !dangling {
		args.Add("dangling", "false")
	}
	res, err := a.pd.Client.ImagePrune(ctx, client.ImagePruneOptions{Filters: args})
	if err != nil {
		return image.PruneReport{}, err
	}
	return res.Report, nil
}

// objImageDeletedIDs returns the Deleted IDs of an image prune report.
func objImageDeletedIDs(rep image.PruneReport) []string {
	out := []string{}
	for _, d := range rep.ImagesDeleted {
		if d.Deleted != "" {
			out = append(out, d.Deleted)
		}
	}
	return out
}

// objNonNil returns ss, or an empty slice for nil (a result list is never null).
func objNonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

// ---------------------------------------------------------------------------
// docker_system_prune

// NewSystemPruneAction returns the docker_system_prune action.
func NewSystemPruneAction() action.Action { return &systemPruneAction{} }

type systemPruneAction struct{ actBase }

type systemPruneActionModel struct {
	Containers types.Bool   `tfsdk:"containers"`
	Images     types.Bool   `tfsdk:"images"`
	All        types.Bool   `tfsdk:"all"`
	Volumes    types.Bool   `tfsdk:"volumes"`
	Networks   types.Bool   `tfsdk:"networks"`
	BuildCache types.Bool   `tfsdk:"build_cache"`
	Filters    types.Map    `tfsdk:"filters"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *systemPruneAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_prune"
}

func (a *systemPruneAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Removes unused Docker objects, like `docker system prune`, and reports what it removed and the space reclaimed. " +
			"By default it removes stopped containers, unused networks, dangling images and build cache; volumes only when `volumes = true`. " +
			"It acts on every object on the daemon that matches, not only on objects Terraform manages, so narrow it with `filters`. " +
			"The `result` object merges every prune response: `containers_deleted`, `networks_deleted`, `images_deleted`, " +
			"`caches_deleted`, `volumes_deleted` and the total `space_reclaimed`.",
		Attributes: map[string]schema.Attribute{
			"containers":  schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove stopped containers. Default `true`."},
			"images":      schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove dangling images. Default `true`."},
			"all":         schema.BoolAttribute{Optional: true, MarkdownDescription: "With `images`, remove every image no container uses, not only dangling ones, and all build cache (`docker system prune --all`). Default `false`."},
			"volumes":     schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove anonymous volumes no container uses. Default `false`."},
			"networks":    schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove networks no container uses. Default `true`."},
			"build_cache": schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove build cache. Default `true`. The daemon ignores label filters for build cache, so `label`/`label!` filters require `build_cache = false`."},
			"filters":     schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: actPruneFiltersDoc},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *systemPruneAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m systemPruneActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f := actValidatePruneFilters(ctx, m.Filters, &resp.Diagnostics)
	_, hasLabel := f["label"]
	_, hasNotLabel := f["label!"]
	if (hasLabel || hasNotLabel) && !m.BuildCache.IsUnknown() && actBool(m.BuildCache, true) {
		resp.Diagnostics.AddAttributeError(path.Root("build_cache"), "Label filters do not apply to build cache",
			"The daemon accepts but ignores label filters when pruning build cache, so all build cache would be removed. Set build_cache = false when filtering by label.")
	}
}

func (a *systemPruneAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m systemPruneActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	f := actValidatePruneFilters(ctx, m.Filters, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var tot actPruneTotals
	result := map[string]any{
		"ContainersDeleted": []string{}, "NetworksDeleted": []string{}, "ImagesDeleted": []image.DeleteResponse{},
		"CachesDeleted": []string{}, "VolumesDeleted": []string{},
	}
	fail := func(what string, err error) {
		resp.Diagnostics.AddError("docker_system_prune failed", fmt.Sprintf("pruning %s: %v (so far: %s)", what, err, tot.String()))
	}
	if actBool(m.Containers, true) {
		res, err := c.ContainerPrune(ctx, client.ContainerPruneOptions{Filters: actPruneFilters(f, "label", "label!", "until")})
		if err != nil {
			fail("containers", err)
			return
		}
		rep := res.Report
		result["ContainersDeleted"] = objNonNil(rep.ContainersDeleted)
		tot.add("containers", len(rep.ContainersDeleted), rep.SpaceReclaimed)
		actProgress(ctx, resp, "pruned %d containers (%s)", len(rep.ContainersDeleted), actHumanSize(rep.SpaceReclaimed))
	}
	if actBool(m.Networks, true) {
		res, err := c.NetworkPrune(ctx, client.NetworkPruneOptions{Filters: actPruneFilters(f, "label", "label!", "until")})
		if err != nil {
			fail("networks", err)
			return
		}
		rep := res.Report
		result["NetworksDeleted"] = objNonNil(rep.NetworksDeleted)
		tot.add("networks", len(rep.NetworksDeleted), 0)
		actProgress(ctx, resp, "pruned %d networks", len(rep.NetworksDeleted))
	}
	if actBool(m.Volumes, false) {
		res, err := c.VolumePrune(ctx, client.VolumePruneOptions{Filters: actPruneFilters(f, "label", "label!")})
		if err != nil {
			fail("volumes", err)
			return
		}
		rep := res.Report
		result["VolumesDeleted"] = objNonNil(rep.VolumesDeleted)
		tot.add("volumes", len(rep.VolumesDeleted), rep.SpaceReclaimed)
		actProgress(ctx, resp, "pruned %d volumes (%s)", len(rep.VolumesDeleted), actHumanSize(rep.SpaceReclaimed))
	}
	all := actBool(m.All, false)
	if actBool(m.Images, true) {
		rep, err := actPruneImages(ctx, &a.actBase, !all, f)
		if err != nil {
			fail("images", err)
			return
		}
		if rep.ImagesDeleted != nil {
			result["ImagesDeleted"] = rep.ImagesDeleted
		}
		tot.add("images", len(rep.ImagesDeleted), rep.SpaceReclaimed)
		actProgress(ctx, resp, "pruned %d images (%s)", len(rep.ImagesDeleted), actHumanSize(rep.SpaceReclaimed))
	}
	if actBool(m.BuildCache, true) {
		args := client.Filters{}
		if v, ok := f["until"]; ok {
			args.Add("until", v)
		}
		res, err := c.BuildCachePrune(ctx, client.BuildCachePruneOptions{All: all, Filters: args})
		if err != nil {
			fail("build cache", err)
			return
		}
		rep := res.Report
		result["CachesDeleted"] = objNonNil(rep.CachesDeleted)
		tot.add("build cache entries", len(rep.CachesDeleted), rep.SpaceReclaimed)
		actProgress(ctx, resp, "pruned %d build cache entries (%s)", len(rep.CachesDeleted), actHumanSize(rep.SpaceReclaimed))
	}
	actProgress(ctx, resp, "docker_system_prune: %s", tot.String())
	result["SpaceReclaimed"] = tot.reclaimed
	actFinishResult(ctx, resp, a.pd, "engine:system-prune", map[string]any{
		"result":             result,
		"containers_deleted": result["ContainersDeleted"],
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_image_prune

// NewImagePruneAction returns the docker_image_prune action.
func NewImagePruneAction() action.Action { return &imagePruneAction{} }

type imagePruneAction struct{ actBase }

type imagePruneActionModel struct {
	Dangling   types.Bool   `tfsdk:"dangling"`
	Filters    types.Map    `tfsdk:"filters"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *imagePruneAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_prune"
}

func (a *imagePruneAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Removes unused images, like `docker image prune`, and reports what it removed and the space reclaimed. " +
			"By default only dangling (untagged) images go; with `dangling = false` every image no container uses is removed (`docker image prune -a`). " +
			"The `result` object is the `POST /images/prune` response: `images_deleted` and `space_reclaimed`.",
		Attributes: map[string]schema.Attribute{
			"dangling":    schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove only dangling images. Default `true`; `false` removes all unused images."},
			"filters":     schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: actPruneFiltersDoc},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *imagePruneAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m imagePruneActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actValidatePruneFilters(ctx, m.Filters, &resp.Diagnostics)
}

func (a *imagePruneAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m imagePruneActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	f := actValidatePruneFilters(ctx, m.Filters, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || !a.pd.gateItemAPI(ctx, "engine:image-prune", &resp.Diagnostics) {
		return
	}
	rep, err := actPruneImages(ctx, &a.actBase, actBool(m.Dangling, true), f)
	if err != nil {
		resp.Diagnostics.AddError("docker_image_prune failed", err.Error())
		return
	}
	if rep.ImagesDeleted == nil {
		rep.ImagesDeleted = []image.DeleteResponse{}
	}
	actProgress(ctx, resp, "docker_image_prune: removed %d images; reclaimed %s", len(rep.ImagesDeleted), actHumanSize(rep.SpaceReclaimed))
	actFinishResult(ctx, resp, a.pd, "engine:image-prune", map[string]any{
		"result":         rep,
		"images_deleted": objImageDeletedIDs(rep),
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_container_prune, docker_network_prune, docker_volume_prune: one
// implementation, parameterized by the object kind.

type objPruneKind struct {
	suffix   string // type name suffix
	itemID   string
	noun     string // plural noun for messages and the endpoint
	what     string // what the action removes
	cli      string
	deleted  string // identity output name
	filters  []string
	allowAll bool // volume prune `all`
}

var (
	objContainerPruneKind = objPruneKind{suffix: "_container_prune", itemID: "engine:container-prune", noun: "containers",
		what: "stopped containers", cli: "docker container prune", deleted: "containers_deleted", filters: []string{"label", "label!", "until"}}
	objNetworkPruneKind = objPruneKind{suffix: "_network_prune", itemID: "engine:network-prune", noun: "networks",
		what: "networks no container uses", cli: "docker network prune", deleted: "networks_deleted", filters: []string{"label", "label!", "until"}}
	objVolumePruneKind = objPruneKind{suffix: "_volume_prune", itemID: "engine:volume-prune", noun: "volumes",
		what: "volumes no container uses (anonymous ones only, unless `all = true`)", cli: "docker volume prune",
		deleted: "volumes_deleted", filters: []string{"label", "label!"}, allowAll: true}
)

// NewContainerPruneAction returns the docker_container_prune action.
func NewContainerPruneAction() action.Action {
	return &objObjectPruneAction{kind: objContainerPruneKind}
}

// NewNetworkPruneAction returns the docker_network_prune action.
func NewNetworkPruneAction() action.Action { return &objObjectPruneAction{kind: objNetworkPruneKind} }

// NewVolumePruneAction returns the docker_volume_prune action.
func NewVolumePruneAction() action.Action { return &objObjectPruneAction{kind: objVolumePruneKind} }

type objObjectPruneAction struct {
	actBase
	kind objPruneKind
}

type objObjectPruneModel struct {
	Filters    types.Map    `tfsdk:"filters"`
	All        types.Bool   `tfsdk:"all"`
	ResultPath types.String `tfsdk:"result_path"`
}

type objObjectPruneModelNoAll struct {
	Filters    types.Map    `tfsdk:"filters"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *objObjectPruneAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + a.kind.suffix
}

func (a *objObjectPruneAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	k := a.kind
	space := " and the space reclaimed"
	if k.noun == "networks" {
		space = ""
	}
	filtersDoc := "Prune filters, one value per key: `label` (`key` or `key=value`) and `label!` (the negation)."
	if len(k.filters) == 3 {
		filtersDoc = "Prune filters, one value per key: `until` (e.g. `24h` or a timestamp), `label` (`key` or `key=value`) and `label!` (the negation)."
	}
	attrs := map[string]schema.Attribute{
		"filters":     schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: filtersDoc},
		"result_path": actResultPathAttribute(),
	}
	if k.allowAll {
		attrs["all"] = schema.BoolAttribute{Optional: true,
			MarkdownDescription: "Remove every unused volume, named ones included (`docker volume prune --all`). Default `false`: only anonymous volumes." + apiSince("1.42")}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: fmt.Sprintf("Removes %s, like `%s`, and reports what it removed%s. ", k.what, k.cli, space) +
			"It acts on every matching object on the daemon, not only on objects Terraform manages, so narrow it with `filters`. " +
			fmt.Sprintf("The `result` object is the `POST /%s/prune` response; `%s` lists the removed objects.", k.noun, k.deleted),
		Attributes: attrs,
	}
}

// read decodes the config into the shared model (volume prune has `all`).
func (a *objObjectPruneAction) read(get func(any) bool) (objObjectPruneModel, bool) {
	if a.kind.allowAll {
		var m objObjectPruneModel
		return m, get(&m)
	}
	var m objObjectPruneModelNoAll
	ok := get(&m)
	return objObjectPruneModel{Filters: m.Filters, All: types.BoolNull(), ResultPath: m.ResultPath}, ok
}

func (a *objObjectPruneAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	m, ok := a.read(func(v any) bool {
		resp.Diagnostics.Append(req.Config.Get(ctx, v)...)
		return !resp.Diagnostics.HasError()
	})
	if !ok {
		return
	}
	objValidatePruneFilters(ctx, m.Filters, a.kind.filters, &resp.Diagnostics)
}

// objValidatePruneFilters is actValidatePruneFilters narrowed to the keys one
// prune endpoint accepts.
func objValidatePruneFilters(ctx context.Context, fm types.Map, allowed []string, diags *diag.Diagnostics) map[string]string {
	f := actValidatePruneFilters(ctx, fm, diags)
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	for k := range f {
		if !ok[k] {
			diags.AddAttributeError(path.Root("filters"), "Unsupported prune filter",
				fmt.Sprintf("Filter %q is not accepted here; use one of: %s.", k, strings.Join(allowed, ", ")))
		}
	}
	return f
}

func (a *objObjectPruneAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	m, ok := a.read(func(v any) bool {
		resp.Diagnostics.Append(req.Config.Get(ctx, v)...)
		return !resp.Diagnostics.HasError()
	})
	c := a.client(&resp.Diagnostics)
	if !ok || resp.Diagnostics.HasError() {
		return
	}
	k := a.kind
	f := objValidatePruneFilters(ctx, m.Filters, k.filters, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || !a.pd.gateItemAPI(ctx, k.itemID, &resp.Diagnostics) {
		return
	}
	a.pd.gateAPISince(ctx, "docker"+k.suffix, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	args := actPruneFilters(f, k.filters...)
	var (
		deleted   []string
		reclaimed uint64
		result    any
		err       error
	)
	switch k.noun {
	case "containers":
		var res client.ContainerPruneResult
		res, err = c.ContainerPrune(ctx, client.ContainerPruneOptions{Filters: args})
		deleted, reclaimed = objNonNil(res.Report.ContainersDeleted), res.Report.SpaceReclaimed
		res.Report.ContainersDeleted = deleted
		result = res.Report
	case "networks":
		var res client.NetworkPruneResult
		res, err = c.NetworkPrune(ctx, client.NetworkPruneOptions{Filters: args})
		deleted = objNonNil(res.Report.NetworksDeleted)
		res.Report.NetworksDeleted = deleted
		result = res.Report
	case "volumes":
		var res client.VolumePruneResult
		res, err = c.VolumePrune(ctx, client.VolumePruneOptions{All: m.All.ValueBool(), Filters: args})
		deleted, reclaimed = objNonNil(res.Report.VolumesDeleted), res.Report.SpaceReclaimed
		res.Report.VolumesDeleted = deleted
		result = res.Report
	}
	if err != nil {
		resp.Diagnostics.AddError("docker"+k.suffix+" failed", err.Error())
		return
	}
	if k.noun == "networks" {
		actProgress(ctx, resp, "docker%s: removed %d %s", k.suffix, len(deleted), k.noun)
	} else {
		actProgress(ctx, resp, "docker%s: removed %d %s; reclaimed %s", k.suffix, len(deleted), k.noun, actHumanSize(reclaimed))
	}
	actFinishResult(ctx, resp, a.pd, k.itemID, map[string]any{"result": result, k.deleted: deleted}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_builder_prune (Q2/Q23): the Engine build cache, or a buildx builder's.

// NewBuilderPruneAction returns the docker_builder_prune action.
func NewBuilderPruneAction() action.Action { return &objBuilderPruneAction{} }

type objBuilderPruneAction struct{ actBase }

type objBuilderPruneModel struct {
	Builder       types.String `tfsdk:"builder"`
	All           types.Bool   `tfsdk:"all"`
	Filters       types.Map    `tfsdk:"filters"`
	KeepStorage   types.Int64  `tfsdk:"keep_storage"`
	ReservedSpace types.Int64  `tfsdk:"reserved_space"`
	MaxUsedSpace  types.Int64  `tfsdk:"max_used_space"`
	MinFreeSpace  types.Int64  `tfsdk:"min_free_space"`
	ResultPath    types.String `tfsdk:"result_path"`
}

func (a *objBuilderPruneAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_builder_prune"
}

func (a *objBuilderPruneAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	bytes := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, MarkdownDescription: desc, Validators: []validator.Int64{int64validator.AtLeast(0)}}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Removes build cache, like `docker builder prune`, and reports the cache records removed and the space reclaimed. " +
			"Without `builder` it prunes the Engine's own build cache (`POST /build/prune`); with `builder` it prunes that buildx " +
			"builder's cache, like `docker buildx prune --builder`. Unlike destroying a `docker_buildx_builder`, it never removes a builder.",
		Attributes: map[string]schema.Attribute{
			"builder": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Name of a buildx builder whose cache to prune. Unset: the daemon's own build cache (BuildKit inside dockerd)."},
			"all": schema.BoolAttribute{Optional: true,
				MarkdownDescription: "Remove all build cache, not only unused (dangling) records (`--all`). Default `false`."},
			"filters": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Build cache filters, one value per key, e.g. `until` (`24h`), `id`, `parent`, `type`, `description`, " +
					"`inuse`, `shared` or `private`. With `builder`, `until` and `unused-for` take a Go duration (`24h`, `90m`) and other keys " +
					"match exactly (`key==value`), or as a substring when the value starts with `~` (`key~=value`)."},
			"keep_storage": bytes("Bytes of cache to keep (`--keep-storage`, deprecated by BuildKit). Only with `builder`; the Engine " +
				"endpoint takes `reserved_space` instead."),
			"reserved_space": bytes("Bytes of cache to keep at least (`--reserved-space`)." + apiSince("1.48")),
			"max_used_space": bytes("Remove cache until at most this many bytes are used (`--max-used-space`)." + apiSince("1.48")),
			"min_free_space": bytes("Remove cache until this many bytes of disk are free (`--min-free-space`)." + apiSince("1.48")),
			"result_path":    actResultPathAttribute(),
		},
	}
}

func (a *objBuilderPruneAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m objBuilderPruneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	filters := stringMapFrom(ctx, m.Filters, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var (
		deleted   []string
		reclaimed int64
	)
	if b, ok := knownString(m.Builder); ok && b != "" {
		fm := map[string][]string{}
		for k, v := range filters {
			if k == "until" || k == "unused-for" {
				if _, err := time.ParseDuration(v); err != nil {
					resp.Diagnostics.AddAttributeError(path.Root("filters").AtMapKey(k), "Invalid duration filter",
						fmt.Sprintf("With builder set, filters[%q] must be a Go duration such as 24h: %v", k, err))
					return
				}
			}
			fm[k] = []string{v}
		}
		res, err := seamBuildxPruneImpl.Prune(ctx, a.pd, seamBuildxPruneRequest{
			Builder: b, All: m.All.ValueBool(), Filters: fm,
			KeepStorage: m.KeepStorage.ValueInt64(), ReservedSpace: m.ReservedSpace.ValueInt64(),
			MaxUsedSpace: m.MaxUsedSpace.ValueInt64(), MinFreeSpace: m.MinFreeSpace.ValueInt64(),
		})
		if err != nil {
			resp.Diagnostics.AddError("docker_builder_prune failed", fmt.Sprintf("builder %q: %v", b, err))
			return
		}
		deleted, reclaimed = res.CachesDeleted, res.SpaceReclaimed
	} else {
		if !m.KeepStorage.IsNull() && !m.KeepStorage.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("keep_storage"), "keep_storage needs a builder",
				"The Engine build cache endpoint has no keep-storage setting; use reserved_space, or set builder.")
			return
		}
		a.pd.gateAPISince(ctx, "docker_builder_prune", req.Config, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		args := client.Filters{}
		for k, v := range filters {
			args.Add(k, v)
		}
		res, err := c.BuildCachePrune(ctx, client.BuildCachePruneOptions{
			All: m.All.ValueBool(), ReservedSpace: m.ReservedSpace.ValueInt64(),
			MaxUsedSpace: m.MaxUsedSpace.ValueInt64(), MinFreeSpace: m.MinFreeSpace.ValueInt64(), Filters: args,
		})
		if err != nil {
			resp.Diagnostics.AddError("docker_builder_prune failed", err.Error())
			return
		}
		deleted, reclaimed = res.Report.CachesDeleted, int64(res.Report.SpaceReclaimed)
	}
	deleted = objNonNil(deleted)
	actProgress(ctx, resp, "docker_builder_prune: removed %d build cache entries; reclaimed %s", len(deleted), actHumanSize(uint64(max(reclaimed, 0))))
	actFinishResult(ctx, resp, a.pd, "engine:build-prune", map[string]any{
		"result":         map[string]any{"CachesDeleted": deleted, "SpaceReclaimed": reclaimed},
		"caches_deleted": deleted,
	}, m.ResultPath)
}
