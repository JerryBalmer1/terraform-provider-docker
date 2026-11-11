package provider

// Pass 6d Lane N: data.docker_registry_image reads the image config blob
// from the registry, so `config`, `labels` and `created` are filled on both
// the daemon path and the direct path (insecure_skip_verify set). The
// daemon's GET /distribution/<name>/json returns no image config, so on the
// daemon path the provider asks the registry itself (Registry HTTP API v2,
// registry_auth, TLS verification on; localhost / 127.0.0.1 registries fall
// back to plain http the way the daemon does, see p3Registry.send).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/client"
)

// regImageDataHostFilter, when set, is asked before the daemon path reads
// a registry directly; a non-nil error skips the config read (warning, null
// config). nil (production) allows every registry. Unit tests set it so a
// fake-engine read of e.g. nginx:1.27 never reaches Docker Hub.
var regImageDataHostFilter func(host string) error

// regImageConfig is what one image config blob gives the data source.
type regImageConfig struct {
	Config   *dockerspec.DockerOCIImageConfig
	Labels   map[string]string
	Created  string // RFC 3339 (nano), "" when the blob has none
	Platform string // os/arch[/variant] of the blob
}

// regDecodeImageConfig decodes an OCI / Docker image config blob. Its
// `config` object has the same type data.docker_image gets from
// ImageInspect.Config (dockerspec.DockerOCIImageConfig), so both data
// sources fill `config` with the same shape.
func regDecodeImageConfig(blob []byte) (*regImageConfig, error) {
	var img dockerspec.DockerOCIImage
	if err := json.Unmarshal(blob, &img); err != nil {
		return nil, fmt.Errorf("decoding image config: %w", err)
	}
	cfg := img.Config
	out := &regImageConfig{Config: &cfg, Labels: cfg.Labels}
	if img.Created != nil && !img.Created.IsZero() {
		// The daemon's ImageInspect.Created uses the same layout.
		out.Created = img.Created.UTC().Format(time.RFC3339Nano)
	}
	if img.OS != "" || img.Architecture != "" {
		out.Platform = regPlatformString(img.OS, img.Architecture, img.Variant)
	}
	return out, nil
}

// regDaemonPlatform returns the daemon's os/arch (GET /version), or "", ""
// when the provider has no daemon client or the call fails.
func regDaemonPlatform(ctx context.Context, pd *providerData) (string, string) {
	if pd == nil || pd.Client == nil {
		return "", ""
	}
	v, err := pd.Client.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return "", ""
	}
	return v.Os, v.Arch
}

// regPickIndexEntry picks the entry of an index whose config the data source
// reports: the first entry matching the daemon's os/arch, else the first
// linux/amd64 entry, else the first entry. Attestation manifests never
// count. nil when the index holds no image manifest.
func regPickIndexEntry(manifests []regDescriptor, wantOS, wantArch string) *regDescriptor {
	var cands []regDescriptor
	for _, d := range manifests {
		if !regIsAttestation(d) {
			cands = append(cands, d)
		}
	}
	match := func(os, arch string) *regDescriptor {
		if os == "" || arch == "" {
			return nil
		}
		for i := range cands {
			if p := cands[i].Platform; p != nil && p.OS == os && p.Architecture == arch {
				return &cands[i]
			}
		}
		return nil
	}
	if d := match(wantOS, wantArch); d != nil {
		return d
	}
	if d := match("linux", "amd64"); d != nil {
		return d
	}
	if len(cands) > 0 {
		return &cands[0]
	}
	return nil
}

// regReadImageConfig reads the image config blob behind top: its own config
// descriptor for an image manifest, or the picked entry's (regPickIndexEntry)
// for an index. singleCfg, when non-nil, is the already read config blob of
// an image manifest top. nil, nil when top references no config.
func regReadImageConfig(ctx context.Context, pd *providerData, r *p3Registry, top *regFetched, singleCfg []byte) (*regImageConfig, error) {
	if !top.isIndex() {
		if singleCfg == nil {
			if top.Doc.Config == nil || top.Doc.Config.Digest == "" {
				return nil, nil
			}
			blob, err := regGetBlob(ctx, r, top.Doc.Config.Digest)
			if err != nil {
				return nil, err
			}
			singleCfg = blob
		}
		return regDecodeImageConfig(singleCfg)
	}
	os, arch := regDaemonPlatform(ctx, pd)
	entry := regPickIndexEntry(top.Doc.Manifests, os, arch)
	if entry == nil {
		return nil, nil
	}
	child, err := regGetManifest(ctx, r, entry.Digest)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", entry.Digest, err)
	}
	if child.isIndex() || child.Doc.Config == nil || child.Doc.Config.Digest == "" {
		return nil, nil
	}
	blob, err := regGetBlob(ctx, r, child.Doc.Config.Digest)
	if err != nil {
		return nil, fmt.Errorf("reading image config of %s: %w", entry.Digest, err)
	}
	return regDecodeImageConfig(blob)
}

// regDaemonPathImageConfig reads the image config of name at digest (the
// digest the daemon reported, so the config belongs to the same content)
// straight from the registry. The error says why it could not.
func regDaemonPathImageConfig(ctx context.Context, pd *providerData, name, digest string) (*regImageConfig, error) {
	p, err := parseImageRef(name)
	if err != nil {
		return nil, err
	}
	r := newP3Registry(pd, p.Registry, p.Repository, false)
	if regImageDataHostFilter != nil {
		if err := regImageDataHostFilter(r.host); err != nil {
			return nil, err
		}
	}
	top, err := regGetManifest(ctx, r, digest)
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", r.host, err)
	}
	cfg, err := regReadImageConfig(ctx, pd, r, top, nil)
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", r.host, err)
	}
	return cfg, nil
}

// regImageConfigWarning is the detail of the warning when the image config
// could not be read; the read itself still succeeds.
func regImageConfigWarning(name string, daemonPath bool, err error) string {
	if daemonPath {
		return fmt.Sprintf("The daemon returned the digest and platforms of %s, but GET /distribution returns no image config, "+
			"and the provider could not read the config blob from the registry itself: %v. "+
			"`config`, `labels` and `created` are null. If the daemon trusts this registry only through its insecure-registries "+
			"setting (a certificate the provider host does not trust), set `insecure_skip_verify = true` so the provider reads the "+
			"registry directly; make sure registry_auth covers it. A plain-HTTP registry on a host other than localhost or a loopback "+
			"address is never read over plain HTTP by the provider, so its config stays null.", name, err)
	}
	return fmt.Sprintf("Read the manifest of %s, but not its image config blob: %v. `config`, `labels` and `created` are null.", name, err)
}
