package provider

// Pass 5 Lane E: finding a buildx builder on the provider's daemon and
// connecting to its BuildKit, without the buildx CLI or library.
//
// Builders the provider can see are the ones buildx itself would find on
// the daemon:
//   - "default": the daemon's embedded BuildKit (buildx's `docker`
//     driver). The control API is reached by POST /grpc (Upgrade: h2c) and
//     sessions by POST /session, exactly as buildx's docker driver does.
//   - docker-container builders: containers named buildx_buildkit_<node>
//     (labelled with the builder name when this provider made them).
//     BuildKit is reached by `buildctl dial-stdio` over an exec attach, as
//     buildx's docker-container driver does (bxbDialStdio).
//   - an explicit BuildKit endpoint: docker-container://<container>, or
//     tcp://, unix:// and npipe:// addresses dialled directly (buildx's
//     `remote` driver).
// buildx's client-side store (~/.docker/buildx/instances) is never read.

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	bkDefaultBuilder    = "default"
	bkDriverDocker      = "docker"
	bkDriverRemote      = "remote"
	bkDialTimeout       = 20 * time.Second
	bkStatusRunning     = "running"
	bkStatusInactive    = "inactive"
	bkStatusStopped     = "stopped"
	bkDockerContainerEP = "docker-container://"
)

// bkNode is one builder node as the daemon shows it.
type bkNode struct {
	// Builder is the builder name; Name the node name (container name
	// without the buildx_buildkit_ prefix, or "default").
	Builder string
	Name    string
	Driver  string
	// Endpoint is where BuildKit listens (buildx's node endpoint).
	Endpoint string
	// ContainerID / ContainerName are set for docker-container nodes.
	ContainerID   string
	ContainerName string
	// Status is running, stopped or inactive (buildx ls wording).
	Status string
	// Flags are the buildkitd flags (container command).
	Flags []string
	// DriverOpts are the docker-container driver options visible on the
	// container (image, network, env.*, memory, ...).
	DriverOpts map[string]string
	// Labels are the container labels the provider set (platform pins).
	Labels map[string]string
	// Created is the container creation time (last activity fallback).
	Created time.Time
}

// bkConn is an open BuildKit control connection to one node.
type bkConn struct {
	Node   bkNode
	Client *bkclient.Client
	// Docker is true for the daemon's embedded BuildKit (docker driver).
	Docker bool
}

// Close closes the control connection.
func (c *bkConn) Close() {
	if c != nil && c.Client != nil {
		_ = c.Client.Close()
	}
}

// bkOpen opens a BuildKit client for node; a var so unit tests can serve a
// fake control API.
var bkOpen = bkOpenNode

// bkOpenNode dials node's BuildKit.
func bkOpenNode(ctx context.Context, pd *providerData, node bkNode) (*bkclient.Client, error) {
	if pd == nil || pd.Client == nil {
		return nil, fmt.Errorf("provider not configured")
	}
	switch {
	case node.Driver == bkDriverDocker:
		c := pd.Client
		return bkclient.New(ctx, "",
			bkclient.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return c.DialHijack(ctx, "/grpc", "h2c", nil)
			}),
			bkclient.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
				return c.DialHijack(ctx, "/session", proto, meta)
			}))
	case node.ContainerID != "" || strings.HasPrefix(node.Endpoint, bkDockerContainerEP):
		id := node.ContainerID
		if id == "" {
			id = strings.TrimPrefix(node.Endpoint, bkDockerContainerEP)
		}
		return bkclient.New(ctx, "", bkclient.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return bxbDialStdio(ctx, pd, id)
		}))
	default:
		// remote driver: TLS options from its driver options.
		return bkclient.New(ctx, node.Endpoint, bkTLSOpts(node.DriverOpts)...)
	}
}

// bkNodeRe splits a buildx_buildkit_<builder><index> container name when
// no builder label is present: buildx names node i of builder b "b<i>".
var bkNodeRe = regexp.MustCompile(`^(.+?)([0-9])$`)

