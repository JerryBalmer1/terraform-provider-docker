package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// actFakeExec wires the exec endpoints: the command writes stdout/stderr
// and exits with code. hang keeps the stream open until the client closes.
func actFakeExec(t *testing.T, f *swarmFakeEngine, stdout, stderr string, code int, hang bool) *client.ExecCreateOptions {
	t.Helper()
	var got client.ExecCreateOptions
	f.handle("POST /containers/web/exec", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		swarmFakeJSON(w, container.ExecCreateResponse{ID: "e1"})
	})
	f.handle("POST /exec/e1/start", func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body first: closing a socket with unread
		// data makes Windows send RST, which the client sees as an error.
		_, _ = io.Copy(io.Discard, r.Body)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(buf, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		_, _ = newTestStdWriter(buf, stdcopy.Stdout).Write([]byte(stdout))
		if stderr != "" {
			_, _ = newTestStdWriter(buf, stdcopy.Stderr).Write([]byte(stderr))
		}
		_ = buf.Flush()
		if hang {
			b := make([]byte, 1)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = conn.Read(b)
		}
	})
	f.handle("GET /exec/e1/json", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, client.ExecInspectResult{ID: "e1", Running: false, ExitCode: code})
	})
	return &got
}

func TestExecActionSuccess(t *testing.T) {
	f := swarmNewFakeEngine(t)
	got := actFakeExec(t, f, "hello\nworld\n", "warn\n", 0, false)
	res := actInvoke(t, f, NewExecAction(), map[string]tftypes.Value{
		"container":  actTfStr("web"),
		"command":    actTfList("sh", "-c", "echo hello"),
		"env":        actTfMap(map[string]string{"B": "2", "A": "1"}),
		"user":       actTfStr("nobody"),
		"workdir":    actTfStr("/tmp"),
		"privileged": actTfBool(true),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if strings.Join(got.Cmd, " ") != "sh -c echo hello" || strings.Join(got.Env, ",") != "A=1,B=2" ||
		got.User != "nobody" || got.WorkingDir != "/tmp" || !got.Privileged || !got.AttachStdout || !got.AttachStderr {
		t.Errorf("exec options: %+v", got)
	}
	for _, want := range []string{"hello", "world", "stderr: warn", "exited 0"} {
		if !actHasProgress(res, want) {
			t.Errorf("progress lacks %q: %q", want, res.Progress)
		}
	}
}

func TestExecActionNonZeroExit(t *testing.T) {
	f := swarmNewFakeEngine(t)
	actFakeExec(t, f, "step 1\n", "boom: no such file\n", 3, false)
	res := actInvoke(t, f, NewExecAction(), map[string]tftypes.Value{
		"container": actTfStr("web"),
		"command":   actTfList("false"),
	})
	txt := res.errText()
	if !strings.Contains(txt, "exited with code 3") || !strings.Contains(txt, "stderr: boom: no such file") || !strings.Contains(txt, "step 1") {
		t.Fatalf("error should carry exit code and output tail: %s", txt)
	}
}

func TestExecActionTimeout(t *testing.T) {
	f := swarmNewFakeEngine(t)
	actFakeExec(t, f, "started\n", "", 0, true)
	start := time.Now()
	res := actInvoke(t, f, NewExecAction(), map[string]tftypes.Value{
		"container": actTfStr("web"),
		"command":   actTfList("sleep", "100"),
		"timeout":   actTfStr("300ms"),
	})
	if !strings.Contains(res.errText(), "did not finish within 300ms") {
		t.Fatalf("expected timeout error, got %q", res.errText())
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("timeout not enforced: took %s", time.Since(start))
	}
}

func TestExecActionMissingContainer(t *testing.T) {
	f := swarmNewFakeEngine(t)
	f.handle("POST /containers/web/exec", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusNotFound, "No such container: web")
	})
	res := actInvoke(t, f, NewExecAction(), map[string]tftypes.Value{"container": actTfStr("web"), "command": actTfList("true")})
	if !strings.Contains(res.errText(), "No such container") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestExecActionValidate(t *testing.T) {
	d := actValidate(t, NewExecAction(), map[string]tftypes.Value{
		"container": actTfStr("web"),
		"command":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{}),
		"timeout":   actTfStr("soon"),
	})
	if d.ErrorsCount() != 2 {
		t.Fatalf("want 2 errors (empty command, bad timeout), got %v", d)
	}
}

func TestExecActionService(t *testing.T) {
	f := swarmNewFakeEngine(t)
	got := actFakeExec(t, f, "seeded\n", "", 0, false)
	var filt string
	f.handle("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		filt = r.URL.Query().Get("filters")
		swarmFakeJSON(w, []container.Summary{
			{ID: "other", Names: []string{"/shop_db.2.zzz"}},
			{ID: "web", Names: []string{"/shop_db.1.abc"}},
		})
	})
	res := actInvoke(t, f, NewExecAction(), map[string]tftypes.Value{"service": actTfStr("shop_db"), "command": actTfList("true")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if !strings.Contains(filt, "com.docker.swarm.service.name=shop_db") || !strings.Contains(filt, "running") {
		t.Errorf("container list filters: %s", filt)
	}
	if len(got.Cmd) != 1 {
		t.Errorf("exec not created on the first task by name: %+v", got)
	}
}

func TestExecActionContainerXorService(t *testing.T) {
	for name, cfg := range map[string]map[string]tftypes.Value{
		"neither": {"command": actTfList("true")},
		"both":    {"container": actTfStr("web"), "service": actTfStr("db"), "command": actTfList("true")},
	} {
		if d := actValidate(t, NewExecAction(), cfg); d.ErrorsCount() != 1 {
			t.Errorf("%s: want 1 error, got %v", name, d)
		}
	}
}
