package provider

// Pass 5 Lane A (WP img) unit tests: docker_image, data docker_image,
// docker_tag contract outputs, the image data sources and actions. No daemon:
// everything runs against newP3eFake.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// imgTestInspectBody is an inspect response with a containerd descriptor
// and two platform manifests.
func imgTestInspectBody(id string) map[string]any {
	return map[string]any{
		"Id":           id,
		"RepoTags":     []string{"nginx:1.27"},
		"RepoDigests":  []string{"nginx@sha256:feed"},
		"Created":      "2026-01-02T03:04:05Z",
		"Architecture": "amd64",
		"Os":           "linux",
		"Size":         1234,
		"Config":       map[string]any{"Env": []string{"SECRET=x"}, "Labels": map[string]string{"maintainer": "me"}, "Cmd": []string{"nginx"}},
		"Descriptor":   map[string]any{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": "sha256:index", "size": 100},
		"Manifests": []map[string]any{
			{"ID": "sha256:m1", "Descriptor": map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:m1", "size": 10},
				"Available": true, "Kind": "image", "ImageData": map[string]any{"Platform": map[string]any{"os": "linux", "architecture": "arm64", "variant": "v8"}}},
			{"ID": "sha256:m2", "Descriptor": map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:m2", "size": 11},
				"Available": false, "Kind": "image", "ImageData": map[string]any{"Platform": map[string]any{"os": "linux", "architecture": "amd64"}}},
			{"ID": "sha256:att", "Descriptor": map[string]any{"digest": "sha256:att"}, "Kind": "attestation"},
		},
	}
}

// imgTestEngine wires the routes docker_image needs: pull, inspect (by tag
// and by ID), version and remove.
func imgTestEngine(t *testing.T) (*p3eFake, *providerData, *[]string) {
	t.Helper()
	f, pd := newP3eFake(t)
	var removed []string
	f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.on("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"Pulling from library/nginx"}` + "\n" + `{"status":"Status: Downloaded newer image for nginx:1.27"}` + "\n"))
	})
	f.on("GET /images/*", func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimSuffix(strings.TrimPrefix(p3eVersionRe.ReplaceAllString(r.URL.Path, ""), "/images/"), "/json")
		switch ref {
		case "nginx:1.27", "sha256:abc", "app:1", "sha256:built":
			id := "sha256:abc"
			if ref == "app:1" || ref == "sha256:built" {
				id = "sha256:built"
			}
			p3eWriteJSON(w, http.StatusOK, imgTestInspectBody(id))
		default:
			p3eWriteJSON(w, http.StatusNotFound, map[string]string{"message": "No such image: " + ref})
		}
	})
	f.on("DELETE /images/*", func(w http.ResponseWriter, r *http.Request) {
		removed = append(removed, strings.TrimPrefix(p3eVersionRe.ReplaceAllString(r.URL.Path, ""), "/images/"))
		p3eWriteJSON(w, http.StatusOK, []map[string]string{{"Untagged": "x"}})
	})
	return f, pd, &removed
}

func imgTestSchema(t *testing.T) fwresource.SchemaResponse {
	t.Helper()
	var sr fwresource.SchemaResponse
	NewImageResource().Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatal(sr.Diagnostics)
	}
	return sr
}

// imgTestPlanValue builds a raw plan/state value from attribute values.
func imgTestPlanValue(sr fwresource.SchemaResponse, vals map[string]tftypes.Value) tftypes.Value {
	return p3eObject(sr.Schema.Type().TerraformType(context.Background()), vals)
}

func TestImgResourceSchemaContract(t *testing.T) {
	ctx := context.Background()
	sr := imgTestSchema(t)
	if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatal(d)
	}
	for _, a := range append(contractIdentityOutputNames(imgItemImage), "inspect_json", "inspect", "stages", "dockerfile", "dockerfile_sha256", "inspect_json_sha256", "dockerfile_syntax", "engine_api_version", "docker_server_version") {
		if _, ok := sr.Schema.Attributes[a]; !ok {
			t.Errorf("missing %s", a)
		}
	}
	for _, gone := range []string{"image_id", "repo_digest"} {
		if _, ok := sr.Schema.Attributes[gone]; ok {
			t.Errorf("%s should be gone (Q44/Q46)", gone)
		}
	}
	b := sr.Schema.Blocks["build"]
	for _, a := range []string{"additional_contexts", "cache_to", "cgroup_parent", "cpu_period", "cpu_quota", "cpuset_cpus", "cpuset_mems", "cpu_shares",
		"extra_hosts", "force_rm", "isolation", "memory", "memswap", "network", "provenance", "remote", "sbom", "shm_size", "squash", "tags", "ulimits"} {
		if _, ok := b.GetNestedObject().GetAttributes()[a]; !ok {
			t.Errorf("build.%s missing", a)
		}
	}
}

