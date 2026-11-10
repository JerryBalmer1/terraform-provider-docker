package provider

// Pass 6a Lane F acceptance tests for the Engine 29.9.3 harness (API 1.56):
// the containerd image store node (cdnode, TF_ACC_CONTAINERD_HOST), service
// swap resources on the swarm manager, and the SwarmSpec.TaskDefaults probe.
// TestAccServiceP6a_* needs the -SwarmInit pass (matches $SwarmAccRegex);
// the others run in the plain pass and skip without their environment.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// p6aContainerdHostEnv names the harness's containerd image store daemon
// (test/harness docker-compose.yml service cdnode).
const p6aContainerdHostEnv = "TF_ACC_CONTAINERD_HOST"

// p6aUseContainerdNode points DOCKER_HOST, which the in-process provider and
// testAccDockerClient read, at the harness's containerd-store node for the
// rest of t. It reports false (and changes nothing) when
// TF_ACC_CONTAINERD_HOST is unset.
func p6aUseContainerdNode(t *testing.T) bool {
	t.Helper()
	h := os.Getenv(p6aContainerdHostEnv)
	if h == "" {
		return false
	}
	t.Setenv("DOCKER_HOST", h)
	return true
}

// p6aRequireContainerdNode skips t unless TF_ACC_CONTAINERD_HOST names a
// reachable daemon on the containerd image store, and points DOCKER_HOST at it.
func p6aRequireContainerdNode(t *testing.T) *client.Client {
	t.Helper()
	if !p6aUseContainerdNode(t) {
		t.Skipf("%s not set; skipping (the containerd image store node of test/harness)", p6aContainerdHostEnv)
	}
	c := testAccDockerClient(t)
	ok, drv, err := bkImageStore(context.Background(), &providerData{Client: c})
	if err != nil {
		t.Skipf("cannot reach %s: %v", os.Getenv(p6aContainerdHostEnv), err)
	}
	if !ok {
		t.Skipf("%s uses the %s graph driver, not the containerd image store", os.Getenv(p6aContainerdHostEnv), drv)
	}
	return c
}

// p6aOCILayout maps archive paths to file contents for an OCI image layout
// under construction; add stores one content-addressed blob.
type p6aOCILayout map[string][]byte

func (l p6aOCILayout) add(b []byte) (string, int64) {
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	l["blobs/sha256/"+h] = b
	return "sha256:" + h, int64(len(b))
}

