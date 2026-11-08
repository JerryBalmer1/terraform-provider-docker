package provider

// Test helpers for the github.com/moby/moby client and api modules: the
// pieces of the old github.com/docker/docker packages (stdcopy.NewStdWriter,
// filters.FromJSON / Args.ExactMatch / Args.Get) that fake Engines and
// assertions used and that the new modules no longer export.

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/netip"
	"sort"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// testStdWriter frames writes as one multiplexed stdout/stderr stream, the
// format the daemon uses for non-TTY attach, exec and logs.
type testStdWriter struct {
	w  io.Writer
	fd stdcopy.StdType
}

func newTestStdWriter(w io.Writer, fd stdcopy.StdType) io.Writer {
	return &testStdWriter{w: w, fd: fd}
}

func (s *testStdWriter) Write(p []byte) (int, error) {
	hdr := make([]byte, 8)
	hdr[0] = byte(s.fd)
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(p)))
	if _, err := s.w.Write(append(hdr, p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// testParseFilters decodes a "filters" query parameter the way the daemon
// does ("" is no filters).
func testParseFilters(s string) (client.Filters, error) {
	f := client.Filters{}
	if s == "" {
		return f, nil
	}
	err := json.Unmarshal([]byte(s), &f)
	return f, err
}

// filterExactMatch is the old filters.Args.ExactMatch: true when key has no
// values, or v is one of them.
func filterExactMatch(f client.Filters, key, v string) bool {
	vals, ok := f[key]
	if !ok || len(vals) == 0 {
		return true
	}
	return vals[v]
}

// filterGet is the old filters.Args.Get: the values of key, sorted.
func filterGet(f client.Filters, key string) []string {
	out := make([]string, 0, len(f[key]))
	for v := range f[key] {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
