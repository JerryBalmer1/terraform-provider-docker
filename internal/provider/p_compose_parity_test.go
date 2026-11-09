package provider

// What TestAccComposeParity needs to bring a fixture up on the dind harness
// (Pass 5.1 Lane P). A sub-case either runs its testdata/compose_hash
// fixture as is after some setup on the harness daemon (images that exist
// nowhere, external networks / volumes), or, when the fixture cannot run on
// one Linux daemon driven from a Windows client at all, a replacement under
// test/fixtures/compose that exercises the same compose features. The
// replacement says why in its header; the hash fixture and its golden are
// unchanged (TestComposeHashCompat still pins them).

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// composeParityHostRoot is where host files are staged on the harness
// daemon's own filesystem (the manager dind container), one directory per
// sub-case. A replacement fixture names them as ${PARITY_DIR}/<file>.
const composeParityHostRoot = "/srv/compose-parity"

// composeParityHelperImage runs the staging and image-building helpers.
const composeParityHelperImage = "busybox:1.36"

// composeParityImage is an image a fixture names that no registry serves.
// It is From re-tagged, or, with Files / Changes, a container of From with
// Files added, committed with the Dockerfile-style Changes.
type composeParityImage struct {
	From    string
	Files   map[string]string // absolute path in the image -> content (mode 0755)
	Changes []string
}

// composeParitySetup is created on the harness daemon before either side
// runs. Nothing here carries the project label, so the snapshots never see it.
type composeParitySetup struct {
	Images    map[string]composeParityImage
	Networks  []string          // external networks
	Volumes   []string          // external volumes
	HostFiles map[string]string // relative path -> content, under composeParityHostRoot/<case>
}

// composeParityOverride is how one sub-case runs on the harness.
type composeParityOverride struct {
	// File, when set, replaces the testdata/compose_hash fixture with
	// test/fixtures/compose/<File>.
	File  string
	Setup composeParitySetup
}

// composeParityFixturesDir holds the replacement fixtures, relative to
// internal/provider (the test's working directory).
var composeParityFixturesDir = filepath.Join("..", "..", "test", "fixtures", "compose")

var composeParityWebappHost = map[string]string{
	"secrets/db_password.txt": "db-password",
	"nginx.conf":              "server { listen 80; location / { root /usr/share/nginx/html; } }\n",
	"html/index.html":         "webapp\n",
}

// composeParityOverrides is keyed by sub-case name.
var composeParityOverrides = map[string]composeParityOverride{
	// The image is fictional and the network external: both are created.
	"c2s_minimal": {Setup: composeParitySetup{
		Images:   map[string]composeParityImage{"ghcr.io/example/app:1.0": {From: "nginx:1.27-alpine"}},
		Networks: []string{"traefik_public"},
	}},
	// Windows bind / secret / config paths, 3 replicas on one host port.
	"c2s_webapp":       {File: "c2s_webapp.yml", Setup: composeParitySetup{HostFiles: composeParityWebappHost}},
	"c2s_webapp_debug": {File: "c2s_webapp.yml", Setup: composeParitySetup{HostFiles: composeParityWebappHost}},
	// 2 replicas on one host port.
	"example_stack": {File: "example_stack.yml"},
	// Fictional registry images, replicas with pinned IPs / ports, Windows
	// paths, external network and volume.
	"kitchen_sink": {File: "kitchen_sink.yml", Setup: composeParitySetup{
		Images: map[string]composeParityImage{
			"registry.example.com/sink/api:2.1.0": {From: composeParityHelperImage, Files: map[string]string{
				"/bin/api":    "#!/bin/sh\nexec httpd -f -p 8080 -h /www\n",
				"/www/health": "ok\n",
			}},
			"registry.example.com/sink/worker:2.1.0": {From: composeParityHelperImage, Files: map[string]string{
				"/bin/work": "#!/bin/sh\nexec sleep 86400\n",
			}},
			"registry.example.com/sink/admin:2.1.0": {From: composeParityHelperImage, Changes: []string{`CMD ["sleep", "86400"]`}},
		},
		Networks: []string{"shared_ext"},
		Volumes:  []string{"legacy_data"},
		HostFiles: map[string]string{
			"tls.key":       "-----BEGIN KEY-----\nnot-a-key\n-----END KEY-----\n",
			"api.yaml":      "listen: 8080\n",
			"conf/api.yaml": "listen: 8080\n",
		},
	}},
	// build: . has no Dockerfile; the image x exists nowhere: create it, so
	// neither side pulls nor builds.
	"unit_build_image": {Setup: composeParitySetup{
		Images: map[string]composeParityImage{"x": {From: composeParityHelperImage}},
	}},
	// web pins an IP and a host port with 2 replicas, a Windows secret path,
	// an external network.
	"unit_expand": {File: "unit_expand.yml", Setup: composeParitySetup{
		Networks:  []string{"traefik_public"},
		HostFiles: map[string]string{"pw.txt": "filepw"},
	}},
	// The image api exists nowhere; db cannot satisfy service_healthy.
	"unit_profiles": {File: "unit_profiles.yml", Setup: composeParitySetup{
		Images: map[string]composeParityImage{"api": {From: "nginx:1.27-alpine"}},
	}},
	"unit_profiles_debug": {File: "unit_profiles.yml", Setup: composeParitySetup{
		Images: map[string]composeParityImage{"api": {From: "nginx:1.27-alpine"}},
	}},
	// A Windows config path.
	"unit_resource_shop": {File: "unit_resource_shop.yml", Setup: composeParitySetup{
		HostFiles: map[string]string{"site.conf": "server { listen 80; }"},
	}},
}

