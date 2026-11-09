package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// p3eFakeRegistry is a v2 registry on 127.0.0.1 (plain http, which the
// client reaches through its local-registry fallback) with a token service.
type p3eFakeRegistry struct {
	srv      *httptest.Server
	mux      *http.ServeMux
	scheme   string // "bearer", "basic" or "none"
	user     string
	pass     string
	tokenQ   []string // raw query of each token request
	tokenAut []string
}

func newP3eFakeRegistry(t *testing.T, scheme string) *p3eFakeRegistry {
	t.Helper()
	r := &p3eFakeRegistry{mux: http.NewServeMux(), scheme: scheme, user: "bob", pass: "pw"}
	r.srv = httptest.NewServer(r.mux)
	t.Cleanup(r.srv.Close)
	r.mux.HandleFunc("/token", func(w http.ResponseWriter, req *http.Request) {
		r.tokenQ = append(r.tokenQ, req.URL.RawQuery)
		u, p, ok := req.BasicAuth()
		r.tokenAut = append(r.tokenAut, fmt.Sprintf("%v:%s:%s", ok, u, p))
		if ok && (u != r.user || p != r.pass) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		scope := req.URL.Query().Get("scope")
		p3eWriteJSON(w, http.StatusOK, map[string]any{"token": "tok:" + scope, "expires_in": 300, "issued_at": "2026-11-04T10:00:00Z"})
	})
	return r
}

func (r *p3eFakeRegistry) host() string { return r.srv.Listener.Addr().String() }

// authorized enforces the registry's auth scheme; it writes the 401 and
// returns false when the request lacks credentials.
func (r *p3eFakeRegistry) authorized(w http.ResponseWriter, req *http.Request) bool {
	authz := req.Header.Get("Authorization")
	switch r.scheme {
	case "bearer":
		if strings.HasPrefix(authz, "Bearer tok:") {
			return true
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake-registry"`, r.srv.URL))
	case "basic":
		if u, p, ok := req.BasicAuth(); ok && u == r.user && p == r.pass {
			return true
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
	default:
		return true
	}
	w.WriteHeader(http.StatusUnauthorized)
	return false
}

func (r *p3eFakeRegistry) handle(pattern string, h http.HandlerFunc) {
	r.mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
		if r.authorized(w, req) {
			h(w, req)
		}
	})
}

func p3eRegistryPD(host string) *providerData {
	return &providerData{RegistryAuth: []registryCredential{{Address: host, Username: "bob", Password: "pw"}}}
}

func TestRegistryTokenEphemeralBearer(t *testing.T) {
	reg := newP3eFakeRegistry(t, "bearer")
	reg.handle("/v2/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	res, diags := p3eOpenEphemeral(t, NewRegistryTokenEphemeralResource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{
		"registry": p3eStr(reg.host()),
		"scope":    p3eStr("repository:org/app:pull,push"),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m registryTokenModel
	res.Get(context.Background(), &m)
	var tok types.String
	res.GetAttribute(context.Background(), path.Root("token").AtName("token"), &tok)
	var tokIssued types.String
	res.GetAttribute(context.Background(), path.Root("token").AtName("issued_at"), &tokIssued)
	if tok.ValueString() != "tok:repository:org/app:pull,push" || tokIssued.ValueString() != "2026-11-04T10:00:00Z" || !m.DistributionAPIVersion.IsNull() || m.ExpiresIn.ValueInt64() != 300 ||
		m.IssuedAt.ValueString() != "2026-11-04T10:00:00Z" || m.ExpiresAt.ValueString() != "2026-11-04T10:05:00Z" || m.Service.ValueString() != "fake-registry" {
		t.Errorf("got %+v", m)
	}
	if len(reg.tokenAut) != 1 || reg.tokenAut[0] != "true:bob:pw" || !strings.Contains(reg.tokenQ[0], "service=fake-registry") {
		t.Errorf("token requests: %v %v", reg.tokenAut, reg.tokenQ)
	}
}

func TestRegistryTokenEphemeralAnonymous(t *testing.T) {
	reg := newP3eFakeRegistry(t, "bearer")
	reg.handle("/v2/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	_, diags := p3eOpenEphemeral(t, NewRegistryTokenEphemeralResource(), &providerData{}, map[string]tftypes.Value{"registry": p3eStr(reg.host())})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if reg.tokenAut[0] != "false::" {
		t.Errorf("anonymous token request sent credentials: %v", reg.tokenAut)
	}
}

func TestRegistryTokenEphemeralErrors(t *testing.T) {
	for _, tc := range []struct{ scheme, want string }{
		{"basic", "not bearer tokens"},
		{"none", "does not require authentication"},
	} {
		reg := newP3eFakeRegistry(t, tc.scheme)
		reg.handle("/v2/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		_, diags := p3eOpenEphemeral(t, NewRegistryTokenEphemeralResource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{"registry": p3eStr(reg.host())})
		if !diags.HasError() || !strings.Contains(diags[0].Detail(), tc.want) {
			t.Errorf("%s: diags = %v", tc.scheme, diags)
		}
	}
}

func TestP3NextLink(t *testing.T) {
	h := http.Header{}
	h.Add("Link", `</v2/org/app/tags/list?last=b&n=2>; rel="next"`)
	if got := p3NextLink(h, "http://reg:5000"); got != "/v2/org/app/tags/list?last=b&n=2" {
		t.Errorf("got %q", got)
	}
	h = http.Header{}
	h.Add("Link", `<https://other.example/v2/x/tags/list?last=b>; rel="next"`)
	if got := p3NextLink(h, "https://reg"); got != "https://other.example/v2/x/tags/list?last=b" {
		t.Errorf("got %q", got)
	}
	if got := p3NextLink(http.Header{}, "https://reg"); got != "" {
		t.Errorf("got %q", got)
	}
}

// TestAccRegistryTokenEphemeral asks a token-auth registry (Docker Hub
// unless TF_ACC_TOKEN_REGISTRY names another) for an anonymous pull token.
func TestAccRegistryTokenEphemeral(t *testing.T) {
	testAccPreCheck(t)
	reg := testAccRegistryHost(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccEphemeralFactories(),
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
ephemeral "docker_registry_token" "t" {
  registry = %q
  scope    = "repository:library/alpine:pull"
}

provider "echo" {
  data = ephemeral.docker_registry_token.t.expires_in
}

resource "echo" "t" {}
`, reg),
			Check: resource.TestCheckResourceAttrSet("echo.t", "data"),
		}},
	})
}

// testAccRegistryHost returns TF_ACC_TOKEN_REGISTRY, default docker.io (a
// registry with bearer-token auth and anonymous pulls). Set
// TF_ACC_SKIP_INTERNET to skip tests that need it.
func testAccRegistryHost(t *testing.T) string {
	t.Helper()
	if h := os.Getenv("TF_ACC_TOKEN_REGISTRY"); h != "" {
		return h
	}
	if os.Getenv("TF_ACC_SKIP_INTERNET") != "" {
		t.Skip("TF_ACC_SKIP_INTERNET set; skipping test that reaches Docker Hub")
	}
	return "docker.io"
}
