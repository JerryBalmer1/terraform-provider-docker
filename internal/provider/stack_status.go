package provider

// `docker stack services` / `docker stack ls` without the CLI: services
// grouped by the com.docker.stack.namespace label, replica counts from the
// running tasks. Shared by docker_stack (service_status) and the
// docker_stack / docker_stacks data sources.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// stackServiceRow is one `docker stack services` row.
type stackServiceRow struct {
	ID              string
	Name            string // full service name (<stack>_<short>)
	Short           string
	Mode            string
	Running         uint64
	Desired         uint64
	DesiredKnown    bool
	Image           string
	Ports           []string
	Spec            swarm.ServiceSpec
	NamespaceLabels string
}

// stackServiceMode names a service mode like the CLI.
func stackServiceMode(m swarm.ServiceMode) string {
	switch {
	case m.Global != nil:
		return "global"
	case m.ReplicatedJob != nil:
		return "replicated job"
	case m.GlobalJob != nil:
		return "global job"
	default:
		return "replicated"
	}
}

// stackPortString formats a published port like `docker stack services`.
func stackPortString(p swarm.PortConfig) string {
	proto := string(p.Protocol)
	if proto == "" {
		proto = "tcp"
	}
	if p.PublishedPort == 0 {
		return fmt.Sprintf("%d/%s", p.TargetPort, proto)
	}
	return fmt.Sprintf("*:%d->%d/%s", p.PublishedPort, p.TargetPort, proto)
}

// stackServiceRows lists the services of namespace ns (all namespaces when
// ns is empty) with their running/desired task counts.
func stackServiceRows(ctx context.Context, c *client.Client, ns string) ([]stackServiceRow, error) {
	f := client.Filters{}.Add("label", labelStackNamespace)
	if ns != "" {
		f = client.Filters{}.Add("label", labelStackNamespace+"="+ns)
	}
	sres, err := c.ServiceList(ctx, client.ServiceListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}
	running := map[string]uint64{}
	nodesWithTask := map[string]map[string]bool{}
	if len(sres.Items) > 0 {
		tres, err := c.TaskList(ctx, client.TaskListOptions{Filters: client.Filters{}.Add("desired-state", "running")})
		if err != nil {
			return nil, fmt.Errorf("listing tasks: %w", err)
		}
		for _, t := range tres.Items {
			if nodesWithTask[t.ServiceID] == nil {
				nodesWithTask[t.ServiceID] = map[string]bool{}
			}
			nodesWithTask[t.ServiceID][t.NodeID] = true
			if t.Status.State == swarm.TaskStateRunning {
				running[t.ServiceID]++
			}
		}
	}
	rows := make([]stackServiceRow, 0, len(sres.Items))
	for _, s := range sres.Items {
		space := s.Spec.Labels[labelStackNamespace]
		r := stackServiceRow{
			ID: s.ID, Name: s.Spec.Name, Short: strings.TrimPrefix(s.Spec.Name, space+"_"),
			Mode: stackServiceMode(s.Spec.Mode), Running: running[s.ID], Spec: s.Spec, NamespaceLabels: space,
		}
		if s.Spec.TaskTemplate.ContainerSpec != nil {
			r.Image = s.Spec.TaskTemplate.ContainerSpec.Image
			if i := strings.Index(r.Image, "@sha256:"); i > 0 {
				r.Image = r.Image[:i]
			}
		}
		switch {
		case s.Spec.Mode.Replicated != nil && s.Spec.Mode.Replicated.Replicas != nil:
			r.Desired, r.DesiredKnown = *s.Spec.Mode.Replicated.Replicas, true
		case s.Spec.Mode.Global != nil:
			r.Desired, r.DesiredKnown = uint64(len(nodesWithTask[s.ID])), true
		case s.ServiceStatus != nil:
			r.Desired, r.DesiredKnown = s.ServiceStatus.DesiredTasks, true
		}
		if s.Endpoint.Ports != nil {
			for _, p := range s.Endpoint.Ports {
				r.Ports = append(r.Ports, stackPortString(p))
			}
		} else if s.Spec.EndpointSpec != nil {
			for _, p := range s.Spec.EndpointSpec.Ports {
				r.Ports = append(r.Ports, stackPortString(p))
			}
		}
		if r.Ports == nil {
			r.Ports = []string{}
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// stackServiceStatus is the structured value of one row (the
// service_status / services element, CLI --format json field names).
func stackServiceStatus(r stackServiceRow) map[string]any {
	out := map[string]any{
		"ID": r.ID, "Name": r.Name, "Mode": r.Mode, "Image": r.Image, "Ports": r.Ports,
		"running_replicas": r.Running, "Spec": composePlain(r.Spec, nil),
	}
	if r.DesiredKnown {
		out["Replicas"] = fmt.Sprintf("%d/%d", r.Running, r.Desired)
		out["desired_replicas"] = r.Desired
	} else {
		out["Replicas"] = fmt.Sprintf("%d", r.Running)
	}
	return out
}
