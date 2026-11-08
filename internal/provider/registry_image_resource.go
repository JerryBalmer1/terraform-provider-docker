package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/client"
)

var (
	_ resource.Resource                = (*registryImageResource)(nil)
	_ resource.ResourceWithConfigure   = (*registryImageResource)(nil)
	_ resource.ResourceWithImportState = (*registryImageResource)(nil)
)

// NewRegistryImageResource returns the docker_registry_image resource.
func NewRegistryImageResource() resource.Resource { return &registryImageResource{} }

type registryImageResource struct {
	pd *providerData
}

type registryImageResourceModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	KeepRemotely       types.Bool   `tfsdk:"keep_remotely"`
	Triggers           types.Map    `tfsdk:"triggers"`
	PushRetries        types.Int64  `tfsdk:"push_retries"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
	Sha256Digest       types.String `tfsdk:"sha256_digest"`
}

func (r *registryImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registry_image"
}

func (r *registryImageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Pushes an image that exists on the provider's daemon (typically a `docker_image`) to its registry, " +
			"using the provider's `registry_auth` credentials. Read refreshes `sha256_digest` from the registry. " +
			"A push only happens on create: tie `triggers` to the source image (e.g. `docker_image.app.image_id`) so a rebuilt image is pushed again.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The pushed reference (same as `name`).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Local image reference to push, including registry host and tag (e.g. `registry.example.com/team/app:1.2`; " +
					"no tag means `latest`). The image must already exist on the daemon under this name. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"keep_remotely": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "When false (default), destroy deletes the manifest from the registry through the Registry HTTP API v2 " +
					"(`DELETE /v2/<repo>/manifests/<digest>`, the registry must allow deletes). If that fails the resource is removed from state with a warning. " +
					"When true, the image is left in the registry.",
			},
			"triggers": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Arbitrary string map; any change pushes the image again. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"push_retries": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(3),
				MarkdownDescription: "How many times to retry a failed push, with exponential backoff (1s, 2s, 4s, ... capped at 30s). Default `3`.",
			},
			"insecure_skip_verify": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Skip TLS verification when the provider itself talks to the registry (only for the delete on destroy; " +
					"pushes go through the daemon and follow its insecure-registries setting). Default `false`.",
			},
			"sha256_digest": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Manifest digest (`sha256:...`) of the pushed image, from the push result or the registry.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *registryImageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// registryImageBackoff is the wait before retry n (0-based): 1s, 2s, 4s ... 30s.
func registryImageBackoff(n int) time.Duration {
	if n > 5 {
		return 30 * time.Second
	}
	d := time.Second << n
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// registryImageRetryable is false for errors no retry can fix.
func registryImageRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"does not exist", "no such image", "unauthorized", "authentication required", "denied", "invalid reference"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

// registryImageSleep is replaced in tests.
var registryImageSleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// registryImagePushWithRetry calls push up to 1+retries times and returns
// the digest from the last successful push's aux messages ("" if none).
func registryImagePushWithRetry(ctx context.Context, retries int, push func(context.Context) (io.ReadCloser, error)) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			wait := registryImageBackoff(attempt - 1)
			tflog.Warn(ctx, "push failed, retrying", map[string]any{"attempt": attempt, "wait": wait.String(), "error": lastErr.Error()})
			if err := registryImageSleep(ctx, wait); err != nil {
				return "", err
			}
		}
		rc, err := push(ctx)
		if err == nil {
			var res imgStreamResult
			res, err = imgDecodeStream(rc, imgLogMessage(ctx))
			rc.Close()
			if err == nil {
				return imgPushedDigest(res.Aux), nil
			}
		}
		lastErr = err
		if !registryImageRetryable(err) {
			break
		}
	}
	return "", lastErr
}

func (r *registryImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var plan registryImageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	auth, err := r.pd.registryAuthFor(name)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid image reference", err.Error())
		return
	}
	digest, err := registryImagePushWithRetry(ctx, int(plan.PushRetries.ValueInt64()), func(ctx context.Context) (io.ReadCloser, error) {
		return r.pd.Client.ImagePush(ctx, name, client.ImagePushOptions{RegistryAuth: auth})
	})
	if err != nil {
		resp.Diagnostics.AddError("Image push failed", fmt.Sprintf("%s: %v", name, err))
		return
	}
	if digest == "" {
		di, err := r.pd.Client.DistributionInspect(ctx, name, client.DistributionInspectOptions{EncodedRegistryAuth: auth})
		if err != nil {
			resp.Diagnostics.AddError("Pushed, but cannot read the digest", fmt.Sprintf("%s: %v", name, err))
			return
		}
		digest = di.Descriptor.Digest.String()
	}
	plan.ID = types.StringValue(name)
	plan.Sha256Digest = types.StringValue(digest)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// registryImageNotFound reports registry/daemon "no such manifest" errors.
func registryImageNotFound(err error) bool {
	if isNotFound(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "not found")
}

func (r *registryImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.pd == nil {
		return
	}
	var st registryImageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := st.Name.ValueString()
	if name == "" {
		name = st.ID.ValueString() // import
		st.Name = types.StringValue(name)
	}
	auth, err := r.pd.registryAuthFor(name)
	if err != nil {
		resp.Diagnostics.AddError("Invalid image reference", err.Error())
		return
	}
	di, err := r.pd.Client.DistributionInspect(ctx, name, client.DistributionInspectOptions{EncodedRegistryAuth: auth})
	if err != nil {
		if registryImageNotFound(err) {
			tflog.Warn(ctx, "image gone from registry; removing from state", map[string]any{"name": name})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Cannot inspect image in registry", fmt.Sprintf("%s: %v", name, err))
		return
	}
	st.ID = types.StringValue(name)
	st.Sha256Digest = types.StringValue(di.Descriptor.Digest.String())
	if st.KeepRemotely.IsNull() {
		st.KeepRemotely = types.BoolValue(false)
	}
	if st.PushRetries.IsNull() {
		st.PushRetries = types.Int64Value(3)
	}
	if st.InsecureSkipVerify.IsNull() {
		st.InsecureSkipVerify = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// Update handles keep_remotely / push_retries / insecure_skip_verify only.
func (r *registryImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st registryImageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.Sha256Digest = st.ID, st.Sha256Digest
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *registryImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.pd == nil {
		return
	}
	var st registryImageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() || st.KeepRemotely.ValueBool() {
		return
	}
	name := st.Name.ValueString()
	cfg, _, err := r.pd.registryAuthConfigFor(name)
	if err == nil {
		err = registryImageDeleteRemote(ctx, name, st.Sha256Digest.ValueString(), cfg.Username, cfg.Password, st.InsecureSkipVerify.ValueBool())
	}
	if err != nil {
		resp.Diagnostics.AddWarning("Image not deleted from registry",
			fmt.Sprintf("%s was removed from Terraform state, but deleting %s from the registry failed: %v. "+
				"Delete it manually or set keep_remotely = true.", name, st.Sha256Digest.ValueString(), err))
	}
}

func (r *registryImageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ---------------------------------------------------------------------------
// Registry HTTP API v2 manifest delete

// registryImageAPIHost maps a reference domain to the host serving the v2 API.
func registryImageAPIHost(domain string) string {
	if domain == "docker.io" {
		return "registry-1.docker.io"
	}
	return domain
}

// registryImageIsLocal reports hosts the docker daemon treats as insecure by
// default, where plain http is tried after https fails.
func registryImageIsLocal(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// registryImageDeleteRemote deletes manifest digest of name's repository.
func registryImageDeleteRemote(ctx context.Context, name, digest, user, pass string, insecure bool) error {
	domain, repo, _, err := imgRepoAndTag(name)
	if err != nil {
		return err
	}
	if digest == "" {
		return errors.New("no digest in state")
	}
	host := registryImageAPIHost(domain)
	hc := &http.Client{Timeout: 60 * time.Second}
	if insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: http.ProxyFromEnvironment} //nolint:gosec // user opt-in
	}
	err = registryImageDeleteManifest(ctx, hc, "https://"+host, repo, digest, user, pass)
	var netErr net.Error
	var opErr *net.OpError
	if err != nil && registryImageIsLocal(host) && (errors.As(err, &netErr) || errors.As(err, &opErr) || strings.Contains(err.Error(), "HTTP response to HTTPS client")) {
		err = registryImageDeleteManifest(ctx, hc, "http://"+host, repo, digest, user, pass)
	}
	return err
}

// registryImageDeleteManifest issues DELETE <base>/v2/<repo>/manifests/<digest>,
// answering a Basic or Bearer challenge with user/pass. 404 counts as success.
func registryImageDeleteManifest(ctx context.Context, hc *http.Client, base, repo, digest, user, pass string) error {
	u := strings.TrimRight(base, "/") + "/v2/" + repo + "/manifests/" + digest
	do := func(authz string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
		if err != nil {
			return nil, err
		}
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		return hc.Do(req)
	}
	resp, err := do("")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		authz, err := registryImageAuthorize(ctx, hc, challenge, repo, user, pass)
		if err != nil {
			return err
		}
		if resp, err = do(authz); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound:
		return nil
	case resp.StatusCode == http.StatusMethodNotAllowed:
		return fmt.Errorf("registry does not allow deletes (405; for distribution/registry set REGISTRY_STORAGE_DELETE_ENABLED=true): %s", strings.TrimSpace(string(body)))
	default:
		return fmt.Errorf("DELETE %s: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
}

// registryImageParseChallenge parses `Bearer realm="...",service="..."`.
func registryImageParseChallenge(h string) (scheme string, params map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params = map[string]string{}
	for rest != "" {
		var kv string
		// split on commas outside quotes
		inQ := false
		i := 0
		for ; i < len(rest); i++ {
			if rest[i] == '"' {
				inQ = !inQ
			}
			if rest[i] == ',' && !inQ {
				break
			}
		}
		kv, rest = rest[:i], strings.TrimPrefix(rest[i:], ",")
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		params[strings.ToLower(k)] = strings.Trim(v, `"`)
	}
	return strings.ToLower(scheme), params
}

// registryImageAuthorize answers a 401 challenge and returns the
// Authorization header value to retry with.
func registryImageAuthorize(ctx context.Context, hc *http.Client, challenge, repo, user, pass string) (string, error) {
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
		q.Set("scope", "repository:"+repo+":delete,pull")
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
			return "", fmt.Errorf("fetching registry token: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("fetching registry token: %s", resp.Status)
		}
		var tok struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
			return "", fmt.Errorf("decoding registry token: %w", err)
		}
		t := tok.Token
		if t == "" {
			t = tok.AccessToken
		}
		if t == "" {
			return "", errors.New("registry token response holds no token")
		}
		return "Bearer " + t, nil
	default:
		return "", fmt.Errorf("unsupported registry auth challenge %q", challenge)
	}
}
