package provider

// docker_compose and the provider's `owner` label (Pass 5.3 Lane K; the
// helpers are Lane 0's owner_label.go).
//
// Local mode labels the project's containers, networks and volumes; swarm
// mode its services, networks, secrets and configs. The label is added to
// the create (and service update) request only: it is not part of the
// compose service, so com.docker.compose.config-hash (ServiceHash of the
// service) and the provider's spec hash are unchanged, and the
// TestComposeHashCompat goldens do not move. Changing `owner` therefore
// does not recreate anything; objects created afterwards carry the new
// value.

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

type composeOwnerNoCheckKey struct{}

// composeOwnerNoCheck marks ctx as an action's: containers created under it
// get the owner label (composeLocalWire) but no conflict check, because an
// action owns nothing in state (docker_compose_scale; Pass 5.3 Lane Z).
func composeOwnerNoCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, composeOwnerNoCheckKey{}, true)
}

func composeOwnerChecks(ctx context.Context) bool {
	skip, _ := ctx.Value(composeOwnerNoCheckKey{}).(bool)
	return !skip
}

// composeOwnerCheckContainer fails when a container named name exists and
// carries another owner's label (only checked when `owner` is set).
func composeOwnerCheckContainer(ctx context.Context, pd *providerData, name string) error {
	if pd == nil || pd.Owner == "" {
		return nil
	}
	insp, err := pd.Client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspecting container %s: %w", name, err)
	}
	var labels map[string]string
	if insp.Container.Config != nil {
		labels = insp.Container.Config.Labels
	}
	return ownerCreateCheck(pd, "container", name, labels)
}

// composeOwnerCheckNetwork is composeOwnerCheckContainer for a network.
func composeOwnerCheckNetwork(ctx context.Context, pd *providerData, name string) error {
	if pd == nil || pd.Owner == "" {
		return nil
	}
	insp, err := pd.Client.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspecting network %s: %w", name, err)
	}
	return ownerCreateCheck(pd, "network", name, insp.Network.Labels)
}

// composeOwnerCheckVolume is composeOwnerCheckContainer for a volume (a
// volume create with an existing name succeeds silently, so this is the
// only place the conflict shows).
func composeOwnerCheckVolume(ctx context.Context, pd *providerData, name string) error {
	if pd == nil || pd.Owner == "" {
		return nil
	}
	insp, err := pd.Client.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspecting volume %s: %w", name, err)
	}
	return ownerCreateCheck(pd, "volume", name, insp.Volume.Labels)
}

// composeOwnerOfProject returns the owner labels found on the project's
// objects: the first one that differs from `owner` when there is one (that
// is what ModifyPlan must refuse), else the common value ("" when none).
func composeOwnerOfProject(pd *providerData, labelSets []map[string]string) string {
	found := ""
	for _, l := range labelSets {
		o := ownerOf(l)
		if o == "" {
			continue
		}
		if pd != nil && o != pd.Owner {
			return o
		}
		found = o
	}
	return found
}

// composeOwnerRecord stores the project's owner label in private state
// (Read), from the live objects of either mode.
func composeOwnerRecord(ctx context.Context, pd *providerData, priv privateSetter, labelSets []map[string]string) diag.Diagnostics {
	o := composeOwnerOfProject(pd, labelSets)
	return ownerRecord(ctx, priv, map[string]string{ownerLabelKey: o})
}

// composeOwnerCreateCheck fails a docker_compose Create when the project
// already has objects owned by another configuration.
func composeOwnerCreateCheck(pd *providerData, project string, labelSets []map[string]string) error {
	return ownerCreateCheck(pd, "compose project", project, map[string]string{ownerLabelKey: composeOwnerOfProject(pd, labelSets)})
}

// composeLocalLabelSets lists the labels of a local project's objects.
func composeLocalLabelSets(ex composeLocalExisting) []map[string]string {
	var out []map[string]string
	for _, n := range composeSortedKeys(ex.containers) {
		out = append(out, ex.containers[n].Labels)
	}
	for _, n := range composeSortedKeys(ex.networks) {
		out = append(out, ex.networks[n].Labels)
	}
	for _, n := range composeSortedKeys(ex.volumes) {
		out = append(out, ex.volumes[n].Labels)
	}
	return out
}

// composeSwarmLabelSets lists the labels of a stack's objects.
func composeSwarmLabelSets(ex composeSwarmByName) []map[string]string {
	var out []map[string]string
	for _, n := range composeSortedKeys(ex.services) {
		out = append(out, ex.services[n].Spec.Labels)
	}
	for _, n := range composeSortedKeys(ex.networks) {
		out = append(out, ex.networks[n].Labels)
	}
	for _, n := range composeSortedKeys(ex.secrets) {
		out = append(out, ex.secrets[n].Spec.Labels)
	}
	for _, n := range composeSortedKeys(ex.configs) {
		out = append(out, ex.configs[n].Spec.Labels)
	}
	return out
}

// composeSwarmOwnerSpec returns spec with the owner label on the service.
func composeSwarmOwnerSpec(pd *providerData, spec swarm.ServiceSpec) swarm.ServiceSpec {
	spec.Labels = pd.ownerInject(spec.Labels)
	return spec
}

// composeLocalPullRefresh pulls the images of kept containers whose
// pull_policy refresh period is over (compose pull.go:350-378 mustPull, run
// on every `up`) and reports the containers whose image changed, which
// compose recreates (convergence.go:337 imageUpdated).
func composeLocalPullRefresh(ctx context.Context, pd *providerData, plan *composeLocalPlan, ex composeLocalExisting, now time.Time) (map[string]bool, error) {
	out := map[string]bool{}
	pulled := map[string]string{} // image -> ID after the pull ("" = not due)
	for _, dc := range plan.Containers {
		got, ok := ex.containers[dc.Name]
		if !ok || !dc.PullRefresh || dc.Build != nil {
			continue
		}
		ref := dc.Config.Image
		id, done := pulled[ref]
		if !done {
			due, err := composeRefreshDue(ctx, pd, ref, dc.PullEvery, now)
			if err != nil {
				return nil, fmt.Errorf("inspecting image %s: %w", ref, err)
			}
			if due {
				if err := composePull(ctx, pd, ref, dc.Platform); err != nil {
					return nil, fmt.Errorf("pulling %s: %w", ref, err)
				}
				if insp, err := pd.Client.ImageInspect(ctx, ref); err == nil {
					id = insp.ID
				}
			}
			pulled[ref] = id
		}
		if id != "" && got.ImageID != "" && id != got.ImageID {
			out[dc.Name] = true
		}
	}
	return out, nil
}

// composeLocalPullDrift reports kept containers whose pull_policy refresh
// period is over, so refresh plans an update that pulls (and recreates
// them when the image changed).
func composeLocalPullDrift(ctx context.Context, pd *providerData, plan *composeLocalPlan, ex composeLocalExisting, now time.Time) []string {
	var out []string
	seen := map[string]bool{}
	for _, dc := range plan.Containers {
		if _, ok := ex.containers[dc.Name]; !ok || !dc.PullRefresh || dc.Build != nil || seen[dc.Service] {
			continue
		}
		due, err := composeRefreshDue(ctx, pd, dc.Config.Image, dc.PullEvery, now)
		if err == nil && due {
			seen[dc.Service] = true
			out = append(out, fmt.Sprintf("service %s: pull_policy %s is due for image %s", dc.Service, dc.Pull, dc.Config.Image))
		}
	}
	return out
}