// bkNodeFromContainer describes a buildx_buildkit_* container.
func bkNodeFromContainer(name, id string, labels map[string]string, state string, cmd []string, created time.Time) bkNode {
	nodeName := strings.TrimPrefix(strings.TrimPrefix(name, "/"), bxbContainerPrefix)
	builder := labels[bxbLabelBuilder]
	if builder == "" {
		builder = nodeName
		if m := bkNodeRe.FindStringSubmatch(nodeName); m != nil {
			builder = m[1]
		}
	}
	status := bkStatusStopped
	if state == "running" {
		status = bkStatusRunning
	} else if state == "created" {
		status = bkStatusInactive
	}
	return bkNode{
		Builder: builder, Name: nodeName, Driver: bxbDriverDockerContainer,
		Endpoint:    bkDockerContainerEP + bxbContainerPrefix + nodeName,
		ContainerID: id, ContainerName: bxbContainerPrefix + nodeName,
		Status: status, Flags: append([]string(nil), cmd...), Labels: labels, Created: created,
	}
}

// bkDefaultNode is the daemon's embedded BuildKit.
func bkDefaultNode() bkNode {
	return bkNode{Builder: bkDefaultBuilder, Name: bkDefaultBuilder, Driver: bkDriverDocker, Endpoint: bkDefaultBuilder, Status: bkStatusRunning}
}

// bkListNodes returns every builder node visible on the daemon: the
// default builder first, then buildx_buildkit_* containers by name.
func bkListNodes(ctx context.Context, pd *providerData) ([]bkNode, error) {
	if pd == nil || pd.Client == nil {
		return nil, fmt.Errorf("provider not configured")
	}
	res, err := pd.Client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("name", bxbContainerPrefix)})
	if err != nil {
		return nil, fmt.Errorf("listing builder containers: %w", err)
	}
	out := []bkNode{bkDefaultNode()}
	var nodes []bkNode
	for _, s := range res.Items {
		name := ""
		for _, n := range s.Names {
			n = strings.TrimPrefix(n, "/")
			if strings.HasPrefix(n, bxbContainerPrefix) && !strings.Contains(n, "/") {
				name = n
			}
		}
		if name == "" {
			continue
		}
		var cmd []string
		if s.Command != "" {
			cmd = strings.Fields(s.Command)
			// The summary's Command is "<entrypoint> <args>"; keep the flags.
			for len(cmd) > 0 && !strings.HasPrefix(cmd[0], "-") {
				cmd = cmd[1:]
			}
		}
		nodes = append(nodes, bkNodeFromContainer(name, s.ID, s.Labels, string(s.State), cmd, time.Unix(s.Created, 0).UTC()))
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Builder != nodes[j].Builder {
			return nodes[i].Builder < nodes[j].Builder
		}
		return nodes[i].Name < nodes[j].Name
	})
	return append(out, nodes...), nil
}

// bkBuilderNodes returns the nodes of one builder ("" = default).
func bkBuilderNodes(ctx context.Context, pd *providerData, builder string) ([]bkNode, error) {
	if builder == "" || builder == bkDefaultBuilder {
		return []bkNode{bkDefaultNode()}, nil
	}
	all, err := bkListNodes(ctx, pd)
	if err != nil {
		return nil, err
	}
	var out []bkNode
	for _, n := range all {
		if n.Builder == builder && n.Driver != bkDriverDocker {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no buildx builder %q on the daemon: no container %s0 (or labelled %s=%s) exists. "+
			"Builders live in buildx's client-side store only when created with another driver; use builder endpoint instead", builder, bxbContainerPrefix+builder, bxbLabelBuilder, builder)
	}
	return out, nil
}

