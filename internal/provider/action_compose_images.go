package provider

// Compose image actions, all driven by the compose model:
//   - docker_compose_build   (compose:cli-build): BuildKit seam (Lane E)
//   - docker_compose_pull    (compose:cli-pull)
//   - docker_compose_push    (compose:cli-push)
//   - docker_compose_publish (compose:cli-publish): the compose files as an
//     OCI artifact, pushed with the registry HTTP API v2.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	aschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

// composeImagesActModel is the model of build / pull / push.
type composeImagesActModel struct {
	composeSourceModel
	Services   types.List   `tfsdk:"services"`
	ResultPath types.String `tfsdk:"result_path"`
}

func composeImagesSchema(desc string) aschema.Schema {
	return aschema.Schema{
		MarkdownDescription: desc,
		Attributes: composeActionSource(map[string]aschema.Attribute{
			"services":    composeActionServicesAttr(),
			"result_path": actResultPathAttribute(),
		}),
	}
}

func composeImagesValidate(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m composeImagesActModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() {
		composeValidateSource(m.composeSourceModel, &resp.Diagnostics)
	}
}

// composeImagesLoad loads the project and the selected services.
func composeImagesLoad(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse, a *actBase) (*composeImagesActModel, *ctypes.Project, []ctypes.ServiceConfig) {
	var m composeImagesActModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return nil, nil, nil
	}
	p := composeLoadSource(ctx, m.composeSourceModel, &resp.Diagnostics)
	if p == nil {
		return nil, nil, nil
	}
	svcs, err := composeSelectServices(p, stringListFrom(ctx, m.Services, &resp.Diagnostics))
	if err != nil {
		resp.Diagnostics.AddError("Unknown service", err.Error())
		return nil, nil, nil
	}
	return &m, p, svcs
}

// ---------------------------------------------------------------------------
// docker_compose_build

// NewComposeBuildAction returns docker_compose_build.
func NewComposeBuildAction() action.Action { return &composeBuildAction{} }

type composeBuildAction struct{ actBase }

func (a *composeBuildAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_build"
}

func (a *composeBuildAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = composeImagesSchema("Builds every service of a compose project that has a `build:` section, like `docker compose build`, " +
		"through BuildKit (no docker CLI or buildx binary), and tags each image `image` or `<project>-<service>` plus `build.tags`. " +
		"Services without `build:` are skipped. Build log lines are sent as progress.")
}

func (a *composeBuildAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	composeImagesValidate(ctx, req, resp)
}

