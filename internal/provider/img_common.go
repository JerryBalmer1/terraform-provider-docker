package provider

// Pass 5 Lane A (WP img): helpers shared by the image types: the contract
// identity values (image_ref, image_digest_ref, digest, platforms, labels)
// and a small Dockerfile stage reader for the docker_image `stages` output.

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// imgIdentity is the contract identity of one image.
type imgIdentity struct {
	ImageRef       types.String
	ImageDigestRef types.String
	Digest         types.String
	Platforms      types.List
	Labels         types.Map
}

// imgNormalizedRef returns the normalized `registry/repo:tag` form of name
// (implicit `latest` added), or "" when name has no tag (a digest-only
// reference or an image ID).
func imgNormalizedRef(name string) string {
	if name == "" || strings.HasPrefix(name, "sha256:") {
		return ""
	}
	named, err := reference.ParseNormalizedNamed(name)
	if err != nil {
		return ""
	}
	if _, ok := named.(reference.Digested); ok {
		if _, tagged := named.(reference.Tagged); !tagged {
			return ""
		}
	}
	named = reference.TagNameOnly(named)
	t, ok := named.(reference.Tagged)
	if !ok {
		return ""
	}
	return reference.TrimNamed(named).String() + ":" + t.Tag()
}

// imgPlatformString renders an OCI platform as os/arch[/variant].
func imgPlatformString(p ocispec.Platform) string {
	if p.OS == "" && p.Architecture == "" {
		return ""
	}
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// imgPlatforms returns the platforms of an inspected image: one per image
// manifest (containerd store, with manifests requested), else the image's
// own os/arch/variant.
func imgPlatforms(insp image.InspectResponse) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range insp.Manifests {
		if m.Kind != image.ManifestKindImage || m.ImageData == nil {
			continue
		}
		if s := imgPlatformString(m.ImageData.Platform); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		return out
	}
	if s := imgPlatformString(ocispec.Platform{OS: insp.Os, Architecture: insp.Architecture, Variant: insp.Variant}); s != "" {
		return []string{s}
	}
	return []string{}
}

// imgIdentityOf computes the identity values of insp, looked up as name.
func imgIdentityOf(name string, insp image.InspectResponse) imgIdentity {
	rd := imgPickRepoDigest(name, insp.RepoDigests)
	dg := ""
	if insp.Descriptor != nil && insp.Descriptor.Digest != "" {
		dg = insp.Descriptor.Digest.String()
	} else {
		dg = imgDigestFromRepoDigest(rd)
	}
	var labels map[string]string
	if insp.Config != nil {
		labels = insp.Config.Labels
	}
	return imgIdentity{
		ImageRef:       stringOrNull(imgNormalizedRef(name)),
		ImageDigestRef: stringOrNull(rd),
		Digest:         stringOrNull(dg),
		Platforms:      stringListValue(imgPlatforms(insp), false),
		Labels:         stringMapValue(labels, false),
	}
}

// imgInspectRaw inspects ref and returns the decoded response with the exact
// response body. With manifests it asks for the per-platform manifests when
// the daemon supports them (API 1.48).
func imgInspectRaw(ctx context.Context, pd *providerData, ref string, manifests bool) (image.InspectResponse, []byte, error) {
	var buf bytes.Buffer
	opts := []client.ImageInspectOption{client.ImageInspectWithRawResponse(&buf)}
	if manifests {
		if ok, _, err := pd.APIVersionAtLeast(ctx, "1.48"); err == nil && ok {
			opts = append(opts, client.ImageInspectWithManifests(true))
		}
	}
	res, err := pd.Client.ImageInspect(ctx, ref, opts...)
	if err != nil {
		return image.InspectResponse{}, nil, err
	}
	return res.InspectResponse, rEngineBody(buf.Bytes()), nil
}

// imgManifestsValue maps ImageInspect.Manifests (image kind only) to the
// contract's manifests list.
func imgManifestsValue(insp image.InspectResponse) []map[string]any {
	out := []map[string]any{}
	for _, m := range insp.Manifests {
		if m.Kind != image.ManifestKindImage {
			continue
		}
		row := map[string]any{
			"digest":     m.Descriptor.Digest.String(),
			"media_type": m.Descriptor.MediaType,
			"size":       m.Descriptor.Size,
			"available":  m.Available,
		}
		if m.ImageData != nil {
			p := m.ImageData.Platform
			row["platform"] = map[string]any{
				"architecture": p.Architecture,
				"os":           p.OS,
				"os_version":   p.OSVersion,
				"os_features":  append([]string{}, p.OSFeatures...),
				"variant":      p.Variant,
			}
		}
		out = append(out, row)
	}
	return out
}

// ---------------------------------------------------------------------------
// Dockerfile stages

// imgDockerfileInfo is what the `stages` / `dockerfile_syntax` outputs need.
type imgDockerfileInfo struct {
	Syntax string
	Stages []map[string]any
}

var (
	imgDirectiveRe = regexp.MustCompile(`^#\s*([a-zA-Z][a-zA-Z0-9]*)\s*=\s*(.+?)\s*$`)
	imgHeredocRe   = regexp.MustCompile(`<<-?\s*["']?([A-Za-z_][A-Za-z0-9_]*)["']?`)
	imgArgRefRe    = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
)