// composeParityApplyOverride fills c from its override, if any.
func composeParityApplyOverride(t *testing.T, c *composeParityCase) {
	t.Helper()
	ov, ok := composeParityOverrides[c.Name]
	if !ok {
		return
	}
	if ov.File != "" {
		b, err := os.ReadFile(filepath.Join(composeParityFixturesDir, ov.File))
		if err != nil {
			t.Fatal(err)
		}
		c.Content = string(b)
		c.Source = "test/fixtures/compose/" + ov.File
	}
	setup := ov.Setup
	c.Setup = &setup
	if len(setup.HostFiles) > 0 {
		env := map[string]string{}
		for k, v := range c.Env {
			env[k] = v
		}
		env["PARITY_DIR"] = composeParityHostRoot + "/" + c.Name
		c.Env = env
	}
}

// composeParityTar archives files (path -> content) for CopyToContainer at
// "/", with their parent directories.
func composeParityTar(files map[string]string, mode int64) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		n = strings.TrimPrefix(n, "/")
		var parents []string
		for d := path.Dir(n); d != "." && d != "/" && !dirs[d]; d = path.Dir(d) {
			parents = append([]string{d}, parents...)
			dirs[d] = true
		}
		for _, d := range parents {
			if err := tw.WriteHeader(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				return nil, err
			}
		}
	}
	for _, n := range names {
		data := files[n]
		if err := tw.WriteHeader(&tar.Header{Name: strings.TrimPrefix(n, "/"), Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(data))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			return nil, err
		}
	}
	return &buf, tw.Close()
}

// composeParityRun creates a helper container, copies files into it at "/",
// runs it to completion and removes it. It returns the exit error.
func composeParityRun(ctx context.Context, c *client.Client, cfg *container.Config, hc *container.HostConfig, files map[string]string) error {
	created, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hc})
	if err != nil {
		return err
	}
	defer func() {
		_, _ = c.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if len(files) > 0 {
		tr, err := composeParityTar(files, 0o644)
		if err != nil {
			return err
		}
		if _, err := c.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: tr}); err != nil {
			return err
		}
	}
	if _, err := c.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	wr := c.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case res := <-wr.Result:
		if res.StatusCode != 0 {
			return fmt.Errorf("helper exited %d", res.StatusCode)
		}
	case err := <-wr.Error:
		return err
	}
	return nil
}

// composeParityEnsureImage creates ref on the daemon unless it is there.
func composeParityEnsureImage(ctx context.Context, t *testing.T, c *client.Client, ref string, img composeParityImage) {
	t.Helper()
	if _, err := c.ImageInspect(ctx, ref); err == nil {
		return
	}
	if _, err := c.ImageInspect(ctx, img.From); err != nil {
		actAccPull(t, c, img.From)
	}
	if len(img.Files) == 0 && len(img.Changes) == 0 {
		if _, err := c.ImageTag(ctx, client.ImageTagOptions{Source: img.From, Target: ref}); err != nil {
			t.Fatalf("tagging %s as %s: %v", img.From, ref, err)
		}
		return
	}
	created, err := c.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: img.From, Cmd: []string{"true"}}})
	if err != nil {
		t.Fatalf("image %s: %v", ref, err)
	}
	defer func() {
		_, _ = c.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if len(img.Files) > 0 {
		tr, err := composeParityTar(img.Files, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: tr}); err != nil {
			t.Fatalf("image %s: %v", ref, err)
		}
	}
	if _, err := c.ContainerCommit(ctx, created.ID, client.ContainerCommitOptions{Reference: ref, Changes: img.Changes, Comment: "TestAccComposeParity fixture image"}); err != nil {
		t.Fatalf("image %s: %v", ref, err)
	}
}

