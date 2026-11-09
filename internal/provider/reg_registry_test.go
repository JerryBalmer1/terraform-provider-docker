package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ---------------------------------------------------------------------------
// Schemas and identity outputs

func TestRegSchemasAndIdentityOutputs(t *testing.T) {
	ctx := context.Background()
	check := func(item string, attrs map[string]bool) {
		for _, n := range contractIdentityOutputNames(item) {
			if !attrs[n] {
				t.Errorf("%s: identity output %q missing from the schema", item, n)
			}
		}
	}
	keys := func(m map[string]bool, names ...string) map[string]bool {
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	for item, r := range map[string]fwresource.Resource{regImageItem: NewRegistryImageResource(), regMLItem: NewRegistryManifestListResource()} {
		var sr fwresource.SchemaResponse
		r.Schema(ctx, fwresource.SchemaRequest{}, &sr)
		if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Fatalf("%s: %v", item, d)
		}
		m := map[string]bool{}
		for n := range sr.Schema.Attributes {
			keys(m, n)
		}
		check(item, m)
		if sr.Schema.Attributes["name"].(rschema.StringAttribute).MarkdownDescription == "" ||
			!strings.Contains(sr.Schema.Attributes["name"].GetMarkdownDescription(), "Forces replacement.") {
			t.Errorf("%s: name must say Forces replacement.", item)
		}
	}
	for item, d := range map[string]datasource.DataSource{regImageDataItem: NewRegistryImageDataSource(), regTagsItem: NewRegistryImageTagsDataSource(), regManifestsItem: NewRegistryImageManifestsDataSource()} {
		var sr datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &sr)
		if dg := sr.Schema.ValidateImplementation(ctx); dg.HasError() {
			t.Fatalf("%s: %v", item, dg)
		}
		m := map[string]bool{}
		for n := range sr.Schema.Attributes {
			keys(m, n)
		}
		check(item, m)
		if item == regManifestsItem {
			check("cli-only:manifest-inspect", m)
		}
	}
	for item, e := range map[string]ephemeral.EphemeralResource{regTokenItem: NewRegistryTokenEphemeralResource(), regLoginItem: NewRegistryLoginEphemeralResource()} {
		var sr ephemeral.SchemaResponse
		e.Schema(ctx, ephemeral.SchemaRequest{}, &sr)
		if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Fatalf("%s: %v", item, d)
		}
		m := map[string]bool{}
		for n := range sr.Schema.Attributes {
			keys(m, n)
		}
		check(item, m)
	}
}

// ---------------------------------------------------------------------------
// Resource drive helpers

// regResSchema configures r with pd and returns its schema.
func regResSchema(t *testing.T, r fwresource.Resource, pd *providerData) rschema.Schema {
	t.Helper()
	ctx := context.Background()
	if rc, ok := r.(fwresource.ResourceWithConfigure); ok {
		rc.Configure(ctx, fwresource.ConfigureRequest{ProviderData: pd}, &fwresource.ConfigureResponse{})
	}
	var sr fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &sr)
	return sr.Schema
}

// regPlanValue is cfg with every computed attribute not in cfg unknown.
func regPlanValue(ctx context.Context, s rschema.Schema, cfg map[string]tftypes.Value) tftypes.Value {
	typ := s.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for k, v := range cfg {
		vals[k] = v
	}
	for n, a := range s.Attributes {
		if _, ok := vals[n]; !ok && a.IsComputed() {
			vals[n] = tftypes.NewValue(typ.AttributeTypes[n], tftypes.UnknownValue)
		}
	}
	return p3eObject(typ, vals)
}

// regCreate runs Create with cfg as config and plan.
func regCreate(t *testing.T, r fwresource.Resource, pd *providerData, cfg map[string]tftypes.Value) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	s := regResSchema(t, r, pd)
	typ := s.Type().TerraformType(ctx)
	resp := fwresource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}}
	r.Create(ctx, fwresource.CreateRequest{
		Config: tfsdk.Config{Schema: s, Raw: p3eObject(typ, cfg)},
		Plan:   tfsdk.Plan{Schema: s, Raw: regPlanValue(ctx, s, cfg)},
	}, &resp)
	return resp.State, resp.Diagnostics
}

// regRead runs Read on st; removed reports RemoveResource.
func regRead(t *testing.T, r fwresource.Resource, st tfsdk.State) (tfsdk.State, bool, diag.Diagnostics) {
	t.Helper()
	resp := fwresource.ReadResponse{State: st}
	r.Read(context.Background(), fwresource.ReadRequest{State: st}, &resp)
	return resp.State, resp.State.Raw.IsNull(), resp.Diagnostics
}

