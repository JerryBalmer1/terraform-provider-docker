package provider

// Ownership label (Pass 5.3 Lane 0). The provider attribute `owner` names the
// Terraform configuration that manages the objects this provider creates.
// When it is set:
//
//   - every object the provider creates carries the label
//     io.terraform.provider.owner=<owner> (ownerInject);
//   - Create fails with a clear error when an object with the same name
//     already exists and carries a different owner label (ownerCreateCheck);
//   - importing (or otherwise taking over) an object whose owner label is
//     different needs `adopt = true` on the resource (ownerRecord in Read,
//     ownerPlanCheck in ModifyPlan).
//
// When `owner` is unset nothing is labelled and nothing is checked, exactly
// as before Pass 5.3.
//
// How a lane wires a resource type:
//
//  1. Schema: add "adopt": ownerAdoptAttribute() and an Adopt types.Bool
//     `tfsdk:"adopt"` model field.
//  2. Create: before the create call, look the name up; if it exists, return
//     ownerCreateCheck(pd, "<kind>", name, existingLabels) as an error. Then
//     pass labels through pd.ownerInject(labels).
//  3. Read: ownerRecord(ctx, resp.Private, liveLabels) and strip the label
//     from the user-facing labels attribute with ownerStrip (otherwise every
//     plan shows the injected label as drift).
//  4. ModifyPlan (update / after import): resp.Diagnostics.Append(
//     ownerPlanCheck(ctx, pd, req.Private, "<kind>", name, plan.Adopt)...).
//  5. Update paths that rewrite labels (service, secret/config labels,
//     swarm spec) also go through pd.ownerInject.
//  6. Register the type in ownerLabelledTypes (below) with its lane, so
//     Lane Z's coverage test creates one of each and checks label + conflict.
//
// An action that creates labellable objects (ownerInjectOnlyActions) only
// does step 2's pd.ownerInject: no lookup, no conflict check, no adopt.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ownerLabelKey is the label every created object carries when `owner` is set.
const ownerLabelKey = "io.terraform.provider.owner"

// ownerPrivateKey is the private-state key Read records the live owner
// label under, for ModifyPlan to check.
const ownerPrivateKey = "owner_label"

// The owner-label coverage lists. Every registered resource and action is in
// exactly one of ownerLabelledTypes, ownerInjectOnlyActions or
// ownerLabelExclusions (TestOwnerLabelCoverage), so a new type cannot
// silently miss the label. The value is the Pass 5.3 lane that wires it (or,
// for exclusions, the reason).

// ownerLabelledTypes are the resources with the full treatment: inject,
// conflict check on create, adopt on import. Lane Z's acceptance test
// creates one object of each and checks the label and the conflict error.
var ownerLabelledTypes = map[string]string{
	"docker_container":      "A", // container labels
	"docker_network":        "A", // network labels
	"docker_volume":         "A", // volume labels
	"docker_image":          "B", // build labels (a pulled image cannot be labelled)
	"docker_buildx_builder": "B", // the builder container's labels
	"docker_secret":         "S", // secret spec labels
	"docker_config":         "S", // config spec labels
	"docker_service":        "S", // service spec labels
	"docker_stack":          "S", // labels on the stack's services, networks, secrets and configs
	"docker_swarm_init":     "S", // swarm spec labels
	"docker_swarm_node":     "S", // node spec labels of the joined node: two states managing one node collide here
	"docker_compose":        "K", // labels on the project's containers, networks and volumes
}

// ownerInjectOnlyActions are the actions that create labellable objects:
// they add the owner label (pd.ownerInject) when `owner` is set and nothing
// otherwise, with no conflict check. An action owns nothing in state, so
// there is nothing for it to collide with; the label is for forensics only.
var ownerInjectOnlyActions = map[string]string{
	"docker_container_run":  "A", // the run container's labels
	"docker_image_commit":   "R", // the committed image's config labels
	"docker_image_import":   "R", // the imported image's labels (a LABEL change)
	"docker_compose_run":    "K", // the one-off container's labels
	"docker_buildx_bake":    "B", // every bake-produced image, through the build's labels
	"docker_compose_build":  "Z", // the built service images (compose build labels path, compose_local_apply.go)
	"docker_compose_commit": "Z", // the committed image (a LABEL change, like docker_image_commit)
	"docker_compose_scale":  "Z", // the containers it adds (composeLocalWire; no conflict check)
}

// ownerLabelExclusions are the types that create or manage Docker objects
// but deliberately carry no owner label, with the reason (also in
// reference/CONTRACT.md, owner-label section).
var ownerLabelExclusions = map[string]string{
	"docker_image_load":             "loads images from a tar exactly as saved; the Engine API has no way to add labels on load",
	"docker_node":                   "manages an existing node's spec and creates nothing; the node was created by its join",
	"docker_plugin":                 "plugins have no labels in the Engine API",
	"docker_plugin_create":          "plugins have no labels in the Engine API",
	"docker_tag":                    "a tag is a name on an existing image, not an object with labels",
	"docker_registry_image":         "pushes to a registry; registry manifests carry no Docker labels",
	"docker_registry_manifest_list": "a registry manifest list carries no Docker labels",
}

// ownerInject returns labels plus the owner label when `owner` is set. The
// input map is not modified; nil stays nil when there is nothing to add.
func (pd *providerData) ownerInject(labels map[string]string) map[string]string {
	if pd == nil || pd.Owner == "" {
		return labels
	}
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out[ownerLabelKey] = pd.Owner
	return out
}

