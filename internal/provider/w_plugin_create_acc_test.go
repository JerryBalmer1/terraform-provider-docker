package provider

// Pass 5.2 Lane W: docker_plugin_create end to end from the test host. The
// plugin directory is the harness's tfacc volume plugin (TF_ACC_PLUGIN_DIR,
// config.json + rootfs/tfaccplugin), copied by Go into a temp dir on the host
// running the test, so on Windows the rootfs is written on Windows.

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/client"
)

// wCopyDir copies src into dst, keeping each file's permission bits.
func wCopyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copying %s: %v", src, err)
	}
}

// wPluginRuns checks that plugin name is enabled and serves its volume
// driver API: a volume created with it as the driver round-trips.
func wPluginRuns(c *client.Client, name string) error {
	ctx := context.Background()
	res, err := c.PluginInspect(ctx, name, client.PluginInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspecting %s: %w", name, err)
	}
	if !res.Plugin.Enabled {
		return fmt.Errorf("plugin %s is not enabled", name)
	}
	vol := acctest.RandomWithPrefix("tfacc-w-vol")
	if _, err := c.VolumeCreate(ctx, client.VolumeCreateOptions{Name: vol, Driver: name}); err != nil {
		return fmt.Errorf("creating volume %s with driver %s: %w", vol, name, err)
	}
	if _, err := c.VolumeRemove(ctx, vol, client.VolumeRemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("removing volume %s: %w", vol, err)
	}
	return nil
}

// TestAccWPluginCreateEnable creates the tfacc volume plugin with
// docker_plugin_create (enable = true, so through objTarDir and
// PluginEnable), then checks the plugin is enabled and its entrypoint answers
// the volume driver API. Then the reproduction: the same directory tarred by
// the pre-fix builder (wPreFixTarDir) and posted to /plugins/create; on
// Windows enabling it must fail with a permission error (the entrypoint is
// 0666), elsewhere the two builders are identical and it must enable.
func TestAccWPluginCreateEnable(t *testing.T) {
	testAccPreCheck(t)
	src := p3Env(t, "TF_ACC_PLUGIN_DIR")[0]
	if _, err := os.Stat(filepath.Join(src, "rootfs", "tfaccplugin")); err != nil {
		t.Skipf("TF_ACC_PLUGIN_DIR has no rootfs/tfaccplugin (%v); skipping (harness-up builds it)", err)
	}
	dir := filepath.Join(t.TempDir(), "plugin")
	wCopyDir(t, src, dir)
	c := testAccDockerClient(t)
	fixed := acctest.RandomWithPrefix("tfacc-w-fixed") + ":1"
	prefix := acctest.RandomWithPrefix("tfacc-w-prefix") + ":1"
	t.Cleanup(func() {
		for _, n := range []string{fixed, prefix} {
			_, _ = c.PluginRemove(context.Background(), n, client.PluginRemoveOptions{Force: true})
		}
	})
	out := filepath.ToSlash(filepath.Join(t.TempDir(), "create.json"))
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   actAccVersionChecks,
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
action "docker_plugin_create" "p" {
  config {
    name           = %[1]q
    context_dir    = %[2]q
    enable         = true
    enable_timeout = 60
    result_path    = %[3]q
  }
}

resource "terraform_data" "p" {
  input = "plugin"
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_plugin_create.p]
    }
  }
}
`, fixed, filepath.ToSlash(dir), out),
			Check: resource.ComposeAggregateTestCheckFunc(
				objAccResultFile(out, "id"),
				func(*terraform.State) error {
					b, err := os.ReadFile(out)
					if err != nil {
						return err
					}
					if !strings.Contains(strings.ReplaceAll(string(b), " ", ""), `"enabled":true`) {
						return fmt.Errorf("%s does not report enabled=true: %s", out, b)
					}
					return wPluginRuns(c, fixed)
				},
			),
		}},
	})
	if t.Failed() {
		return
	}

	// Reproduction with the pre-fix tar builder.
	buf, err := wPreFixTarDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.PluginCreate(ctx, buf, client.PluginCreateOptions{RepoName: prefix}); err != nil {
		t.Fatalf("creating %s from the pre-fix tar: %v", prefix, err)
	}
	_, enableErr := c.PluginEnable(ctx, prefix, client.PluginEnableOptions{Timeout: 60})
	if runtime.GOOS != "windows" {
		if enableErr != nil {
			t.Fatalf("on %s the pre-fix builder equals objTarDir, yet enabling %s failed: %v", runtime.GOOS, prefix, enableErr)
		}
		t.Logf("on %s the pre-fix tar enables too (no Windows chmod involved); reproduction only applies on Windows", runtime.GOOS)
		return
	}
	if enableErr == nil {
		t.Fatalf("enabling %s (pre-fix tar from Windows, entrypoint 0666) succeeded; the Lane Z fix would not be needed", prefix)
	}
	t.Logf("pre-fix tar: Engine error enabling %s: %v", prefix, enableErr)
	if !strings.Contains(strings.ToLower(enableErr.Error()), "permission denied") {
		t.Fatalf("enabling %s failed, but not with a permission error: %v", prefix, enableErr)
	}
}