// regModifyPlan runs the resource-level ModifyPlan for an update from st
// to cfg and returns the plan.
func regModifyPlan(t *testing.T, r fwresource.Resource, st tfsdk.State, cfg map[string]tftypes.Value) (tfsdk.Plan, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	s := st.Schema.(rschema.Schema)
	plan := tfsdk.Plan{Schema: s, Raw: regPlanValue(ctx, s, cfg)}
	resp := fwresource.ModifyPlanResponse{Plan: plan}
	req := fwresource.ModifyPlanRequest{Config: tfsdk.Config{Schema: s, Raw: p3eObject(s.Type().TerraformType(ctx), cfg)}, Plan: plan, State: st}
	r.(fwresource.ResourceWithModifyPlan).ModifyPlan(ctx, req, &resp)
	return resp.Plan, resp.Diagnostics
}

// regStateString reads one string attribute of a state or plan.
func regStateString(t *testing.T, get func(context.Context, path.Path, any) diag.Diagnostics, attr string) string {
	t.Helper()
	var v types.String
	if d := get(context.Background(), path.Root(attr), &v); d.HasError() {
		t.Fatalf("%s: %v", attr, d)
	}
	if v.IsUnknown() {
		return "<unknown>"
	}
	return v.ValueString()
}

// ---------------------------------------------------------------------------
// docker_registry_image (resource) and data docker_registry_image

const regTestDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func regDistributionJSON() map[string]any {
	return map[string]any{
		"Descriptor": map[string]any{"mediaType": p3MediaOCIIndex, "digest": regTestDigest, "size": 1234},
		"Platforms": []map[string]any{
			{"architecture": "amd64", "os": "linux"},
			{"architecture": "arm64", "os": "linux", "variant": "v8"},
		},
	}
}

func TestRegRegistryImageResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	var pushes int
	f.on("POST /images/localhost:5000/app/push", func(w http.ResponseWriter, r *http.Request) {
		pushes++
		if r.URL.Query().Get("tag") != "1" {
			t.Errorf("push tag = %q", r.URL.Query().Get("tag"))
		}
		fmt.Fprintf(w, `{"status":"pushing"}`+"\n"+`{"aux":{"Tag":"1","Digest":%q,"Size":1}}`+"\n", regTestDigest)
	})
	gone := false
	f.on("GET /distribution/*", func(w http.ResponseWriter, r *http.Request) {
		if gone {
			p3eWriteJSON(w, http.StatusNotFound, map[string]string{"message": "manifest unknown"})
			return
		}
		p3eWriteJSON(w, http.StatusOK, regDistributionJSON())
	})
	r := NewRegistryImageResource()
	cfg := map[string]tftypes.Value{
		"name":                 p3eStr("localhost:5000/app:1"),
		"keep_remotely":        tftypes.NewValue(tftypes.Bool, true),
		"push_retries":         tftypes.NewValue(tftypes.Number, 0),
		"insecure_skip_verify": tftypes.NewValue(tftypes.Bool, false),
	}
	st, diags := regCreate(t, r, pd, cfg)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if pushes != 1 {
		t.Errorf("pushes = %d", pushes)
	}
	var m registryImageResourceModel
	if d := st.Get(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	if m.Digest.ValueString() != regTestDigest || m.ImageRef.ValueString() != "localhost:5000/app:1" ||
		m.ImageDigestRef.ValueString() != "localhost:5000/app@"+regTestDigest {
		t.Errorf("refs: %s %s %s", m.Digest, m.ImageRef, m.ImageDigestRef)
	}
	var plats []string
	m.Platforms.ElementsAs(ctx, &plats, false)
	if strings.Join(plats, ",") != "linux/amd64,linux/arm64/v8" {
		t.Errorf("platforms = %v", plats)
	}
	if !strings.Contains(m.DistributionJSON.ValueString(), regTestDigest) || len(m.ContentSHA256.ValueString()) != 64 {
		t.Errorf("distribution_json = %s, hash = %s", m.DistributionJSON, m.ContentSHA256)
	}
	if m.EngineAPIVersion.IsUnknown() || m.DockerServerVersion.ValueString() != "29.0.0" {
		t.Errorf("versions: %s %s", m.EngineAPIVersion, m.DockerServerVersion)
	}
	var desc types.String
	st.GetAttribute(ctx, path.Root("distribution").AtName("descriptor").AtName("digest"), &desc)
	if desc.ValueString() != regTestDigest {
		t.Errorf("distribution.descriptor.digest = %s", desc)
	}

	// In-place update (push_retries) keeps the outputs; a trigger change does not.
	upd := map[string]tftypes.Value{}
	for k, v := range cfg {
		upd[k] = v
	}
	upd["push_retries"] = tftypes.NewValue(tftypes.Number, 2)
	plan, diags := regModifyPlan(t, r, st, upd)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if got := regStateString(t, plan.GetAttribute, "distribution_json"); got != m.DistributionJSON.ValueString() {
		t.Errorf("update plan distribution_json = %s", got)
	}
	upd["triggers"] = p3eStrMap(map[string]string{"v": "2"})
	plan, _ = regModifyPlan(t, r, st, upd)
	if got := regStateString(t, plan.GetAttribute, "distribution_json"); got != "<unknown>" {
		t.Errorf("replace plan kept distribution_json: %s", got)
	}

	// Read refreshes; import (only id) fills name.
	st2, removed, diags := regRead(t, r, st)
	if diags.HasError() || removed {
		t.Fatal(removed, diags)
	}
	if regStateString(t, st2.GetAttribute, "digest") != regTestDigest {
		t.Error("read digest")
	}
	imp := tfsdk.State{Schema: st.Schema, Raw: p3eObject(st.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{"id": p3eStr("localhost:5000/app:1")})}
	st3, _, diags := regRead(t, r, imp)
	if diags.HasError() || regStateString(t, st3.GetAttribute, "name") != "localhost:5000/app:1" || regStateString(t, st3.GetAttribute, "image_digest_ref") == "" {
		t.Fatalf("import: %v", diags)
	}

	// Gone from the registry: removed from state.
	gone = true
	if _, removed, _ := regRead(t, r, st); !removed {
		t.Error("read of a deleted image should remove it")
	}

	// keep_remotely = true: delete talks to nobody.
	n := len(f.requests())
	var dr fwresource.DeleteResponse
	r.Delete(ctx, fwresource.DeleteRequest{State: st}, &dr)
	if dr.Diagnostics.HasError() || len(f.requests()) != n {
		t.Errorf("delete: %v %v", dr.Diagnostics, f.requests()[n:])
	}
}

