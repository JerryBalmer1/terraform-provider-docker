package provider

// Registry v2 manifest reads shared by docker_registry_image_manifests and
// docker_registry_manifest_list: the raw top-level document, child
// manifests, image config blobs and in-toto attestations.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Annotations BuildKit puts on attestation manifests in an index.
const (
	regAnnRefType      = "vnd.docker.reference.type"
	regAnnRefDigest    = "vnd.docker.reference.digest"
	regAttestationType = "attestation-manifest"
	regAnnPredicate    = "in-toto.io/predicate-type"
)

// regDescriptor is an OCI content descriptor (field order is the order
// written into a pushed index).
type regDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	URLs         []string          `json:"urls,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Platform     *ocispec.Platform `json:"platform,omitempty"`
}

// regManifestDoc covers image manifests, OCI indexes and manifest lists.
type regManifestDoc struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        *regDescriptor    `json:"config,omitempty"`
	Layers        []regDescriptor   `json:"layers,omitempty"`
	Manifests     []regDescriptor   `json:"manifests,omitempty"`
	Subject       *regDescriptor    `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// regFetched is one manifest as the registry served it.
type regFetched struct {
	Body      []byte
	Digest    string
	MediaType string
	Doc       regManifestDoc
}

// isIndex reports an OCI index or Docker manifest list.
func (f *regFetched) isIndex() bool {
	return f.MediaType == p3MediaOCIIndex || f.MediaType == p3MediaDockerList
}

// regErrNotFound marks a 404 from the registry.
var regErrNotFound = errors.New("not found in registry")

// regGetManifest GETs /v2/<repo>/manifests/<ref>.
func regGetManifest(ctx context.Context, r *p3Registry, ref string) (*regFetched, error) {
	resp, err := r.get(ctx, "/v2/"+r.repo+"/manifests/"+ref, p3ManifestAccept...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("manifest %s:%s: %w", r.repo, ref, regErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET manifest %s:%s: %s: %s", r.repo, ref, resp.Status, strings.TrimSpace(string(body)))
	}
	f := &regFetched{Body: body}
	if err := json.Unmarshal(body, &f.Doc); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	f.MediaType = f.Doc.MediaType
	if f.MediaType == "" {
		mt, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
		f.MediaType = strings.TrimSpace(mt)
	}
	f.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	if d := resp.Header.Get("Docker-Content-Digest"); d != "" && !strings.HasPrefix(ref, "sha256:") {
		f.Digest = d
	}
	switch f.MediaType {
	case p3MediaOCIIndex, p3MediaDockerList, p3MediaOCIManifest, p3MediaDockerManif2:
	default:
		return nil, fmt.Errorf("unsupported manifest media type %q", f.MediaType)
	}
	return f, nil
}

// regGetBlob GETs a blob (at most 32 MiB).
func regGetBlob(ctx context.Context, r *p3Registry, digest string) ([]byte, error) {
	resp, err := r.get(ctx, "/v2/"+r.repo+"/blobs/"+digest)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET blob %s@%s: %s: %s", r.repo, digest, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// regIsAttestation reports an attestation manifest entry of an index.
func regIsAttestation(d regDescriptor) bool {
	if d.Annotations[regAnnRefType] == regAttestationType {
		return true
	}
	return d.Platform != nil && d.Platform.OS == "unknown" && d.Platform.Architecture == "unknown"
}

// regPlatformOf renders a descriptor's platform ("" when it has none).
func regPlatformOf(d regDescriptor) string {
	if d.Platform == nil {
		return ""
	}
	return regPlatformString(d.Platform.OS, d.Platform.Architecture, d.Platform.Variant)
}

// regInspection is what docker_registry_image_manifests reports.
type regInspection struct {
	Top        *regFetched
	Platforms  []string
	Configs    map[string]json.RawMessage // platform -> config blob (index)
	Provenance map[string]any             // platform -> {"SLSA": predicate}
	SBOM       map[string]any             // platform -> {"SPDX": predicate}
	SingleCfg  []byte                     // config blob of a single manifest
}

// regInspect reads ref and, unless shallow, every image config and the
// SLSA provenance / SPDX SBOM attestations of an index (the content of
// docker buildx imagetools inspect --format with .Image, .Provenance, .SBOM).
func regInspect(ctx context.Context, r *p3Registry, ref string, shallow bool) (*regInspection, error) {
	top, err := regGetManifest(ctx, r, ref)
	if err != nil {
		return nil, err
	}
	in := &regInspection{Top: top, Configs: map[string]json.RawMessage{}, Provenance: map[string]any{}, SBOM: map[string]any{}}
	if !top.isIndex() {
		if top.Doc.Config != nil && top.Doc.Config.Digest != "" {
			blob, err := regGetBlob(ctx, r, top.Doc.Config.Digest)
			if err != nil {
				return nil, fmt.Errorf("reading image config: %w", err)
			}
			in.SingleCfg = blob
			var p ocispec.Platform
			if json.Unmarshal(blob, &p) == nil && p.OS != "" {
				in.Platforms = []string{regPlatformString(p.OS, p.Architecture, p.Variant)}
			}
		}
		return in, nil
	}
	byDigest := map[string]string{}
	for _, d := range top.Doc.Manifests {
		if regIsAttestation(d) {
			continue
		}
		plat := regPlatformOf(d)
		byDigest[d.Digest] = plat
		in.Platforms = append(in.Platforms, plat)
	}
	if shallow {
		return in, nil
	}
	for _, d := range top.Doc.Manifests {
		if regIsAttestation(d) {
			plat, ok := byDigest[d.Annotations[regAnnRefDigest]]
			if !ok {
				continue
			}
			if err := regReadAttestation(ctx, r, d.Digest, plat, in); err != nil {
				return nil, fmt.Errorf("reading attestation %s: %w", d.Digest, err)
			}
			continue
		}
		child, err := regGetManifest(ctx, r, d.Digest)
		if err != nil {
			return nil, fmt.Errorf("reading manifest %s: %w", d.Digest, err)
		}
		if child.Doc.Config == nil || child.Doc.Config.Digest == "" {
			continue
		}
		blob, err := regGetBlob(ctx, r, child.Doc.Config.Digest)
		if err != nil {
			return nil, fmt.Errorf("reading image config of %s: %w", d.Digest, err)
		}
		in.Configs[regPlatformOf(d)] = json.RawMessage(blob)
	}
	return in, nil
}

// regReadAttestation reads one attestation manifest and files its SLSA
// provenance and SPDX SBOM predicates under plat.
func regReadAttestation(ctx context.Context, r *p3Registry, digest, plat string, in *regInspection) error {
	att, err := regGetManifest(ctx, r, digest)
	if err != nil {
		return err
	}
	for _, l := range att.Doc.Layers {
		pt := l.Annotations[regAnnPredicate]
		var key string
		var dst map[string]any
		switch {
		case strings.Contains(pt, "slsa.dev/provenance"):
			key, dst = "SLSA", in.Provenance
		case strings.Contains(pt, "spdx.dev/Document"):
			key, dst = "SPDX", in.SBOM
		default:
			continue
		}
		blob, err := regGetBlob(ctx, r, l.Digest)
		if err != nil {
			return err
		}
		var stmt struct {
			Predicate json.RawMessage `json:"predicate"`
		}
		if err := json.Unmarshal(blob, &stmt); err != nil {
			return fmt.Errorf("decoding in-toto statement %s: %w", l.Digest, err)
		}
		dst[plat] = map[string]any{key: stmt.Predicate}
	}
	return nil
}