func (a *composeBuildAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	m, p, svcs := composeImagesLoad(ctx, req, resp, &a.actBase)
	if p == nil {
		return
	}
	var names []string
	refs, ids := map[string]string{}, map[string]string{}
	built := map[string]any{}
	for _, svc := range svcs {
		if svc.Build == nil {
			continue
		}
		image := composeImageName(p.Name, svc)
		actProgress(ctx, resp, "building %s (%s)", svc.Name, image)
		res, err := composeBuild(ctx, a.pd, p, svc, func(line string) { actProgress(ctx, resp, "%s: %s", svc.Name, line) })
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_build failed", err.Error())
			return
		}
		names = append(names, svc.Name)
		refs[svc.Name] = image
		ids[svc.Name] = res.ImageID
		entry := map[string]any{}
		if insp, err := a.pd.Client.ImageInspect(ctx, image); err == nil {
			entry = composePlain(insp.InspectResponse, nil)
			if ids[svc.Name] == "" {
				ids[svc.Name] = insp.ID
			}
		}
		built[svc.Name] = entry
	}
	if len(names) == 0 {
		resp.Diagnostics.AddWarning("Nothing to build", fmt.Sprintf("no selected service of project %s has a build section", p.Name))
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-build", map[string]any{
		"name": p.Name, "service_names": names, "image_refs": refs, "image_ids": ids,
		"result": map[string]any{"built_images": built},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_compose_pull

// NewComposePullAction returns docker_compose_pull.
func NewComposePullAction() action.Action { return &composePullAction{} }

type composePullAction struct{ actBase }

func (a *composePullAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_pull"
}

func (a *composePullAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = composeImagesSchema("Pulls the image of every service of a compose project, like `docker compose pull`, with the " +
		"provider's `registry_auth`. Services that only build (no `image`) and services with `pull_policy: build` are skipped " +
		"(CLI `--ignore-buildable`). `result.pull_status` says what happened per service.")
}

func (a *composePullAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	composeImagesValidate(ctx, req, resp)
}

func (a *composePullAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	m, p, svcs := composeImagesLoad(ctx, req, resp, &a.actBase)
	if p == nil {
		return
	}
	var names []string
	status, digests, ids := map[string]string{}, map[string]string{}, map[string]string{}
	for _, svc := range svcs {
		names = append(names, svc.Name)
		if svc.Image == "" || svc.PullPolicy == ctypes.PullPolicyBuild {
			status[svc.Name] = "skipped (built)"
			continue
		}
		actProgress(ctx, resp, "pulling %s (%s)", svc.Name, svc.Image)
		if err := composePull(ctx, a.pd, svc.Image, svc.Platform); err != nil {
			resp.Diagnostics.AddError("docker_compose_pull failed", fmt.Sprintf("service %s image %s: %v", svc.Name, svc.Image, err))
			return
		}
		status[svc.Name] = "pulled"
		if insp, err := a.pd.Client.ImageInspect(ctx, svc.Image); err == nil {
			ids[svc.Name] = insp.ID
			if len(insp.RepoDigests) > 0 {
				digests[svc.Name] = insp.RepoDigests[0]
			}
		}
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-pull", map[string]any{
		"name": p.Name, "service_names": names, "image_digest_refs": digests, "image_ids": ids,
		"result": map[string]any{"pull_status": status},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_compose_push

// NewComposePushAction returns docker_compose_push.
func NewComposePushAction() action.Action { return &composePushAction{} }

type composePushAction struct{ actBase }

func (a *composePushAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_push"
}

func (a *composePushAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = composeImagesSchema("Pushes the image of every service of a compose project that names one (`image`), like " +
		"`docker compose push`, with the provider's `registry_auth`. `image_digest_refs` records what was pushed.")
}

func (a *composePushAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	composeImagesValidate(ctx, req, resp)
}

func (a *composePushAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	m, p, svcs := composeImagesLoad(ctx, req, resp, &a.actBase)
	if p == nil {
		return
	}
	var names []string
	status, refs, digests := map[string]string{}, map[string]string{}, map[string]string{}
	for _, svc := range svcs {
		names = append(names, svc.Name)
		if svc.Image == "" {
			status[svc.Name] = "skipped (no image name)"
			continue
		}
		auth, err := a.pd.registryAuthFor(svc.Image)
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_push failed", fmt.Sprintf("service %s: %v", svc.Name, err))
			return
		}
		actProgress(ctx, resp, "pushing %s (%s)", svc.Name, svc.Image)
		image := svc.Image
		digest, err := registryImagePushWithRetry(ctx, 0, func(ctx context.Context) (io.ReadCloser, error) {
			return a.pd.Client.ImagePush(ctx, image, client.ImagePushOptions{RegistryAuth: auth})
		})
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_push failed", fmt.Sprintf("service %s image %s: %v", svc.Name, svc.Image, err))
			return
		}
		refs[svc.Name] = svc.Image
		status[svc.Name] = "pushed"
		if digest != "" {
			repo := svc.Image
			if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
				repo = repo[:i]
			}
			digests[svc.Name] = repo + "@" + digest
		}
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-push", map[string]any{
		"name": p.Name, "service_names": names, "image_refs": refs, "image_digest_refs": digests,
		"result": map[string]any{"push_status": status},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_compose_publish

// Media types of a compose OCI artifact (docker compose publish).
const (
	composeArtifactType    = "application/vnd.docker.compose.project"
	composeFileMediaType   = "application/vnd.docker.compose.file+yaml"
	composeEmptyMediaType  = "application/vnd.oci.empty.v1+json"
	composeManifestMedia   = "application/vnd.oci.image.manifest.v1+json"
	composeFileAnnotation  = "com.docker.compose.file"
	composeVersionAnnotate = "com.docker.compose.version"
	// compose v5.1.4 internal/oci/push.go:40-61: the OCI 1.0 config media
	// type and the env file layers (`--with-env`).
	composeEmptyConfig10   = "application/vnd.docker.compose.config.empty.v1+json"
	composeEnvFileMedia    = "application/vnd.docker.compose.envfile"
	composeEnvFileAnnotate = "com.docker.compose.envfile"
	composeOCI10           = "1.0"
	composeOCI11           = "1.1"
)

// NewComposePublishAction returns docker_compose_publish.
func NewComposePublishAction() action.Action { return &composePublishAction{} }

type composePublishAction struct{ actBase }

type composePublishModel struct {
	composeSourceModel
	Repository          types.String `tfsdk:"repository"`
	InsecureSkipVerify  types.Bool   `tfsdk:"insecure_skip_verify"`
	WithEnv             types.Bool   `tfsdk:"with_env"`
	ResolveImageDigests types.Bool   `tfsdk:"resolve_image_digests"`
	OCIVersion          types.String `tfsdk:"oci_version"`
	ResultPath          types.String `tfsdk:"result_path"`
}

func (a *composePublishAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_publish"
}

func (a *composePublishAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Publishes a compose application as an OCI artifact, like `docker compose publish`: each compose file " +
			"becomes a `application/vnd.docker.compose.file+yaml` layer of an OCI manifest with artifact type " +
			"`application/vnd.docker.compose.project`, pushed with the registry HTTP API v2 (credentials from `registry_auth`). " +
			"The project is loaded and validated first. As in `docker compose publish`, each `env_file` path in the published files is " +
			"replaced by `<sha256 of the path>.env`; the env files themselves are published only with `with_env`. " +
			"`--app` (copying the service images into the repository) is not supported.",
		Attributes: composeActionSource(map[string]aschema.Attribute{
			"repository": aschema.StringAttribute{Required: true,
				MarkdownDescription: "Where to publish, `registry/repository:tag` (e.g. `registry.example.com/apps/shop:1.0`)."},
			"insecure_skip_verify": aschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Skip TLS verification of the registry. Default `false`. Loopback registries also fall back to plain http."},
			"with_env": aschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Publish the services' env files as `application/vnd.docker.compose.envfile` layers (CLI `--with-env`). Default `false`. Env files often hold secrets: check them first."},
			"resolve_image_digests": aschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Pin every service image to its registry digest in an extra `image-digests.yaml` layer (CLI `--resolve-image-digests`), resolved through the Engine API's distribution inspect. Default `false`."},
			"oci_version": aschema.StringAttribute{Optional: true,
				Validators:          []validator.String{stringvalidator.OneOf(composeOCI10, composeOCI11)},
				MarkdownDescription: "OCI image specification version of the manifest (CLI `--oci-version`): `1.1` (artifact type, empty config) or `1.0` (no artifact type, config media type `application/vnd.docker.compose.config.empty.v1+json`). Unset: try 1.1 and fall back to 1.0 when the registry rejects the manifest with a non-auth 4xx, like the CLI."},
			"result_path": actResultPathAttribute(),
		}),
	}
}

func (a *composePublishAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m composePublishModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() {
		composeValidateSource(m.composeSourceModel, &resp.Diagnostics)
	}
}

// composeArtifactFile is one layer to publish: a compose file, or an env
// file (MediaType composeEnvFileMedia).
type composeArtifactFile struct {
	Name      string
	Content   []byte
	MediaType string
}

// composeArtifactFiles returns the compose files of the source in order.
func composeArtifactFiles(content string, files []string, base string) ([]composeArtifactFile, error) {
	if content != "" {
		return []composeArtifactFile{{Name: composeContentFilename, Content: []byte(content)}}, nil
	}
	var out []composeArtifactFile
	for _, f := range files {
		p := f
		if base != "" && !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, composeArtifactFile{Name: filepath.Base(f), Content: b})
	}
	return out, nil
}

