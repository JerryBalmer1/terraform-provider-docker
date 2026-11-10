package provider

// docker_compose_publish (compose:cli-publish): the layers `docker compose
// publish` adds besides the compose files (compose v5.1.4
// pkg/compose/publish.go:163-195 createLayers):
//
//   - every service env_file path in a published compose file is replaced by
//     "<sha256 of the absolute path>.env" (publish.go:230-260 processFile,
//     transform/replace.go:65-106 ReplaceEnvFile, done on the YAML text so
//     the rest of the file keeps its formatting);
//   - with `--with-env`, each env file becomes an
//     application/vnd.docker.compose.envfile layer named that way
//     (publish.go:626-638 envFileLayers);
//   - with `--resolve-image-digests`, an "image-digests.yaml" layer pins
//     every service image to its digest (publish.go:265-280
//     generateImageDigestsOverride, resolved through the Engine API's
//     GET /distribution/{name}/json like pull.go:259-274
//     ImageDigestResolver).

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"github.com/moby/moby/client"
	"gopkg.in/yaml.v3"
)

// composeEnvFileLayerName is the published name of an env file.
func composeEnvFileLayerName(absPath string) string {
	return fmt.Sprintf("%x.env", sha256.Sum256([]byte(absPath)))
}

// composePublishLayers rewrites the env_file paths of the compose files
// and appends the env file and image digest layers.
func composePublishLayers(ctx context.Context, pd *providerData, p *ctypes.Project, files []composeArtifactFile, withEnv, resolveDigests bool) ([]composeArtifactFile, error) {
	out := make([]composeArtifactFile, 0, len(files)+2)
	envFiles := map[string]string{} // absolute path -> layer name
	var envOrder []string
	for _, f := range files {
		content, paths, err := composeRewriteEnvFiles(f.Content, p.WorkingDir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		for _, ap := range paths {
			if _, ok := envFiles[ap]; !ok {
				envFiles[ap] = composeEnvFileLayerName(ap)
				envOrder = append(envOrder, ap)
			}
		}
		f.Content = content
		out = append(out, f)
	}
	if withEnv {
		// Also the env files compose-go resolved (absolute already), in
		// case a file was referenced only through include/extends.
		for _, name := range composeSortedKeys(p.Services) {
			for _, ef := range p.Services[name].EnvFiles {
				if _, ok := envFiles[ef.Path]; !ok {
					envFiles[ef.Path] = composeEnvFileLayerName(ef.Path)
					envOrder = append(envOrder, ef.Path)
				}
			}
		}
		for _, ap := range envOrder {
			b, err := os.ReadFile(ap)
			if err != nil {
				continue // like envFileLayers: an unreadable file is skipped
			}
			out = append(out, composeArtifactFile{Name: envFiles[ap], Content: b, MediaType: composeEnvFileMedia})
		}
	}
	if resolveDigests {
		y, err := composeImageDigestsOverride(ctx, pd, p)
		if err != nil {
			return nil, err
		}
		out = append(out, composeArtifactFile{Name: "image-digests.yaml", Content: y})
	}
	return out, nil
}

// composeRewriteEnvFiles replaces each services.<s>.env_file entry (a
// string, a list of strings, or a list of {path: ...}) in a compose file
// by its layer name, and returns the absolute paths it found.
func composeRewriteEnvFiles(in []byte, workingDir string) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return in, nil, nil
	}
	services := composeYAMLGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return in, nil, nil
	}
	type edit struct {
		line, col, n int
		value        string
	}
	var edits []edit
	var paths []string
	add := func(n *yaml.Node) {
		if n == nil || n.Kind != yaml.ScalarNode {
			return
		}
		ap := n.Value
		if !filepath.IsAbs(ap) {
			ap = filepath.Join(workingDir, ap)
		}
		paths = append(paths, ap)
		width := len(n.Value)
		if n.Style == yaml.DoubleQuotedStyle || n.Style == yaml.SingleQuotedStyle {
			width += 2
		}
		edits = append(edits, edit{n.Line, n.Column, width, composeEnvFileLayerName(ap)})
	}
	for i := 1; i < len(services.Content); i += 2 {
		ef := composeYAMLGet(services.Content[i], "env_file")
		switch {
		case ef == nil:
		case ef.Kind == yaml.SequenceNode:
			for _, e := range ef.Content {
				if e.Kind == yaml.MappingNode {
					add(composeYAMLGet(e, "path"))
				} else {
					add(e)
				}
			}
		default:
			add(ef)
		}
	}
	// Bottom-up and right to left, so earlier positions stay valid.
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].line != edits[j].line {
			return edits[i].line > edits[j].line
		}
		return edits[i].col > edits[j].col
	})
	for _, e := range edits {
		in = composeYAMLReplace(in, e.line, e.col, e.n, e.value)
	}
	return in, paths, nil
}

// composeYAMLGet returns the value of key in a mapping node (nil if absent).
func composeYAMLGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// composeYAMLReplace replaces the n bytes of the scalar at line `line`,
// column `col` (1-based) with value. compose (transform/replace.go:125-145)
// replaces up to the end of the line; replacing only the scalar also keeps
// flow-style lists and trailing comments intact.
func composeYAMLReplace(in []byte, line, col, n int, value string) []byte {
	l, pos := 1, 0
	for pos < len(in) && l < line {
		if in[pos] == '\n' {
			l++
		}
		pos++
	}
	pos += col - 1
	if pos > len(in) {
		return in
	}
	end := pos + n
	if end > len(in) {
		end = len(in)
	}
	out := append([]byte(nil), in[:pos]...)
	out = append(out, value...)
	return append(out, in[end:]...)
}

// composeImageDigestsOverride is the image-digests.yaml layer.
func composeImageDigestsOverride(ctx context.Context, pd *providerData, p *ctypes.Project) ([]byte, error) {
	if pd == nil {
		return nil, fmt.Errorf("resolve_image_digests needs a configured provider")
	}
	var sample client.DistributionInspectResult
	resolved, err := p.WithImagesResolved(composeDigestResolver(ctx, pd, sample.Descriptor.Digest))
	if err != nil {
		return nil, fmt.Errorf("resolving image digests: %w", err)
	}
	override := ctypes.Project{Services: ctypes.Services{}}
	for name, svc := range resolved.Services {
		override.Services[name] = ctypes.ServiceConfig{ContainerSpec: ctypes.ContainerSpec{Image: svc.Image}}
	}
	return override.MarshalYAML()
}

// composeDigestResolver returns compose-go's resolver function (the digest
// type is inferred from sample, so the go-digest module stays indirect).
func composeDigestResolver[D ~string](ctx context.Context, pd *providerData, _ D) func(reference.Named) (D, error) {
	return func(named reference.Named) (D, error) {
		auth, err := pd.registryAuthFor(named.String())
		if err != nil {
			return "", err
		}
		res, err := pd.Client.DistributionInspect(ctx, named.String(), client.DistributionInspectOptions{EncodedRegistryAuth: auth})
		if err != nil {
			return "", fmt.Errorf("failed to resolve digest for %s: %w", named.String(), err)
		}
		return D(res.Descriptor.Digest), nil
	}
}
