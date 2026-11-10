package provider

// Lane 3 (Swarm topology) private helpers. Every identifier is prefixed with
// "swarm" so it cannot collide with other lanes in this package.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

const (
	// swarmDefaultListenAddr is the Docker default for --listen-addr.
	swarmDefaultListenAddr = "0.0.0.0:2377"
	// swarmNodeUpdateAttempts bounds the NodeUpdate version-conflict retry.
	swarmNodeUpdateAttempts = 5
	// swarmLeaveTimeout bounds how long Delete waits for the manager to see a
	// leaving node as down.
	swarmLeaveTimeout = 2 * time.Minute
	// swarmJoinTimeout bounds how long Create waits for a joined node to be
	// active and visible to the manager.
	swarmJoinTimeout = 2 * time.Minute
)

// swarmPollInterval is a var so unit tests can shorten it.
var swarmPollInterval = 2 * time.Second

var (
	swarmValidAvailabilities = []string{string(swarm.NodeAvailabilityActive), string(swarm.NodeAvailabilityPause), string(swarm.NodeAvailabilityDrain)}
	swarmValidRoles          = []string{string(swarm.NodeRoleWorker), string(swarm.NodeRoleManager)}
)

// swarmIsValidAvailability reports whether s is a Docker node availability.
func swarmIsValidAvailability(s string) bool { return swarmContains(swarmValidAvailabilities, s) }

// swarmIsValidRole reports whether s is a Docker node role.
func swarmIsValidRole(s string) bool { return swarmContains(swarmValidRoles, s) }

func swarmContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// swarmIsVersionConflict reports whether err is swarmkit's optimistic
// concurrency failure ("update out of sequence"), which is retried with a
// fresh object version.
func swarmIsVersionConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "update out of sequence") || strings.Contains(msg, "version mismatch")
}

// swarmIsNotInSwarm reports whether err says the daemon is not part of a swarm
// (for example SwarmLeave on a node that already left).
func swarmIsNotInSwarm(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not part of a swarm")
}

// swarmNodeSpecChange is the desired node spec managed by docker_swarm_node.
// Nil fields are left as the daemon has them.
type swarmNodeSpecChange struct {
	Availability *swarm.NodeAvailability
	Role         *swarm.NodeRole
	Labels       *map[string]string
	// Name is NodeSpec.Name (docker_node `name`); nil leaves it alone.
	Name *string
}

// swarmApplyNodeSpec returns a copy of spec with change applied, and whether
// anything differs.
func swarmApplyNodeSpec(spec swarm.NodeSpec, change swarmNodeSpecChange) (swarm.NodeSpec, bool) {
	out := spec
	changed := false
	if change.Availability != nil && out.Availability != *change.Availability {
		out.Availability = *change.Availability
		changed = true
	}
	if change.Role != nil && out.Role != *change.Role {
		out.Role = *change.Role
		changed = true
	}
	if change.Name != nil && out.Name != *change.Name {
		out.Name = *change.Name
		changed = true
	}
	if change.Labels != nil {
		want := *change.Labels
		if want == nil {
			want = map[string]string{}
		}
		// A label set that does not mention the owner label keeps the node's
		// current one: docker_node's `labels` must not strip the owner label
		// docker_swarm_node put there (Pass 5.3 Lane Z). docker_swarm_node
		// injects its own value, which wins.
		if cur, ok := out.Labels[ownerLabelKey]; ok {
			if _, set := want[ownerLabelKey]; !set {
				want = mergeLabels(want, map[string]string{ownerLabelKey: cur})
			}
		}
		if !swarmStringMapsEqual(out.Labels, want) {
			out.Labels = mergeLabels(want)
			changed = true
		}
	}
	return out, changed
}

func swarmStringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// swarmUpdateNode applies change to nodeID through c (a manager), reading a
// fresh version and retrying when the update races another writer.
func swarmUpdateNode(ctx context.Context, c *client.Client, nodeID string, change swarmNodeSpecChange) error {
	var lastErr error
	for attempt := 0; attempt < swarmNodeUpdateAttempts; attempt++ {
		res, err := c.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
		if err != nil {
			return fmt.Errorf("inspecting node %s: %w", nodeID, err)
		}
		n := res.Node
		spec, changed := swarmApplyNodeSpec(n.Spec, change)
		if !changed {
			return nil
		}
		_, err = c.NodeUpdate(ctx, nodeID, client.NodeUpdateOptions{Version: n.Version, Spec: spec})
		if err == nil {
			return nil
		}
		// A just-promoted manager may not be in the raft member list yet;
		// that settles within seconds, so it is retried with a longer wait.
		raftLag := strings.Contains(err.Error(), "raft memberlist")
		if !swarmIsVersionConflict(err) && !raftLag {
			return fmt.Errorf("updating node %s: %w", nodeID, err)
		}
		lastErr = err
		wait := time.Duration(attempt+1) * 100 * time.Millisecond
		if raftLag {
			wait = time.Duration(attempt+1) * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return fmt.Errorf("updating node %s: gave up after %d version conflicts: %w", nodeID, swarmNodeUpdateAttempts, lastErr)
}

// swarmBuildJoinRequest builds the JoinRequest docker_swarm_node sends to
// the joining node's own daemon.
func swarmBuildJoinRequest(managerAddr, token, advertiseAddr, listenAddr string) client.SwarmJoinOptions {
	if listenAddr == "" {
		listenAddr = swarmDefaultListenAddr
	}
	return client.SwarmJoinOptions{
		ListenAddr:    listenAddr,
		AdvertiseAddr: advertiseAddr,
		RemoteAddrs:   []string{managerAddr},
		JoinToken:     token,
	}
}

// swarmJoinNode makes the daemon behind nodeClient join the swarm and
// returns its node ID once the manager can see it. A daemon that is already
// an active member that the manager knows is adopted instead of re-joined.
func swarmJoinNode(ctx context.Context, nodeClient, manager *client.Client, req client.SwarmJoinOptions, timeout time.Duration) (string, error) {
	infoRes, err := nodeClient.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("reading info from joining node: %w", err)
	}
	info := infoRes.Info
	if info.Swarm.LocalNodeState == swarm.LocalNodeStateActive && info.Swarm.NodeID != "" {
		if _, err := manager.NodeInspect(ctx, info.Swarm.NodeID, client.NodeInspectOptions{}); err == nil {
			return info.Swarm.NodeID, nil
		}
		return "", fmt.Errorf("node %s is already part of a different swarm; leave it first", info.Swarm.NodeID)
	}
	if _, err := nodeClient.SwarmJoin(ctx, req); err != nil {
		return "", fmt.Errorf("joining swarm via %v: %w", req.RemoteAddrs, err)
	}
	var nodeID string
	err = waitFor(ctx, timeout, swarmPollInterval, func(ctx context.Context) (bool, error) {
		infoRes, err := nodeClient.Info(ctx, client.InfoOptions{})
		if err != nil {
			return false, err
		}
		info := infoRes.Info
		if info.Swarm.LocalNodeState == swarm.LocalNodeStateError {
			return false, fmt.Errorf("node reports swarm error: %s", info.Swarm.Error)
		}
		if info.Swarm.NodeID == "" || info.Swarm.LocalNodeState != swarm.LocalNodeStateActive {
			return false, nil
		}
		nodeID = info.Swarm.NodeID
		_, err = manager.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
		if isNotFound(err) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		return "", fmt.Errorf("waiting for joined node to become active: %w", err)
	}
	return nodeID, nil
}

// swarmLeaveAndRemove takes a node out of the swarm: demote it through the
// manager when it is a manager, SwarmLeave(force) on its own daemon, wait
// until the manager reports it down, then NodeRemove(force) on the manager.
// A node daemon that cannot be reached is skipped (the manager will mark it
// down); a node already gone from the manager is success.
func swarmLeaveAndRemove(ctx context.Context, nodeClient, manager *client.Client, nodeID string, timeout time.Duration) error {
	nres, err := manager.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
	n := nres.Node
	if isNotFound(err) {
		if nodeClient != nil {
			if _, lerr := nodeClient.SwarmLeave(ctx, client.SwarmLeaveOptions{Force: true}); lerr != nil && !swarmIsNotInSwarm(lerr) && !client.IsErrConnectionFailed(lerr) {
				return fmt.Errorf("leaving swarm: %w", lerr)
			}
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting node %s: %w", nodeID, err)
	}
	if n.Spec.Role == swarm.NodeRoleManager {
		worker := swarm.NodeRoleWorker
		if err := swarmUpdateNode(ctx, manager, nodeID, swarmNodeSpecChange{Role: &worker}); err != nil {
			return fmt.Errorf("demoting node before leave: %w", err)
		}
	}
	if nodeClient != nil {
		if _, err := nodeClient.SwarmLeave(ctx, client.SwarmLeaveOptions{Force: true}); err != nil && !swarmIsNotInSwarm(err) && !client.IsErrConnectionFailed(err) {
			return fmt.Errorf("leaving swarm: %w", err)
		}
	}
	err = waitFor(ctx, timeout, swarmPollInterval, func(ctx context.Context) (bool, error) {
		res, err := manager.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
		if isNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return res.Node.Status.State == swarm.NodeStateDown, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for manager to see node %s down: %w", nodeID, err)
	}
	if _, err := manager.NodeRemove(ctx, nodeID, client.NodeRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
		return fmt.Errorf("removing node %s: %w", nodeID, err)
	}
	return nil
}

// swarmLeave makes c's daemon leave its swarm; not being in a swarm is fine.
func swarmLeave(ctx context.Context, c *client.Client) error {
	if _, err := c.SwarmLeave(ctx, client.SwarmLeaveOptions{Force: true}); err != nil && !swarmIsNotInSwarm(err) {
		return err
	}
	return nil
}

// swarmLabelsValue converts daemon labels to state, keeping an explicitly
// empty map ({}) when the prior value was empty rather than null.
func swarmLabelsValue(prior types.Map, got map[string]string) types.Map {
	if len(got) == 0 && !prior.IsNull() && !prior.IsUnknown() && len(prior.Elements()) == 0 {
		return stringMapValue(nil, false)
	}
	return stringMapValue(got, true)
}

// swarmRequireManager returns an error unless the daemon behind c is an
// active swarm manager.
func swarmRequireManager(ctx context.Context, c *client.Client) error {
	infoRes, err := c.Info(ctx, client.InfoOptions{})
	if err != nil {
		return err
	}
	info := infoRes.Info
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateActive {
		return errors.New("the provider's Docker host is not part of an active swarm")
	}
	if !info.Swarm.ControlAvailable {
		return errors.New("the provider's Docker host is a swarm worker; point the provider at a manager")
	}
	return nil
}
