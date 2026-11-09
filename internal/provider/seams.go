package provider

// Pass 5 cross-lane seams. Each seam is an interface with a fixed
// signature and a package-level variable holding the implementation. The
// variable defaults to a stub that returns a "not implemented (Pass 5)"
// error naming the lane that provides it.
//
// The implementing lane sets the variable from an init() in its own file:
//
//	func init() { seamBuildKitImpl = laneEBuildKit{} }
//
// Callers always go through the variable (seamBuildKitImpl.Build(...)),
// never through a concrete type, so lanes compile and test independently.
// Lane 0 owns this file; a lane that needs a signature change logs a TODO
// with the exact change instead of editing it.

import (
	"context"
	"fmt"
	"io"

	ctypes "github.com/compose-spec/compose-go/v2/types"
)

// seamNotImplemented is the error every default stub returns.
type seamNotImplemented struct {
	Seam string
	Lane string
}

func (e *seamNotImplemented) Error() string {
	return fmt.Sprintf("not implemented (Pass 5): %s is provided by Lane %s", e.Seam, e.Lane)
}

// ---------------------------------------------------------------------------
// BuildKit build: Lane E implements; Lane A's docker_image build block (and
// the buildx types) call it.

// seamBuildKit runs one BuildKit build.
type seamBuildKit interface {
	// Build runs req against the builder req.Builder selects (the
	// provider's daemon when empty) and returns what it produced. pd gives
	// the Docker client and registry_auth.
	Build(ctx context.Context, pd *providerData, req seamBuildRequest) (*seamBuildResult, error)
}

// seamBuildRequest is everything a build can be asked for. Exactly one of
// ContextDir and ContextTar is set; at most one of Dockerfile and
// DockerfileInline.
type seamBuildRequest struct {
	// ContextDir is a local build context directory (.dockerignore applies).
	ContextDir string
	// ContextTar is a build context already packed as a tar stream.
	ContextTar io.Reader
	// Dockerfile is the Dockerfile path, relative to the context ("" =
	// "Dockerfile"); a path outside the context is allowed.
	Dockerfile string
	// DockerfileInline is the Dockerfile text itself (sensitive: may carry
	// ARG defaults and ENV values).
	DockerfileInline string
	// Target is the stage to build ("" = last).
	Target string
	// Platforms are os/arch[/variant] values; several means a multi-platform build.
	Platforms []string
	// BuildArgs are --build-arg values (sensitive).
	BuildArgs map[string]string
	// Labels are image labels.
	Labels map[string]string
	// Secrets are --secret entries.
	Secrets []seamBuildSecret
	// SSH are --ssh entries.
	SSH []seamBuildSSH
	// CacheFrom / CacheTo are buildx cache specs ("type=registry,ref=...").
	CacheFrom []string
	CacheTo   []string
	// Outputs selects what the build exports.
	Outputs seamBuildOutputs
	// Network is the RUN network mode ("default", "none", "host").
	Network string
	// NoCache disables the cache; NoCacheFilter disables it for these stages only.
	NoCache       bool
	NoCacheFilter []string
	// Pull always pulls referenced images.
	Pull bool
	// ExtraHosts are host:ip entries added to /etc/hosts.
	ExtraHosts []string
	// ShmSize is /dev/shm size in bytes for RUN (0 = default).
	ShmSize int64
	// Ulimits are --ulimit values ("nofile=1024:2048").
	Ulimits []string
	// Builder selects the builder.
	Builder seamBuilderRef
	// Attestations selects provenance / SBOM attestations.
	Attestations seamBuildAttestations
	// Progress, when set, receives each build log line as it arrives.
	Progress func(line string)
	// Contexts are named build contexts (--build-context name=value): a local
	// directory, docker-image://, a git or HTTP(S) URL. Added in Pass 5 Lane Z
	// for docker_image build.additional_contexts and bake targets.
	Contexts map[string]string
	// Annotations are --annotation values ([type:]key=value).
	Annotations []string
	// Call is the frontend method (--call): build (default), check, outline, targets.
	Call string
	// Entitlements are extra allowed entitlements (network.host, security.insecure).
	Entitlements []string
}

// seamBuildSecret is one --secret: exactly one of Src (file), Env
// (environment variable of the provider process) and Value (sensitive
// literal, e.g. from a *_wo argument).
type seamBuildSecret struct {
	ID    string
	Src   string
	Env   string
	Value string
}

// seamBuildSSH is one --ssh: ID ("default") and agent socket or key paths
// (empty = SSH_AUTH_SOCK).
type seamBuildSSH struct {
	ID    string
	Paths []string
}

