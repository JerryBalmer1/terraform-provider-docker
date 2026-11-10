package provider

// Pass 5.3 Lane S: owner-label wiring shared by the swarm workload types
// (docker_secret, docker_config, docker_service, docker_stack) and
// docker_swarm_init. owner_label.go has the how-to; these helpers only look
// up the existing object of a name before create and pick the label a
// stack's objects carry.

import (
	"context"
	"fmt"
	"reflect"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// swarmOwnerCreateCheck fails when `owner` is set and a swarm object of kind
// ("secret", "config" or "service") named name already exists with a
// different owner label. With `owner` unset it does not contact the daemon.
// The list filter matches names by prefix, so the name is compared exactly.
func swarmOwnerCreateCheck(ctx context.Context, pd *providerData, kind, name string) error {
	if pd == nil || pd.Owner == "" || pd.Client == nil {
		return nil
	}
	f := make(client.Filters)
	f.Add("name", name)
	var labels map[string]string
	found := false
	switch kind {
	case "secret":
		res, err := pd.Client.SecretList(ctx, client.SecretListOptions{Filters: f})
		if err != nil {
			return fmt.Errorf("looking up secret %q: %w", name, err)
		}
		for _, s := range res.Items {
			if s.Spec.Name == name {
				labels, found = s.Spec.Labels, true
			}
		}
	case "config":
		res, err := pd.Client.ConfigList(ctx, client.ConfigListOptions{Filters: f})
		if err != nil {
			return fmt.Errorf("looking up config %q: %w", name, err)
		}
		for _, s := range res.Items {
			if s.Spec.Name == name {
				labels, found = s.Spec.Labels, true
			}
		}
	case "service":
		res, err := pd.Client.ServiceList(ctx, client.ServiceListOptions{Filters: f})
		if err != nil {
			return fmt.Errorf("looking up service %q: %w", name, err)
		}
		for _, s := range res.Items {
			if s.Spec.Name == name {
				labels, found = s.Spec.Labels, true
			}
		}
	default:
		return fmt.Errorf("swarmOwnerCreateCheck: unknown kind %q", kind)
	}
	if !found {
		return nil
	}
	return ownerCreateCheck(pd, kind, name, labels)
}

// stackOwnerLabels returns the labels to record for a stack: the owner label
// of the first object (services, networks, secrets, configs in name order)
// that carries one different from owner, else owner's own label when any
// object carries it, else nil. It drives both the create check and the
// private-state record, so one foreign object is enough to need adopt.
func stackOwnerLabels(ex stackExisting, owner string) map[string]string {
	var all []map[string]string
	for _, k := range stackSortedKeys(ex.services) {
		all = append(all, ex.services[k].Spec.Labels)
	}
	for _, k := range stackSortedKeys(ex.networks) {
		all = append(all, ex.networks[k].Labels)
	}
	for _, k := range stackSortedKeys(ex.secrets) {
		all = append(all, ex.secrets[k].Spec.Labels)
	}
	for _, k := range stackSortedKeys(ex.configs) {
		all = append(all, ex.configs[k].Spec.Labels)
	}
	var mine map[string]string
	for _, l := range all {
		o := ownerOf(l)
		switch {
		case o == "":
		case o != owner:
			return map[string]string{ownerLabelKey: o}
		default:
			mine = map[string]string{ownerLabelKey: o}
		}
	}
	return mine
}

// swarmNodeOwnerCreateCheck runs before docker_swarm_node joins the daemon
// behind nodeClient: when that daemon is already an active member the
// manager knows (swarmJoinNode would adopt it) and its node carries another
// owner label, it returns ownerCreateCheck's error. With `owner` unset it
// does nothing.
func swarmNodeOwnerCreateCheck(ctx context.Context, pd *providerData, nodeClient *client.Client) error {
	if pd == nil || pd.Owner == "" || nodeClient == nil {
		return nil
	}
	infoRes, err := nodeClient.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("reading info from joining node: %w", err)
	}
	id := infoRes.Info.Swarm.NodeID
	if infoRes.Info.Swarm.LocalNodeState != swarm.LocalNodeStateActive || id == "" {
		return nil
	}
	n, err := pd.Client.NodeInspect(ctx, id, client.NodeInspectOptions{})
	if err != nil {
		return nil // not this swarm's node: swarmJoinNode reports it
	}
	return ownerCreateCheck(pd, "node", id, n.Node.Spec.Labels)
}

// swarmPriv returns p, or nil when p is a nil pointer (a response built by
// hand in a unit test has no private state), so ownerRecord skips it.
func swarmPriv(p privateSetter) privateSetter {
	if p == nil {
		return nil
	}
	if v := reflect.ValueOf(p); v.Kind() == reflect.Pointer && v.IsNil() {
		return nil
	}
	return p
}

// swarmInitOwnerCreateCheck runs before docker_swarm_init initializes: when
// the provider's host already manages a swarm whose spec labels name another
// owner, it returns ownerCreateCheck's error (instead of the daemon's "already
// part of a swarm"). With `owner` unset, or no swarm to inspect, it is nil.
func swarmInitOwnerCreateCheck(ctx context.Context, pd *providerData) error {
	if pd == nil || pd.Owner == "" || pd.Client == nil {
		return nil
	}
	sw, err := pd.Client.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		return nil
	}
	return ownerCreateCheck(pd, "swarm", sw.Swarm.ID, sw.Swarm.Spec.Labels)
}
