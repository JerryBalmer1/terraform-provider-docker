package provider

// Pass 5 Lane E: the buildx builder cache prune seam (seamBuildxPruneImpl),
// `docker buildx prune --builder <name>` over BuildKit's control API.
// docker_builder_prune (Lane A) calls it when `builder` is set.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	bkclient "github.com/moby/buildkit/client"
)

func init() { seamBuildxPruneImpl = laneEBuildxPrune{} }

// laneEBuildxPrune implements seamBuildxPrune.
type laneEBuildxPrune struct{}

// Prune prunes the cache of every running node of req.Builder.
func (laneEBuildxPrune) Prune(ctx context.Context, pd *providerData, req seamBuildxPruneRequest) (seamBuildxPruneResult, error) {
	var res seamBuildxPruneResult
	opts, err := bkPruneOptions(req)
	if err != nil {
		return res, err
	}
	nodes, err := bkBuilderNodes(ctx, pd, req.Builder)
	if err != nil {
		return res, err
	}
	pruned := 0
	for _, n := range nodes {
		if n.Status != bkStatusRunning {
			continue
		}
		c, err := bkOpen(ctx, pd, n)
		if err != nil {
			return res, fmt.Errorf("builder %q node %s: %w", n.Builder, n.Name, err)
		}
		r, err := bkPruneClient(ctx, c, opts)
		_ = c.Close()
		if err != nil {
			return res, fmt.Errorf("builder %q node %s: prune: %w", n.Builder, n.Name, err)
		}
		res.CachesDeleted = append(res.CachesDeleted, r.CachesDeleted...)
		res.SpaceReclaimed += r.SpaceReclaimed
		pruned++
	}
	if pruned == 0 {
		return res, fmt.Errorf("builder %q has no running node to prune (start it, or bootstrap it with docker_buildx_builder)", req.Builder)
	}
	sort.Strings(res.CachesDeleted)
	return res, nil
}

// bkPruneOptions maps the request onto BuildKit prune options. filters
// use buildx's names: until (a Go duration, becoming keep-duration) and
// BuildKit filter keys (id, type, description, inuse, shared, private).
func bkPruneOptions(req seamBuildxPruneRequest) ([]bkclient.PruneOption, error) {
	var opts []bkclient.PruneOption
	if req.All {
		opts = append(opts, bkclient.PruneAll)
	}
	var keep time.Duration
	var filters []string
	keys := make([]string, 0, len(req.Filters))
	for k := range req.Filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range req.Filters[k] {
			switch strings.ToLower(k) {
			case "until":
				d, err := time.ParseDuration(v)
				if err != nil {
					return nil, fmt.Errorf("filter until=%q: %w", v, err)
				}
				keep = d
			case "unused-for", "unusedfor":
				d, err := time.ParseDuration(v)
				if err != nil {
					return nil, fmt.Errorf("filter %s=%q: %w", k, v, err)
				}
				keep = d
			default:
				op := "=="
				if strings.HasPrefix(v, "~") || strings.Contains(k, "~") {
					op = "~="
					v = strings.TrimPrefix(v, "~")
				}
				filters = append(filters, strings.TrimSuffix(k, "~")+op+v)
			}
		}
	}
	reserved := req.ReservedSpace
	if reserved == 0 {
		reserved = req.KeepStorage
	}
	if keep > 0 || reserved > 0 || req.MaxUsedSpace > 0 || req.MinFreeSpace > 0 {
		opts = append(opts, bkclient.WithKeepOpt(keep, reserved, req.MaxUsedSpace, req.MinFreeSpace))
	}
	if len(filters) > 0 {
		opts = append(opts, bkclient.WithFilter(filters))
	}
	return opts, nil
}

// bkPruneClient runs one prune and collects what it removed.
func bkPruneClient(ctx context.Context, c *bkclient.Client, opts []bkclient.PruneOption) (seamBuildxPruneResult, error) {
	var res seamBuildxPruneResult
	ch := make(chan bkclient.UsageInfo)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for u := range ch {
			res.CachesDeleted = append(res.CachesDeleted, u.ID)
			res.SpaceReclaimed += u.Size
		}
	}()
	err := c.Prune(ctx, ch, opts...)
	close(ch)
	wg.Wait()
	return res, err
}