// composeDescriptor is an OCI descriptor.
type composeDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func composeDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// composeArtifactManifest builds the manifest and the blobs it references.
func composeArtifactManifest(files []composeArtifactFile) ([]byte, map[string][]byte, []composeDescriptor) {
	return composeArtifactManifestVersion(files, composeOCI11)
}

// composeArtifactManifestVersion builds the manifest for an OCI version
// (compose internal/oci/push.go:156-205 generateManifest): 1.1 has the
// artifact type and the empty config, 1.0 no artifact type and the compose
// empty config media type.
func composeArtifactManifestVersion(files []composeArtifactFile, oci string) ([]byte, map[string][]byte, []composeDescriptor) {
	empty := []byte("{}")
	blobs := map[string][]byte{composeDigest(empty): empty}
	var layers []composeDescriptor
	for _, f := range files {
		d := composeDigest(f.Content)
		blobs[d] = f.Content
		desc := composeDescriptor{MediaType: composeFileMediaType, Digest: d, Size: int64(len(f.Content)),
			Annotations: map[string]string{composeFileAnnotation: f.Name, composeVersionAnnotate: contractComposeGoVersion}}
		if f.MediaType == composeEnvFileMedia {
			desc.MediaType = composeEnvFileMedia
			desc.Annotations = map[string]string{composeEnvFileAnnotate: f.Name, composeVersionAnnotate: contractComposeGoVersion}
		}
		layers = append(layers, desc)
	}
	man := map[string]any{
		"schemaVersion": 2,
		"mediaType":     composeManifestMedia,
		"config":        composeDescriptor{MediaType: composeEmptyMediaType, Digest: composeDigest(empty), Size: int64(len(empty))},
		"layers":        layers,
	}
	if oci == composeOCI10 {
		man["config"] = composeDescriptor{MediaType: composeEmptyConfig10, Digest: composeDigest(empty), Size: int64(len(empty))}
	} else {
		man["artifactType"] = composeArtifactType
	}
	b, _ := json.Marshal(man)
	return b, blobs, layers
}

