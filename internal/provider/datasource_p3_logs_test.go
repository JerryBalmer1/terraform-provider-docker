package provider

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
)

func p3eLogsFake(t *testing.T, tty bool) (*p3eFake, *providerData) {
	f, pd := newP3eFake(t)
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /containers/web/json", container.InspectResponse{
		ID:     "cid1",
		Name:   "/web",
		Config: &container.Config{Tty: tty},
	})
	f.on("GET /containers/cid1/logs", func(w http.ResponseWriter, _ *http.Request) {
		if tty {
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			_, _ = w.Write([]byte("2026-01-02T03:04:05.000000001Z tty one\r\n2026-01-02T03:04:06.000000001Z tty two\r\n"))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = newTestStdWriter(w, stdcopy.Stdout).Write([]byte("2026-01-02T03:04:05.000000001Z out one\n"))
		_, _ = newTestStdWriter(w, stdcopy.Stderr).Write([]byte("2026-01-02T03:04:06.000000001Z err one\n"))
		_, _ = newTestStdWriter(w, stdcopy.Stdout).Write([]byte("2026-01-02T03:04:07.000000001Z out two\n"))
	})
	return f, pd
}

// p3eLogLines returns "<stream>:<message>" per log entry.
func p3eLogLines(t *testing.T, cfg map[string]tftypes.Value, pd *providerData) []string {
	t.Helper()
	state, diags := p3eReadDS(t, NewContainerLogsDataSource(), pd, cfg)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var entries []struct {
		Timestamp string `tfsdk:"timestamp"`
		Stream    string `tfsdk:"stream"`
		Source    string `tfsdk:"source"`
		Message   string `tfsdk:"message"`
	}
	if d := state.GetAttribute(context.Background(), path.Root("log_entries"), &entries); d.HasError() {
		t.Fatal(d)
	}
	lines := []string{}
	for _, e := range entries {
		if e.Source != "web" || e.Timestamp == "" {
			t.Errorf("entry %+v: want source web and a timestamp", e)
		}
		lines = append(lines, e.Stream+":"+e.Message)
	}
	return lines
}

func TestP3LogsDemux(t *testing.T) {
	f, pd := p3eLogsFake(t, false)
	got := p3eLogLines(t, map[string]tftypes.Value{
		"container": p3eStr("web"),
		"tail":      tftypes.NewValue(tftypes.Number, 50),
		"since":     p3eStr("10m"),
		"stderr":    tftypes.NewValue(tftypes.Bool, true),
	}, pd)
	if want := []string{"stdout:out one", "stderr:err one", "stdout:out two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	var q string
	for _, s := range f.requests() {
		if strings.HasPrefix(s, "GET /containers/cid1/logs?") {
			q = s
		}
	}
	for _, want := range []string{"tail=50", "since=", "timestamps=1", "stdout=1", "stderr=1"} {
		if !strings.Contains(q, want) {
			t.Errorf("logs query %q lacks %s", q, want)
		}
	}
}

func TestP3LogsTTY(t *testing.T) {
	_, pd := p3eLogsFake(t, true)
	got := p3eLogLines(t, map[string]tftypes.Value{"container": p3eStr("web")}, pd)
	if want := []string{"combined:tty one", "combined:tty two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

func TestP3LogsNoStream(t *testing.T) {
	_, pd := p3eLogsFake(t, false)
	_, diags := p3eReadDS(t, NewContainerLogsDataSource(), pd, map[string]tftypes.Value{
		"container": p3eStr("web"),
		"stdout":    tftypes.NewValue(tftypes.Bool, false),
		"stderr":    tftypes.NewValue(tftypes.Bool, false),
	})
	if !diags.HasError() {
		t.Fatal("expected an error with both streams off")
	}
}

func TestCopLogsTailAndRaw(t *testing.T) {
	f, pd := p3eLogsFake(t, false)
	state, diags := p3eReadDS(t, NewContainerLogsDataSource(), pd, map[string]tftypes.Value{
		"container": p3eStr("web"),
		"tail":      tftypes.NewValue(tftypes.Number, 2),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var logs, id, sum, ver string
	state.GetAttribute(context.Background(), path.Root("logs"), &logs)
	state.GetAttribute(context.Background(), path.Root("container_id"), &id)
	state.GetAttribute(context.Background(), path.Root("content_sha256"), &sum)
	state.GetAttribute(context.Background(), path.Root("docker_server_version"), &ver)
	if id != "cid1" || len(sum) != 64 || ver != "29.0.0" {
		t.Errorf("container_id=%q content_sha256=%q docker_server_version=%q", id, sum, ver)
	}
	if strings.Contains(logs, "out one") || !strings.Contains(logs, "err one") || !strings.Contains(logs, "out two") {
		t.Errorf("tail 2 kept the wrong lines: %q", logs)
	}
	for _, s := range f.requests() {
		if strings.HasPrefix(s, "GET /containers/cid1/logs?") && !strings.Contains(s, "tail=2") {
			t.Errorf("query %q lacks tail=2", s)
		}
	}
}

func TestCopLogsBadUntil(t *testing.T) {
	_, pd := p3eLogsFake(t, false)
	_, diags := p3eReadDS(t, NewContainerLogsDataSource(), pd, map[string]tftypes.Value{
		"container": p3eStr("web"),
		"until":     p3eStr("yesterday"),
	})
	if !diags.HasError() {
		t.Fatal("expected an error for until = yesterday")
	}
}

func TestCopLogsMissingContainer(t *testing.T) {
	_, pd := p3eLogsFake(t, false)
	_, diags := p3eReadDS(t, NewContainerLogsDataSource(), pd, map[string]tftypes.Value{"container": p3eStr("nope")})
	if !diags.HasError() {
		t.Fatal("expected an error for a missing container")
	}
}

func TestAccP3LogsDataSource(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_container" "logs" {
  name    = "tfacc-logs-ds"
  image   = "busybox:1.36"
  command = ["sh", "-c", "echo hello-logs; echo oops >&2; sleep 3600"]
}

data "docker_container_logs" "l" {
  container  = docker_container.logs.id
  depends_on = [docker_container.logs]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckTypeSetElemNestedAttrs("data.docker_container_logs.l", "log_entries.*", map[string]string{"stream": "stdout", "message": "hello-logs"}),
				resource.TestCheckTypeSetElemNestedAttrs("data.docker_container_logs.l", "log_entries.*", map[string]string{"stream": "stderr", "message": "oops"}),
				resource.TestCheckResourceAttrSet("data.docker_container_logs.l", "content_sha256"),
			),
		}},
	})
}
