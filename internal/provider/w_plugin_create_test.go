package provider

// Pass 5.2 Lane W: the tar modes docker_plugin_create (objTarDir) sends to
// POST /plugins/create. On a Windows host tar.FileInfoHeader reports 0666
// for regular files and 0777 for directories (Windows has no executable
// bit), so without the Pass 5.1 chmod the plugin's entrypoint cannot be
// executed and the plugin cannot be enabled. wPreFixTarDir is objTarDir as it
// was before that fix, kept side by side for the reproduction (unit test
// here, Engine round trip in w_plugin_create_acc_test.go).

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// wPreFixTarDir is objTarDir without the runtime.GOOS == "windows" chmod
// block (obj_plugin_actions.go before Pass 5.1 Lane Z).
func wPreFixTarDir(dir string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// wChmodTarEntry is the docker CLI's rule, verbatim from
// github.com/moby/go-archive v0.3.3 archive_windows.go (the version docker/cli
// v29.9.3 vendors; `docker plugin create` tars the context with
// archive.TarWithOptions, whose FileInfoHeader applies it to every entry).
// On unix chmodTarEntry is the identity.
func wChmodTarEntry(mode int64) int64 {
	// Remove group- and world-writable bits.
	mode &= 0o755

	// Add the x bit: make everything +x on Windows
	return mode | 0o111
}

// wLaneZFix is the expression objTarDir applies on Windows (Pass 5.1 Lane Z).
func wLaneZFix(mode int64) int64 { return (mode &^ 0o777) | ((mode & 0o755) | 0o111) }

// wPluginDir writes a plugin directory: config.json, rootfs/ with an
// entrypoint binary, nested directories and, when the host allows it, a
// relative symlink. It returns the dir and whether the symlink exists.
func wPluginDir(t *testing.T) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"entrypoint":["/tfaccplugin"]}`), 0o644))
	for _, d := range []string{"rootfs/run/docker/plugins", "rootfs/etc", "rootfs/sub/deeper"} {
		must(os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755))
	}
	ep := filepath.Join(dir, "rootfs", "tfaccplugin")
	must(os.WriteFile(ep, []byte("\x7fELF not really"), 0o755))
	must(os.Chmod(ep, 0o755)) // umask-proof on unix; a no-op for the x bit on Windows
	must(os.WriteFile(filepath.Join(dir, "rootfs", "etc", "hosts"), []byte("127.0.0.1 localhost\n"), 0o644))
	symlink := os.Symlink("tfaccplugin", filepath.Join(dir, "rootfs", "entry")) == nil
	return dir, symlink
}

// wTarHeaders reads every header of a tar stream, keyed by name.
func wTarHeaders(t *testing.T, buf *bytes.Buffer) map[string]*tar.Header {
	t.Helper()
	out := map[string]*tar.Header{}
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = h
	}
}

// TestWObjTarDirModes: the entrypoint is executable, nothing is group- or
// world-writable, directories are traversable, types and the symlink target
// survive, and on Windows every entry's mode is exactly the docker CLI's
// chmodTarEntry of the raw tar.FileInfoHeader mode.
func TestWObjTarDirModes(t *testing.T) {
	dir, symlink := wPluginDir(t)
	buf, err := objTarDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := wPreFixTarDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, raw := wTarHeaders(t, buf), wTarHeaders(t, pre)
	if len(got) != len(raw) {
		t.Fatalf("entry count: objTarDir %d, pre-fix %d", len(got), len(raw))
	}
	for _, d := range []string{"rootfs/", "rootfs/run/", "rootfs/run/docker/", "rootfs/run/docker/plugins/", "rootfs/etc/", "rootfs/sub/", "rootfs/sub/deeper/"} {
		h := got[d]
		if h == nil {
			t.Fatalf("no %s entry", d)
		}
		if h.Typeflag != tar.TypeDir || h.Mode&0o111 != 0o111 || h.Mode&0o022 != 0 {
			t.Errorf("%s: type %q mode %#o, want a dir with 0o111 set and 0o022 clear", d, h.Typeflag, h.Mode)
		}
	}
	ep := got["rootfs/tfaccplugin"]
	if ep == nil || ep.Typeflag != tar.TypeReg {
		t.Fatalf("entrypoint entry: %+v", ep)
	}
	if ep.Mode&0o111 != 0o111 || ep.Mode&0o022 != 0 {
		t.Errorf("entrypoint mode %#o: want executable by all and not group/world writable", ep.Mode)
	}
	if h := got["config.json"]; h == nil || h.Typeflag != tar.TypeReg || h.Mode&0o022 != 0 {
		t.Errorf("config.json entry: %+v", h)
	}
	if symlink {
		h := got["rootfs/entry"]
		if h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != "tfaccplugin" || h.Size != 0 {
			t.Errorf("symlink entry: %+v", h)
		}
	} else {
		t.Log("os.Symlink not permitted on this host (Windows without Developer Mode); symlink entry not checked")
	}
	for name, h := range got {
		if h.Mode&^0o7777 != 0 {
			t.Errorf("%s: mode %#o carries bits above 0o7777", name, h.Mode)
		}
		r := raw[name]
		if r == nil || r.Typeflag != h.Typeflag || r.Size != h.Size || r.Linkname != h.Linkname {
			t.Errorf("%s: objTarDir and the pre-fix builder differ beyond the mode: %+v vs %+v", name, h, r)
			continue
		}
		if runtime.GOOS == "windows" {
			if want := wChmodTarEntry(r.Mode); h.Mode != want {
				t.Errorf("%s: mode %#o, docker CLI chmodTarEntry(%#o) = %#o", name, h.Mode, r.Mode, want)
			}
		} else if h.Mode != r.Mode {
			t.Errorf("%s: objTarDir changed the mode on %s: %#o -> %#o", name, runtime.GOOS, r.Mode, h.Mode)
		}
	}
	if runtime.GOOS != "windows" {
		return
	}
	// The reproduction: what the pre-fix builder sent from Windows.
	if m := raw["rootfs/tfaccplugin"].Mode; m != 0o666 {
		t.Errorf("pre-fix entrypoint mode %#o, want 0o666 (no x bit on Windows)", m)
	}
	if m := raw["rootfs/"].Mode; m != 0o777 {
		t.Errorf("pre-fix rootfs/ mode %#o, want 0o777", m)
	}
	t.Logf("Windows: entrypoint pre-fix %#o -> objTarDir %#o; rootfs/ pre-fix %#o -> %#o",
		raw["rootfs/tfaccplugin"].Mode, ep.Mode, raw["rootfs/"].Mode, got["rootfs/"].Mode)
}

// TestWObjTarDirReadOnlyFile: a read-only file on Windows (0444) still gets
// the x bit (0555), like the docker CLI.
func TestWObjTarDirReadOnlyFile(t *testing.T) {
	dir, _ := wPluginDir(t)
	p := filepath.Join(dir, "rootfs", "ro")
	if err := os.WriteFile(p, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	buf, err := objTarDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := wTarHeaders(t, buf)["rootfs/ro"]
	want := int64(0o444)
	if runtime.GOOS == "windows" {
		want = 0o555
	}
	if h == nil || h.Mode != want {
		t.Fatalf("rootfs/ro: %+v, want mode %#o", h, want)
	}
}

// TestWLaneZFixVsChmodTarEntry compares the Lane Z expression with the docker
// CLI rule over every permission + setuid/setgid/sticky combination. They
// agree on the nine permission bits everywhere; they differ only above 0o777
// (Lane Z keeps setuid/setgid/sticky, chmodTarEntry drops them), which
// tar.FileInfoHeader never sets on Windows (no such bits in a Windows
// os.FileMode), so the two are equivalent for every header objTarDir builds.
// Type bits are never in hdr.Mode (archive/tar keeps them in Typeflag).
func TestWLaneZFixVsChmodTarEntry(t *testing.T) {
	high := 0
	for m := int64(0); m <= 0o7777; m++ {
		a, b := wLaneZFix(m), wChmodTarEntry(m)
		if a&0o777 != b&0o777 {
			t.Fatalf("%#o: permission bits differ: lane Z %#o, chmodTarEntry %#o", m, a, b)
		}
		if a != b {
			high++
			if a&^0o777 != m&^0o777 {
				t.Fatalf("%#o: lane Z changed bits above 0o777: %#o", m, a)
			}
		}
	}
	if high != 7*512 {
		t.Fatalf("expected differences only where setuid/setgid/sticky are set (%d), got %d", 7*512, high)
	}
}
