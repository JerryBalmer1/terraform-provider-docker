package provider

// Pass 5 Lane A (WP img): action docker_image_commit (POST /commit).

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ action.ActionWithConfigure      = (*imgCommitAction)(nil)
	_ action.ActionWithValidateConfig = (*imgCommitAction)(nil)
)

// NewImageCommitAction returns the docker_image_commit action.
func NewImageCommitAction() action.Action { return &imgCommitAction{} }

type imgCommitAction struct{ actBase }

type imgCommitModel struct {
	ContainerID types.String `tfsdk:"container_id"`
	Reference   types.String `tfsdk:"reference"`
	Author      types.String `tfsdk:"author"`
	Message     types.String `tfsdk:"message"`
	Changes     types.List   `tfsdk:"changes"`
	Pause       types.Bool   `tfsdk:"pause"`
	ResultPath  types.String `tfsdk:"result_path"`
}

func (a *imgCommitAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_commit"
}

func (a *imgCommitAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates an image from a container's filesystem changes, like `docker container commit` (`POST /commit`). " +
			"The new image is not tracked in state. The result object holds `id`, `image_ref`, `container_id` and `result.changes` " +
			"(the filesystem changes that went into the image, from `GET /containers/{id}/changes` just before the commit).",
		Attributes: map[string]schema.Attribute{
			"container_id": schema.StringAttribute{Required: true, MarkdownDescription: "The container (ID or name) to commit."},
			"reference": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Reference to tag the new image with, `repository[:tag]` (no tag means `latest`). Without it the image is untagged.",
			},
			"author":  schema.StringAttribute{Optional: true, MarkdownDescription: "Author recorded in the image (`--author`)."},
			"message": schema.StringAttribute{Optional: true, MarkdownDescription: "Commit message recorded in the image history (`--message`)."},
			"changes": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Dockerfile instructions applied to the image config, e.g. `[\"ENV A=b\", \"EXPOSE 80\"]` (`--change`).",
			},
			"pause":       schema.BoolAttribute{Optional: true, MarkdownDescription: "Pause the container during the commit (`--pause`). Default `true`."},
			"result_path": actResultPathAttribute(),
		},
	}
}

func (a *imgCommitAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m imgCommitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r, ok := knownString(m.Reference); ok && r != "" {
		if _, err := tagNormalizeTarget(r); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("reference"), "Invalid reference", err.Error())
		}
	}
}

func (a *imgCommitAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m imgCommitModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	changes := stringListFrom(ctx, m.Changes, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctr := m.ContainerID.ValueString()
	insp, err := c.ContainerInspect(ctx, ctr, client.ContainerInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("container_id"), "docker_image_commit: cannot find container", err.Error())
		return
	}
	containerID := insp.Container.ID
	var fsChanges []map[string]any
	if diff, err := c.ContainerDiff(ctx, containerID, client.ContainerDiffOptions{}); err == nil {
		for _, ch := range diff.Changes {
			fsChanges = append(fsChanges, map[string]any{"path": ch.Path, "kind": int64(ch.Kind)})
		}
		sort.SliceStable(fsChanges, func(i, j int) bool { return fsChanges[i]["path"].(string) < fsChanges[j]["path"].(string) })
	} else {
		resp.Diagnostics.AddWarning("docker_image_commit: cannot list container changes", err.Error())
	}
	ref := m.Reference.ValueString()
	actProgress(ctx, resp, "committing container %s", ctr)
	res, err := c.ContainerCommit(ctx, containerID, client.ContainerCommitOptions{
		Reference: ref,
		Comment:   m.Message.ValueString(),
		Author:    m.Author.ValueString(),
		Changes:   changes,
		NoPause:   !actBool(m.Pause, true),
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_image_commit failed", err.Error())
		return
	}
	actProgress(ctx, resp, "committed %s as %s", ctr, res.ID)
	if fsChanges == nil {
		fsChanges = []map[string]any{}
	}
	values := map[string]any{
		"id":           res.ID,
		"container_id": containerID,
		"result":       map[string]any{"changes": fsChanges},
	}
	if ref != "" {
		values["image_ref"] = imgNormalizedRef(ref)
	}
	actFinishResult(ctx, resp, a.pd, "engine:image-commit", values, m.ResultPath)
}
