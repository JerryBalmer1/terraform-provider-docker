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
	f.json("GET /containers/web/json", container.InspectResponse{
		ID:     "cid1",
		Config: &container.Config{Tty: tty},
	})
	f.on("GET /containers/cid1/logs", func(w http.ResponseWriter, _ *http.Request) {
		if tty {
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			_, _ = w.Write([]byte("tty one\r\ntty two\r\n"))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = newTestStdWriter(w, stdcopy.Stdout).Write([]byte("out one\n"))
		_, _ = newTestStdWriter(w, stdcopy.Stderr).Write([]byte("err one\n"))
		_, _ = newTestStdWriter(w, stdcopy.Stdout).Write([]byte("out two\n"))
	})
	return f, pd
}

func p3eLogLines(t *testing.T, cfg map[string]tftypes.Value, pd *providerData) []string {
	t.Helper()
	state, diags := p3eReadDS(t, NewP3LogsDataSource(), pd, cfg)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var lines []string
	state.GetAttribute(context.Background(), path.Root("logs_list_string"), &lines)
	return lines
}

func TestP3LogsDemux(t *testing.T) {
	f, pd := p3eLogsFake(t, false)
	got := p3eLogLines(t, map[string]tftypes.Value{
		"container":   p3eStr("web"),
		"tail":        p3eStr("50"),
		"since":       p3eStr("10m"),
		"timestamps":  tftypes.NewValue(tftypes.Bool, true),
		"show_stderr": tftypes.NewValue(tftypes.Bool, true),
	}, pd)
	if want := []string{"out one", "err one", "out two"}; !reflect.DeepEqual(got, want) {
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
	if want := []string{"tty one", "tty two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

func TestP3LogsNoStream(t *testing.T) {
	_, pd := p3eLogsFake(t, false)
	_, diags := p3eReadDS(t, NewP3LogsDataSource(), pd, map[string]tftypes.Value{
		"container":   p3eStr("web"),
		"show_stdout": tftypes.NewValue(tftypes.Bool, false),
		"show_stderr": tftypes.NewValue(tftypes.Bool, false),
	})
	if !diags.HasError() {
		t.Fatal("expected an error with both streams off")
	}
}

func TestP3SplitLines(t *testing.T) {
	for in, want := range map[string][]string{
		"":         {},
		"a":        {"a"},
		"a\nb\n":   {"a", "b"},
		"a\r\n\nb": {"a", "", "b"},
	} {
		if got := p3SplitLines(in); !reflect.DeepEqual(got, want) {
			t.Errorf("p3SplitLines(%q) = %q, want %q", in, got, want)
		}
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

data "docker_logs" "l" {
  container  = docker_container.logs.id
  depends_on = [docker_container.logs]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckTypeSetElemAttr("data.docker_logs.l", "logs_list_string.*", "hello-logs"),
				resource.TestCheckTypeSetElemAttr("data.docker_logs.l", "logs_list_string.*", "oops"),
			),
		}},
	})
}
