package provider

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Pass 6b Lane Z: docker_compose_push matches compose v5.7.0
// pkg/compose/push.go:50-64: a service without `build` (or without
// `image`) is skipped, and a built service's `build.tags` are pushed too.
func TestComposePushBuildTagsP6bZ2(t *testing.T) {
	f, pd := composeCFake(t)
	dir := t.TempDir()
	yaml := `name: app
services:
  api:
    build:
      context: .
      tags: ["reg.io/api:extra", "reg.io/api:gone"]
    image: "reg.io/api:1"
  web:
    image: "nginx:1.27"
  noname:
    build: {context: .}
`
	pushOK := func(tag string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"pushed"}` + "\n" + `{"aux":{"Tag":"` + tag + `","Digest":"sha256:` + strings.Repeat("e", 64) + `","Size":1}}` + "\n"))
		}
	}
	f.on("POST /images/reg.io/api/push", pushOK("1"))
	f.json("GET /images/reg.io/api:1/json", map[string]any{"Id": "sha256:api"})
	f.json("GET /images/reg.io/api:extra/json", map[string]any{"Id": "sha256:api"})
	// reg.io/api:gone has no inspect route: the fake answers 404.

	res := actInvokePD(t, pd, NewComposePushAction(), map[string]tftypes.Value{
		"content": p3eStr(yaml), "project_directory": p3eStr(dir), "ignore_missing": actTfBool(true),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	r := composeCResult(t, res)
	st := r["result"].(map[string]any)["push_status"].(map[string]any)
	if st["web"] != "skipped (not built)" || st["noname"] != "skipped (no image name)" ||
		st["api"] != "pushed; skipped (no local image): reg.io/api:gone" {
		t.Errorf("push status %v", st)
	}
	pushes := composeCSeen(f, "POST /images/reg.io/api/push")
	if len(pushes) != 2 {
		t.Errorf("want the image and one build tag pushed, got %d pushes", len(pushes))
	}
	if len(composeCSeen(f, "POST /images/docker.io/library/nginx/push")) != 0 {
		t.Error("a service without build was pushed")
	}
	if r["image_refs"].(map[string]any)["api"] != "reg.io/api:1" ||
		r["image_digest_refs"].(map[string]any)["api"] != "reg.io/api@sha256:"+strings.Repeat("e", 64) {
		t.Errorf("refs %v %v", r["image_refs"], r["image_digest_refs"])
	}
	if _, ok := r["image_refs"].(map[string]any)["web"]; ok {
		t.Errorf("web in image_refs: %v", r["image_refs"])
	}
}
