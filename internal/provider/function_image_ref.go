package provider

import (
	"context"
	"fmt"

	"github.com/distribution/reference"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// imageRefAttrTypes is the object parse_image_ref returns.
var imageRefAttrTypes = map[string]attr.Type{
	"registry":   types.StringType,
	"repository": types.StringType,
	"tag":        types.StringType,
	"digest":     types.StringType,
}

// imageRefParts is the parsed form of an image reference.
type imageRefParts struct {
	Registry, Repository, Tag, Digest string
}

// parseImageRef normalizes ref the way the docker CLI does: no registry means
// docker.io, a single-segment Hub name gains `library/`, and no tag and no
// digest means tag `latest`.
func parseImageRef(ref string) (imageRefParts, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return imageRefParts{}, err
	}
	p := imageRefParts{Registry: reference.Domain(named), Repository: reference.Path(named)}
	if t, ok := named.(reference.Tagged); ok {
		p.Tag = t.Tag()
	}
	if c, ok := named.(reference.Digested); ok {
		p.Digest = c.Digest().String()
	}
	if p.Tag == "" && p.Digest == "" {
		p.Tag = "latest"
	}
	return p, nil
}

// buildImageRef assembles and validates a fully qualified reference.
// The error carries the 0-based index of the offending argument.
func buildImageRef(registry, repository, tag, dgst string) (string, int, error) {
	if repository == "" {
		return "", 1, fmt.Errorf("repository must not be empty")
	}
	name := repository
	if registry != "" {
		name = registry + "/" + repository
	}
	named, err := reference.ParseNormalizedNamed(name)
	if err != nil {
		return "", 1, fmt.Errorf("invalid registry/repository %q: %w", name, err)
	}
	if !reference.IsNameOnly(named) {
		return "", 1, fmt.Errorf("repository %q must not carry a tag or digest; pass them as the tag and digest arguments", repository)
	}
	if registry != "" && reference.Domain(named) != registry && !(registry == "index.docker.io" || registry == "registry-1.docker.io") {
		return "", 0, fmt.Errorf("registry %q is not a valid registry host (it parses as part of the repository)", registry)
	}
	if tag != "" {
		if named, err = reference.WithTag(named, tag); err != nil {
			return "", 2, fmt.Errorf("invalid tag %q: %w", tag, err)
		}
	}
	if dgst != "" {
		withDigest, err := reference.ParseNormalizedNamed(named.String() + "@" + dgst)
		if err != nil {
			return "", 3, fmt.Errorf("invalid digest %q: %w", dgst, err)
		}
		named = withDigest
	}
	return named.String(), 0, nil
}

// ---------------------------------------------------------------------------
// parse_image_ref

var _ function.Function = (*parseImageRefFunction)(nil)

// NewParseImageRefFunction returns provider::docker::parse_image_ref.
func NewParseImageRefFunction() function.Function { return &parseImageRefFunction{} }

type parseImageRefFunction struct{}

func (f *parseImageRefFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "parse_image_ref"
}

func (f *parseImageRefFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary: "Split an image reference into registry, repository, tag and digest.",
		MarkdownDescription: "Splits an image reference into `registry`, `repository`, `tag` and `digest`, normalized like the docker CLI. " +
			"No registry means `docker.io`, a one-segment Docker Hub name gains `library/` (`nginx` is `docker.io` / `library/nginx`), " +
			"and a reference with neither tag nor digest gets tag `latest`. Absent parts are empty strings. Invalid references are an error.",
		Parameters: []function.Parameter{
			function.StringParameter{Name: "ref", MarkdownDescription: "Image reference, e.g. `nginx`, `ghcr.io/org/app:1.2` or `app@sha256:...`."},
		},
		Return: function.ObjectReturn{AttributeTypes: imageRefAttrTypes},
	}
}

func (f *parseImageRefFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var ref string
	resp.Error = req.Arguments.Get(ctx, &ref)
	if resp.Error != nil {
		return
	}
	p, err := parseImageRef(ref)
	if err != nil {
		resp.Error = function.NewArgumentFuncError(0, fmt.Sprintf("invalid image reference %q: %v", ref, err))
		return
	}
	obj, diags := types.ObjectValue(imageRefAttrTypes, map[string]attr.Value{
		"registry":   types.StringValue(p.Registry),
		"repository": types.StringValue(p.Repository),
		"tag":        types.StringValue(p.Tag),
		"digest":     types.StringValue(p.Digest),
	})
	if diags.HasError() {
		resp.Error = function.FuncErrorFromDiags(ctx, diags)
		return
	}
	resp.Error = resp.Result.Set(ctx, obj)
}

// ---------------------------------------------------------------------------
// image_ref

var _ function.Function = (*imageRefFunction)(nil)

// NewImageRefFunction returns provider::docker::image_ref.
func NewImageRefFunction() function.Function { return &imageRefFunction{} }

type imageRefFunction struct{}

func (f *imageRefFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "image_ref"
}

func (f *imageRefFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	opt := func(name, desc string) function.StringParameter {
		return function.StringParameter{Name: name, AllowNullValue: true, MarkdownDescription: desc}
	}
	resp.Definition = function.Definition{
		Summary: "Build a fully qualified image reference from its parts.",
		MarkdownDescription: "Builds a fully qualified image reference such as `docker.io/library/nginx:1.27@sha256:...` from its parts, validating each one. " +
			"It is the inverse of `parse_image_ref`. `registry`, `tag` and `digest` may be empty or null; an empty registry means Docker Hub.",
		Parameters: []function.Parameter{
			opt("registry", "Registry host, e.g. `ghcr.io` or `localhost:5000`. Empty or null for Docker Hub."),
			function.StringParameter{Name: "repository", MarkdownDescription: "Repository path, e.g. `org/app` or `nginx`."},
			opt("tag", "Tag, e.g. `1.27`. Empty or null for none."),
			opt("digest", "Digest, e.g. `sha256:...`. Empty or null for none."),
		},
		Return: function.StringReturn{},
	}
}

func (f *imageRefFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var registry, repository, tag, dgst types.String
	resp.Error = req.Arguments.Get(ctx, &registry, &repository, &tag, &dgst)
	if resp.Error != nil {
		return
	}
	out, idx, err := buildImageRef(registry.ValueString(), repository.ValueString(), tag.ValueString(), dgst.ValueString())
	if err != nil {
		resp.Error = function.NewArgumentFuncError(int64(idx), err.Error())
		return
	}
	resp.Error = resp.Result.Set(ctx, out)
}