// imgParseDockerfile reads the stages of a Dockerfile. It uses the Dockerfile
// seam (Lane D) when it is available and falls back to a line reader that
// knows parser directives, comments, line continuations and heredocs.
func imgParseDockerfile(text string) imgDockerfileInfo {
	if doc, err := seamDockerfileImpl.Parse(text); err == nil {
		return imgInfoFromSeam(doc)
	} else {
		var ni *seamNotImplemented
		if !errors.As(err, &ni) {
			// A parse error: still report what the line reader finds.
			return imgParseDockerfileLines(text)
		}
	}
	return imgParseDockerfileLines(text)
}

// imgInfoFromSeam maps a parsed seam document to stages.
func imgInfoFromSeam(doc seamDockerfileDoc) imgDockerfileInfo {
	info := imgDockerfileInfo{Syntax: doc.Syntax}
	args := map[string]string{}
	for _, a := range doc.GlobalArgs {
		imgCollectArg(strings.Join(a.Args, " "), args)
	}
	names := map[string]bool{}
	for i, st := range doc.Stages {
		var ins []map[string]any
		for j, in := range st.Instructions {
			text := in.Keyword
			for _, f := range in.Flags {
				for _, v := range f.Values {
					text += " --" + f.Name + "=" + v
				}
			}
			if len(in.Args) > 0 {
				text += " " + strings.Join(in.Args, " ")
			}
			ins = append(ins, map[string]any{"index": j, "keyword": strings.ToUpper(in.Keyword), "dockerfile": text})
		}
		info.Stages = append(info.Stages, imgStageRow(i, st.Name, st.From, st.Platform, args, names, ins))
		if st.Name != "" {
			names[strings.ToLower(st.Name)] = true
		}
	}
	return info
}

// imgCollectArg records an `ARG name[=default]` argument string.
func imgCollectArg(s string, args map[string]string) {
	for _, w := range strings.Fields(s) {
		k, v, _ := strings.Cut(w, "=")
		args[k] = strings.Trim(v, `"'`)
	}
}

// imgStageRow builds one `stages` element.
func imgStageRow(index int, name, from, platform string, args map[string]string, earlier map[string]bool, ins []map[string]any) map[string]any {
	base := imgArgRefRe.ReplaceAllStringFunc(from, func(m string) string {
		k := imgArgRefRe.FindStringSubmatch(m)[1]
		if v, ok := args[k]; ok && v != "" {
			return v
		}
		return m
	})
	row := map[string]any{
		"index":        index,
		"base_image":   base,
		"instructions": ins,
	}
	if name != "" {
		row["name"] = name
	}
	if platform != "" {
		row["platform"] = platform
	}
	switch {
	case earlier[strings.ToLower(base)]:
		row["base_stage"] = base
	case strings.EqualFold(base, "scratch") || strings.Contains(base, "$"):
	default:
		if ref := imgNormalizedRef(base); ref != "" {
			row["base_image_ref"] = ref
		}
	}
	if ins == nil {
		row["instructions"] = []map[string]any{}
	}
	return row
}

// imgParseDockerfileLines is the fallback line reader.
func imgParseDockerfileLines(text string) imgDockerfileInfo {
	var info imgDockerfileInfo
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	escape := `\`
	// Parser directives: only at the very top, before any other line.
	i := 0
	for ; i < len(lines); i++ {
		m := imgDirectiveRe.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			break
		}
		switch strings.ToLower(m[1]) {
		case "syntax":
			info.Syntax = m[2]
		case "escape":
			escape = m[2]
		}
	}
	type stage struct {
		name, from, platform string
		ins                  []map[string]any
	}
	var stages []stage
	args := map[string]string{}
	for i < len(lines) {
		start := i + 1
		l := strings.TrimSpace(lines[i])
		i++
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		full := l
		for strings.HasSuffix(full, escape) && i < len(lines) {
			full = strings.TrimSuffix(full, escape)
			next := strings.TrimSpace(lines[i])
			i++
			if strings.HasPrefix(next, "#") {
				continue
			}
			full += " " + next
		}
		// Heredocs: skip their bodies.
		for _, hm := range imgHeredocRe.FindAllStringSubmatch(full, -1) {
			for i < len(lines) {
				body := lines[i]
				i++
				if strings.TrimSpace(body) == hm[1] {
					break
				}
			}
		}
		kw, rest, _ := strings.Cut(full, " ")
		kw = strings.ToUpper(kw)
		rest = strings.TrimSpace(rest)
		if kw == "FROM" {
			st := stage{}
			words := strings.Fields(rest)
			var plain []string
			for _, w := range words {
				if strings.HasPrefix(w, "--platform=") {
					st.platform = strings.TrimPrefix(w, "--platform=")
					continue
				}
				plain = append(plain, w)
			}
			if len(plain) > 0 {
				st.from = plain[0]
			}
			if len(plain) >= 3 && strings.EqualFold(plain[1], "AS") {
				st.name = plain[2]
			}
			stages = append(stages, st)
			continue
		}
		if len(stages) == 0 {
			if kw == "ARG" {
				imgCollectArg(rest, args)
			}
			continue
		}
		cur := &stages[len(stages)-1]
		cur.ins = append(cur.ins, map[string]any{"index": len(cur.ins), "keyword": kw, "line": start, "dockerfile": full})
	}
	names := map[string]bool{}
	for idx, st := range stages {
		info.Stages = append(info.Stages, imgStageRow(idx, st.name, st.from, st.platform, args, names, st.ins))
		if st.name != "" {
			names[strings.ToLower(st.name)] = true
		}
	}
	return info
}
