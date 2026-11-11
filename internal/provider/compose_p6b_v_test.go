package provider

// Pass 6b Lane V: docker_compose_push ignore_missing (compose v5.7.0
// `push --ignore-missing`) and docker_compose_run start_dependencies /
// no_recreate (`run` without `--no-deps`, `run --no-recreate`).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/client"
)

func TestComposePushIgnoreMissingP6bV(t *testing.T) {
	f, pd := composeCFake(t)
	dir := t.TempDir()
	// Both are built: compose pushes only built services (push.go:50-60, v5.7.0).
	yaml := "name: app\nservices:\n  web: {image: \"nginx:1.27\", build: {context: .}}\n  db: {image: \"postgres:16\", build: {context: .}}\n"
	f.json("GET /images/nginx:1.27/json", map[string]any{"Id": "sha256:img1"})
	// postgres:16 has no route: the fake answers 404 (not in the local store).
	f.on("POST /images/docker.io/library/nginx/push", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pushed"}` + "\n" + `{"aux":{"Tag":"1.27","Digest":"sha256:` + strings.Repeat("d", 64) + `","Size":1}}` + "\n"))
	})
	res := actInvokePD(t, pd, NewComposePushAction(), map[string]tftypes.Value{
		"content": p3eStr(yaml), "project_directory": p3eStr(dir), "ignore_missing": actTfBool(true),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	r := composeCResult(t, res)
	st := r["result"].(map[string]any)["push_status"].(map[string]any)
	if st["db"] != "skipped (no local image)" || st["web"] != "pushed" {
		t.Errorf("push status %v", st)
	}
	if len(composeCSeen(f, "POST /images/docker.io/library/postgres/push")) != 0 {
		t.Error("the missing image was pushed")
	}
	// Without ignore_missing the missing image fails the push.
	res = actInvokePD(t, pd, NewComposePushAction(), map[string]tftypes.Value{
		"content": p3eStr(yaml), "project_directory": p3eStr(dir),
	})
	if !res.Diags.HasError() || !strings.Contains(res.errText(), "postgres:16") {
		t.Errorf("want a push failure for postgres:16, got %q", res.errText())
	}
}

// composeRunDepsFake is an Engine with project "shop": db exists (running,
// with spec hash dbHash), cache does not. Every create returns "id-<name>".
func composeRunDepsFake(t *testing.T, dbHash string) (*p3eFake, *providerData, func() []string) {
	t.Helper()
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /containers/json", []map[string]any{{
		"Id": "d1", "Names": []string{"/shop-db-1"}, "Image": "busybox:1.36", "State": "running",
		"Labels": map[string]string{composeLabelProject: "shop", composeLabelService: "db", composeLabelNumber: "1",
			composeLabelOneoff: "False", composeLabelSpecHash: dbHash},
	}})
	f.json("GET /networks", []any{})
	f.json("GET /volumes", map[string]any{"Volumes": []any{}})
	f.json("GET /images/busybox:1.36/json", map[string]any{"Id": "sha256:img2"})
	var mu sync.Mutex
	var created []string
	f.on("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		mu.Lock()
		created = append(created, name)
		mu.Unlock()
		p3eWriteJSON(w, 201, map[string]string{"Id": "id-" + name})
	})
	f.on("POST /containers/*", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait") {
			p3eWriteJSON(w, 200, map[string]any{"StatusCode": 0})
			return
		}
		w.WriteHeader(204)
	})
	f.on("GET /containers/*", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(composeCFrame(1, "ok\n")) })
	f.on("DELETE /containers/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	return f, pd, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), created...)
	}
}

const composeRunDepsYAML = "name: shop\nservices:\n  db: {image: \"busybox:1.36\"}\n  cache: {image: \"busybox:1.36\"}\n" +
	"  other: {image: \"busybox:1.36\"}\n  worker:\n    image: \"busybox:1.36\"\n    depends_on: [db, cache]\n"

func composeRunDepsInvoke(t *testing.T, pd *providerData, extra map[string]tftypes.Value) actResult {
	t.Helper()
	vals := map[string]tftypes.Value{
		"content": p3eStr(composeRunDepsYAML), "project_directory": p3eStr(t.TempDir()), "service": p3eStr("worker"),
	}
	for k, v := range extra {
		vals[k] = v
	}
	return actInvokePD(t, pd, NewComposeRunAction(), vals)
}

