package provider

// docker_system_prune and docker_image_prune.

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ action.ActionWithConfigure      = (*systemPruneAction)(nil)
	_ action.ActionWithValidateConfig = (*systemPruneAction)(nil)
	_ action.ActionWithConfigure      = (*imagePruneAction)(nil)
	_ action.ActionWithValidateConfig = (*imagePruneAction)(nil)
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

// actPruneImages runs ImagesPrune with the dangling switch and filters.
func actPruneImages(ctx context.Context, a *actBase, dangling bool, f map[string]string) (int, uint64, error) {
	args := actPruneFilters(f, "label", "label!", "until")
	if !dangling {
		args.Add("dangling", "false")
	}
	rep, err := a.pd.Client.ImagesPrune(ctx, args)
	if err != nil {
		return 0, 0, err
	}
	return len(rep.ImagesDeleted), rep.SpaceReclaimed, nil
}

// ---------------------------------------------------------------------------
// docker_system_prune

// NewSystemPruneAction returns the docker_system_prune action.
func NewSystemPruneAction() action.Action { return &systemPruneAction{} }

type systemPruneAction struct{ actBase }

type systemPruneActionModel struct {
	Containers types.Bool `tfsdk:"containers"`
	Images     types.Bool `tfsdk:"images"`
	AllImages  types.Bool `tfsdk:"all_images"`
	Volumes    types.Bool `tfsdk:"volumes"`
	Networks   types.Bool `tfsdk:"networks"`
	BuildCache types.Bool `tfsdk:"build_cache"`
	Filters    types.Map  `tfsdk:"filters"`
}

func (a *systemPruneAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_prune"
}

func (a *systemPruneAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Removes unused Docker objects, like `docker system prune`, and reports the space reclaimed. " +
			"By default it removes stopped containers, unused networks, dangling images and build cache; volumes only when `volumes = true`. " +
			"It acts on every object on the daemon that matches, not only on objects Terraform manages, so narrow it with `filters`.",
		Attributes: map[string]schema.Attribute{
			"containers":  schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove stopped containers. Default `true`."},
			"images":      schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove dangling images. Default `true`."},
			"all_images":  schema.BoolAttribute{Optional: true, MarkdownDescription: "With `images`, remove every image no container uses, not only dangling ones, and all build cache (`docker system prune -a`). Default `false`."},
			"volumes":     schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove anonymous volumes no container uses. Default `false`."},
			"networks":    schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove networks no container uses. Default `true`."},
			"build_cache": schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove build cache. Default `true`. The daemon ignores label filters for build cache, so `label`/`label!` filters require `build_cache = false`."},
			"filters":     schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: actPruneFiltersDoc},
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
	fail := func(what string, err error) {
		resp.Diagnostics.AddError("docker_system_prune failed", fmt.Sprintf("pruning %s: %v (so far: %s)", what, err, tot.String()))
	}
	if actBool(m.Containers, true) {
		rep, err := c.ContainersPrune(ctx, actPruneFilters(f, "label", "label!", "until"))
		if err != nil {
			fail("containers", err)
			return
		}
		tot.add("containers", len(rep.ContainersDeleted), rep.SpaceReclaimed)
		actProgress(ctx, resp, "pruned %d containers (%s)", len(rep.ContainersDeleted), actHumanSize(rep.SpaceReclaimed))
	}
	if actBool(m.Networks, true) {
		rep, err := c.NetworksPrune(ctx, actPruneFilters(f, "label", "label!", "until"))
		if err != nil {
			fail("networks", err)
			return
		}
		tot.add("networks", len(rep.NetworksDeleted), 0)
		actProgress(ctx, resp, "pruned %d networks", len(rep.NetworksDeleted))
	}
	if actBool(m.Volumes, false) {
		rep, err := c.VolumesPrune(ctx, actPruneFilters(f, "label", "label!"))
		if err != nil {
			fail("volumes", err)
			return
		}
		tot.add("volumes", len(rep.VolumesDeleted), rep.SpaceReclaimed)
		actProgress(ctx, resp, "pruned %d volumes (%s)", len(rep.VolumesDeleted), actHumanSize(rep.SpaceReclaimed))
	}
	all := actBool(m.AllImages, false)
	if actBool(m.Images, true) {
		n, sz, err := actPruneImages(ctx, &a.actBase, !all, f)
		if err != nil {
			fail("images", err)
			return
		}
		tot.add("images", n, sz)
		actProgress(ctx, resp, "pruned %d images (%s)", n, actHumanSize(sz))
	}
	if actBool(m.BuildCache, true) {
		args := filters.NewArgs()
		if v, ok := f["until"]; ok {
			args.Add("until", v)
		}
		rep, err := c.BuildCachePrune(ctx, build.CachePruneOptions{All: all, Filters: args})
		if err != nil {
			fail("build cache", err)
			return
		}
		tot.add("build cache entries", len(rep.CachesDeleted), rep.SpaceReclaimed)
		actProgress(ctx, resp, "pruned %d build cache entries (%s)", len(rep.CachesDeleted), actHumanSize(rep.SpaceReclaimed))
	}
	actProgress(ctx, resp, "docker_system_prune: %s", tot.String())
}

// ---------------------------------------------------------------------------
// docker_image_prune

// NewImagePruneAction returns the docker_image_prune action.
func NewImagePruneAction() action.Action { return &imagePruneAction{} }

type imagePruneAction struct{ actBase }

type imagePruneActionModel struct {
	Dangling types.Bool `tfsdk:"dangling"`
	Filters  types.Map  `tfsdk:"filters"`
}

func (a *imagePruneAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_prune"
}

func (a *imagePruneAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Removes unused images, like `docker image prune`, and reports the space reclaimed. " +
			"By default only dangling (untagged) images go; with `dangling = false` every image no container uses is removed (`docker image prune -a`).",
		Attributes: map[string]schema.Attribute{
			"dangling": schema.BoolAttribute{Optional: true, MarkdownDescription: "Remove only dangling images. Default `true`; `false` removes all unused images."},
			"filters":  schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: actPruneFiltersDoc},
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
	if resp.Diagnostics.HasError() {
		return
	}
	n, sz, err := actPruneImages(ctx, &a.actBase, actBool(m.Dangling, true), f)
	if err != nil {
		resp.Diagnostics.AddError("docker_image_prune failed", err.Error())
		return
	}
	actProgress(ctx, resp, "docker_image_prune: removed %d images; reclaimed %s", n, actHumanSize(sz))
}