func TestRegRegistryImageResourcePushError(t *testing.T) {
	f, pd := newP3eFake(t)
	f.on("POST /images/localhost:5000/app/push", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"errorDetail":{"message":"unauthorized: authentication required"},"error":"unauthorized: authentication required"}`+"\n")
	})
	_, diags := regCreate(t, NewRegistryImageResource(), pd, map[string]tftypes.Value{
		"name":                 p3eStr("localhost:5000/app:1"),
		"keep_remotely":        tftypes.NewValue(tftypes.Bool, true),
		"push_retries":         tftypes.NewValue(tftypes.Number, 3),
		"insecure_skip_verify": tftypes.NewValue(tftypes.Bool, false),
	})
	if !diags.HasError() || !strings.Contains(diags[0].Detail(), "unauthorized") {
		t.Fatalf("diags = %v", diags)
	}
}

func TestRegRegistryImageDataSource(t *testing.T) {
	ctx := context.Background()
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /distribution/*", regDistributionJSON())
	st, diags := p3eReadDS(t, NewRegistryImageDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("nginx:1.27")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m registryImageDataSourceModel
	if d := st.Get(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	if m.Digest.ValueString() != regTestDigest || m.ID.ValueString() != "nginx:1.27@"+regTestDigest ||
		m.ImageRef.ValueString() != "docker.io/library/nginx:1.27" || m.ImageDigestRef.ValueString() != "docker.io/library/nginx@"+regTestDigest {
		t.Errorf("got %s %s %s %s", m.Digest, m.ID, m.ImageRef, m.ImageDigestRef)
	}
	if !m.Labels.IsNull() || !m.Config.IsNull() || !m.Created.IsNull() {
		t.Errorf("registry side must leave labels/config/created null")
	}
	var os types.String
	st.GetAttribute(ctx, path.Root("manifests").AtListIndex(1).AtName("platform").AtName("variant"), &os)
	if os.ValueString() != "v8" || len(m.Manifests.Elements()) != 2 {
		t.Errorf("manifests = %s", m.Manifests)
	}
	if !strings.Contains(m.InspectJSON.ValueString(), `"Platforms"`) || m.ContentSHA256.IsNull() {
		t.Errorf("inspect_json = %s", m.InspectJSON)
	}
}

func TestRegRegistryImageDataSourceError(t *testing.T) {
	f, pd := newP3eFake(t)
	f.on("GET /distribution/*", func(w http.ResponseWriter, _ *http.Request) {
		p3eWriteJSON(w, http.StatusUnauthorized, map[string]string{"message": "denied"})
	})
	_, diags := p3eReadDS(t, NewRegistryImageDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("org/private:1")})
	if !diags.HasError() {
		t.Fatal("want error")
	}
	_, diags = p3eReadDS(t, NewRegistryImageDataSource(), pd, map[string]tftypes.Value{"name": p3eStr("Not A Ref")})
	if !diags.HasError() {
		t.Fatal("want invalid reference error")
	}
}

// ---------------------------------------------------------------------------
// docker_registry_login

func TestRegLoginEphemeral(t *testing.T) {
	ctx := context.Background()
	f, pd := newP3eFake(t)
	pd.RegistryAuth = []registryCredential{{Address: "ghcr.io", Username: "bob", Password: "pw"}}
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	var got map[string]string
	f.on("POST /auth", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["password"] != "pw" {
			p3eWriteJSON(w, http.StatusUnauthorized, map[string]string{"message": "login attempt failed"})
			return
		}
		p3eWriteJSON(w, http.StatusOK, map[string]string{"Status": "Login Succeeded", "IdentityToken": "idtok"})
	})
	res, diags := p3eOpenEphemeral(t, NewRegistryLoginEphemeralResource(), pd, map[string]tftypes.Value{"server_address": p3eStr("ghcr.io")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if got["username"] != "bob" || got["serveraddress"] != "ghcr.io" {
		t.Errorf("request = %v", got)
	}
	var m regLoginModel
	res.Get(ctx, &m)
	if m.Username.ValueString() != "bob" || !strings.Contains(m.AuthJSON.ValueString(), "idtok") || len(m.ContentSHA256.ValueString()) != 64 {
		t.Errorf("got %+v", m)
	}
	var tok, status types.String
	res.GetAttribute(ctx, path.Root("auth").AtName("identity_token"), &tok)
	res.GetAttribute(ctx, path.Root("auth").AtName("status"), &status)
	if tok.ValueString() != "idtok" || status.ValueString() != "Login Succeeded" {
		t.Errorf("auth = %s %s", tok, status)
	}

	// Explicit wrong password: error. No credentials at all: error.
	_, diags = p3eOpenEphemeral(t, NewRegistryLoginEphemeralResource(), pd, map[string]tftypes.Value{
		"server_address": p3eStr("ghcr.io"), "username": p3eStr("bob"), "password": p3eStr("bad"),
	})
	if !diags.HasError() {
		t.Error("wrong password should fail")
	}
	_, diags = p3eOpenEphemeral(t, NewRegistryLoginEphemeralResource(), pd, map[string]tftypes.Value{})
	if !diags.HasError() || !strings.Contains(diags[0].Detail(), "https://index.docker.io/v1/") {
		t.Errorf("no credentials: %v", diags)
	}
}

// ---------------------------------------------------------------------------
// docker_registry_image_tags and docker_registry_token contract outputs

func TestRegTagsContract(t *testing.T) {
	ctx := context.Background()
	reg := newP3eFakeRegistry(t, "bearer")
	reg.handle("/v2/org/app/tags/list", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		p3eWriteJSON(w, http.StatusOK, map[string]any{"name": "org/app", "tags": []string{"1.0", "2.0"}})
	})
	st, diags := p3eReadDS(t, NewRegistryImageTagsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{"name": p3eStr(reg.host() + "/org/app")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m p3RegistryTagsModel
	st.Get(ctx, &m)
	if m.TagsJSON.ValueString() != `{"name":"org/app","tags":["1.0","2.0"]}` || m.DistributionAPIVersion.ValueString() != "registry/2.0" {
		t.Errorf("tags_json = %s, api = %s", m.TagsJSON, m.DistributionAPIVersion)
	}
	var tags []string
	st.GetAttribute(ctx, path.Root("tag_list").AtName("tags"), &tags)
	if strings.Join(tags, ",") != "1.0,2.0" {
		t.Errorf("tag_list.tags = %v", tags)
	}
	sum := sha256.Sum256([]byte(m.TagsJSON.ValueString()))
	if m.ContentSHA256.ValueString() != fmt.Sprintf("%x", sum) {
		t.Errorf("content_sha256 = %s", m.ContentSHA256)
	}
}

// ---------------------------------------------------------------------------
// docker_registry_image_manifests

// regFakeStore is an in-memory v2 registry content store behind a
// p3eFakeRegistry: manifests by repo + tag/digest, blobs by repo + digest.
type regFakeStore struct {
	mu        sync.Mutex
	manifests map[string][2]string // repo@ref -> (media type, body)
	blobs     map[string]string    // repo@digest -> body
	log       []string
	noMount   bool
}

func regNewFakeStore(reg *p3eFakeRegistry) *regFakeStore {
	s := &regFakeStore{manifests: map[string][2]string{}, blobs: map[string]string{}}
	reg.handle("/v2/", s.serve)
	return s
}

func regSHA(b string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(b))) }

// putManifest stores body under its digest and, when tag is set, the tag.
func (s *regFakeStore) putManifest(repo, tag, mt, body string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := regSHA(body)
	s.manifests[repo+"@"+d] = [2]string{mt, body}
	if tag != "" {
		s.manifests[repo+"@"+tag] = [2]string{mt, body}
	}
	return d
}

func (s *regFakeStore) putBlob(repo, body string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := regSHA(body)
	s.blobs[repo+"@"+d] = body
	return d
}

// image stores an image manifest with a config blob and one layer.
func (s *regFakeStore) image(repo, tag, mt, os, arch, variant string) string {
	cfg := s.putBlob(repo, fmt.Sprintf(`{"architecture":%q,"os":%q,"variant":%q,"config":{"Env":["A=1"]}}`, arch, os, variant))
	layer := s.putBlob(repo, "layer-"+arch+variant)
	body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":10},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":5}]}`, mt, cfg, layer)
	return s.putManifest(repo, tag, mt, body)
}