// bkNodeForRef resolves a seam builder reference to one node.
func bkNodeForRef(ctx context.Context, pd *providerData, ref seamBuilderRef) (bkNode, error) {
	if ep := strings.TrimSpace(ref.Endpoint); ep != "" {
		name := ref.Name
		if strings.HasPrefix(ep, bkDockerContainerEP) {
			cname := strings.TrimPrefix(ep, bkDockerContainerEP)
			if name == "" {
				name = strings.TrimPrefix(cname, bxbContainerPrefix)
			}
			return bkNode{Builder: name, Name: strings.TrimPrefix(cname, bxbContainerPrefix), Driver: bxbDriverDockerContainer, Endpoint: ep, ContainerName: cname, Status: bkStatusRunning}, nil
		}
		if name == "" {
			name = ep
		}
		return bkNode{Builder: name, Name: name + "0", Driver: bkDriverRemote, Endpoint: ep, Status: bkStatusRunning}, nil
	}
	nodes, err := bkBuilderNodes(ctx, pd, ref.Name)
	if err != nil {
		return bkNode{}, err
	}
	for _, n := range nodes {
		if n.Status == bkStatusRunning {
			return n, nil
		}
	}
	// Not running: start the first node, as buildx boots a stopped builder.
	n := nodes[0]
	if n.ContainerID != "" {
		if _, err := pd.Client.ContainerStart(ctx, n.ContainerID, client.ContainerStartOptions{}); err != nil {
			return bkNode{}, fmt.Errorf("starting builder node %s: %w", n.ContainerName, err)
		}
		n.Status = bkStatusRunning
	}
	return n, nil
}

// bkConnect resolves ref and opens its BuildKit.
func bkConnect(ctx context.Context, pd *providerData, ref seamBuilderRef) (*bkConn, error) {
	node, err := bkNodeForRef(ctx, pd, ref)
	if err != nil {
		return nil, err
	}
	c, err := bkOpen(ctx, pd, node)
	if err != nil {
		return nil, fmt.Errorf("connecting to BuildKit of builder %q (%s): %w", node.Builder, node.Endpoint, err)
	}
	return &bkConn{Node: node, Client: c, Docker: node.Driver == bkDriverDocker}, nil
}

// bkNodeDriverOpts reads driver options back from a builder container
// (inspect), in buildx's `--driver-opt` key names.
func bkNodeDriverOpts(insp container.InspectResponse) map[string]string {
	out := map[string]string{}
	if insp.Config != nil {
		if insp.Config.Image != "" {
			out["image"] = insp.Config.Image
		}
		for _, e := range insp.Config.Env {
			k, v, ok := strings.Cut(e, "=")
			if ok && !bkImageEnv(k) {
				out["env."+k] = v
			}
		}
		if v := insp.Config.Labels[bxbLabelDefaultLoad]; v != "" {
			out["default-load"] = v
		}
	}
	if hc := insp.HostConfig; hc != nil {
		if hc.NetworkMode != "" && hc.NetworkMode != "default" && hc.NetworkMode != "bridge" {
			out["network"] = string(hc.NetworkMode)
		}
		if hc.RestartPolicy.Name != "" {
			out["restart-policy"] = string(hc.RestartPolicy.Name)
		}
		if hc.Memory > 0 {
			out["memory"] = fmt.Sprint(hc.Memory)
		}
		if hc.MemorySwap != 0 {
			out["memory-swap"] = fmt.Sprint(hc.MemorySwap)
		}
		if hc.CPUQuota > 0 {
			out["cpu-quota"] = fmt.Sprint(hc.CPUQuota)
		}
		if hc.CPUPeriod > 0 {
			out["cpu-period"] = fmt.Sprint(hc.CPUPeriod)
		}
		if hc.CPUShares > 0 {
			out["cpu-shares"] = fmt.Sprint(hc.CPUShares)
		}
		if hc.CpusetCpus != "" {
			out["cpuset-cpus"] = hc.CpusetCpus
		}
		if hc.CpusetMems != "" {
			out["cpuset-mems"] = hc.CpusetMems
		}
		if hc.CgroupParent != "" {
			out["cgroup-parent"] = hc.CgroupParent
		}
	}
	return out
}

// bkImageEnv reports env keys that come from the BuildKit image itself.
func bkImageEnv(k string) bool {
	return k == "PATH"
}