func TestComposeRunStartDependenciesP6bV(t *testing.T) {
	t.Run("default no deps", func(t *testing.T) {
		f, pd, created := composeRunDepsFake(t, "stale")
		res := composeRunDepsInvoke(t, pd, nil)
		if res.Diags.HasError() {
			t.Fatal(res.errText())
		}
		if c := created(); len(c) != 1 || !strings.HasPrefix(c[0], "shop-worker-run-") {
			t.Errorf("created %v, want only the run container", c)
		}
		if len(composeCSeen(f, "GET /containers/json")) != 0 {
			t.Error("dependencies were looked at without start_dependencies")
		}
	})
	t.Run("diverged dependency recreated", func(t *testing.T) {
		f, pd, created := composeRunDepsFake(t, "stale")
		res := composeRunDepsInvoke(t, pd, map[string]tftypes.Value{"start_dependencies": actTfBool(true)})
		if res.Diags.HasError() {
			t.Fatal(res.errText())
		}
		c := created()
		if len(c) != 3 || !composeHas(c, "shop-db-1") || !composeHas(c, "shop-cache-1") || !strings.HasPrefix(c[2], "shop-worker-run-") {
			t.Errorf("created %v, want db (recreated), cache (missing), then the run container", c)
		}
		if composeHas(c, "shop-other-1") {
			t.Error("a service outside the depends_on closure was created")
		}
		if len(composeCSeen(f, "DELETE /containers/d1")) != 1 {
			t.Error("the diverged db container was not removed")
		}
		if !actHasProgress(res, "recreating dependency shop-db-1") || !actHasProgress(res, "creating dependency shop-cache-1") {
			t.Errorf("progress %q", res.Progress)
		}
	})
	t.Run("no_recreate keeps the diverged dependency", func(t *testing.T) {
		f, pd, created := composeRunDepsFake(t, "stale")
		res := composeRunDepsInvoke(t, pd, map[string]tftypes.Value{"start_dependencies": actTfBool(true), "no_recreate": actTfBool(true)})
		if res.Diags.HasError() {
			t.Fatal(res.errText())
		}
		c := created()
		if len(c) != 2 || c[0] != "shop-cache-1" || !strings.HasPrefix(c[1], "shop-worker-run-") {
			t.Errorf("created %v, want cache (missing) then the run container", c)
		}
		if len(composeCSeen(f, "DELETE /containers/d1")) != 0 {
			t.Error("no_recreate removed the db container")
		}
	})
}

