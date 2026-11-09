package provider

// Compose service builds (compose:service-build): a service with a `build:`
// section is built through the BuildKit seam (seamBuildKitImpl, Lane E) and
// tagged with its image name, like `docker compose up` / `docker compose
// build`. Used by docker_compose (local mode) and docker_compose_build.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

// composeImageName is the image a service runs: `image`, or
// `<project>-<service>` for a build-only service (docker compose v2 naming).
func composeImageName(project string, svc ctypes.ServiceConfig) string {
	if svc.Image != "" {
		return svc.Image
	}
	return strings.ToLower(project + "-" + svc.Name)
}

// composeBuildRequest maps a service's build section onto the seam request.
// Secrets resolve against the project's top-level secrets: file sources
// become Src, environment sources become Value from the project environment.
func composeBuildRequest(p *ctypes.Project, svc ctypes.ServiceConfig, image string) (seamBuildRequest, error) {
	b := svc.Build
	if b == nil {
		return seamBuildRequest{}, fmt.Errorf("service %s has no build section", svc.Name)
	}
	req := seamBuildRequest{
		ContextDir:       b.Context,
		Dockerfile:       b.Dockerfile,
		DockerfileInline: b.DockerfileInline,
		Target:           b.Target,
		Platforms:        append([]string(nil), b.Platforms...),
		Labels:           map[string]string(b.Labels),
		CacheFrom:        append([]string(nil), b.CacheFrom...),
		CacheTo:          append([]string(nil), b.CacheTo...),
		Network:          b.Network,
		NoCache:          b.NoCache,
		NoCacheFilter:    append([]string(nil), b.NoCacheFilter...),
		Pull:             b.Pull,
		ShmSize:          int64(b.ShmSize),
		Outputs:          seamBuildOutputs{Names: append([]string{image}, b.Tags...), Load: true},
	}
	if req.ContextDir == "" {
		req.ContextDir = p.WorkingDir
	}
	if len(req.Platforms) == 0 && svc.Platform != "" {
		req.Platforms = []string{svc.Platform}
	}
	if len(b.Args) > 0 {
		req.BuildArgs = map[string]string{}
		for k, v := range b.Args {
			if v != nil {
				req.BuildArgs[k] = *v
			}
		}
	}
	req.ExtraHosts = b.ExtraHosts.AsList(":")
	sort.Strings(req.ExtraHosts)
	for _, name := range composeSortedKeys(b.Ulimits) {
		u := b.Ulimits[name]
		if u == nil {
			continue
		}
		if u.Single != 0 {
			req.Ulimits = append(req.Ulimits, fmt.Sprintf("%s=%d", name, u.Single))
		} else {
			req.Ulimits = append(req.Ulimits, fmt.Sprintf("%s=%d:%d", name, u.Soft, u.Hard))
		}
	}
	for _, k := range b.SSH {
		s := seamBuildSSH{ID: k.ID}
		if k.Path != "" {
			s.Paths = []string{k.Path}
		}
		req.SSH = append(req.SSH, s)
	}
	for _, ref := range b.Secrets {
		obj, ok := p.Secrets[ref.Source]
		if !ok {
			return req, fmt.Errorf("service %s build secret %s is not a top-level secret", svc.Name, ref.Source)
		}
		id := ref.Target
		if id == "" {
			id = ref.Source
		}
		switch {
		case obj.File != "":
			req.Secrets = append(req.Secrets, seamBuildSecret{ID: id, Src: obj.File})
		default:
			data, ok, err := composeBlobData(ctypes.FileObjectConfig(obj), p.Environment)
			if err != nil {
				return req, fmt.Errorf("service %s build secret %s: %w", svc.Name, ref.Source, err)
			}
			if !ok {
				return req, fmt.Errorf("service %s build secret %s: external secrets cannot be used in builds", svc.Name, ref.Source)
			}
			req.Secrets = append(req.Secrets, seamBuildSecret{ID: id, Value: string(data)})
		}
	}
	if b.Provenance != "" {
		req.Attestations.Provenance = b.Provenance
	}
	if b.SBOM != "" && b.SBOM != "false" {
		req.Attestations.SBOM = true
	}
	return req, nil
}

// composeBuild builds one service and returns the seam result.
func composeBuild(ctx context.Context, pd *providerData, p *ctypes.Project, svc ctypes.ServiceConfig, progress func(string)) (*seamBuildResult, error) {
	image := composeImageName(p.Name, svc)
	req, err := composeBuildRequest(p, svc, image)
	if err != nil {
		return nil, err
	}
	req.Progress = progress
	res, err := seamBuildKitImpl.Build(ctx, pd, req)
	if err != nil {
		return nil, fmt.Errorf("building service %s (%s): %w", svc.Name, image, err)
	}
	return res, nil
}

// composeImageExists reports whether ref is in the daemon's image store.
func composeImageExists(ctx context.Context, pd *providerData, ref string) (bool, error) {
	if _, err := pd.Client.ImageInspect(ctx, ref); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