func TestImgResourcePullLifecycle(t *testing.T) {
	ctx := context.Background()
	f, pd, removed := imgTestEngine(t)
	r := &imageResource{pd: pd}
	sr := imgTestSchema(t)
	plan := tfsdk.Plan{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{
		"name": p3eStr("nginx:1.27"), "keep_locally": tftypes.NewValue(tftypes.Bool, false), "force_remove": tftypes.NewValue(tftypes.Bool, false),
	})}
	cresp := fwresource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, fwresource.CreateRequest{Plan: plan}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	var st imageResourceModel
	cresp.State.Get(ctx, &st)
	if st.ID.ValueString() != "sha256:abc" || st.ImageRef.ValueString() != "docker.io/library/nginx:1.27" ||
		st.ImageDigestRef.ValueString() != "nginx@sha256:feed" || st.Digest.ValueString() != "sha256:index" {
		t.Errorf("identity: %+v", st)
	}
	var plats []string
	st.Platforms.ElementsAs(ctx, &plats, false)
	if strings.Join(plats, ",") != "linux/amd64,linux/arm64/v8" {
		t.Errorf("platforms %v", plats)
	}
	if st.InspectJSON.IsNull() || st.InspectJSONSHA256.IsNull() || st.Inspect.IsNull() {
		t.Errorf("inspect outputs: %+v", st.contractEngineImage)
	}
	if !st.Dockerfile.IsNull() || !st.DockerfileSyntax.IsNull() || !st.Stages.IsNull() {
		t.Errorf("pulled image has no dockerfile outputs: %+v", st.contractEngineImage)
	}
	if st.DockerServerVersion.ValueString() != "29.0.0" {
		t.Errorf("server version %v", st.DockerServerVersion)
	}
	if !strings.Contains(strings.Join(f.requests(), " "), "manifests=1") {
		// The fake reports API 1.47, below 1.48: manifests must not be asked for.
	} else {
		t.Errorf("manifests asked below API 1.48: %v", f.requests())
	}

	// Read refreshes; a vanished image leaves state.
	rresp := fwresource.ReadResponse{State: cresp.State}
	r.Read(ctx, fwresource.ReadRequest{State: cresp.State}, &rresp)
	if rresp.Diagnostics.HasError() || rresp.State.Raw.IsNull() {
		t.Fatalf("read: %v", rresp.Diagnostics)
	}

	// ModifyPlan of an in-place change keeps every computed value.
	var mresp fwresource.ModifyPlanResponse
	newPlan := st
	newPlan.KeepLocally = types.BoolValue(true)
	newPlan.ID = types.StringUnknown()
	newPlan.contractSetUnknown()
	pl := tfsdk.Plan{Schema: sr.Schema}
	pl.Raw = tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)
	if d := pl.Set(ctx, &newPlan); d.HasError() {
		t.Fatal(d)
	}
	mresp.Plan = pl
	r.ModifyPlan(ctx, fwresource.ModifyPlanRequest{Plan: pl, State: cresp.State}, &mresp)
	var mp imageResourceModel
	mresp.Plan.Get(ctx, &mp)
	if mresp.Diagnostics.HasError() || mp.ID.ValueString() != "sha256:abc" || mp.InspectJSON.IsUnknown() || len(mresp.RequiresReplace) != 0 {
		t.Errorf("modify plan: %v %+v", mresp.Diagnostics, mp.ID)
	}

	// Update copies state.
	uresp := fwresource.UpdateResponse{State: cresp.State}
	r.Update(ctx, fwresource.UpdateRequest{Plan: mresp.Plan, State: cresp.State}, &uresp)
	var ust imageResourceModel
	uresp.State.Get(ctx, &ust)
	if uresp.Diagnostics.HasError() || !ust.KeepLocally.ValueBool() || ust.ID.ValueString() != "sha256:abc" {
		t.Errorf("update: %v %+v", uresp.Diagnostics, ust)
	}

	// Delete untags the name.
	dresp := fwresource.DeleteResponse{State: cresp.State}
	r.Delete(ctx, fwresource.DeleteRequest{State: cresp.State}, &dresp)
	if dresp.Diagnostics.HasError() || strings.Join(*removed, ",") != "nginx:1.27" {
		t.Errorf("delete: %v %v", dresp.Diagnostics, *removed)
	}
}