func (s *regFakeStore) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	if p == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	switch {
	case strings.Contains(p, "/manifests/"):
		repo, ref, _ := strings.Cut(p, "/manifests/")
		switch r.Method {
		case http.MethodGet:
			m, ok := s.manifests[repo+"@"+ref]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", m[0])
			w.Header().Set("Docker-Content-Digest", regSHA(m[1]))
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			_, _ = io.WriteString(w, m[1])
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			d := regSHA(string(b))
			if strings.HasPrefix(ref, "sha256:") && ref != d {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.manifests[repo+"@"+d] = [2]string{r.Header.Get("Content-Type"), string(b)}
			s.manifests[repo+"@"+ref] = [2]string{r.Header.Get("Content-Type"), string(b)}
			w.Header().Set("Docker-Content-Digest", d)
			w.WriteHeader(http.StatusCreated)
		case http.MethodDelete:
			delete(s.manifests, repo+"@"+ref)
			w.WriteHeader(http.StatusAccepted)
		}
	case strings.Contains(p, "/blobs/uploads/"):
		repo, _, _ := strings.Cut(p, "/blobs/uploads/")
		d, from := r.URL.Query().Get("mount"), r.URL.Query().Get("from")
		b, ok := s.blobs[from+"@"+d]
		if s.noMount || !ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.blobs[repo+"@"+d] = b
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(p, "/blobs/"):
		repo, d, _ := strings.Cut(p, "/blobs/")
		b, ok := s.blobs[repo+"@"+d]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = io.WriteString(w, b)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestRegManifestsAttestations(t *testing.T) {
	ctx := context.Background()
	reg := newP3eFakeRegistry(t, "bearer")
	s := regNewFakeStore(reg)
	amd := s.image("org/app", "", p3MediaOCIManifest, "linux", "amd64", "")
	arm := s.image("org/app", "", p3MediaOCIManifest, "linux", "arm64", "v8")
	prov := s.putBlob("org/app", `{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"https://slsa.dev/provenance/v0.2","predicate":{"builder":{"id":"bk"}}}`)
	sbom := s.putBlob("org/app", `{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"https://spdx.dev/Document","predicate":{"spdxVersion":"SPDX-2.3"}}`)
	attCfg := s.putBlob("org/app", `{}`)
	att := s.putManifest("org/app", "", p3MediaOCIManifest, fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":2},"layers":[
	 {"mediaType":"application/vnd.in-toto+json","digest":%q,"size":1,"annotations":{"in-toto.io/predicate-type":"https://slsa.dev/provenance/v0.2"}},
	 {"mediaType":"application/vnd.in-toto+json","digest":%q,"size":1,"annotations":{"in-toto.io/predicate-type":"https://spdx.dev/Document"}}]}`, p3MediaOCIManifest, attCfg, prov, sbom))
	index := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[
	 {"mediaType":%q,"digest":%q,"size":100,"platform":{"architecture":"amd64","os":"linux"}},
	 {"mediaType":%q,"digest":%q,"size":100,"platform":{"architecture":"arm64","os":"linux","variant":"v8"}},
	 {"mediaType":%q,"digest":%q,"size":100,"platform":{"architecture":"unknown","os":"unknown"},"annotations":{"vnd.docker.reference.type":"attestation-manifest","vnd.docker.reference.digest":%q}}]}`,
		p3MediaOCIIndex, p3MediaOCIManifest, amd, p3MediaOCIManifest, arm, p3MediaOCIManifest, att, amd)
	idx := s.putManifest("org/app", "1.0", p3MediaOCIIndex, index)
	st, diags := p3eReadDS(t, NewRegistryImageManifestsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{"name": p3eStr(reg.host() + "/org/app:1.0")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m p3RegistryManifestsModel
	if d := st.Get(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	var plats []string
	m.Platforms.ElementsAs(ctx, &plats, false)
	if strings.Join(plats, ",") != "linux/amd64,linux/arm64/v8" || len(m.Manifests.Elements()) != 3 {
		t.Errorf("platforms = %v, manifests = %d", plats, len(m.Manifests.Elements()))
	}
	if m.ImageRef.ValueString() != reg.host()+"/org/app:1.0" || m.ImageDigestRef.ValueString() != reg.host()+"/org/app@"+idx || m.Digest.ValueString() != idx {
		t.Errorf("refs: %s %s %s", m.ImageRef, m.ImageDigestRef, m.Digest)
	}
	if m.ManifestJSON.ValueString() != index || m.ManifestJSONSHA256.ValueString() != strings.TrimPrefix(idx, "sha256:") {
		t.Errorf("manifest_json / sha: %s", m.ManifestJSONSHA256)
	}
	var cfgs map[string]map[string]any
	if err := json.Unmarshal([]byte(m.ImageConfigJSON.ValueString()), &cfgs); err != nil || cfgs["linux/arm64/v8"]["variant"] != "v8" || len(cfgs) != 2 {
		t.Errorf("image_config_json = %s (%v)", m.ImageConfigJSON, err)
	}
	if !m.ImageConfig.IsNull() {
		t.Error("image_config must be null for a multi-platform index")
	}
	var provs map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(m.ProvenanceJSON.ValueString()), &provs); err != nil || provs["linux/amd64"]["SLSA"]["builder"] == nil {
		t.Errorf("provenance_json = %s", m.ProvenanceJSON)
	}
	if !strings.Contains(m.SbomJSON.ValueString(), `"SPDX":{"spdxVersion":"SPDX-2.3"}`) || m.SbomJSONSHA256.IsNull() {
		t.Errorf("sbom_json = %s", m.SbomJSON)
	}
	if m.BuildxVersion.ValueString() != contractBuildxVersion {
		t.Errorf("buildx_version = %s", m.BuildxVersion)
	}
	var mt types.String
	st.GetAttribute(ctx, path.Root("manifest").AtName("manifests").AtListIndex(2).AtName("annotations").AtMapKey("vnd.docker.reference.type"), &mt)
	if mt.ValueString() != "attestation-manifest" {
		t.Errorf("manifest.manifests[2].annotations = %s", mt)
	}

	// attestations = false: top-level document only.
	st, diags = p3eReadDS(t, NewRegistryImageManifestsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{
		"name": p3eStr(reg.host() + "/org/app:1.0"), "attestations": tftypes.NewValue(tftypes.Bool, false),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	st.Get(ctx, &m)
	if !m.ProvenanceJSON.IsNull() || !m.ImageConfigJSON.IsNull() || m.ManifestJSON.IsNull() {
		t.Errorf("shallow read: provenance=%s config=%s", m.ProvenanceJSON, m.ImageConfigJSON)
	}
}

func TestRegManifestsSingleImageConfig(t *testing.T) {
	ctx := context.Background()
	reg := newP3eFakeRegistry(t, "basic")
	s := regNewFakeStore(reg)
	d := s.image("library/solo", "latest", p3MediaDockerManif2, "linux", "arm", "v7")
	st, diags := p3eReadDS(t, NewRegistryImageManifestsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{"name": p3eStr(reg.host() + "/library/solo")})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var arch types.String
	st.GetAttribute(ctx, path.Root("image_config").AtName("architecture"), &arch)
	var dg types.String
	st.GetAttribute(ctx, path.Root("digest"), &dg)
	if arch.ValueString() != "arm" || dg.ValueString() != d {
		t.Errorf("image_config.architecture = %s, digest = %s", arch, dg)
	}
	var plats []string
	st.GetAttribute(ctx, path.Root("platforms"), &plats)
	if strings.Join(plats, ",") != "linux/arm/v7" {
		t.Errorf("platforms = %v", plats)
	}
}

func TestRegManifestsNotFound(t *testing.T) {
	reg := newP3eFakeRegistry(t, "none")
	regNewFakeStore(reg)
	_, diags := p3eReadDS(t, NewRegistryImageManifestsDataSource(), p3eRegistryPD(reg.host()), map[string]tftypes.Value{"name": p3eStr(reg.host() + "/nope:1")})
	if !diags.HasError() || !strings.Contains(diags[0].Detail(), "not found") {
		t.Fatalf("diags = %v", diags)
	}
}

// ---------------------------------------------------------------------------
// docker_registry_manifest_list

func regMLEntryValue(t *testing.T, s rschema.Schema, vals map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	lt := s.Type().TerraformType(context.Background()).(tftypes.Object).AttributeTypes["manifests"].(tftypes.List)
	return p3eObject(lt.ElementType, vals)
}

func regMLConfig(t *testing.T, s rschema.Schema, name string, entries []map[string]tftypes.Value, ann map[string]string, keep bool) map[string]tftypes.Value {
	t.Helper()
	lt := s.Type().TerraformType(context.Background()).(tftypes.Object).AttributeTypes["manifests"].(tftypes.List)
	var list []tftypes.Value
	for _, e := range entries {
		list = append(list, regMLEntryValue(t, s, e))
	}
	cfg := map[string]tftypes.Value{
		"name":                 p3eStr(name),
		"manifests":            tftypes.NewValue(lt, list),
		"keep_remotely":        tftypes.NewValue(tftypes.Bool, keep),
		"insecure_skip_verify": tftypes.NewValue(tftypes.Bool, false),
	}
	if ann != nil {
		cfg["annotations"] = p3eStrMap(ann)
	}
	return cfg
}

func TestRegManifestListLifecycle(t *testing.T) {
	ctx := context.Background()
	reg := newP3eFakeRegistry(t, "bearer")
	store := regNewFakeStore(reg)
	host := reg.host()
	amd := store.image("org/app", "amd64", p3MediaDockerManif2, "linux", "amd64", "")
	arm := store.image("org/app", "arm64", p3MediaDockerManif2, "linux", "arm64", "")
	pd := p3eRegistryPD(host)
	r := NewRegistryManifestListResource()
	s := regResSchema(t, r, pd)
	cfg := regMLConfig(t, s, host+"/org/app:1.0", []map[string]tftypes.Value{
		{"image": p3eStr(host + "/org/app:amd64")},
		{"image": p3eStr(host + "/org/app:arm64"), "variant": p3eStr("v8")},
	}, nil, false)
	st, diags := regCreate(t, r, pd, cfg)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m regMLModel
	if d := st.Get(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	pushed := store.manifests["org/app@1.0"]
	if pushed[0] != p3MediaDockerList || m.MediaType.ValueString() != p3MediaDockerList {
		t.Fatalf("pushed media type %q / %s", pushed[0], m.MediaType)
	}
	if m.Digest.ValueString() != regSHA(pushed[1]) || m.ManifestJSON.ValueString() != pushed[1] || m.ImageDigestRef.ValueString() != host+"/org/app@"+regSHA(pushed[1]) {
		t.Errorf("digest %s vs %s", m.Digest, regSHA(pushed[1]))
	}
	var digests map[string]string
	m.ManifestDigests.ElementsAs(ctx, &digests, false)
	if digests["linux/amd64"] != amd || digests["linux/arm64/v8"] != arm || m.ImageRef.ValueString() != host+"/org/app:1.0" {
		t.Errorf("manifest_digests = %v", digests)
	}
	if m.BuildxVersion.ValueString() != contractBuildxVersion || m.ContentSHA256.ValueString() != strings.TrimPrefix(m.Digest.ValueString(), "sha256:") {
		t.Errorf("contract: %s %s", m.BuildxVersion, m.ContentSHA256)
	}
	var variant types.String
	st.GetAttribute(ctx, path.Root("manifest").AtName("manifests").AtListIndex(1).AtName("platform").AtName("variant"), &variant)
	if variant.ValueString() != "v8" {
		t.Errorf("manifest.manifests[1].platform.variant = %s", variant)
	}

	// No change: plan keeps outputs. Tag moved outside Terraform: Read
	// picks up the new digest and the plan pushes again.
	plan, diags := regModifyPlan(t, r, st, cfg)
	if diags.HasError() || regStateString(t, plan.GetAttribute, "digest") != m.Digest.ValueString() {
		t.Fatalf("no-change plan: %v %s", diags, regStateString(t, plan.GetAttribute, "digest"))
	}
	pushedDigest := m.Digest.ValueString()
	store.putManifest("org/app", "1.0", p3MediaDockerList, `{"schemaVersion":2,"mediaType":"`+p3MediaDockerList+`","manifests":[]}`)
	st2, removed, diags := regRead(t, r, st)
	if diags.HasError() || removed {
		t.Fatal(diags)
	}
	var drifted, planned regMLModel
	st2.Get(ctx, &drifted)
	planned = drifted
	if d := regMLPlanOutputs(&planned, &drifted, pushedDigest); d.WarningsCount() != 1 || !planned.Digest.IsUnknown() || !planned.ManifestJSON.IsUnknown() {
		t.Errorf("drift plan: %v %s", d, planned.Digest)
	}
	planned = drifted
	if d := regMLPlanOutputs(&planned, &drifted, drifted.Digest.ValueString()); d.WarningsCount() != 0 || planned.Digest.IsUnknown() {
		t.Errorf("no-drift plan: %v", d)
	}

	// Update with annotations: an OCI index is pushed under the same tag.
	cfg2 := regMLConfig(t, s, host+"/org/app:1.0", []map[string]tftypes.Value{
		{"image": p3eStr(host + "/org/app:amd64"), "annotations": p3eStrMap(map[string]string{"a": "b"})},
	}, map[string]string{"org.opencontainers.image.source": "https://example.com"}, false)
	plan, _ = regModifyPlan(t, r, st2, cfg2)
	ur := fwresource.UpdateResponse{State: st2}
	r.Update(ctx, fwresource.UpdateRequest{Plan: plan, State: st2, Config: tfsdk.Config{Schema: s, Raw: p3eObject(s.Type().TerraformType(ctx), cfg2)}}, &ur)
	if ur.Diagnostics.HasError() {
		t.Fatal(ur.Diagnostics)
	}
	if got := store.manifests["org/app@1.0"]; got[0] != p3MediaOCIIndex || !strings.Contains(got[1], `"annotations":{"org.opencontainers.image.source"`) {
		t.Errorf("updated index = %v", got)
	}

	// Import rebuilds the entries from the index.
	imp := tfsdk.State{Schema: s, Raw: p3eObject(s.Type().TerraformType(ctx), map[string]tftypes.Value{"id": p3eStr(host + "/org/app:1.0")})}
	st3, _, diags := regRead(t, r, imp)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var img types.String
	st3.GetAttribute(ctx, path.Root("manifests").AtListIndex(0).AtName("image"), &img)
	if img.ValueString() != host+"/org/app@"+amd {
		t.Errorf("imported image = %s", img)
	}

	// Delete removes the index; a later Read drops it from state.
	var dr fwresource.DeleteResponse
	r.Delete(ctx, fwresource.DeleteRequest{State: ur.State}, &dr)
	if dr.Diagnostics.HasError() || dr.Diagnostics.WarningsCount() > 0 {
		t.Fatal(dr.Diagnostics)
	}
	store.mu.Lock()
	delete(store.manifests, "org/app@1.0")
	store.mu.Unlock()
	if _, removed, _ := regRead(t, r, ur.State); !removed {
		t.Error("read after delete should remove the resource")
	}
}

func TestRegManifestListCrossRepository(t *testing.T) {
	ctx := context.Background()
	reg := newP3eFakeRegistry(t, "bearer")
	store := regNewFakeStore(reg)
	host := reg.host()
	other := store.image("org/other", "x", p3MediaOCIManifest, "linux", "s390x", "")
	pd := p3eRegistryPD(host)
	r := NewRegistryManifestListResource()
	s := regResSchema(t, r, pd)
	cfg := regMLConfig(t, s, host+"/org/app:multi", []map[string]tftypes.Value{{"image": p3eStr(host + "/org/other:x")}}, nil, true)
	st, diags := regCreate(t, r, pd, cfg)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if _, ok := store.manifests["org/app@"+other]; !ok {
		t.Error("child manifest was not copied into org/app")
	}
	if regStateString(t, st.GetAttribute, "media_type") != p3MediaOCIIndex {
		t.Error("OCI child must give an OCI index")
	}
	mounts := 0
	for _, l := range store.log {
		if strings.Contains(l, "mount=") {
			mounts++
		}
	}
	if mounts != 2 {
		t.Errorf("mounts = %d (%v)", mounts, store.log)
	}
	_ = ctx

	// A registry that cannot mount is an error.
	store.noMount = true
	cfg = regMLConfig(t, s, host+"/org/third:1", []map[string]tftypes.Value{{"image": p3eStr(host + "/org/other:x")}}, nil, true)
	if _, diags := regCreate(t, r, pd, cfg); !diags.HasError() || !strings.Contains(diags[0].Detail(), "did not mount") {
		t.Errorf("no-mount diags = %v", diags)
	}
}

func TestRegManifestListErrors(t *testing.T) {
	reg := newP3eFakeRegistry(t, "none")
	store := regNewFakeStore(reg)
	host := reg.host()
	store.image("org/app", "a", p3MediaOCIManifest, "linux", "amd64", "")
	store.image("org/app", "b", p3MediaOCIManifest, "linux", "amd64", "")
	idx := store.putManifest("org/app", "idx", p3MediaOCIIndex, `{"schemaVersion":2,"mediaType":"`+p3MediaOCIIndex+`","manifests":[]}`)
	_ = idx
	pd := p3eRegistryPD(host)
	r := NewRegistryManifestListResource()
	s := regResSchema(t, r, pd)
	for name, tc := range map[string]struct {
		target  string
		entries []map[string]tftypes.Value
		want    string
	}{
		"other registry": {host + "/org/app:1", []map[string]tftypes.Value{{"image": p3eStr("ghcr.io/org/app:a")}}, "not " + host},
		"duplicate":      {host + "/org/app:1", []map[string]tftypes.Value{{"image": p3eStr(host + "/org/app:a")}, {"image": p3eStr(host + "/org/app:b")}}, "appears twice"},
		"index override": {host + "/org/app:1", []map[string]tftypes.Value{{"image": p3eStr(host + "/org/app:idx"), "os": p3eStr("linux")}}, "platform overrides"},
		"missing":        {host + "/org/app:1", []map[string]tftypes.Value{{"image": p3eStr(host + "/org/app:nope")}}, "not found"},
		"digest target":  {host + "/org/app@" + regTestDigest, []map[string]tftypes.Value{{"image": p3eStr(host + "/org/app:a")}}, "tag reference"},
	} {
		_, diags := regCreate(t, r, pd, regMLConfig(t, s, tc.target, tc.entries, nil, true))
		if !diags.HasError() || !strings.Contains(diags[0].Detail(), tc.want) {
			t.Errorf("%s: diags = %v", name, diags)
		}
	}
}

func TestRegMLDocMediaType(t *testing.T) {
	d2 := regDescriptor{MediaType: p3MediaDockerManif2, Digest: "sha256:a"}
	oci := regDescriptor{MediaType: p3MediaOCIManifest, Digest: "sha256:b"}
	for _, tc := range []struct {
		descs []regDescriptor
		ann   map[string]string
		want  string
	}{
		{[]regDescriptor{d2}, nil, p3MediaDockerList},
		{[]regDescriptor{d2, oci}, nil, p3MediaOCIIndex},
		{[]regDescriptor{d2}, map[string]string{"a": "b"}, p3MediaOCIIndex},
	} {
		doc, _ := regMLDoc(tc.descs, tc.ann)
		if doc.MediaType != tc.want {
			t.Errorf("%v %v: %s", tc.descs, tc.ann, doc.MediaType)
		}
	}
	var _ attr.Value = types.StringNull()
}
