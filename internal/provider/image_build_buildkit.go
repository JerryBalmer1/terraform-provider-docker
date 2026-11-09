package provider

// BuildKit support for docker_image: a BuildKit session is opened over the
// daemon's /session endpoint (HTTP upgrade to h2c, gRPC served by the
// provider) exposing build secrets and registry credentials, then
// POST /build runs with version=2 and session=<id>. The build context is
// still sent as the tar request body (the daemon's builder-next turns the
// body into the "context" frontend input), so .dockerignore handling and
// context_digest are shared with the classic path.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

const (
	imgBuilderClassic  = "classic"
	imgBuilderBuildKit = "buildkit"

	// Aux message IDs emitted by the daemon's BuildKit builder.
	imgAuxBuildKitTrace = "moby.buildkit.trace"
	imgAuxImageID       = "moby.image.id"
)

// imageBuildSecretModel is one build.secrets block.
type imageBuildSecretModel struct {
	ID  types.String `tfsdk:"id"`
	Src types.String `tfsdk:"src"`
	Env types.String `tfsdk:"env"`
}

// imgIsBuildKit reports whether the build block selects BuildKit.
func imgIsBuildKit(b *imageBuildModel) bool {
	return b != nil && b.Builder.ValueString() == imgBuilderBuildKit
}

// PARTIAL(pass5) buildx:build build.platforms takes one platform; multi-platform builds (`docker buildx build --platform a,b`) are only available through docker_buildx_bake

// imgValidateBuildKit checks the BuildKit-only attributes of a build block.
// Unknown values are skipped (validated again at apply).
func imgValidateBuildKit(ctx context.Context, b *imageBuildModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if b == nil {
		return diags
	}
	root := path.Root("build")
	bk := imgIsBuildKit(b)
	builderKnown := !b.Builder.IsUnknown()
	if builderKnown && !bk {
		why := "is only supported with builder = \"buildkit\" (the classic builder has no session, cache import or platform list support)."
		if len(b.Secrets) > 0 {
			diags.AddAttributeError(root.AtName("secrets"), "BuildKit-only attribute", "build.secrets "+why)
		}
		if !b.CacheFrom.IsNull() {
			diags.AddAttributeError(root.AtName("cache_from"), "BuildKit-only attribute", "build.cache_from "+why)
		}
		if !b.Platforms.IsNull() {
			diags.AddAttributeError(root.AtName("platforms"), "BuildKit-only attribute", "build.platforms "+why)
		}
	}
	seen := map[string]bool{}
	for i, s := range b.Secrets {
		p := root.AtName("secrets").AtListIndex(i)
		if id, ok := knownString(s.ID); ok {
			if id == "" {
				diags.AddAttributeError(p.AtName("id"), "Invalid secret id", "build.secrets.id must not be empty.")
			} else if seen[id] {
				diags.AddAttributeError(p.AtName("id"), "Duplicate secret id", fmt.Sprintf("Secret id %q is declared more than once.", id))
			}
			seen[id] = true
		}
		if s.Src.IsUnknown() || s.Env.IsUnknown() {
			continue
		}
		if s.Src.IsNull() == s.Env.IsNull() {
			diags.AddAttributeError(p, "Invalid secret source",
				"Each build.secrets block needs exactly one of src (file path) or env (environment variable name).")
		}
	}
	if !b.Platforms.IsUnknown() && !b.Platforms.IsNull() {
		elems := b.Platforms.Elements()
		if len(elems) > 1 {
			diags.AddAttributeError(root.AtName("platforms"), "Multi-platform build not supported",
				"The Engine API /build endpoint accepts a single platform (the daemon parses it with containerd's platforms.Parse, "+
					"which rejects comma-separated lists, whatever the image store). Multi-platform builds need buildx/BuildKit's "+
					"own API. Declare one docker_image per platform instead.")
		}
		if len(elems) > 0 && !b.Platform.IsNull() && !b.Platform.IsUnknown() {
			diags.AddAttributeError(root.AtName("platforms"), "Conflicting attributes",
				"Set either build.platform or build.platforms, not both.")
		}
	}
	return diags
}

