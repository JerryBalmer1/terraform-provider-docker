package provider

// Pass 5 Lane B: outputs-contract helpers shared by the swarm resources,
// data sources and actions (reference/CONTRACT.md). Every inspect_json is
// the Engine's response body as received (client.*InspectResult.Raw) where
// the client exposes it; SwarmInspect and the list endpoints do not, so
// those raws are the decoded moby value re-encoded with encoding/json
// (same field names, omitempty as the Engine's own types).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/distribution/reference"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
)

// swarmInspectInput is the contract input of an inspect-shaped item: the
// raw `inspect_json` body (structured `inspect` is decoded from it).
func swarmInspectInput(raw []byte, v any) contractFillInput {
	in := contractFillInput{Raw: map[string][]byte{}, Structured: map[string]any{}}
	if len(raw) > 0 {
		in.Raw["inspect_json"] = raw
	} else if v != nil {
		in.Structured["inspect"] = v
	}
	return in
}

// swarmListInput is the contract input of a plural data source: the list
// under name (e.g. "nodes") and its *_json raw.
func swarmListInput(name string, items any) contractFillInput {
	b, _ := json.Marshal(items)
	if string(b) == "null" {
		b = []byte("[]")
	}
	return contractFillInput{Raw: map[string][]byte{name + "_json": b}, Structured: map[string]any{name: json.RawMessage(b)}}
}

// swarmVersionIndex is a swarm object's Version.Index as the identity
// output version_index.
func swarmVersionIndex(v swarm.Version) types.Int64 { return types.Int64Value(int64(v.Index)) }

// svcImageDigestRef returns repo@sha256:... for an image reference that
// carries a digest (the daemon pins service images), or "" when it has
// none.
func svcImageDigestRef(img string) string {
	named, err := reference.ParseNormalizedNamed(img)
	if err != nil {
		return ""
	}
	c, ok := named.(reference.Canonical)
	if !ok {
		return ""
	}
	return reference.FamiliarName(named) + "@" + c.Digest().String()
}

// svcPublishedPorts is the structured published_ports value: the ports
// the service publishes as the Engine reports them (Endpoint.Ports, which
// includes the ports Swarm assigned), falling back to the spec.
func svcPublishedPorts(svc swarm.Service) []swarm.PortConfig {
	ports := svc.Endpoint.Ports
	if len(ports) == 0 && svc.Spec.EndpointSpec != nil {
		ports = svc.Spec.EndpointSpec.Ports
	}
	if ports == nil {
		ports = []swarm.PortConfig{}
	}
	return ports
}

// svcContractInput is docker_service's / data.docker_service's contract
// input.
func svcContractInput(raw []byte, svc swarm.Service) contractFillInput {
	in := swarmInspectInput(raw, svc)
	in.Structured["published_ports"] = svcPublishedPorts(svc)
	return in
}

// svcReplicasValue is the identity output replicas: the wanted task count
// of a replicated service, null for the other modes.
func svcReplicasValue(svc swarm.Service) types.Int64 {
	if r := svc.Spec.Mode.Replicated; r != nil {
		if r.Replicas == nil {
			return types.Int64Value(1)
		}
		return types.Int64Value(int64(*r.Replicas))
	}
	return types.Int64Null()
}

// swarmListNames returns the IDs and names lists of plural data sources, in
// the order given.
func swarmListNames(ids, names []string) (types.List, types.List) {
	if ids == nil {
		ids = []string{}
	}
	if names == nil {
		names = []string{}
	}
	return stringListValue(ids, false), stringListValue(names, false)
}

// swarmRetryConflict runs update, retrying with a short backoff while it
// fails with swarmkit's "update out of sequence" (another writer bumped the
// object version between inspect and update).
func swarmRetryConflict(ctx context.Context, update func(ctx context.Context) error) error {
	var err error
	for attempt := 0; attempt < swarmNodeUpdateAttempts; attempt++ {
		if err = update(ctx); err == nil || !swarmIsVersionConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
		}
	}
	return fmt.Errorf("gave up after %d version conflicts: %w", swarmNodeUpdateAttempts, err)
}

// svcDeployOutputs are the outputs of inventory item compose:service-deploy
// that docker_service carries beside swarm:service's (engine_api_version is
// shared, so that item's struct is not embedded).
var svcDeployOutputs = []string{"deploy", "compose_spec_version", "compose_go_version"}

// svcComposeMode is the compose-spec spelling of a service mode.
func svcComposeMode(spec swarm.ServiceSpec) string {
	switch {
	case spec.Mode.Global != nil:
		return "global"
	case spec.Mode.ReplicatedJob != nil:
		return "replicated-job"
	case spec.Mode.GlobalJob != nil:
		return "global-job"
	}
	return "replicated"
}

func svcComposeUpdate(u *swarm.UpdateConfig) map[string]any {
	if u == nil {
		return nil
	}
	return map[string]any{
		"parallelism": u.Parallelism, "delay": u.Delay.String(), "failure_action": u.FailureAction,
		"monitor": u.Monitor.String(), "max_failure_ratio": u.MaxFailureRatio, "order": u.Order,
	}
}

// svcComposeDeploy renders a service spec as the compose-spec `deploy`
// section (the shape `docker stack` reads), for the deploy output.
func svcComposeDeploy(spec swarm.ServiceSpec) map[string]any {
	d := map[string]any{"mode": svcComposeMode(spec), "labels": userLabels(spec.Labels, false)}
	if es := spec.EndpointSpec; es != nil && es.Mode != "" {
		d["endpoint_mode"] = string(es.Mode)
	}
	if r := spec.Mode.Replicated; r != nil && r.Replicas != nil {
		d["replicas"] = *r.Replicas
	}
	if u := svcComposeUpdate(spec.UpdateConfig); u != nil {
		d["update_config"] = u
	}
	if u := svcComposeUpdate(spec.RollbackConfig); u != nil {
		d["rollback_config"] = u
	}
	if rr := spec.TaskTemplate.Resources; rr != nil {
		res := map[string]any{}
		if l := rr.Limits; l != nil {
			res["limits"] = map[string]any{"cpus": fmt.Sprintf("%g", float64(l.NanoCPUs)/1e9), "memory": l.MemoryBytes, "pids": l.Pids}
		}
		if l := rr.Reservations; l != nil {
			res["reservations"] = map[string]any{"cpus": fmt.Sprintf("%g", float64(l.NanoCPUs)/1e9), "memory": l.MemoryBytes, "generic_resources": l.GenericResources}
		}
		d["resources"] = res
	}
	if rp := spec.TaskTemplate.RestartPolicy; rp != nil {
		r := map[string]any{"condition": string(rp.Condition)}
		if rp.Delay != nil {
			r["delay"] = rp.Delay.String()
		}
		if rp.MaxAttempts != nil {
			r["max_attempts"] = *rp.MaxAttempts
		}
		if rp.Window != nil {
			r["window"] = rp.Window.String()
		}
		d["restart_policy"] = r
	}
	if pl := spec.TaskTemplate.Placement; pl != nil {
		var prefs []map[string]any
		for _, p := range pl.Preferences {
			if p.Spread != nil {
				prefs = append(prefs, map[string]any{"spread": p.Spread.SpreadDescriptor})
			}
		}
		d["placement"] = map[string]any{"constraints": pl.Constraints, "preferences": prefs, "max_replicas_per_node": pl.MaxReplicas}
	}
	return d
}
