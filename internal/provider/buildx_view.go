package provider

// Pass 5 Lane E: the `docker buildx inspect` / `buildx ls --format json`
// view of a builder, shared by the docker_buildx_builder resource and the
// docker_buildx_builder(s) data sources.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/moby/client"
)

// bxNodeView is one node in buildx's JSON shape (buildx ls --format json).
type bxNodeView struct {
	Name       string            `json:"Name"`
	Endpoint   string            `json:"Endpoint"`
	DriverOpts map[string]string `json:"DriverOpts,omitempty"`
	Status     string            `json:"Status,omitempty"`
	Flags      []string          `json:"Flags,omitempty"`
	Version    string            `json:"Version,omitempty"`
	Platforms  []string          `json:"Platforms,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	GCPolicy   []bxGCPolicyView  `json:"GCPolicy,omitempty"`
	Err        string            `json:"Err,omitempty"`
}

// bxGCPolicyView is one GC policy rule.
type bxGCPolicyView struct {
	All           bool     `json:"all,omitempty"`
	Filter        []string `json:"filter,omitempty"`
	KeepDuration  string   `json:"keepDuration,omitempty"`
	ReservedSpace int64    `json:"reservedSpace,omitempty"`
	MaxUsedSpace  int64    `json:"maxUsedSpace,omitempty"`
	MinFreeSpace  int64    `json:"minFreeSpace,omitempty"`
}

// bxBuilderView is one builder in buildx's JSON shape.
type bxBuilderView struct {
	Name         string       `json:"Name"`
	Driver       string       `json:"Driver"`
	LastActivity string       `json:"LastActivity,omitempty"`
	Dynamic      bool         `json:"Dynamic"`
	Nodes        []bxNodeView `json:"Nodes"`
	Err          string       `json:"Err,omitempty"`
}

// bkWorkers lists a node's BuildKit workers; a var so unit tests can fake
// BuildKit.
var bkWorkers = func(ctx context.Context, pd *providerData, node bkNode) ([]*bkclient.WorkerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, bkDialTimeout)
	defer cancel()
	c, err := bkOpen(ctx, pd, node)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.ListWorkers(ctx)
}

// bkPlatformString prints an OCI platform as os/arch[/variant].
func bkPlatformString(os, arch, variant string) string {
	s := os + "/" + arch
	if variant != "" {
		s += "/" + variant
	}
	return s
}

// bxApplyWorkers fills the BuildKit part of a node view from its workers.
func bxApplyWorkers(nv *bxNodeView, pinned []string, ws []*bkclient.WorkerInfo) {
	seen := map[string]bool{}
	nv.Platforms = nil
	for _, p := range pinned {
		if p != "" && !seen[p] {
			seen[p] = true
			nv.Platforms = append(nv.Platforms, p)
		}
	}
	if len(ws) == 0 {
		return
	}
	w := ws[0]
	nv.Version = w.BuildkitVersion.Version
	nv.Labels = w.Labels
	for _, wi := range ws {
		for _, p := range wi.Platforms {
			s := bkPlatformString(p.OS, p.Architecture, p.Variant)
			if !seen[s] {
				seen[s] = true
				nv.Platforms = append(nv.Platforms, s)
			}
		}
	}
	nv.GCPolicy = nil
	for _, g := range w.GCPolicy {
		gv := bxGCPolicyView{All: g.All, Filter: g.Filter, ReservedSpace: g.ReservedSpace, MaxUsedSpace: g.MaxUsedSpace, MinFreeSpace: g.MinFreeSpace}
		if g.KeepDuration > 0 {
			gv.KeepDuration = g.KeepDuration.String()
		}
		nv.GCPolicy = append(nv.GCPolicy, gv)
	}
}

// bxPinnedPlatforms reads the platform pins the resource stores on a node
// container.
func bxPinnedPlatforms(labels map[string]string) []string {
	v := labels[bxbLabelPlatforms]
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// bxNodeViewOf describes one node, querying BuildKit when it is running
// and ws is nil. A failure is the node's Err, not an error.
func bxNodeViewOf(ctx context.Context, pd *providerData, n bkNode, ws []*bkclient.WorkerInfo) bxNodeView {
	nv := bxNodeView{Name: n.Name, Endpoint: n.Endpoint, Status: n.Status, Flags: n.Flags}
	if n.ContainerID != "" && pd != nil && pd.Client != nil {
		if res, err := pd.Client.ContainerInspect(ctx, n.ContainerID, client.ContainerInspectOptions{}); err == nil {
			insp := res.Container
			nv.DriverOpts = bkNodeDriverOpts(insp)
			if insp.Config != nil {
				if len(insp.Config.Cmd) > 0 {
					nv.Flags = append([]string(nil), insp.Config.Cmd...)
				}
				if n.Labels == nil {
					n.Labels = insp.Config.Labels
				}
			}
			if insp.State != nil {
				switch {
				case insp.State.Running:
					nv.Status = bkStatusRunning
				case insp.State.Status == "created":
					nv.Status = bkStatusInactive
				default:
					nv.Status = bkStatusStopped
				}
			}
		} else {
			nv.Err = err.Error()
		}
	}
	pinned := bxPinnedPlatforms(n.Labels)
	if ws == nil && nv.Status == bkStatusRunning && nv.Err == "" {
		var err error
		ws, err = bkWorkers(ctx, pd, n)
		if err != nil {
			nv.Err = err.Error()
		}
	}
	bxApplyWorkers(&nv, pinned, ws)
	return nv
}

// bxBuilderViewOf describes a builder from its nodes.
func bxBuilderViewOf(ctx context.Context, pd *providerData, nodes []bkNode, ws map[string][]*bkclient.WorkerInfo) bxBuilderView {
	v := bxBuilderView{Nodes: []bxNodeView{}}
	if len(nodes) == 0 {
		return v
	}
	v.Name, v.Driver = nodes[0].Builder, nodes[0].Driver
	var last time.Time
	for _, n := range nodes {
		v.Nodes = append(v.Nodes, bxNodeViewOf(ctx, pd, n, ws[n.Name]))
		if n.Created.After(last) {
			last = n.Created
		}
	}
	if !last.IsZero() {
		v.LastActivity = last.UTC().Format(time.RFC3339)
	}
	return v
}

// json renders the view as buildx prints it.
func (v bxBuilderView) json() []byte {
	b, _ := json.Marshal(v)
	return b
}

// structured is the contract `builder` object (snake_case keys).
func (v bxBuilderView) structured() map[string]any {
	nodes := make([]any, 0, len(v.Nodes))
	for _, n := range v.Nodes {
		gc := make([]any, 0, len(n.GCPolicy))
		for _, g := range n.GCPolicy {
			keep := g.ReservedSpace
			gc = append(gc, map[string]any{"all": g.All, "filters": bxStrings(g.Filter), "keep_duration": g.KeepDuration, "keep_bytes": keep})
		}
		nodes = append(nodes, map[string]any{
			"name": n.Name, "endpoint": n.Endpoint, "driver_opts": bxStringMap(n.DriverOpts), "status": n.Status,
			"flags": bxStrings(n.Flags), "buildkit_version": n.Version, "platforms": bxStrings(n.Platforms),
			"labels": bxStringMap(n.Labels), "gc_policy": gc, "error": n.Err,
		})
	}
	return map[string]any{"name": v.Name, "driver": v.Driver, "last_activity": v.LastActivity, "nodes": nodes, "error": v.Err}
}

// first returns the first node view (zero when none).
func (v bxBuilderView) first() bxNodeView {
	if len(v.Nodes) == 0 {
		return bxNodeView{}
	}
	return v.Nodes[0]
}

// platforms is the union of node platforms.
func (v bxBuilderView) platforms() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range v.Nodes {
		for _, p := range n.Platforms {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func bxStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func bxStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// bxGroupBuilders groups nodes by builder, keeping the order of first
// appearance (the default builder first).
func bxGroupBuilders(nodes []bkNode) [][]bkNode {
	idx := map[string]int{}
	var out [][]bkNode
	for _, n := range nodes {
		i, ok := idx[n.Builder]
		if !ok {
			i = len(out)
			idx[n.Builder] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], n)
	}
	for _, g := range out {
		sort.SliceStable(g, func(a, b int) bool { return g[a].Name < g[b].Name })
	}
	return out
}

// bxDescribeBuilder resolves a builder by name and returns its view.
func bxDescribeBuilder(ctx context.Context, pd *providerData, name string) (bxBuilderView, error) {
	nodes, err := bkBuilderNodes(ctx, pd, name)
	if err != nil {
		return bxBuilderView{}, err
	}
	if len(nodes) == 0 {
		return bxBuilderView{}, fmt.Errorf("builder %q has no nodes", name)
	}
	return bxBuilderViewOf(ctx, pd, nodes, nil), nil
}
