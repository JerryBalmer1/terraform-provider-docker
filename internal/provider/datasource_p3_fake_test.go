package provider

// Test helpers shared by lane E's unit tests: a route-table fake Engine API
// and builders for framework request values.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/client"
)

// p3eFake is a fake Docker Engine: handlers keyed "METHOD /path" (API version
// prefix stripped). A key ending in "*" matches any path with that prefix.
type p3eFake struct {
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	// seen records "METHOD /path?query" for every request.
	seen []string
}

var p3eVersionRe = regexp.MustCompile(`^/v[0-9.]+`)

func newP3eFake(t *testing.T) (*p3eFake, *providerData) {
	t.Helper()
	f := &p3eFake{routes: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	c, err := client.NewClientWithOpts(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	return f, &providerData{Client: c}
}

func (f *p3eFake) on(key string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[key] = h
}

// json registers a handler answering 200 with v as JSON.
func (f *p3eFake) json(key string, v any) {
	f.on(key, func(w http.ResponseWriter, _ *http.Request) { p3eWriteJSON(w, http.StatusOK, v) })
}

func (f *p3eFake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *p3eFake) serve(w http.ResponseWriter, r *http.Request) {
	p := p3eVersionRe.ReplaceAllString(r.URL.Path, "")
	key := r.Method + " " + p
	f.mu.Lock()
	f.seen = append(f.seen, key+"?"+r.URL.RawQuery)
	h, ok := f.routes[key]
	if !ok {
		best := ""
		for k, v := range f.routes {
			if strings.HasSuffix(k, "*") && strings.HasPrefix(key, strings.TrimSuffix(k, "*")) && len(k) > len(best) {
				best, h = k, v
			}
		}
		ok = best != ""
	}
	f.mu.Unlock()
	w.Header().Set("Api-Version", "1.47")
	if p == "/_ping" {
		_, _ = w.Write([]byte("OK"))
		return
	}
	if !ok {
		p3eWriteJSON(w, http.StatusNotFound, map[string]string{"message": "fake engine: no route " + key})
		return
	}
	h(w, r)
}

func p3eWriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// p3eObject builds a value of object type typ, null for every attribute not in vals.
func p3eObject(typ tftypes.Type, vals map[string]tftypes.Value) tftypes.Value {
	obj := typ.(tftypes.Object)
	all := map[string]tftypes.Value{}
	for k, at := range obj.AttributeTypes {
		if v, ok := vals[k]; ok {
			all[k] = v
		} else {
			all[k] = tftypes.NewValue(at, nil)
		}
	}
	return tftypes.NewValue(obj, all)
}

func p3eStr(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

func p3eStrMap(m map[string]string) tftypes.Value {
	vals := map[string]tftypes.Value{}
	for k, v := range m {
		vals[k] = p3eStr(v)
	}
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, vals)
}

// p3eAttrString returns the Go string of a framework string value ("" for null).
func p3eAttrString(v attr.Value) string {
	if v == nil || v.IsNull() || v.IsUnknown() {
		return ""
	}
	s, _ := v.ToTerraformValue(context.Background())
	var out string
	_ = s.As(&out)
	return out
}