// seamBuildOutputs selects the exports.
type seamBuildOutputs struct {
	// Names are the image names (repo:tag) to tag the result with.
	Names []string
	// Push pushes Names to their registry.
	Push bool
	// Load loads the image into the provider's daemon image store.
	Load bool
	// Raw are extra buildx --output specs ("type=local,dest=out").
	Raw []string
}

// seamBuilderRef selects a builder: by buildx builder Name, or a BuildKit
// Endpoint (tcp://, unix://, docker-container://...). Both empty = the
// provider daemon's embedded BuildKit.
type seamBuilderRef struct {
	Name     string
	Endpoint string
}

// seamBuildAttestations selects attestations. Provenance is "", "min",
// "max" or "false"; SBOM true adds an SPDX SBOM. Extra are raw
// --attest values.
type seamBuildAttestations struct {
	Provenance string
	SBOM       bool
	Extra      []string
}

// seamBuildResult is what a build produced.
type seamBuildResult struct {
	// ImageID is the config digest (sha256:...) of the image (of the
	// single-platform image, or of the index for a multi-platform build).
	ImageID string
	// Digest is the manifest (or index) digest, set when pushed.
	Digest string
	// ImageDigestRef is repo@sha256:... of the first pushed name.
	ImageDigestRef string
	// Platforms are the platforms built.
	Platforms []string
	// Metadata is the buildx --metadata-file object (containerimage.digest,
	// buildx.build.ref, buildx.build.provenance...).
	Metadata map[string]any
	// BuildLog is the plain-progress build log (sensitive: may echo
	// secrets).
	BuildLog string
}

// seamBuildKitImpl is the BuildKit build implementation. Lane E sets it.
var seamBuildKitImpl seamBuildKit = seamBuildKitStub{}

type seamBuildKitStub struct{}

func (seamBuildKitStub) Build(context.Context, *providerData, seamBuildRequest) (*seamBuildResult, error) {
	return nil, &seamNotImplemented{Seam: "the BuildKit build seam (seamBuildKitImpl)", Lane: "E"}
}

// ---------------------------------------------------------------------------
// Buildx builder cache prune: Lane E implements; Lane A's
// docker_builder_prune action calls it when `builder` is set (unset uses
// the Engine's POST /build/prune directly).

// seamBuildxPrune prunes one buildx builder's cache.
type seamBuildxPrune interface {
	Prune(ctx context.Context, pd *providerData, req seamBuildxPruneRequest) (seamBuildxPruneResult, error)
}

// seamBuildxPruneRequest mirrors docker buildx prune.
type seamBuildxPruneRequest struct {
	// Builder is the buildx builder name.
	Builder string
	// All removes all cache, not only dangling (--all).
	All bool
	// Filters are --filter values (until, id, type, description...).
	Filters map[string][]string
	// Space limits in bytes (0 = unset): --keep-storage (older name of
	// --reserved-space), --reserved-space, --max-used-space, --min-free-space.
	KeepStorage   int64
	ReservedSpace int64
	MaxUsedSpace  int64
	MinFreeSpace  int64
}

// seamBuildxPruneResult is what the prune removed.
type seamBuildxPruneResult struct {
	CachesDeleted  []string
	SpaceReclaimed int64
}

// seamBuildxPruneImpl is the builder prune implementation. Lane E sets it.
var seamBuildxPruneImpl seamBuildxPrune = seamBuildxPruneStub{}

type seamBuildxPruneStub struct{}

func (seamBuildxPruneStub) Prune(context.Context, *providerData, seamBuildxPruneRequest) (seamBuildxPruneResult, error) {
	return seamBuildxPruneResult{}, &seamNotImplemented{Seam: "the buildx builder prune seam (seamBuildxPruneImpl)", Lane: "E"}
}

// ---------------------------------------------------------------------------
// Dockerfile render / parse: Lane D implements; Lanes A and E call it.

// seamDockerfile renders and parses Dockerfiles.
type seamDockerfile interface {
	// Render prints doc as Dockerfile text (parser directives first).
	Render(doc seamDockerfileDoc) (string, error)
	// Parse reads Dockerfile text into a document.
	Parse(text string) (seamDockerfileDoc, error)
}

// seamDockerfileDoc is a whole Dockerfile.
type seamDockerfileDoc struct {
	// Syntax is the # syntax= directive ("" = none).
	Syntax string
	// Escape is the # escape= directive ("" = default "\").
	Escape string
	// Check is the # check= directive.
	Check seamDockerfileCheck
	// GlobalArgs are the ARG instructions before the first FROM, in order.
	GlobalArgs []seamDockerfileInstruction
	// Stages are the build stages in order.
	Stages []seamDockerfileStage
}

// seamDockerfileCheck is # check=skip=<a,b|all>;error=<bool>.
type seamDockerfileCheck struct {
	Skip  []string
	Error bool
}

