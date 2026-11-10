package provider

// Pass 5.3 Lane B: multi-platform builds through the BuildKit seam
// (`docker buildx build --platform a,b`) and per-step resource limits
// (`--resource`, bake `target.resources`).
//
// A multi-platform build produces an OCI image index. What happens to it:
//   - pushed (`type=registry`, `outputs`, bake `push`): the index is pushed
//     as is; `digest` is the index digest.
//   - loaded into the daemon (docker_image always loads; bake `load`): the
//     daemon's image store must hold an index, which only the containerd
//     image store does (GET /info DriverStatus has driver-type =
//     io.containerd.snapshotter.v1, set by moby's daemon/containerd
//     ImageService.LayerStoreStatus). The graph-driver store cannot: moby's
//     own exporter rejects several references ("exporting multiple
//     references to image store is currently unsupported",
//     builder/builder-next/exporter/mobyexporter/export.go) and BuildKit's
//     docker exporter rejects an index ("docker exporter does not currently
//     support exporting manifest lists", buildkit exporter/oci/export.go).
//     The provider checks the store before building and fails with a clear
//     error instead. On a containerd store, the daemon's own BuildKit loads
//     through the moby exporter, and other builders stream an OCI tarball
//     (exporter `oci`) into POST /images/load, as buildx does.
//   - the image ID of the loaded index is the index digest (the containerd
//     store's image ID is the target descriptor's digest), so docker_image's
//     `id` and `digest` are both the index digest and `platforms` lists
//     every platform built.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	units "github.com/docker/go-units"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/moby/client"
)

// bkContainerdSnapshotter is the DriverStatus driver-type value of a daemon
// that uses the containerd image store.
const bkContainerdSnapshotter = "io.containerd.snapshotter.v1"

// bkImageStore reports whether the daemon uses the containerd image store,
// and its storage driver name; a var so unit tests can fake it.
var bkImageStore = func(ctx context.Context, pd *providerData) (containerd bool, driver string, err error) {
	if pd == nil || pd.Client == nil {
		return false, "", fmt.Errorf("provider not configured")
	}
	res, err := pd.Client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return false, "", err
	}
	return bkIsContainerdStore(res.Info.DriverStatus), res.Info.Driver, nil
}

// bkIsContainerdStore reads /info DriverStatus.
func bkIsContainerdStore(status [][2]string) bool {
	for _, kv := range status {
		if kv[0] == "driver-type" && kv[1] == bkContainerdSnapshotter {
			return true
		}
	}
	return false
}

// bkMultiPlatformExports checks and adapts the exports of a build for
// several platforms (see the file comment).
func bkMultiPlatformExports(ctx context.Context, pd *providerData, conn *bkConn, platforms []string, p *bkPrepared) error {
	if len(platforms) < 2 {
		return nil
	}
	intoDaemon := len(p.loads) > 0
	for _, e := range p.opt.Exports {
		if e.Type == "moby" {
			intoDaemon = true
		}
	}
	if !intoDaemon {
		return nil
	}
	ok, driver, err := bkImageStore(ctx, pd)
	if err != nil {
		return fmt.Errorf("multi-platform build (%s): cannot tell whether the daemon uses the containerd image store: %w",
			strings.Join(platforms, ","), err)
	}
	if !ok {
		where := "loading it into the daemon"
		if conn != nil && conn.Docker {
			where = "building it with the daemon's own BuildKit (whose results always go to the daemon's image store)"
		}
		return fmt.Errorf("multi-platform build (%s) produces an OCI image index, and %s needs the containerd image store, which this "+
			"daemon does not use (GET /info: storage driver %q, no DriverStatus driver-type=%s). Enable it (dockerd "+
			"\"features\": {\"containerd-snapshotter\": true}, or Docker Desktop's \"Use containerd for pulling and storing images\"), "+
			"build one platform per image, or build on a docker-container builder and push without loading (docker_buildx_bake "+
			"with push = true and load = false)", strings.Join(platforms, ","), where, driver, bkContainerdSnapshotter)
	}
	for _, l := range p.loads {
		if l.idx >= 0 && l.idx < len(p.opt.Exports) && p.opt.Exports[l.idx].Type == bkclient.ExporterDocker {
			// The docker exporter cannot write an index; the containerd
			// store loads an OCI layout tarball.
			p.opt.Exports[l.idx].Type = bkclient.ExporterOCI
		}
	}
	return nil
}

// bkResourceKeys maps buildx `--resource` / bake `resources` keys to the
// dockerfile frontend options (buildkit frontend/dockerui/config.go:
// keyMemory "memory", keyMemorySwap "memswap", keyCPUShares "cpushares",
// keyCPUPeriod "cpuperiod", keyCPUQuota "cpuquota", keyCpusetCpus
// "cpusetcpus", keyCpusetMems "cpusetmems").
var bkResourceKeys = map[string]string{
	"memory":      "memory",
	"memory-swap": "memswap",
	"cpu-shares":  "cpushares",
	"cpu-period":  "cpuperiod",
	"cpu-quota":   "cpuquota",
	"cpuset-cpus": "cpusetcpus",
	"cpuset-mems": "cpusetmems",
}

// bkApplyResources adds per-step resource limits to the frontend options.
// Memory sizes take units (`2g`); memory-swap also takes -1 (unlimited).
// BuildKit applies them only when its solver has the
// exec.meta.linux.resources capability; older daemons ignore them.
func bkApplyResources(attrs map[string]string, res map[string]string) error {
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(res[k])
		key := strings.ReplaceAll(strings.ToLower(k), "_", "-")
		fa, ok := bkResourceKeys[key]
		if !ok {
			return fmt.Errorf("resource %q: unknown key (memory, memory-swap, cpu-shares, cpu-period, cpu-quota, cpuset-cpus, cpuset-mems)", k)
		}
		if v == "" {
			continue
		}
		switch key {
		case "memory", "memory-swap":
			if key == "memory-swap" && v == "-1" {
				break
			}
			n, err := units.RAMInBytes(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("resource %s=%q: expected a size such as 512m or 2g", k, v)
			}
			v = strconv.FormatInt(n, 10)
		case "cpu-shares", "cpu-period", "cpu-quota":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("resource %s=%q: expected a positive integer", k, v)
			}
		}
		attrs[fa] = v
	}
	return nil
}