func composeHas(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestComposeRunNoRecreateValidateP6bV(t *testing.T) {
	d := actValidate(t, NewComposeRunAction(), map[string]tftypes.Value{
		"content": p3eStr(composeRunDepsYAML), "service": p3eStr("worker"), "no_recreate": actTfBool(true),
	})
	if d.ErrorsCount() != 1 || !strings.Contains(d.Errors()[0].Summary(), "start_dependencies") {
		t.Errorf("want one no_recreate error, got %v", d)
	}
	d = actValidate(t, NewComposeRunAction(), map[string]tftypes.Value{
		"content": p3eStr(composeRunDepsYAML), "service": p3eStr("worker"), "no_recreate": actTfBool(true), "start_dependencies": actTfBool(true),
	})
	if d.HasError() {
		t.Errorf("valid config: %v", d)
	}
}

// TestAccComposeRunDepsP6bV: docker_compose_run with start_dependencies
// brings up a missing dependency, and with no_recreate keeps a dependency
// whose compose definition changed.
func TestAccComposeRunDepsP6bV(t *testing.T) {
	testAccPreCheck(t)
	project := acctest.RandomWithPrefix("tfacc-runp6b")
	c := testAccDockerClient(t)
	resultPath := filepath.Join(t.TempDir(), "run.json")
	cfg := func(dbCmd string, noRecreate bool) string {
		return fmt.Sprintf(`
action "docker_compose_run" "job" {
  config {
    content = <<-EOT
      name: %s
      services:
        db:
          image: busybox:1.36
          command: ["sh", "-c", "%s"]
        job:
          image: busybox:1.36
          command: ["sh", "-c", "echo ran"]
          depends_on: [db]
    EOT
    service            = "job"
    start_dependencies = true
    no_recreate        = %t
    result_path        = %q
  }
}

resource "terraform_data" "run" {
  input = %q
  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_compose_run.job]
    }
  }
}
`, project, dbCmd, noRecreate, filepath.ToSlash(resultPath), dbCmd)
	}
	dbID := ""
	readDB := func() (string, error) {
		res, err := c.ContainerList(context.Background(), client.ContainerListOptions{All: true,
			Filters: client.Filters{}.Add("label", composeLabelProject+"="+project).Add("label", composeLabelService+"=db")})
		if err != nil {
			return "", err
		}
		if len(res.Items) != 1 || res.Items[0].State != "running" {
			return "", fmt.Errorf("db containers: %+v", res.Items)
		}
		return res.Items[0].ID, nil
	}
	t.Cleanup(func() {
		res, err := c.ContainerList(context.Background(), client.ContainerListOptions{All: true,
			Filters: client.Filters{}.Add("label", composeLabelProject+"="+project)})
		if err == nil {
			for _, s := range res.Items {
				_, _ = c.ContainerRemove(context.Background(), s.ID, client.ContainerRemoveOptions{Force: true})
			}
		}
		_, _ = c.NetworkRemove(context.Background(), project+"_default", client.NetworkRemoveOptions{})
	})
	// The project network must exist (docker_compose_run's precondition).
	if _, err := c.NetworkCreate(context.Background(), project+"_default", client.NetworkCreateOptions{
		Labels: map[string]string{composeLabelProject: project, "com.docker.compose.network": "default"}}); err != nil {
		t.Fatal(err)
	}
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("sleep 3600", false),
				Check: func(*terraform.State) error {
					id, err := readDB()
					if err != nil {
						return fmt.Errorf("start_dependencies did not bring db up: %w", err)
					}
					dbID = id
					return composeRunResultOK(resultPath)
				},
			},
			{
				// db's command changes: no_recreate keeps the running db.
				Config: cfg("sleep 3601", true),
				Check: func(*terraform.State) error {
					id, err := readDB()
					if err != nil {
						return err
					}
					if id != dbID {
						return fmt.Errorf("no_recreate recreated db (%s -> %s)", dbID, id)
					}
					return composeRunResultOK(resultPath)
				},
			},
			{
				// Same change without no_recreate: db is recreated.
				Config: cfg("sleep 3602", false),
				Check: func(*terraform.State) error {
					id, err := readDB()
					if err != nil {
						return err
					}
					if id == dbID {
						return fmt.Errorf("diverged db was not recreated")
					}
					return composeRunResultOK(resultPath)
				},
			},
		},
	})
}

func composeRunResultOK(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("result_path: %w", err)
	}
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	rr, _ := r["result"].(map[string]any)
	if rr == nil || rr["stdout"] != "ran\n" {
		return fmt.Errorf("run result %s", b)
	}
	return nil
}

// TestAccComposePushIgnoreMissingP6bV: a service whose image is not in the
// local store is skipped with ignore_missing; the other one is pushed to
// the harness registry.
func TestAccComposePushIgnoreMissingP6bV(t *testing.T) {
	testAccPreCheck(t)
	reg := os.Getenv("TF_ACC_REGISTRY")
	if reg == "" {
		t.Skip("TF_ACC_REGISTRY not set (the harness registry, host:port)")
	}
	c := testAccDockerClient(t)
	actAccPull(t, c, "busybox:1.36")
	present := reg + "/tfacc-p6b-push:present"
	missing := reg + "/tfacc-p6b-push:missing-" + acctest.RandString(8)
	if _, err := c.ImageTag(context.Background(), client.ImageTagOptions{Source: "busybox:1.36", Target: present}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = c.ImageRemove(context.Background(), present, client.ImageRemoveOptions{}) })
	resultPath := filepath.Join(t.TempDir(), "push.json")
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_compose_push" "p" {
  config {
    content = <<-EOT
      name: tfacc-p6b-push
      services:
        present: {image: "%s", build: {context: .}}
        missing: {image: "%s", build: {context: .}}
    EOT
    ignore_missing = true
    result_path    = %q
  }
}

resource "terraform_data" "p" {
  input = "x"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_compose_push.p]
    }
  }
}
`, present, missing, filepath.ToSlash(resultPath)),
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(resultPath)
				if err != nil {
					return err
				}
				var r map[string]any
				if err := json.Unmarshal(b, &r); err != nil {
					return err
				}
				st, _ := r["result"].(map[string]any)["push_status"].(map[string]any)
				if st["present"] != "pushed" || st["missing"] != "skipped (no local image)" {
					return fmt.Errorf("push status %v", st)
				}
				return nil
			},
		}},
	})
}