// composeRegistry is a minimal registry HTTP API v2 client for one repository.
type composeRegistry struct {
	hc         *http.Client
	base, repo string
	user, pass string
	authz      string
}

func (r *composeRegistry) do(ctx context.Context, method, u string, body []byte, header map[string]string) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(body))
		for k, v := range header {
			req.Header.Set(k, v)
		}
		if r.authz != "" {
			req.Header.Set("Authorization", r.authz)
		}
		resp, err := r.hc.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if r.authz, err = composeRegistryAuthorize(ctx, r.hc, challenge, r.repo, r.user, r.pass); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("unreachable")
}

func composeHTTPError(resp *http.Response, what string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(b)))
}

// pushBlob uploads one blob unless the registry has it (monolithic upload).
func (r *composeRegistry) pushBlob(ctx context.Context, digest string, data []byte) error {
	resp, err := r.do(ctx, http.MethodHead, r.base+"/v2/"+r.repo+"/blobs/"+digest, nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	resp, err = r.do(ctx, http.MethodPost, r.base+"/v2/"+r.repo+"/blobs/uploads/", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return composeHTTPError(resp, "starting blob upload")
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return err
	}
	baseURL, _ := url.Parse(r.base)
	loc = baseURL.ResolveReference(loc)
	q := loc.Query()
	q.Set("digest", digest)
	loc.RawQuery = q.Encode()
	put, err := r.do(ctx, http.MethodPut, loc.String(), data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	defer put.Body.Close()
	if put.StatusCode != http.StatusCreated {
		return composeHTTPError(put, "uploading blob "+digest)
	}
	return nil
}

// composeRegistryAuthorize answers a 401 challenge for push access.
func composeRegistryAuthorize(ctx context.Context, hc *http.Client, challenge, repo, user, pass string) (string, error) {
	scheme, params := registryImageParseChallenge(challenge)
	switch scheme {
	case "basic":
		if user == "" {
			return "", errors.New("registry requires basic auth and no registry_auth matches this registry")
		}
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		req.SetBasicAuth(user, pass)
		return req.Header.Get("Authorization"), nil
	case "bearer":
		realm := params["realm"]
		if realm == "" {
			return "", fmt.Errorf("bearer challenge without realm: %q", challenge)
		}
		q := url.Values{}
		if s := params["service"]; s != "" {
			q.Set("service", s)
		}
		q.Set("scope", "repository:"+repo+":pull,push")
		sep := "?"
		if strings.Contains(realm, "?") {
			sep = "&"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+sep+q.Encode(), nil)
		if err != nil {
			return "", err
		}
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", composeHTTPError(resp, "token request")
		}
		var tok struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
			return "", err
		}
		if tok.Token == "" {
			tok.Token = tok.AccessToken
		}
		return "Bearer " + tok.Token, nil
	}
	return "", fmt.Errorf("unsupported auth challenge %q", challenge)
}

