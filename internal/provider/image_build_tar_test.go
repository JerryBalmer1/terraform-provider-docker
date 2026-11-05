package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestImageTarFirstBlockIsPlainHeader guards the BuildKit context sniff: the
// first 512-byte block must be an ordinary file header, not a PAX record
// ('x'), or the daemon reads the upload as a bare Dockerfile.
func TestImageTarFirstBlockIsPlainHeader(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(df, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Sub-second mtime is what used to force a PAX mtime record.
	mt := time.Date(2026, 1, 2, 3, 4, 5, 678900000, time.UTC)
	if err := os.Chtimes(df, mt, mt); err != nil {
		t.Fatal(err)
	}
	bc, err := imgCollectContext(dir, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := imgWriteTar(bc, &buf); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) < 512 {
		t.Fatalf("tar too short: %d bytes", len(b))
	}
	if flag := b[156]; flag == 'x' || flag == 'g' {
		t.Fatalf("first tar block is a PAX header (typeflag %q)", flag)
	}
}