// ownerStrip returns labels without the owner label, for user-facing label
// attributes. The input map is not modified.
func ownerStrip(labels map[string]string) map[string]string {
	if _, ok := labels[ownerLabelKey]; !ok {
		return labels
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		if k != ownerLabelKey {
			out[k] = v
		}
	}
	return out
}

// ownerOf returns the owner label of an object ("" when absent).
func ownerOf(labels map[string]string) string { return labels[ownerLabelKey] }

// ownerCreateCheck is called before creating an object named name when an
// object of that name already exists with existingLabels. It fails when
// `owner` is set and the existing object carries a different, non-empty
// owner label. An existing object without the label is not a conflict here:
// Docker's own create semantics apply to it, as before Pass 5.3.
func ownerCreateCheck(pd *providerData, kind, name string, existingLabels map[string]string) error {
	if pd == nil || pd.Owner == "" {
		return nil
	}
	other := ownerOf(existingLabels)
	if other == "" || other == pd.Owner {
		return nil
	}
	return fmt.Errorf("%s %q already exists and is owned by %q (label %s), not by this provider's owner %q. "+
		"Remove it, rename the new %s, or import it with adopt = true to take it over", kind, name, other, ownerLabelKey,
		pd.Owner, kind)
}

// privateSetter is the part of the framework's private state handle Read
// writes to (resp.Private).
type privateSetter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// privateGetter is the part of the private state handle ModifyPlan reads
// (req.Private).
type privateGetter interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

// ownerRecord stores the live owner label in private state (Read).
func ownerRecord(ctx context.Context, priv privateSetter, liveLabels map[string]string) diag.Diagnostics {
	if priv == nil {
		return nil
	}
	b, _ := json.Marshal(ownerOf(liveLabels))
	return priv.SetKey(ctx, ownerPrivateKey, b)
}

// ownerPlanCheck fails the plan of an existing object whose recorded owner
// label differs from `owner`, unless adopt is true. With `owner` unset, or
// no recorded label (the object predates Pass 5.3 or carries none), it does
// nothing.
func ownerPlanCheck(ctx context.Context, pd *providerData, priv privateGetter, kind, name string, adopt types.Bool) diag.Diagnostics {
	var diags diag.Diagnostics
	if pd == nil || pd.Owner == "" || priv == nil {
		return diags
	}
	b, d := priv.GetKey(ctx, ownerPrivateKey)
	diags.Append(d...)
	if len(b) == 0 {
		return diags
	}
	var live string
	if err := json.Unmarshal(b, &live); err != nil || live == "" || live == pd.Owner {
		return diags
	}
	if adopt.ValueBool() {
		return diags
	}
	diags.AddError("Object owned by another configuration",
		fmt.Sprintf("%s %q carries %s=%q, but this provider's owner is %q. Set adopt = true on the resource to manage it "+
			"anyway (labels that Docker lets the provider update are rewritten to %q).", kind, name, ownerLabelKey, live,
			pd.Owner, pd.Owner))
	return diags
}

// ownerAdoptAttribute is the `adopt` argument every labelled resource takes.
func ownerAdoptAttribute() schema.BoolAttribute {
	return schema.BoolAttribute{
		Optional: true,
		MarkdownDescription: "Take over an object whose `" + ownerLabelKey + "` label names a different owner than the provider's " +
			"`owner` argument (for example after `terraform import`). Without it, planning such an object fails. Only " +
			"checked when the provider sets `owner`. Default `false`.",
	}
}

// ownerNoObjectActions are the actions that create no labellable Docker
// object: they act on existing objects, remove objects, or write outside the
// daemon (a tar file, a registry). Listed so TestOwnerLabelCoverage can
// require every registered action to be classified.
var ownerNoObjectActions = map[string]string{}

func init() {
	for reason, names := range map[string][]string{
		"acts on an existing container or exec session; creates nothing": {
			"docker_container_cp", "docker_container_export", "docker_container_kill", "docker_container_pause",
			"docker_container_restart", "docker_container_start", "docker_container_stop", "docker_container_unpause",
			"docker_container_wait", "docker_exec", "docker_compose_cp", "docker_compose_exec", "docker_compose_export",
			"docker_compose_kill", "docker_compose_pause", "docker_compose_restart", "docker_compose_start",
			"docker_compose_stop", "docker_compose_unpause", "docker_compose_wait"},
		"removes objects; creates nothing": {
			"docker_builder_prune", "docker_container_prune", "docker_image_prune", "docker_network_prune",
			"docker_system_prune", "docker_volume_prune"},
		"updates an existing swarm, node or service; creates nothing": {
			"docker_node_activate", "docker_node_demote", "docker_node_drain", "docker_node_promote",
			"docker_service_force_update", "docker_service_rollback", "docker_service_scale", "docker_swarm_rotate_ca",
			"docker_swarm_rotate_tokens", "docker_swarm_rotate_unlock_key", "docker_swarm_unlock"},
		"writes outside the daemon (a tar file or a registry) or pulls an image, which cannot be labelled": {
			"docker_image_save", "docker_plugin_push", "docker_compose_publish", "docker_compose_pull",
			"docker_compose_push"},
	} {
		for _, n := range names {
			ownerNoObjectActions[n] = reason
		}
	}
}