func TestImgResourceCreatePlanUnknown(t *testing.T) {
	ctx := context.Background()
	sr := imgTestSchema(t)
	r := &imageResource{}
	raw := imgTestPlanValue(sr, map[string]tftypes.Value{"name": p3eStr("nginx:1.27")})
	pl := tfsdk.Plan{Schema: sr.Schema, Raw: raw}
	resp := fwresource.ModifyPlanResponse{Plan: pl}
	r.ModifyPlan(ctx, fwresource.ModifyPlanRequest{Plan: pl, State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}, &resp)
	var m imageResourceModel
	resp.Plan.Get(ctx, &m)
	if resp.Diagnostics.HasError() || !m.InspectJSON.IsUnknown() || !m.ContextDigest.IsNull() {
		t.Errorf("create plan: %v %+v", resp.Diagnostics, m.contractEngineImage)
	}
}

func TestImgResourceReadGone(t *testing.T) {
	ctx := context.Background()
	_, pd, _ := imgTestEngine(t)
	sr := imgTestSchema(t)
	st := tfsdk.State{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{"id": p3eStr("sha256:gone"), "name": p3eStr("gone:1")})}
	resp := fwresource.ReadResponse{State: st}
	(&imageResource{pd: pd}).Read(ctx, fwresource.ReadRequest{State: st}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Errorf("gone image should leave state: %v", resp.Diagnostics)
	}
}

func TestImgResourceImportByName(t *testing.T) {
	ctx := context.Background()
	_, pd, _ := imgTestEngine(t)
	sr := imgTestSchema(t)
	st := tfsdk.State{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{"id": p3eStr("nginx:1.27")})}
	resp := fwresource.ReadResponse{State: st}
	(&imageResource{pd: pd}).Read(ctx, fwresource.ReadRequest{State: st}, &resp)
	var m imageResourceModel
	resp.State.Get(ctx, &m)
	if resp.Diagnostics.HasError() || m.ID.ValueString() != "sha256:abc" || m.Name.ValueString() != "nginx:1.27" || m.InspectJSON.IsNull() {
		t.Errorf("import: %v %+v", resp.Diagnostics, m)
	}
}

func TestImgResourcePullError(t *testing.T) {
	ctx := context.Background()
	f, pd, _ := imgTestEngine(t)
	f.on("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}` + "\n"))
	})
	sr := imgTestSchema(t)
	plan := tfsdk.Plan{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{"name": p3eStr("nginx:1.27")})}
	cresp := fwresource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&imageResource{pd: pd}).Create(ctx, fwresource.CreateRequest{Plan: plan}, &cresp)
	if !cresp.Diagnostics.HasError() || !strings.Contains(cresp.Diagnostics.Errors()[0].Detail(), "manifest unknown") {
		t.Errorf("want pull error, got %v", cresp.Diagnostics)
	}
}

// imgTestSeam records the BuildKit seam request.
type imgTestSeam struct {
	got seamBuildRequest
	err error
}

func (s *imgTestSeam) Build(_ context.Context, _ *providerData, req seamBuildRequest) (*seamBuildResult, error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return &seamBuildResult{ImageID: "sha256:built", Digest: "sha256:pushed", Platforms: []string{"linux/amd64"},
		Metadata: map[string]any{"buildx.build.ref": "b/n/ref1", "containerimage.digest": "sha256:pushed"}}, nil
}

func imgTestWithSeam(t *testing.T, s seamBuildKit) {
	t.Helper()
	old := seamBuildKitImpl
	seamBuildKitImpl = s
	t.Cleanup(func() { seamBuildKitImpl = old })
}