// imgApplyBuildKitOptions turns classic build options into BuildKit ones.
func imgApplyBuildKitOptions(ctx context.Context, b *imageBuildModel, opts *client.ImageBuildOptions, diags *diag.Diagnostics) {
	opts.Version = build.BuilderBuildKit
	opts.CacheFrom = stringListFrom(ctx, b.CacheFrom, diags)
	if ps := stringListFrom(ctx, b.Platforms, diags); len(ps) == 1 {
		opts.Platforms = platformsFromString(ps[0])
	} else if len(ps) > 1 {
		diags.AddAttributeError(path.Root("build").AtName("platforms"), "Multi-platform build not supported",
			"The Engine API /build endpoint accepts a single platform.")
	}
}

// imgBuildKitSecretSources maps secrets blocks to secretsprovider sources,
// checking that every file exists and every environment variable is set
// (secretsprovider would silently send an empty value for a missing env).
func imgBuildKitSecretSources(secrets []imageBuildSecretModel) ([]secretsprovider.Source, error) {
	out := make([]secretsprovider.Source, 0, len(secrets))
	for _, s := range secrets {
		src := secretsprovider.Source{ID: s.ID.ValueString(), FilePath: s.Src.ValueString(), Env: s.Env.ValueString()}
		switch {
		case src.ID == "":
			return nil, errors.New("build secret without id")
		case (src.FilePath == "") == (src.Env == ""):
			return nil, fmt.Errorf("build secret %q: set exactly one of src or env", src.ID)
		case src.Env != "":
			if _, ok := os.LookupEnv(src.Env); !ok {
				return nil, fmt.Errorf("build secret %q: environment variable %s is not set in the provider's environment", src.ID, src.Env)
			}
		default:
			abs, err := filepath.Abs(src.FilePath)
			if err != nil {
				return nil, fmt.Errorf("build secret %q: %w", src.ID, err)
			}
			src.FilePath = abs
		}
		out = append(out, src)
	}
	return out, nil
}

// imgBuildKitAttachables returns the session services for a build: secrets
// (when any) and registry credentials from the provider's registry_auth.
func imgBuildKitAttachables(secrets []imageBuildSecretModel, creds []registryCredential) ([]session.Attachable, error) {
	var out []session.Attachable
	if len(secrets) > 0 {
		srcs, err := imgBuildKitSecretSources(secrets)
		if err != nil {
			return nil, err
		}
		store, err := secretsprovider.NewStore(srcs)
		if err != nil {
			return nil, fmt.Errorf("build secrets: %w", err)
		}
		out = append(out, secretsprovider.NewSecretProvider(store))
	}
	out = append(out, &imgBuildKitAuth{creds: creds})
	return out, nil
}

// imgBuildKitAuth answers BuildKit's credential requests (pulling FROM
// images) from registry_auth. Token-authority methods stay unimplemented,
// which makes BuildKit fetch registry tokens itself with these credentials.
type imgBuildKitAuth struct {
	auth.UnimplementedAuthServer
	creds []registryCredential
}

func (a *imgBuildKitAuth) Register(s *grpc.Server) { auth.RegisterAuthServer(s, a) }

func (a *imgBuildKitAuth) Credentials(_ context.Context, req *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	host := normalizeRegistry(req.GetHost())
	for _, c := range a.creds {
		if normalizeRegistry(c.Address) != host {
			continue
		}
		if c.IdentityToken != "" {
			// An identity (refresh) token goes in Secret with no username, as the CLI does.
			return &auth.CredentialsResponse{Secret: c.IdentityToken}, nil
		}
		user, pass := c.Username, c.Password
		if user == "" && c.Auth != "" {
			if dec, err := base64.StdEncoding.DecodeString(c.Auth); err == nil {
				user, pass, _ = strings.Cut(string(dec), ":")
			}
		}
		return &auth.CredentialsResponse{Username: user, Secret: pass}, nil
	}
	return &auth.CredentialsResponse{}, nil
}

