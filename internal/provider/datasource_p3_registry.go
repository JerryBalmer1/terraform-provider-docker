package provider

// Registry HTTP API v2 client for lane E: docker_registry_token (ephemeral),
// docker_registry_image_manifests and docker_registry_image_tags. It answers
// Basic and Bearer (WWW-Authenticate) challenges with the provider's
// registry_auth credentials, and falls back to plain http for local
// registries the way the daemon does.

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
)

// p3RegistryCreds returns the registry_auth username/password for domain.
func (pd *providerData) p3RegistryCreds(domain string) (string, string) {
	if pd == nil {
		return "", ""
	}
	for _, c := range pd.RegistryAuth {
		if normalizeRegistry(c.Address) == normalizeRegistry(domain) {
			return c.Username, c.Password
		}
	}
	return "", ""
}

// p3Registry talks to one registry (and, for data calls, one repository).
type p3Registry struct {
	hc         *http.Client
	host       string // API host, e.g. registry-1.docker.io
	base       string // "" until the first request picks https or http
	repo       string
	user, pass string
	authz      string // cached Authorization header value
}

func newP3Registry(pd *providerData, domain, repo string, insecure bool) *p3Registry {
	hc := &http.Client{Timeout: 60 * time.Second}
	if insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: http.ProxyFromEnvironment} //nolint:gosec // user opt-in
	}
	user, pass := pd.p3RegistryCreds(domain)
	return &p3Registry{hc: hc, host: registryImageAPIHost(normalizeRegistry(domain)), repo: repo, user: user, pass: pass}
}

// send issues one request to base+p, picking the scheme on first use.
func (r *p3Registry) send(ctx context.Context, p string, accept []string) (*http.Response, error) {
	do := func(base string) (*http.Response, error) {
		target := base + p
		if strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "http://") {
			target = p
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if len(accept) > 0 {
			req.Header.Set("Accept", strings.Join(accept, ", "))
		}
		if r.authz != "" {
			req.Header.Set("Authorization", r.authz)
		}
		return r.hc.Do(req)
	}
	if r.base != "" {
		return do(r.base)
	}
	resp, err := do("https://" + r.host)
	if err == nil {
		r.base = "https://" + r.host
		return resp, nil
	}
	var netErr net.Error
	if registryImageIsLocal(r.host) && (errors.As(err, &netErr) || strings.Contains(err.Error(), "HTTP response to HTTPS client")) {
		if resp, err2 := do("http://" + r.host); err2 == nil {
			r.base = "http://" + r.host
			return resp, nil
		}
	}
	return nil, err
}

// get GETs p (a /v2/... path or an absolute URL on the same registry),
// answering one auth challenge. scope is the bearer scope to ask for; "" uses
// the challenge's own scope or repository:<repo>:pull.
func (r *p3Registry) get(ctx context.Context, p string, accept ...string) (*http.Response, error) {
	resp, err := r.send(ctx, p, accept)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || r.authz != "" {
		return resp, nil
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	scope := ""
	if r.repo != "" {
		scope = "repository:" + r.repo + ":pull"
	}
	authz, _, err := p3Authorize(ctx, r.hc, challenge, scope, r.user, r.pass)
	if err != nil {
		return nil, err
	}
	r.authz = authz
	return r.send(ctx, p, accept)
}

// getJSON GETs p and decodes a 200 JSON body into v, returning the response
// headers.
func (r *p3Registry) getJSON(ctx context.Context, p string, v any, accept ...string) (http.Header, error) {
	resp, err := r.get(ctx, p, accept...)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GET %s: %s: %s", p, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", p, err)
	}
	return resp.Header, nil
}

// p3Token is a registry bearer token response.
type p3Token struct {
	Token     string
	ExpiresIn int64
	IssuedAt  time.Time
	Realm     string
	Service   string
}

// p3Authorize answers a 401 challenge and returns the Authorization header
// value. For Bearer it also returns the token. scope "" uses the challenge's
// scope parameter, if any.
func p3Authorize(ctx context.Context, hc *http.Client, challenge, scope, user, pass string) (string, *p3Token, error) {
	scheme, params := registryImageParseChallenge(challenge)
	switch scheme {
	case "basic":
		if user == "" {
			return "", nil, errors.New("registry requires basic auth and no registry_auth matches this registry")
		}
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		req.SetBasicAuth(user, pass)
		return req.Header.Get("Authorization"), nil, nil
	case "bearer":
		if scope == "" {
			scope = params["scope"]
		}
		tok, err := p3FetchToken(ctx, hc, params["realm"], params["service"], scope, user, pass)
		if err != nil {
			return "", nil, err
		}
		return "Bearer " + tok.Token, tok, nil
	default:
		return "", nil, fmt.Errorf("unsupported registry auth challenge %q", challenge)
	}
}

// p3FetchToken runs the token flow of the distribution auth spec: GET
// realm?service=..&scope=.. with basic credentials when known.
func p3FetchToken(ctx context.Context, hc *http.Client, realm, service, scope, user, pass string) (*p3Token, error) {
	if realm == "" {
		return nil, errors.New("bearer challenge without realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return nil, fmt.Errorf("bearer realm %q: %w", realm, err)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	for _, s := range strings.Fields(scope) {
		q.Add("scope", s)
	}
	q.Set("client_id", "terraform-provider-docker")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching registry token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("fetching registry token from %s: %s: %s", realm, resp.Status, strings.TrimSpace(string(body)))
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		IssuedAt    string `json:"issued_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding registry token: %w", err)
	}
	t := &p3Token{Token: body.Token, ExpiresIn: body.ExpiresIn, Realm: realm, Service: service}
	if t.Token == "" {
		t.Token = body.AccessToken
	}
	if t.Token == "" {
		return nil, errors.New("registry token response holds no token")
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = 60 // spec default
	}
	t.IssuedAt = time.Now().UTC()
	if body.IssuedAt != "" {
		if ts, err := time.Parse(time.RFC3339, body.IssuedAt); err == nil {
			t.IssuedAt = ts.UTC()
		}
	}
	return t, nil
}

// p3RegistryToken pings /v2/ and turns its challenge into a bearer token for
// scope.
func p3RegistryToken(ctx context.Context, r *p3Registry, scope string) (*p3Token, error) {
	resp, err := r.send(ctx, "/v2/", nil)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
	case http.StatusOK:
		return nil, fmt.Errorf("registry %s does not require authentication (GET /v2/ answered 200), so it issues no token", r.host)
	default:
		return nil, fmt.Errorf("GET %s/v2/: %s", r.base, resp.Status)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	scheme, params := registryImageParseChallenge(challenge)
	if scheme != "bearer" {
		return nil, fmt.Errorf("registry %s uses %q auth, not bearer tokens; use the registry_auth username and password directly", r.host, scheme)
	}
	return p3FetchToken(ctx, r.hc, params["realm"], params["service"], scope, r.user, r.pass)
}

// p3NextLink returns the URL of a `Link: <...>; rel="next"` header, resolved
// against base, or "".
func p3NextLink(h http.Header, base string) string {
	for _, l := range h.Values("Link") {
		for _, part := range strings.Split(l, ",") {
			target, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
				continue
			}
			target = strings.Trim(strings.TrimSpace(target), "<>")
			bu, err := url.Parse(base)
			if err != nil {
				return target
			}
			tu, err := bu.Parse(target)
			if err != nil {
				return target
			}
			if tu.Host == bu.Host {
				return tu.RequestURI()
			}
			return tu.String()
		}
	}
	return ""
}