func TestImgResourceBuildKitSeam(t *testing.T) {
	ctx := context.Background()
	_, pd, _ := imgTestEngine(t)
	seam := &imgTestSeam{}
	imgTestWithSeam(t, seam)
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": "# syntax=docker/dockerfile:1.7\nARG BASE=alpine:3.20\nFROM ${BASE} AS base\nRUN echo hi\nFROM base\nCOPY . /app\n"})
	sr := imgTestSchema(t)
	buildType := sr.Schema.Blocks["build"].Type().TerraformType(ctx)
	build := p3eObject(buildType, map[string]tftypes.Value{
		"context":    p3eStr(dir),
		"builder":    p3eStr("buildkit"),
		"cache_to":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("type=inline")}),
		"provenance": p3eStr("min"),
		"sbom":       tftypes.NewValue(tftypes.Bool, true),
		"tags":       tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("app:latest")}),
		"secrets":    tftypes.NewValue(buildType.(tftypes.Object).AttributeTypes["secrets"], []tftypes.Value{}),
	})
	plan := tfsdk.Plan{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{"name": p3eStr("app:1"), "build": build})}
	cresp := fwresource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&imageResource{pd: pd}).Create(ctx, fwresource.CreateRequest{Plan: plan}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	g := seam.got
	if g.ContextDir == "" || strings.Join(g.Outputs.Names, ",") != "app:1,app:latest" || !g.Outputs.Load ||
		strings.Join(g.CacheTo, ",") != "type=inline" || g.Attestations.Provenance != "min" || !g.Attestations.SBOM || g.Labels[labelManagedBy] == "" {
		t.Errorf("seam request: %+v", g)
	}
	var st imageResourceModel
	cresp.State.Get(ctx, &st)
	if st.ID.ValueString() != "sha256:built" || st.DockerfileSyntax.ValueString() != "docker/dockerfile:1.7" || st.Dockerfile.IsNull() ||
		st.DockerfileSHA256.IsNull() || st.ContextDigest.IsNull() {
		t.Errorf("state: %+v", st)
	}
	if st.Digest.ValueString() != "sha256:pushed" || st.BuildRef.ValueString() != "b/n/ref1" || st.MetadataJSONSHA256.IsNull() ||
		!strings.Contains(st.MetadataJSON.ValueString(), "b/n/ref1") || st.BuildMetadata.IsNull() || st.BuildxVersion.IsNull() ||
		len(st.Platforms.Elements()) != 1 {
		t.Errorf("buildx outputs: %+v", st.imgBuildxOutputs)
	}
	if g.Builder != (seamBuilderRef{}) {
		t.Errorf("builder = buildkit must use the embedded BuildKit: %+v", g.Builder)
	}
	if n := len(st.Stages.Elements()); n != 2 {
		t.Fatalf("stages: %v", st.Stages)
	}

	// A failing seam is a build error.
	seam.err = errors.New("boom")
	cresp = fwresource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&imageResource{pd: pd}).Create(ctx, fwresource.CreateRequest{Plan: plan}, &cresp)
	if !cresp.Diagnostics.HasError() || !strings.Contains(cresp.Diagnostics.Errors()[0].Detail(), "boom") {
		t.Errorf("seam error: %v", cresp.Diagnostics)
	}

	// A named buildx builder goes to the seam as Builder.Name.
	seam.err = nil
	named := p3eObject(buildType, map[string]tftypes.Value{
		"context": p3eStr(dir),
		"builder": p3eStr("remote-bk"),
		"ssh": tftypes.NewValue(buildType.(tftypes.Object).AttributeTypes["ssh"], []tftypes.Value{
			p3eObject(buildType.(tftypes.Object).AttributeTypes["ssh"].(tftypes.List).ElementType, map[string]tftypes.Value{"id": p3eStr("default")})}),
		"no_cache_filter": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("base")}),
		"outputs":         tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("type=local,dest=out")}),
		"attestations":    tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("type=sbom")}),
		"secrets":         tftypes.NewValue(buildType.(tftypes.Object).AttributeTypes["secrets"], []tftypes.Value{}),
	})
	plan = tfsdk.Plan{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{"name": p3eStr("app:1"), "build": named})}
	cresp = fwresource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&imageResource{pd: pd}).Create(ctx, fwresource.CreateRequest{Plan: plan}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	g = seam.got
	if g.Builder.Name != "remote-bk" || len(g.SSH) != 1 || g.SSH[0].ID != "default" || strings.Join(g.NoCacheFilter, ",") != "base" ||
		strings.Join(g.Outputs.Raw, ",") != "type=local,dest=out" || strings.Join(g.Attestations.Extra, ",") != "type=sbom" {
		t.Errorf("named builder request: %+v", g)
	}

	// The default stub: the build fails with the seam's error (no fallback).
	imgTestWithSeam(t, seamBuildKitStub{})
	cresp = fwresource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&imageResource{pd: pd}).Create(ctx, fwresource.CreateRequest{Plan: plan}, &cresp)
	if !cresp.Diagnostics.HasError() || !strings.Contains(cresp.Diagnostics.Errors()[0].Detail(), "not implemented") {
		t.Errorf("stub seam: %v", cresp.Diagnostics)
	}
}

