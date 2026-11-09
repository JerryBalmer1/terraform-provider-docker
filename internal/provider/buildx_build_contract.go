package provider

// Pass 5 Lane E: the outputs contract of buildx:build (docker_image's
// BuildKit build). Lane A owns docker_image's schema and model; it embeds
// contractBuildxBuild and, after seamBuildKitImpl.Build, calls
//
//	in, ident := buildxBuildContract(req, res)
//	diags.Append(m.contractBuildxBuild.contractFill(ctx, pd, in)...)
//
// and sets the identity attributes it owns from ident.

import (
	"encoding/json"
	"os"
)

// buildxBuildIdentity holds the identity outputs of a build.
type buildxBuildIdentity struct {
	// ID is the image ID (config digest).
	ID string
	// Digest is the manifest / index digest (containerimage.digest).
	Digest string
	// ImageDigestRef is repo@digest of the first pushed name.
	ImageDigestRef string
	// Platforms are the platforms built.
	Platforms []string
	// BuildRef is builder/node/ref (input for docker_buildx_history).
	BuildRef string
}

// buildxBuildDockerfile returns the Dockerfile text of a request: the
// inline text, else the file it names ("" when unreadable or remote).
func buildxBuildDockerfile(req seamBuildRequest) string {
	if req.DockerfileInline != "" {
		return req.DockerfileInline
	}
	if req.ContextDir == "" || bkRemoteContext(req.ContextDir) {
		return ""
	}
	b, err := os.ReadFile(bkDockerfilePath(expandHome(req.ContextDir), req.Dockerfile))
	if err != nil {
		return ""
	}
	return string(b)
}

// buildxBuildContract returns the contract fill input (metadata_json,
// dockerfile, build_metadata, dockerfile_syntax) and the identity values
// of a finished build.
func buildxBuildContract(req seamBuildRequest, res *seamBuildResult) (contractFillInput, buildxBuildIdentity) {
	in := contractFillInput{Raw: map[string][]byte{}, Structured: map[string]any{}}
	var id buildxBuildIdentity
	if res == nil {
		return in, id
	}
	meta := res.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	b, _ := json.Marshal(meta)
	in.Raw["metadata_json"] = b
	in.Structured["build_metadata"] = meta
	if df := buildxBuildDockerfile(req); df != "" {
		in.Raw["dockerfile"] = []byte(df)
		in.DockerfileSyntax = bkDockerfileSyntax(df)
	}
	id.ID = res.ImageID
	id.Digest = res.Digest
	id.ImageDigestRef = res.ImageDigestRef
	id.Platforms = append([]string{}, res.Platforms...)
	if s, ok := meta["buildx.build.ref"].(string); ok {
		id.BuildRef = s
	}
	return in, id
}
