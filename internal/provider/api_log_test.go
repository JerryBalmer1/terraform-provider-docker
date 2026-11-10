package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"
	"github.com/moby/moby/client"
)

func TestAPILogPathRedaction(t *testing.T) {
	cases := []struct{ in, path, ver string }{
		{"/v1.47/containers/abc123/json", "/containers/abc123/json", "1.47"},
		{"/_ping", "/_ping", ""},
		{"/v1.47/build?t=img&buildargs=%7B%22PASSWORD%22%3A%22hunter2%22%7D&labels=%7B%7D",
			"/build?buildargs=REDACTED&labels=%7B%7D&t=img", "1.47"},
		{"/v1.56/images/create?fromImage=alpine&tag=3&x-registry-auth=abc&token=t0k",
			"/images/create?fromImage=alpine&tag=3&token=REDACTED&x-registry-auth=REDACTED", "1.56"},
		{"/v1.56/containers/abc/kill?signal=KILL", "/containers/abc/kill?signal=KILL", "1.56"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatal(err)
		}
		p, v := apiLogPath(u)
		if p != c.path || v != c.ver {
			t.Errorf("apiLogPath(%s) = %q, %q; want %q, %q", c.in, p, v, c.path, c.ver)
		}
		if strings.Contains(p, "hunter2") || strings.Contains(p, "t0k") {
			t.Errorf("secret leaked: %s", p)
		}
	}
}

func apiLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	all, err := tflogtest.MultilineJSONDecode(buf)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range all {
		if e["@message"] == "docker engine api call" {
			out = append(out, e)
		}
	}
	return out
}

func TestAPILogOneEntryPerCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/build"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"stream":"ok"}`+"\n")
		case strings.Contains(r.URL.Path, "/containers/missing/json"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such container: missing"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &buf)
	host := "tcp://" + strings.TrimPrefix(srv.URL, "http://")
	c, err := client.New(append([]client.Opt{client.WithHost(host), client.WithAPIVersion("1.47")}, apiLogOpts()...)...)
	if err != nil {
		t.Fatal(err)
	}
	secret := "hunter2"
	res, err := c.ImageBuild(ctx, strings.NewReader(""), client.ImageBuildOptions{
		Tags: []string{"img:1"}, BuildArgs: map[string]*string{"PASSWORD": &secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if _, err := c.ContainerInspect(ctx, "missing", client.ContainerInspectOptions{}); err == nil {
		t.Fatal("expected not found")
	}

	if strings.Contains(buf.String(), secret) {
		t.Fatalf("build arg value leaked into the log:\n%s", buf.String())
	}
	entries := apiLogEntries(t, &buf)
	if len(entries) != 2 {
		t.Fatalf("got %d api log entries, want 2:\n%s", len(entries), buf.String())
	}
	idRe := regexp.MustCompile(`^[0-9a-f]{16}$`)
	ids := map[any]bool{}
	for _, e := range entries {
		for _, k := range []string{"request_id", "method", "path", "status", "duration_ms", "api_version"} {
			if _, ok := e[k]; !ok {
				t.Errorf("entry lacks %s: %v", k, e)
			}
		}
		if !idRe.MatchString(e["request_id"].(string)) {
			t.Errorf("request_id %v is not 16 hex digits", e["request_id"])
		}
		ids[e["request_id"]] = true
		if e["api_version"] != "1.47" {
			t.Errorf("api_version = %v", e["api_version"])
		}
	}
	if len(ids) != 2 {
		t.Errorf("request ids are not per call: %v", ids)
	}
	b := entries[0]
	if b["method"] != "POST" || b["status"].(float64) != 200 || !strings.HasPrefix(b["path"].(string), "/build?") ||
		!strings.Contains(b["path"].(string), "buildargs=REDACTED") {
		t.Errorf("build entry = %v", b)
	}
	i := entries[1]
	if i["method"] != "GET" || i["status"].(float64) != 404 || i["path"] != "/containers/missing/json" {
		t.Errorf("inspect entry = %v", i)
	}
}

func TestAPILogDialError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing listens there now

	var buf bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &buf)
	c, err := client.New(append([]client.Opt{client.WithHost("tcp://" + addr), client.WithAPIVersion("1.47")}, apiLogOpts()...)...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContainerInspect(ctx, "abc", client.ContainerInspectOptions{}); err == nil {
		t.Fatal("expected a dial error")
	}
	entries := apiLogEntries(t, &buf)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1:\n%s", len(entries), buf.String())
	}
	if entries[0]["status"].(float64) != 0 || entries[0]["error"] == nil || entries[0]["path"] != "/containers/abc/json" {
		t.Errorf("dial-error entry = %v", entries[0])
	}
}