func TestImgResourceClassicBuildOptions(t *testing.T) {
	ctx := context.Background()
	f, pd, _ := imgTestEngine(t)
	var q map[string][]string
	f.on("POST /build", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"stream":"Step 1/1 : FROM scratch\n"}` + "\n" + `{"aux":{"ID":"sha256:built"}}` + "\n"))
	})
	dir := t.TempDir()
	imgTestWrite(t, dir, map[string]string{"Dockerfile": "FROM scratch\n"})
	sr := imgTestSchema(t)
	buildType := sr.Schema.Blocks["build"].Type().TerraformType(ctx).(tftypes.Object)
	ul := buildType.AttributeTypes["ulimits"]
	build := p3eObject(buildType, map[string]tftypes.Value{
		"context":   p3eStr(dir),
		"memory":    tftypes.NewValue(tftypes.Number, 1024),
		"cpu_quota": tftypes.NewValue(tftypes.Number, 5000),
		"network":   p3eStr("host"),
		"force_rm":  tftypes.NewValue(tftypes.Bool, false),
		"squash":    tftypes.NewValue(tftypes.Bool, true),
		"ulimits": tftypes.NewValue(ul, map[string]tftypes.Value{"nofile": tftypes.NewValue(ul.(tftypes.Map).ElementType, map[string]tftypes.Value{
			"soft": tftypes.NewValue(tftypes.Number, 10), "hard": tftypes.NewValue(tftypes.Number, 20)})}),
		"secrets": tftypes.NewValue(buildType.AttributeTypes["secrets"], []tftypes.Value{}),
	})
	plan := tfsdk.Plan{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{"name": p3eStr("app:1"), "build": build})}
	cresp := fwresource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	(&imageResource{pd: pd}).Create(ctx, fwresource.CreateRequest{Plan: plan}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if get("memory") != "1024" || get("cpuquota") != "5000" || get("networkmode") != "host" || get("forcerm") == "1" ||
		get("squash") != "1" || !strings.Contains(get("ulimits"), `"Name":"nofile"`) {
		t.Errorf("build query: %v", q)
	}
	var st imageResourceModel
	cresp.State.Get(ctx, &st)
	if st.ID.ValueString() != "sha256:built" || st.DockerfileSyntax.ValueString() != contractDockerfileDefaultSyntax {
		t.Errorf("state: %+v", st)
	}
}

func TestImgValidateBuild(t *testing.T) {
	ctx := context.Background()
	classic := &imageBuildModel{Builder: types.StringNull(), CacheTo: imgTestList("type=inline"), Provenance: types.StringValue("min")}
	if d := imgValidateBuildGaps(ctx, classic); d.ErrorsCount() != 2 {
		t.Errorf("BuildKit-only on classic: %v", d)
	}
	bk := &imageBuildModel{Builder: types.StringValue("buildkit"), Memory: types.Int64Value(1), Remote: types.StringValue("https://x/y.git"), Squash: types.BoolValue(true)}
	if d := imgValidateBuildGaps(ctx, bk); d.ErrorsCount() != 3 {
		t.Errorf("classic-only on BuildKit: %v", d)
	}
	if d := imgValidateBuildGaps(ctx, &imageBuildModel{Builder: types.StringUnknown(), Memory: types.Int64Value(1)}); d.HasError() {
		t.Errorf("unknown builder: %v", d)
	}
	// Remote context: no digest, no local context needed.
	if d := imgPlanContextDigest(&imageBuildModel{Context: types.StringNull(), Remote: types.StringValue("https://x/y.git")}); !d.IsNull() {
		t.Errorf("remote digest: %v", d)
	}
}

func TestImgValidateConfigContext(t *testing.T) {
	ctx := context.Background()
	sr := imgTestSchema(t)
	buildType := sr.Schema.Blocks["build"].Type().TerraformType(ctx).(tftypes.Object)
	check := func(vals map[string]tftypes.Value) diag.Diagnostics {
		vals["secrets"] = tftypes.NewValue(buildType.AttributeTypes["secrets"], []tftypes.Value{})
		cfg := tfsdk.Config{Schema: sr.Schema, Raw: imgTestPlanValue(sr, map[string]tftypes.Value{"name": p3eStr("a:1"), "build": p3eObject(buildType, vals)})}
		var resp fwresource.ValidateConfigResponse
		(&imageResource{}).ValidateConfig(ctx, fwresource.ValidateConfigRequest{Config: cfg}, &resp)
		return resp.Diagnostics
	}
	if d := check(map[string]tftypes.Value{}); !d.HasError() {
		t.Error("no context and no remote must fail")
	}
	if d := check(map[string]tftypes.Value{"context": p3eStr("."), "remote": p3eStr("https://x")}); !d.HasError() {
		t.Error("context and remote must fail")
	}
	if d := check(map[string]tftypes.Value{"remote": p3eStr("https://x")}); d.HasError() {
		t.Errorf("remote alone: %v", d)
	}
}

func imgTestList(s ...string) types.List { return stringListValue(s, false) }

func TestImgParseDockerfileLines(t *testing.T) {
	info := imgParseDockerfileLines("# syntax=docker/dockerfile:1\n# escape=\\\nARG GO=1.27\n" +
		"FROM --platform=$BUILDPLATFORM golang:${GO} AS build\n# comment\nRUN go build \\\n  -o /out ./...\n" +
		"RUN <<EOF\necho FROM nothing\nEOF\nFROM scratch\nCOPY --from=build /out /\nFROM build AS test\n")
	if info.Syntax != "docker/dockerfile:1" || len(info.Stages) != 3 {
		t.Fatalf("info: %+v", info)
	}
	s0 := info.Stages[0]
	if s0["name"] != "build" || s0["base_image"] != "golang:1.27" || s0["platform"] != "$BUILDPLATFORM" || s0["base_image_ref"] != "docker.io/library/golang:1.27" {
		t.Errorf("stage 0: %v", s0)
	}
	ins := s0["instructions"].([]map[string]any)
	if len(ins) != 2 || ins[0]["dockerfile"] != "RUN go build  -o /out ./..." || ins[1]["keyword"] != "RUN" {
		t.Errorf("instructions: %v", ins)
	}
	if _, ok := info.Stages[1]["base_image_ref"]; ok {
		t.Errorf("scratch has no ref: %v", info.Stages[1])
	}
	if info.Stages[2]["base_stage"] != "build" {
		t.Errorf("stage base: %v", info.Stages[2])
	}
}

func TestImgNormalizedRef(t *testing.T) {
	for in, want := range map[string]string{
		"nginx":                      "docker.io/library/nginx:latest",
		"ghcr.io/o/a:1":              "ghcr.io/o/a:1",
		"nginx@sha256:" + imgHex64:   "",
		"nginx:1@sha256:" + imgHex64: "docker.io/library/nginx:1",
		"sha256:abc":                 "",
		"NOT VALID":                  "",
	} {
		if got := imgNormalizedRef(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

const imgHex64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// ---------------------------------------------------------------------------
// data sources

// imgTestReadDS runs a data source Read with config values.
func imgTestReadDS(t *testing.T, pd *providerData, d datasource.DataSource, vals map[string]tftypes.Value) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	if diags := sr.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatal(diags)
	}
	if c, ok := d.(datasource.DataSourceWithConfigure); ok {
		var cr datasource.ConfigureResponse
		c.Configure(ctx, datasource.ConfigureRequest{ProviderData: pd}, &cr)
	}
	typ := sr.Schema.Type().TerraformType(ctx)
	cfg := tfsdk.Config{Schema: sr.Schema, Raw: p3eObject(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	d.Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	return resp.State, resp.Diagnostics
}

func TestImgDataSourceImage(t *testing.T) {
	ctx := context.Background()
	_, pd, _ := imgTestEngine(t)
	st, d := imgTestReadDS(t, pd, NewImageDataSource(), map[string]tftypes.Value{"name": p3eStr("nginx:1.27")})
	if d.HasError() {
		t.Fatal(d)
	}
	var m imageDataSourceModel
	st.Get(ctx, &m)
	if m.ID.ValueString() != "sha256:abc" || m.Created.ValueString() != "2026-01-02T03:04:05Z" || m.Config.IsNull() || m.InspectJSON.IsNull() ||
		m.ContentSHA256.IsNull() || len(m.Manifests.Elements()) != 2 || m.ImageDigestRef.ValueString() != "nginx@sha256:feed" {
		t.Errorf("model: %+v", m)
	}
	if _, d := imgTestReadDS(t, pd, NewImageDataSource(), map[string]tftypes.Value{"name": p3eStr("missing:1")}); !d.HasError() {
		t.Error("missing image must fail")
	}
}

func TestImgDataSourceHistory(t *testing.T) {
	ctx := context.Background()
	f, pd := newP3eFake(t)
	f.json("GET /images/nginx:1.27/history", []image.HistoryResponseItem{
		{ID: "sha256:top", Created: 2, CreatedBy: "CMD [\"nginx\"]", Tags: []string{"nginx:1.27"}, Size: 0},
		{ID: "<missing>", Created: 1, CreatedBy: "ADD rootfs /", Size: 100},
	})
	st, d := imgTestReadDS(t, pd, NewImageHistoryDataSource(), map[string]tftypes.Value{"name": p3eStr("nginx:1.27")})
	if d.HasError() {
		t.Fatal(d)
	}
	var m imgHistoryModel
	st.Get(ctx, &m)
	if m.ID.ValueString() != "sha256:top" || len(m.History.Elements()) != 2 || m.HistoryJSON.IsNull() || m.ContentSHA256.IsNull() {
		t.Errorf("model: %+v", m)
	}
	if _, d := imgTestReadDS(t, pd, NewImageHistoryDataSource(), map[string]tftypes.Value{"name": p3eStr("nope")}); !d.HasError() {
		t.Error("error expected")
	}
}

func TestImgDataSourceSearch(t *testing.T) {
	ctx := context.Background()
	f, pd := newP3eFake(t)
	var q string
	f.on("GET /images/search", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		p3eWriteJSON(w, http.StatusOK, []registry.SearchResult{{Name: "nginx", IsOfficial: true, StarCount: 100}, {Name: "bitnami/nginx"}})
	})
	st, d := imgTestReadDS(t, pd, NewImageSearchDataSource(), map[string]tftypes.Value{
		"term": p3eStr("nginx"), "limit": tftypes.NewValue(tftypes.Number, 5), "is_official": tftypes.NewValue(tftypes.Bool, true),
	})
	if d.HasError() {
		t.Fatal(d)
	}
	var m imgSearchModel
	st.Get(ctx, &m)
	var names []string
	m.Names.ElementsAs(ctx, &names, false)
	if strings.Join(names, ",") != "nginx,bitnami/nginx" || len(m.Results.Elements()) != 2 || !strings.Contains(q, "limit=5") || !strings.Contains(q, "is-official") {
		t.Errorf("model %+v query %s", m, q)
	}
}

func TestImgDataSourceAttestations(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]any{"Version": "29.0.0", "ApiVersion": "1.47"})
	// The fake daemon negotiates 1.47: the 1.55 gate fails before any call.
	_, d := imgTestReadDS(t, pd, NewImageAttestationsDataSource(), map[string]tftypes.Value{"name": p3eStr("app:1")})
	if !d.HasError() || !strings.Contains(d.Errors()[0].Detail()+d.Errors()[0].Summary(), "1.55") {
		t.Errorf("gate: %v", d)
	}
	for _, r := range f.requests() {
		if strings.Contains(r, "attestations") {
			t.Errorf("attestations called below the gate: %s", r)
		}
	}
}

func TestImgDataSourceAttestationsFill(t *testing.T) {
	// Fill path without the gate: the contract struct accepts the response.
	ctx := context.Background()
	items := []image.AttestationStatement{{Descriptor: ocispec.Descriptor{MediaType: "application/vnd.in-toto+json", Digest: "sha256:aa", Size: 3},
		PredicateType: "https://spdx.dev/Document"}}
	raw, err := imgMarshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var m imgAttestationsModel
	if d := m.contractFill(ctx, nil, contractFillInput{Raw: map[string][]byte{"attestations_json": raw}}); d.HasError() {
		t.Fatal(d)
	}
	if len(m.Attestations.Elements()) != 1 || m.ContentSHA256.IsNull() {
		t.Errorf("fill: %+v", m.contractEngineImageAttestations)
	}
}

func TestImgDataSourceImages(t *testing.T) {
	ctx := context.Background()
	f, pd := newP3eFake(t)
	var q string
	f.on("GET /images/json", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		p3eWriteJSON(w, http.StatusOK, []image.Summary{{ID: "sha256:b", RepoTags: []string{"b:1"}}, {ID: "sha256:a", Labels: map[string]string{"k": "v"}}})
	})
	st, d := imgTestReadDS(t, pd, NewImagesDataSource(), map[string]tftypes.Value{
		"reference": p3eStr("nginx*"), "dangling": tftypes.NewValue(tftypes.Bool, false), "labels": p3eStrMap(map[string]string{"k": "v"}),
	})
	if d.HasError() {
		t.Fatal(d)
	}
	var m imgImagesModel
	st.Get(ctx, &m)
	var ids []string
	m.IDs.ElementsAs(ctx, &ids, false)
	if strings.Join(ids, ",") != "sha256:a,sha256:b" || len(m.Images.Elements()) != 2 || !strings.Contains(q, "reference") || !strings.Contains(q, "dangling") {
		t.Errorf("model %+v query %s", ids, q)
	}
	f.on("GET /images/json", func(w http.ResponseWriter, r *http.Request) {
		p3eWriteJSON(w, http.StatusInternalServerError, map[string]string{"message": "boom"})
	})
	if _, d := imgTestReadDS(t, pd, NewImagesDataSource(), nil); !d.HasError() {
		t.Error("error expected")
	}
}

// ---------------------------------------------------------------------------
// tag + actions

func TestImgTagSchemaContract(t *testing.T) {
	ctx := context.Background()
	var sr fwresource.SchemaResponse
	NewTagResource().Schema(ctx, fwresource.SchemaRequest{}, &sr)
	if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatal(d)
	}
	for _, a := range append(contractIdentityOutputNames(tagItem), "triggers", "inspect_json", "content_sha256") {
		if _, ok := sr.Schema.Attributes[a]; !ok {
			t.Errorf("missing %s", a)
		}
	}
}

// imgResultFile reads a result_path file.
func imgResultFile(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestImgActionCommit(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /containers/web/json", map[string]any{"Id": "c0ffee", "Name": "/web"})
	f.json("GET /containers/c0ffee/changes", []container.FilesystemChange{{Kind: container.ChangeAdd, Path: "/b"}, {Kind: container.ChangeModify, Path: "/a"}})
	var q string
	f.on("POST /commit", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		p3eWriteJSON(w, http.StatusCreated, map[string]string{"Id": "sha256:new"})
	})
	out := filepath.Join(t.TempDir(), "r.json")
	res := actInvokePD(t, pd, NewImageCommitAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"), "reference": actTfStr("snap:1"), "pause": actTfBool(false), "result_path": actTfStr(out),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if !strings.Contains(q, "container=c0ffee") || !strings.Contains(q, "pause=0") || !strings.Contains(q, "repo=docker.io%2Flibrary%2Fsnap&tag=1") {
		t.Errorf("query %s", q)
	}
	r := imgResultFile(t, out)
	ch := r["result"].(map[string]any)["changes"].([]any)
	if r["id"] != "sha256:new" || r["container_id"] != "c0ffee" || r["image_ref"] != "docker.io/library/snap:1" || len(ch) != 2 ||
		ch[0].(map[string]any)["path"] != "/a" {
		t.Errorf("result %v", r)
	}
	if d := actValidate(t, NewImageCommitAction(), map[string]tftypes.Value{"container_id": actTfStr("web"), "reference": actTfStr("BAD REF")}); !d.HasError() {
		t.Error("bad reference must fail validation")
	}
	res = actInvokePD(t, pd, NewImageCommitAction(), map[string]tftypes.Value{"container_id": actTfStr("nope")})
	if !strings.Contains(res.errText(), "cannot find container") {
		t.Errorf("missing container: %s", res.errText())
	}
}

func TestImgActionSaveLoadImportResults(t *testing.T) {
	f, pd := newP3eFake(t)
	f.on("GET /images/get", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("SAVED")) })
	f.on("POST /images/load", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"stream":"Loaded image: busybox:1.36\n"}` + "\n" + `{"stream":"Loaded image ID: sha256:x\n"}` + "\n"))
	})
	f.on("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"status":"sha256:imported"}` + "\n"))
	})
	f.json("GET /images/sha256:imported/json", map[string]any{"Id": "sha256:imported", "Size": 6, "Os": "linux", "Architecture": "amd64"})
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "imgs.tar")
	res := actInvokePD(t, pd, NewImageSaveAction(), map[string]tftypes.Value{
		"images": actTfList("busybox:1.36"), "output": actTfStr(tarPath), "result_path": actTfStr(filepath.Join(dir, "save.json")),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	r := imgResultFile(t, filepath.Join(dir, "save.json"))
	sum := sha256.Sum256([]byte("SAVED"))
	if r["archive_sha256"] != hex.EncodeToString(sum[:]) {
		t.Errorf("hash %v", r["archive_sha256"])
	}
	if r["path"] != tarPath || r["result"].(map[string]any)["archive_size"].(float64) != 5 {
		t.Errorf("save result %v", r)
	}
	res = actInvokePD(t, pd, NewImageLoadAction(), map[string]tftypes.Value{"input": actTfStr(tarPath), "result_path": actTfStr(filepath.Join(dir, "load.json"))})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	r = imgResultFile(t, filepath.Join(dir, "load.json"))
	if l := r["loaded_images"].([]any); len(l) != 2 || l[0] != "busybox:1.36" || l[1] != "sha256:x" {
		t.Errorf("load result %v", r)
	}
	res = actInvokePD(t, pd, NewImageImportAction(), map[string]tftypes.Value{
		"source": actTfStr(tarPath), "repository": actTfStr("example/rootfs"), "result_path": actTfStr(filepath.Join(dir, "import.json")),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	r = imgResultFile(t, filepath.Join(dir, "import.json"))
	if r["id"] != "sha256:imported" || r["image_ref"] != "docker.io/example/rootfs:latest" || r["result"].(map[string]any)["image"].(map[string]any)["os"] != "linux" {
		t.Errorf("import result %v", r)
	}
	if !actHasProgress(res, "docker_image_import result:") {
		t.Errorf("progress %v", res.Progress)
	}
}