// composeParityPrepare creates s on the harness daemon for case name.
func composeParityPrepare(ctx context.Context, t *testing.T, c *client.Client, name string, s *composeParitySetup) {
	t.Helper()
	if s == nil {
		return
	}
	for _, ref := range composeSortedKeys(s.Images) {
		composeParityEnsureImage(ctx, t, c, ref, s.Images[ref])
	}
	for _, n := range s.Networks {
		if _, err := c.NetworkInspect(ctx, n, client.NetworkInspectOptions{}); err == nil {
			continue
		}
		if _, err := c.NetworkCreate(ctx, n, client.NetworkCreateOptions{Driver: "bridge"}); err != nil {
			t.Fatalf("external network %s: %v", n, err)
		}
	}
	for _, v := range s.Volumes {
		if _, err := c.VolumeCreate(ctx, client.VolumeCreateOptions{Name: v}); err != nil {
			t.Fatalf("external volume %s: %v", v, err)
		}
	}
	if len(s.HostFiles) == 0 {
		return
	}
	if _, err := c.ImageInspect(ctx, composeParityHelperImage); err != nil {
		actAccPull(t, c, composeParityHelperImage)
	}
	staged := map[string]string{}
	for rel, data := range s.HostFiles {
		staged["/parity-in/"+rel] = data
	}
	dst := "/stage/" + name
	err := composeParityRun(ctx, c,
		&container.Config{Image: composeParityHelperImage, Cmd: []string{"sh", "-c", "rm -rf " + dst + " && mkdir -p " + dst + " && cp -a /parity-in/. " + dst + "/"}},
		&container.HostConfig{Binds: []string{composeParityHostRoot + ":/stage"}}, staged)
	if err != nil {
		t.Fatalf("staging host files under %s/%s: %v", composeParityHostRoot, name, err)
	}
}

// composeParityClean removes everything labelled with project (leftovers
// of a failed or interrupted run, by either side).
func composeParityClean(ctx context.Context, c *client.Client, project string) error {
	f := client.Filters{}.Add("label", composeLabelProject+"="+project)
	var errs []string
	cs, err := c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return err
	}
	for _, s := range cs.Items {
		if _, err := c.ContainerRemove(ctx, s.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNotFound(err) {
			errs = append(errs, err.Error())
		}
	}
	ns, err := c.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	if err != nil {
		return err
	}
	for _, n := range ns.Items {
		if _, err := c.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil && !isNotFound(err) {
			errs = append(errs, err.Error())
		}
	}
	vs, err := c.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	if err != nil {
		return err
	}
	for _, v := range vs.Items {
		if _, err := c.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("cleaning project %s: %s", project, strings.Join(errs, "; "))
	}
	return nil
}

// TestComposeParityFixtures checks the overrides without a daemon: every
// override names a real sub-case, every replacement fixture loads with its
// case's inputs, and local expansion plans it.
func TestComposeParityFixtures(t *testing.T) {
	cases := composeParityCases(t)
	byName := map[string]composeParityCase{}
	for _, c := range cases {
		byName[c.Name] = c
	}
	for _, name := range composeSortedKeys(composeParityOverrides) {
		ov := composeParityOverrides[name]
		c, ok := byName[name]
		if !ok {
			t.Errorf("override %s: no such sub-case", name)
			continue
		}
		if ov.File == "" {
			continue
		}
		if c.Source == "" || !strings.Contains(c.Content, "ComposeParity replacement") {
			t.Errorf("%s: replacement %s not applied or has no header saying why", name, ov.File)
		}
		d := composeMustLoad(t, composeInputs{WorkingDir: t.TempDir(), ProjectName: c.Project, Content: c.Content, Env: c.Env, Profiles: c.Profiles})
		if _, err := composeLocalExpand(d.Project); err != nil {
			t.Errorf("%s: local expand: %v", name, err)
		}
		if len(ov.Setup.HostFiles) > 0 && c.Env["PARITY_DIR"] != composeParityHostRoot+"/"+name {
			t.Errorf("%s: PARITY_DIR %q", name, c.Env["PARITY_DIR"])
		}
		if strings.Contains(c.Content, "${PARITY_DIR}") && len(ov.Setup.HostFiles) == 0 {
			t.Errorf("%s: uses ${PARITY_DIR} but stages no host files", name)
		}
	}
	tr, err := composeParityTar(map[string]string{"/a/b/c.txt": "x", "/a/d": "y"}, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	rd := tar.NewReader(tr)
	for {
		h, err := rd.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "a/,a/b/,a/b/c.txt,a/d" {
		t.Errorf("tar entries %v", names)
	}
}