// imgSessionDialer is the part of *client.Client the session needs.
type imgSessionDialer interface {
	DialHijack(ctx context.Context, url, proto string, meta map[string][]string) (net.Conn, error)
}

// imgBuildKitSharedKey mirrors the CLI: a stable key per context directory
// (BuildKit uses it to scope local caches).
func imgBuildKitSharedKey(contextDir string) string {
	abs, err := filepath.Abs(contextDir)
	if err != nil {
		abs = contextDir
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])
}

// imgStartBuildKitSession creates a session with the attachables and runs it
// in the background over POST /session (Upgrade: h2c). onFail is called
// when the session cannot be established or ends with an error.
func imgStartBuildKitSession(ctx context.Context, d imgSessionDialer, sharedKey string, attachables []session.Attachable, onFail func(error)) (*session.Session, error) {
	s, err := session.NewSession(ctx, sharedKey)
	if err != nil {
		return nil, fmt.Errorf("creating BuildKit session: %w", err)
	}
	for _, a := range attachables {
		s.Allow(a)
	}
	go func() {
		err := s.Run(ctx, func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return d.DialHijack(ctx, "/session", proto, meta)
		})
		if err != nil && onFail != nil {
			onFail(err)
		}
	}()
	return s, nil
}

// imgBuildKitBuild runs a BuildKit build of bc with opts (classic options
// already filled in) and returns the built image ID.
func imgBuildKitBuild(ctx context.Context, c *client.Client, creds []registryCredential, b *imageBuildModel, bc *imgBuildContext, opts client.ImageBuildOptions) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	imgApplyBuildKitOptions(ctx, b, &opts, &diags)
	if diags.HasError() {
		return "", diags
	}
	attachables, err := imgBuildKitAttachables(b.Secrets, creds)
	if err != nil {
		diags.AddAttributeError(path.Root("build").AtName("secrets"), "Invalid build secret", err.Error())
		return "", diags
	}
	buildCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s, err := imgStartBuildKitSession(buildCtx, c, imgBuildKitSharedKey(b.Context.ValueString()), attachables, func(err error) {
		cancel(fmt.Errorf("BuildKit session: %w", err))
	})
	if err != nil {
		diags.AddError("Cannot start BuildKit session", err.Error())
		return "", diags
	}
	defer func() {
		// Cancel first: Close waits for a dial in progress to return.
		cancel(nil)
		_ = s.Close()
	}()
	opts.SessionID = s.ID()

	tflog.Info(ctx, "building image with BuildKit", map[string]any{"session": s.ID(), "cache_from": opts.CacheFrom, "platform": platformsString(opts.Platforms), "secrets": len(b.Secrets)})
	tarStream := imgTarReader(bc)
	defer tarStream.Close()
	res, err := c.ImageBuild(buildCtx, tarStream, opts)
	if err != nil {
		if cause := context.Cause(buildCtx); cause != nil && !errors.Is(cause, context.Canceled) {
			err = fmt.Errorf("%w (%v)", err, cause)
		}
		diags.AddError("Cannot start image build", err.Error())
		return "", diags
	}
	defer res.Body.Close()
	id, err := imgDecodeBuildKitStream(res.Body, imgLogMessage(ctx), func(line string) { tflog.Debug(ctx, line) })
	if err != nil {
		if cause := context.Cause(buildCtx); cause != nil && !errors.Is(err, cause) {
			err = fmt.Errorf("%w (%v)", err, cause)
		}
		diags.AddError("Image build failed", err.Error())
		return "", diags
	}
	return id, diags
}

// ---------------------------------------------------------------------------
// Stream decoding