func p6aJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// p6aMultiPlatformOCITar writes a two-platform (linux/amd64, linux/arm64)
// OCI image layout archive for ref (docker.io/library/<name>:<tag>), the
// shape `docker save` writes on the containerd store: index.json -> image
// index -> one manifest per platform, each with a one-file layer. Nothing is
// executed from it, so no emulation is involved.
func p6aMultiPlatformOCITar(t *testing.T, ref, tag string) string {
	t.Helper()
	l := p6aOCILayout{}
	var manifests []map[string]any
	for _, arch := range []string{"amd64", "arm64"} {
		var layer bytes.Buffer
		tw := tar.NewWriter(&layer)
		body := []byte("tpd p6a " + arch + "\n")
		if err := tw.WriteHeader(&tar.Header{Name: "platform.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(body)
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		ld, ls := l.add(layer.Bytes())
		cd, cs := l.add(p6aJSON(t, map[string]any{
			"architecture": arch, "os": "linux", "created": "2026-01-01T00:00:00Z",
			"config": map[string]any{"Cmd": []string{"/platform.txt"}},
			"rootfs": map[string]any{"type": "layers", "diff_ids": []string{ld}},
		}))
		md, ms := l.add(p6aJSON(t, map[string]any{
			"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cd, "size": cs},
			"layers": []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": ld, "size": ls}},
		}))
		manifests = append(manifests, map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": md, "size": ms,
			"platform": map[string]any{"architecture": arch, "os": "linux"}})
	}
	id, is := l.add(p6aJSON(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": manifests}))
	l["index.json"] = p6aJSON(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": id, "size": is,
			"annotations": map[string]string{"io.containerd.image.name": ref + ":" + tag, "org.opencontainers.image.ref.name": tag}}}})
	l["oci-layout"] = []byte(`{"imageLayoutVersion":"1.0.0"}`)

	p := filepath.Join(t.TempDir(), "multi.tar")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, dir := range []string{"blobs/", "blobs/sha256/"} {
		if err := tw.WriteHeader(&tar.Header{Name: dir, Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	for name, b := range l {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(b)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAccImageP6a_loadMultiPlatform loads a two-platform OCI archive with
// the docker_image_load action on the containerd-store node and checks that
// the daemon keeps both platform manifests of the image.
func TestAccImageP6a_loadMultiPlatform(t *testing.T) {
	testAccPreCheck(t)
	c := p6aRequireContainerdNode(t)
	ctx := context.Background()
	name := "tpd-p6a-multi"
	ref := "docker.io/library/" + name + ":1"
	in := filepath.ToSlash(p6aMultiPlatformOCITar(t, "docker.io/library/"+name, "1"))
	out := filepath.ToSlash(filepath.Join(t.TempDir(), "load.json"))
	t.Cleanup(func() { _, _ = c.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{Force: true}) })
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_image_load" "l" {
  config {
    input       = %q
    result_path = %q
  }
}
resource "terraform_data" "load" {
  input = "p6a"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_image_load.l]
    }
  }
}
`, in, out),
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(out)
				if err != nil {
					return fmt.Errorf("result_path: %w", err)
				}
				if !strings.Contains(string(b), name+":1") {
					return fmt.Errorf("loaded_images does not name %s:1: %s", name, b)
				}
				res, err := c.ImageInspect(ctx, ref, client.ImageInspectWithManifests(true))
				if err != nil {
					return err
				}
				got := map[string]bool{}
				for _, m := range res.Manifests {
					if m.ImageData != nil {
						got[m.ImageData.Platform.OS+"/"+m.ImageData.Platform.Architecture] = m.Available
					}
				}
				if _, ok := got["linux/amd64"]; !ok {
					return fmt.Errorf("linux/amd64 manifest missing: %v", got)
				}
				if _, ok := got["linux/arm64"]; !ok {
					return fmt.Errorf("linux/arm64 manifest missing: %v", got)
				}
				t.Logf("platform manifests (available): %v", got)
				return nil
			},
		}},
	})
}

// TestAccServiceP6a_swapResources sets docker_service resources.swap_bytes
// and memory_swappiness (Engine API 1.52+) on the harness swarm manager and
// reads them back from the daemon; the post-apply plan must be empty.
func TestAccServiceP6a_swapResources(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	name := acctest.RandomWithPrefix("tfacc-svc6a-swap")
	cfg := func(swap, swappiness int64) string {
		return fmt.Sprintf(`
resource "docker_service" "s" {
  name    = %[1]q
  image   = "busybox:1.36"
  command = ["sleep", "3600"]
  resources {
    swap_bytes        = %[2]d
    memory_swappiness = %[3]d
    limits {
      memory_bytes = 33554432
    }
  }
}
`, name, swap, swappiness)
	}
	check := func(swap, swappiness int64) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("docker_service.s", "resources.swap_bytes", fmt.Sprint(swap)),
			resource.TestCheckResourceAttr("docker_service.s", "resources.memory_swappiness", fmt.Sprint(swappiness)),
			func(*terraform.State) error {
				res, err := testAccDockerClient(t).ServiceInspect(context.Background(), name, client.ServiceInspectOptions{})
				if err != nil {
					return err
				}
				r := res.Service.Spec.TaskTemplate.Resources
				switch {
				case r == nil:
					return fmt.Errorf("no resources in the service spec")
				case r.SwapBytes == nil || *r.SwapBytes != swap:
					return fmt.Errorf("SwapBytes = %v, want %d", r.SwapBytes, swap)
				case r.MemorySwappiness == nil || *r.MemorySwappiness != swappiness:
					return fmt.Errorf("MemorySwappiness = %v, want %d", r.MemorySwappiness, swappiness)
				}
				return nil
			},
		)
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg(67108864, 10), Check: check(67108864, 10)},
			{Config: cfg(134217728, 60), Check: check(134217728, 60)},
		},
	})
}

// TestAccSwarmInitP6a_taskDefaults pins the reason of the field coverage
// skip of SwarmSpec.TaskDefaults.LogDriver (reference/_gen/fieldmap.d/S.json):
// Engine 29.9.3 (API 1.56) still drops it on swarm init and on swarm update
// (read back nil both times). When this test fails, the Engine started
// keeping it: implement task_defaults in docker_swarm_init. It uses the
// containerd-store node, a standalone daemon outside the swarm, and leaves
// the swarm again afterwards.
func TestAccSwarmInitP6a_taskDefaults(t *testing.T) {
	testAccPreCheck(t)
	c := p6aRequireContainerdNode(t)
	ctx := context.Background()
	infoRes, err := c.Info(ctx, client.InfoOptions{})
	if err != nil {
		t.Skipf("cannot reach %s: %v", os.Getenv(p6aContainerdHostEnv), err)
	}
	if infoRes.Info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skipf("%s is already part of a swarm", os.Getenv(p6aContainerdHostEnv))
	}
	initLog := &swarm.Driver{Name: "json-file", Options: map[string]string{"max-size": "1m"}}
	if _, err := c.SwarmInit(ctx, client.SwarmInitOptions{ListenAddr: "0.0.0.0:2377", AdvertiseAddr: "127.0.0.1",
		Spec: swarm.Spec{TaskDefaults: swarm.TaskDefaults{LogDriver: initLog}}}); err != nil {
		t.Fatalf("swarm init: %v", err)
	}
	t.Cleanup(func() { _, _ = c.SwarmLeave(context.Background(), client.SwarmLeaveOptions{Force: true}) })
	sw, err := c.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	onInit := sw.Swarm.Spec.TaskDefaults.LogDriver
	t.Logf("TaskDefaults.LogDriver after init with %+v: %+v", *initLog, onInit)

	updLog := &swarm.Driver{Name: "local", Options: map[string]string{"max-size": "2m"}}
	spec := sw.Swarm.Spec
	spec.TaskDefaults.LogDriver = updLog
	if _, err := c.SwarmUpdate(ctx, client.SwarmUpdateOptions{Version: sw.Swarm.Version, Spec: spec}); err != nil {
		t.Fatalf("swarm update: %v", err)
	}
	sw, err = c.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	onUpdate := sw.Swarm.Spec.TaskDefaults.LogDriver
	t.Logf("TaskDefaults.LogDriver after update with %+v: %+v", *updLog, onUpdate)

	if onInit != nil {
		t.Errorf("init: the Engine now keeps TaskDefaults.LogDriver (%+v): implement it in docker_swarm_init", *onInit)
	}
	if onUpdate != nil {
		t.Errorf("update: the Engine now keeps TaskDefaults.LogDriver (%+v): implement it in docker_swarm_init", *onUpdate)
	}
}

// TestAccContainerP6a_umask sets docker_container umask (Engine API 1.56,
// which the Engine 29.9.3 harness speaks) and checks the daemon kept it and
// the container's processes run with it.
func TestAccContainerP6a_umask(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-p6a-umask")
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             ctrP3CheckDestroy(t),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_container" "c" {
  name      = %q
  image     = %q
  command   = ["sh", "-c", "umask; sleep 3600"]
  umask     = "0027"
  logs_tail = 5
}
`, name, actAccImage),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_container.c", "umask", "0027"),
				func(s *terraform.State) error {
					insp, err := ctrP3Inspect(t, s, "docker_container.c")
					if err != nil {
						return err
					}
					if u := insp.HostConfig.Umask; u == nil || *u != 0o027 {
						return fmt.Errorf("HostConfig.Umask = %v, want 0027", u)
					}
					return nil
				},
			),
		}},
	})
}