// seamDockerfileStage is one FROM and its instructions.
type seamDockerfileStage struct {
	// Name is the AS name ("" = unnamed).
	Name string
	// From is the base image or stage.
	From string
	// Platform is FROM --platform ("" = none).
	Platform string
	// Comments are the comment lines directly above FROM.
	Comments []string
	// Instructions follow FROM, in order.
	Instructions []seamDockerfileInstruction
}

// seamDockerfileInstruction is one instruction.
type seamDockerfileInstruction struct {
	// Keyword is the instruction in upper case (RUN, COPY, ARG...).
	Keyword string
	// Flags are the --name=value flags in order (a repeated flag is
	// repeated; Values holds comma-free values as written).
	Flags []seamDockerfileFlag
	// Args are the arguments: the exec-form list elements when JSONForm,
	// else the shell-form words (the whole command line as one element for
	// RUN/CMD/ENTRYPOINT/SHELL shell form).
	Args []string
	// JSONForm is the exec / JSON-array form.
	JSONForm bool
	// Heredocs are the here-documents attached to the instruction.
	Heredocs []seamDockerfileHeredoc
	// OnBuild wraps the instruction in ONBUILD.
	OnBuild bool
	// Comments are the comment lines directly above the instruction.
	Comments []string
}

// seamDockerfileFlag is one instruction flag (name without dashes).
type seamDockerfileFlag struct {
	Name   string
	Values []string
}

// seamDockerfileHeredoc is one here-document.
type seamDockerfileHeredoc struct {
	// Name is the delimiter word (EOF).
	Name string
	// Content is the text up to the delimiter.
	Content string
	// StripTabs is <<- (leading tabs removed).
	StripTabs bool
	// Quoted is <<"EOF" (no expansion).
	Quoted bool
	// Target is the destination file name on COPY/ADD ("" = none).
	Target string
}

// seamDockerfileImpl is the Dockerfile implementation. Lane D sets it.
var seamDockerfileImpl seamDockerfile = seamDockerfileStub{}

type seamDockerfileStub struct{}

func (seamDockerfileStub) Render(seamDockerfileDoc) (string, error) {
	return "", &seamNotImplemented{Seam: "the Dockerfile render seam (seamDockerfileImpl)", Lane: "D"}
}

func (seamDockerfileStub) Parse(string) (seamDockerfileDoc, error) {
	return seamDockerfileDoc{}, &seamNotImplemented{Seam: "the Dockerfile parse seam (seamDockerfileImpl)", Lane: "D"}
}

// ---------------------------------------------------------------------------
// Compose project load / normalize: Lane C implements; the stack code
// calls it.

// seamCompose loads and normalizes compose projects (compose-go v2).
type seamCompose interface {
	// Load parses, interpolates and validates a project. Only the values
	// in req reach the loader (no OS environment).
	Load(ctx context.Context, req seamComposeLoadRequest) (*ctypes.Project, error)
	// Normalize prints the project as `docker compose config` does
	// (canonical YAML).
	Normalize(p *ctypes.Project) ([]byte, error)
	// ConfigHash is the com.docker.compose.config-hash value of one
	// service, as docker_compose stamps it on the service's containers.
	ConfigHash(p *ctypes.Project, service string) (string, error)
}

// seamComposeLoadRequest selects and shapes a project. Content and Paths
// are alternatives (Content is loaded as docker-compose.yml in WorkingDir).
type seamComposeLoadRequest struct {
	Content     string
	Paths       []string
	WorkingDir  string
	ProjectName string
	// Env is the interpolation environment (sensitive values allowed).
	Env map[string]string
	// EnvFiles are .env files read before Env (later files win, Env on top).
	EnvFiles []string
	Profiles []string
}

// seamComposeImpl is the compose implementation. Lane C sets it (and may
// start from the Pass 3/4 composeLoad in compose_parse.go, which already
// implements Load for this request shape).
var seamComposeImpl seamCompose = seamComposeStub{}

type seamComposeStub struct{}

func (seamComposeStub) Load(context.Context, seamComposeLoadRequest) (*ctypes.Project, error) {
	return nil, &seamNotImplemented{Seam: "the compose load seam (seamComposeImpl)", Lane: "C"}
}

func (seamComposeStub) Normalize(*ctypes.Project) ([]byte, error) {
	return nil, &seamNotImplemented{Seam: "the compose normalize seam (seamComposeImpl)", Lane: "C"}
}

func (seamComposeStub) ConfigHash(*ctypes.Project, string) (string, error) {
	return "", &seamNotImplemented{Seam: "the compose config-hash seam (seamComposeImpl)", Lane: "C"}
}