// imgBuildKitTrace accumulates what matters from BuildKit aux messages.
type imgBuildKitTrace struct {
	ImageID string
	// Tail keeps the last lines of build step output.
	Tail []string
	// Errors lists failed vertices ("[name] error").
	Errors  []string
	vtxName map[string]string
	partial map[string]string
	onLine  func(string)
}

// handle processes one stream message; non-BuildKit messages are ignored.
func (t *imgBuildKitTrace) handle(m jsonstream.Message) {
	if m.Aux == nil {
		return
	}
	switch m.ID {
	case imgAuxImageID:
		if id := imgParseImageIDAux(*m.Aux); id != "" {
			t.ImageID = id
		}
	case imgAuxBuildKitTrace:
		sr, err := imgParseBuildKitTrace(*m.Aux)
		if err != nil {
			return
		}
		t.add(sr)
	}
}

func (t *imgBuildKitTrace) add(sr *controlapi.StatusResponse) {
	if t.vtxName == nil {
		t.vtxName, t.partial = map[string]string{}, map[string]string{}
	}
	for _, v := range sr.GetVertexes() {
		if v.GetName() != "" {
			t.vtxName[v.GetDigest()] = v.GetName()
		}
		if e := v.GetError(); e != "" {
			msg := fmt.Sprintf("[%s] %s", t.vtxName[v.GetDigest()], e)
			dup := false
			for _, x := range t.Errors {
				dup = dup || x == msg
			}
			if !dup {
				t.Errors = append(t.Errors, msg)
			}
		}
	}
	for _, l := range sr.GetLogs() {
		data := t.partial[l.GetVertex()] + string(l.GetMsg())
		lines := strings.Split(data, "\n")
		t.partial[l.GetVertex()] = lines[len(lines)-1]
		for _, line := range lines[:len(lines)-1] {
			t.line(strings.TrimRight(line, "\r"))
		}
	}
	for _, w := range sr.GetWarnings() {
		t.line("WARNING: " + string(w.GetShort()))
	}
}

func (t *imgBuildKitTrace) line(s string) {
	if s == "" {
		return
	}
	if t.onLine != nil {
		t.onLine(s)
	}
	t.Tail = append(t.Tail, s)
	if len(t.Tail) > imgStreamTailLines {
		t.Tail = t.Tail[len(t.Tail)-imgStreamTailLines:]
	}
}

// imgParseImageIDAux reads {"ID": "sha256:..."} (moby.image.id aux).
func imgParseImageIDAux(raw json.RawMessage) string {
	var v build.Result
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.ID
}

// imgParseBuildKitTrace decodes a moby.buildkit.trace aux payload: a JSON
// string holding base64 of a protobuf control.StatusResponse.
func imgParseBuildKitTrace(raw json.RawMessage) (*controlapi.StatusResponse, error) {
	var dt []byte
	if err := json.Unmarshal(raw, &dt); err != nil {
		return nil, err
	}
	var sr controlapi.StatusResponse
	if err := proto.Unmarshal(dt, &sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

// imgDecodeBuildKitStream drains a BuildKit build stream. It returns the
// image ID (moby.image.id aux, else a classic {"ID"} aux). A stream error is
// returned with the failed vertices and last step output attached.
func imgDecodeBuildKitStream(r io.Reader, onMsg func(jsonstream.Message), onLine func(string)) (string, error) {
	tr := &imgBuildKitTrace{onLine: onLine}
	res, err := imgDecodeStream(r, func(m jsonstream.Message) {
		tr.handle(m)
		if onMsg != nil {
			onMsg(m)
		}
	})
	if err != nil {
		var se *imgStreamError
		if errors.As(err, &se) {
			tail := append(append([]string(nil), tr.Errors...), tr.Tail...)
			if len(tail) == 0 {
				tail = se.Tail
			}
			se.Tail = tail
		}
		return "", err
	}
	if tr.ImageID != "" {
		return tr.ImageID, nil
	}
	return imgBuiltImageID(res.Aux), nil
}
