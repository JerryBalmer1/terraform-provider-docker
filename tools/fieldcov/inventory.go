package main

// Inventory scope: which Engine operations count, which inventory item owns
// each field by default, and which Pass 5.3 lane closes its gaps.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type invSource struct {
	Type        string `json:"type"`
	OperationID string `json:"operationId"`
	Method      string `json:"method"`
	Primary     bool   `json:"primary"`
}

type invItem struct {
	ID         string      `json:"id"`
	Layer      string      `json:"layer"`
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	Registered string      `json:"registered"`
	Status     string      `json:"status"`
	Sources    []invSource `json:"sources"`
}

// tfType is the Terraform type name the item is registered as
// ("action.docker_x" -> "docker_x").
func (i invItem) tfType() string {
	n := i.Registered
	if n == "" {
		n = i.Name
	}
	if j := strings.LastIndex(n, "."); j >= 0 {
		n = n[j+1:]
	}
	return n
}

// fieldKinds are the inventory kinds whose write operations count.
var fieldKinds = map[string]bool{"resource": true, "action": true, "ephemeral_resource": true}

func loadInventory(path string) ([]invItem, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []invItem
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if _, ok := t["id"]; ok {
				if _, ok := t["kind"]; ok {
					if _, ok := t["layer"]; ok {
						bb, _ := json.Marshal(t)
						var it invItem
						if json.Unmarshal(bb, &it) == nil {
							out = append(out, it)
						}
						return
					}
				}
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(raw)
	return out, nil
}

// opOwner is the scope of one Engine operation: every in-scope item that
// lists it, and the default owner (the item a gap is filed against).
type opOwner struct {
	Op    string
	Items []invItem
	Owner invItem
}

// engineScope returns the in-scope write operations (sorted) and their
// owners. A write operation is any swagger source that is not GET, HEAD or
// DELETE: create, update, start, exec, push, prune and the like.
func engineScope(items []invItem) []opOwner {
	byOp := map[string]*opOwner{}
	for _, it := range items {
		if !fieldKinds[it.Kind] || it.Status == "skipped" || it.Kind == "skip" {
			continue
		}
		for _, s := range it.Sources {
			if s.Type != "swagger" || s.OperationID == "" {
				continue
			}
			switch strings.ToUpper(s.Method) {
			case "GET", "HEAD", "DELETE", "":
				continue
			}
			o := byOp[s.OperationID]
			if o == nil {
				o = &opOwner{Op: s.OperationID}
				byOp[s.OperationID] = o
			}
			o.Items = append(o.Items, it)
		}
	}
	ops := make([]string, 0, len(byOp))
	for k := range byOp {
		ops = append(ops, k)
	}
	sort.Strings(ops)
	out := make([]opOwner, 0, len(ops))
	for _, k := range ops {
		o := byOp[k]
		o.Owner = pickOwner(o.Op, o.Items, items)
		out = append(out, *o)
	}
	return out
}

// ownerOverride names the default owner for operations several items share.
var ownerOverride = map[string]string{
	"ContainerCreate":     "engine:container",
	"ContainerUpdate":     "engine:container",
	"ContainerStart":      "engine:container",
	"PutContainerArchive": "engine:container",
	"NetworkConnect":      "engine:container",
	"ImageBuild":          "buildx:build",
	"ImageCreate":         "engine:image",
	"ServiceCreate":       "swarm:service",
	"ServiceUpdate":       "swarm:service",
	"NetworkCreate":       "engine:network",
	"NodeUpdate":          "swarm:node",
	"SwarmUpdate":         "swarm:swarm-init",
	"ContainerPrune":      "engine:container-prune",
	"ImagePrune":          "engine:image-prune",
	"NetworkPrune":        "engine:network-prune",
	"VolumePrune":         "engine:volume-prune",
	"BuildPrune":          "engine:build-prune",
}

func pickOwner(op string, cands, all []invItem) invItem {
	if id, ok := ownerOverride[op]; ok {
		for _, it := range all {
			if it.ID == id {
				return it
			}
		}
	}
	for _, it := range cands {
		for _, s := range it.Sources {
			if s.OperationID == op && s.Primary {
				return it
			}
		}
	}
	for _, it := range cands {
		if it.Kind == "resource" {
			return it
		}
	}
	return cands[0]
}

// ---------------------------------------------------------------------------
// Pass 5.3 lanes

// laneOf returns the lane that closes gaps on an item: A engine/container,
// R registry/image, S swarm, K compose, B buildx.
func laneOf(itemID string) string {
	switch {
	case strings.HasPrefix(itemID, "swarm:"), itemID == "cli-only:stack", strings.HasPrefix(itemID, "cli-only:stack"):
		return "S"
	case strings.HasPrefix(itemID, "compose:"):
		return "K"
	case strings.HasPrefix(itemID, "buildx:"), itemID == "engine:build-prune":
		return "B"
	}
	switch itemID {
	case "engine:image", "engine:registry-image", "engine:tag", "engine:image-import", "engine:image-load",
		"engine:image-save", "engine:image-prune", "engine:image-commit", "engine:registry-image-data",
		"cli-only:manifest-list", "engine:registry-token":
		return "R"
	}
	return "A"
}

// composeItem is the inventory item a compose definition's fields belong to.
func composeItem(def string) string {
	switch def {
	case "service":
		return "compose:services"
	case "network":
		return "compose:networks"
	case "volume":
		return "compose:volumes"
	case "config":
		return "compose:configs"
	case "secret":
		return "compose:secrets"
	case "model":
		return "compose:models"
	case "include":
		return "compose:include"
	}
	return "compose:" + def
}

// ---------------------------------------------------------------------------
// File families: which provider files implement which items

// sharedFiles are helpers several item families call (mount and port
// expansion, moby conversions, client plumbing); a write there counts for
// every item.
var sharedFiles = map[string]bool{
	"container_expand.go": true, "moby_convert.go": true, "convert.go": true, "client.go": true,
	"seams.go": true, "action_common.go": true, "obj_common.go": true,
}

// familyPrefixes maps an item to the file-name prefixes that implement it.
func familyPrefixes(itemID string) []string {
	switch {
	case itemID == "engine:exec" || itemID == "engine:exec-output":
		return []string{"action_exec", "cop_exec", "ephemeral_exec"}
	case itemID == "engine:container" || strings.HasPrefix(itemID, "engine:container-"):
		return []string{"container_", "ctr_", "cop_container", "cop_common", "action_container"}
	case itemID == "swarm:service" || strings.HasPrefix(itemID, "swarm:service-") || itemID == "cli-only:stack":
		return []string{"service_", "action_service", "stack_"}
	case itemID == "swarm:secret":
		return []string{"secret_"}
	case itemID == "swarm:config":
		return []string{"docker_config_"}
	case itemID == "swarm:node" || itemID == "swarm:swarm-node" || strings.HasPrefix(itemID, "swarm:node-"):
		return []string{"node_", "swarm_node", "swarm_common", "action_swarm"}
	case strings.HasPrefix(itemID, "swarm:"):
		return []string{"swarm_", "action_swarm", "ephemeral_swarm"}
	case itemID == "engine:network" || itemID == "engine:network-prune":
		return []string{"network_", "action_prune"}
	case itemID == "engine:volume" || itemID == "engine:volume-prune":
		return []string{"volume_", "obj_cluster_volume", "action_prune"}
	case strings.HasPrefix(itemID, "engine:plugin"):
		return []string{"plugin_", "obj_plugin"}
	case strings.HasPrefix(itemID, "buildx:") || itemID == "engine:build-prune":
		return []string{"image_build", "img_build", "img_buildx", "buildkit_", "buildx_", "action_buildx"}
	case itemID == "engine:image" || itemID == "engine:tag" || itemID == "engine:registry-image" ||
		strings.HasPrefix(itemID, "engine:image-") || itemID == "cli-only:manifest-list":
		return []string{"image_", "img_", "reg_", "registry_image", "tag_", "action_image", "action_prune"}
	case strings.HasSuffix(itemID, "-prune"):
		return []string{"action_prune", "buildkit_prune"}
	}
	return nil
}

// allFamilyPrefixes is the union of every family's prefixes.
func allFamilyPrefixes() []string {
	ids := []string{"engine:exec", "engine:container", "swarm:service", "swarm:secret", "swarm:config", "swarm:node",
		"swarm:swarm-init", "engine:network", "engine:volume", "engine:plugin", "buildx:build", "engine:image",
		"engine:system-prune"}
	var out []string
	for _, id := range ids {
		out = append(out, familyPrefixes(id)...)
	}
	return out
}

// creditsItem reports whether a write at pos ("file.go:line") counts for
// one of the items: a shared file counts for all, a family file only for its
// own family.
func creditsItem(pos string, itemIDs []string) bool {
	f, _, _ := strings.Cut(pos, ":")
	if sharedFiles[f] {
		return true
	}
	specific := false
	for _, p := range allFamilyPrefixes() {
		if strings.HasPrefix(f, p) {
			specific = true
			break
		}
	}
	if !specific {
		return true
	}
	for _, id := range itemIDs {
		for _, p := range familyPrefixes(id) {
			if strings.HasPrefix(f, p) {
				return true
			}
		}
	}
	return false
}