// composePublish pushes the artifact and returns the manifest digest.
func composePublish(ctx context.Context, r *composeRegistry, tag string, files []composeArtifactFile) (string, []composeDescriptor, error) {
	return composePublishVersion(ctx, r, tag, files, "")
}

// composeManifestRejected is a manifest push refused with a non-auth 4xx.
type composeManifestRejected struct{ err error }

func (e composeManifestRejected) Error() string { return e.err.Error() }

// composePublishVersion pushes the artifact as OCI oci ("1.0", "1.1"), or
// with oci "" as 1.1 falling back to 1.0 when the registry rejects the
// manifest with a 4xx other than 401/403/407 (compose
// internal/oci/push.go:99-126 PushManifest, :147-153 isNonAuthClientError).
func composePublishVersion(ctx context.Context, r *composeRegistry, tag string, files []composeArtifactFile, oci string) (string, []composeDescriptor, error) {
	try := oci
	if try == "" {
		try = composeOCI11
	}
	digest, layers, err := composePushManifest(ctx, r, tag, files, try)
	var rej composeManifestRejected
	if oci == "" && errors.As(err, &rej) {
		return composePushManifest(ctx, r, tag, files, composeOCI10)
	}
	return digest, layers, err
}

func composePushManifest(ctx context.Context, r *composeRegistry, tag string, files []composeArtifactFile, oci string) (string, []composeDescriptor, error) {
	man, blobs, layers := composeArtifactManifestVersion(files, oci)
	for _, d := range composeSortedKeys(blobs) {
		if err := r.pushBlob(ctx, d, blobs[d]); err != nil {
			return "", nil, err
		}
	}
	resp, err := r.do(ctx, http.MethodPut, r.base+"/v2/"+r.repo+"/manifests/"+tag, man, map[string]string{"Content-Type": composeManifestMedia})
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		err := composeHTTPError(resp, "pushing manifest")
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusProxyAuthRequired:
		default:
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return "", nil, composeManifestRejected{err}
			}
		}
		return "", nil, err
	}
	return composeDigest(man), layers, nil
}

func (a *composePublishAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composePublishModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p := composeLoadSource(ctx, m.composeSourceModel, &resp.Diagnostics)
	if p == nil {
		return
	}
	files, err := composeArtifactFiles(m.Content.ValueString(), stringListFrom(ctx, m.Files, &resp.Diagnostics), m.ProjectDirectory.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_publish: cannot read compose files", err.Error())
		return
	}
	files, err = composePublishLayers(ctx, a.pd, p, files, m.WithEnv.ValueBool(), m.ResolveImageDigests.ValueBool())
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_publish: cannot prepare the artifact", err.Error())
		return
	}
	ref := m.Repository.ValueString()
	domain, repo, tag, err := imgRepoAndTag(ref)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_publish: invalid repository", err.Error())
		return
	}
	if tag == "" || strings.HasPrefix(tag, "sha256:") {
		tag = "latest"
	}
	var user, pass string
	if a.pd != nil {
		if cfg, ok, err := a.pd.registryAuthConfigFor(ref); err == nil && ok {
			user, pass = cfg.Username, cfg.Password
		}
	}
	host := registryImageAPIHost(domain)
	hc := &http.Client{Timeout: 2 * time.Minute}
	if m.InsecureSkipVerify.ValueBool() {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: http.ProxyFromEnvironment} //nolint:gosec // user opt-in
	}
	reg := &composeRegistry{hc: hc, base: "https://" + host, repo: repo, user: user, pass: pass}
	oci := m.OCIVersion.ValueString()
	digest, layers, err := composePublishVersion(ctx, reg, tag, files, oci)
	var netErr net.Error
	if err != nil && registryImageIsLocal(host) && (errors.As(err, &netErr) || strings.Contains(err.Error(), "HTTP response to HTTPS client")) {
		reg.base = "http://" + host
		digest, layers, err = composePublishVersion(ctx, reg, tag, files, oci)
	}
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_publish failed", fmt.Sprintf("%s: %v", ref, err))
		return
	}
	name := ref
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-publish", map[string]any{
		"name": p.Name, "image_ref": name + ":" + tag, "image_digest_ref": name + "@" + digest, "digest": digest,
		"result": map[string]any{"layers": layers},
	}, m.ResultPath)
}
