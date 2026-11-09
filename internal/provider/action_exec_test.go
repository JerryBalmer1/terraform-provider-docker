package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// actFakeExec wires inspect of container "web" and the exec endpoints into
// f: the command writes stdout/stderr and exits with code. hang keeps the
// stream open until the client closes.
func actFakeExec(t *testing.T, f *p3eFake, stdout, stderr string, code int, hang bool) *client.ExecCreateOptions {
	t.Helper()
	var got client.ExecCreateOptions
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /containers/web/json", container.InspectResponse{ID: "web", Name: "/web"})
	f.on("POST /containers/web/exec", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		p3eWriteJSON(w, http.StatusCreated, container.ExecCreateResponse{ID: "e1"})
	})
	f.on("POST /exec/e1/start", func(w http.ResponseWriter, r *http.Request) {
		// Drain the request body first: closing a socket with unread
		// data makes Windows send RST, which the client sees as an error.
		_, _ = io.Copy(io.Discard, r.Body)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if tc, ok := conn.(*net.TCPConn); ok && !hang {
				_ = tc.CloseWrite()
				_ = tc.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _ = io.Copy(io.Discard, tc)
			}
			conn.Close()
		}()
		fmt.Fprint(buf, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
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
	f.json("GET /exec/e1/json", map[string]any{
		"ID": "e1", "ContainerID": "web", "Running": false, "ExitCode": code, "Pid": 42, "OpenStdout": true, "OpenStderr": true,
		"ProcessConfig": map[string]any{"entrypoint": "sh", "arguments": []string{"-c", "x"}, "privileged": false, "tty": false, "user": ""},
	})
	return &got
}

func TestExecActionSuccess(t *testing.T) {
	f, pd := newP3eFake(t)
	got := actFakeExec(t, f, "hello\nworld\n", "warn\n", 0, false)
	envFile := filepath.Join(t.TempDir(), "app.env")
	if err := os.WriteFile(envFile, []byte("# comment\nA=from-file\nC=3\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "out", "exec.json")
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("sh", "-c", "echo hello"),
		"env":          actTfMap(map[string]string{"B": "2", "A": "1"}),
		"env_file":     actTfList(envFile),
		"user":         actTfStr("nobody"),
		"workdir":      actTfStr("/tmp"),
		"privileged":   actTfBool(true),
		"result_path":  actTfStr(resultPath),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if strings.Join(got.Cmd, " ") != "sh -c echo hello" || strings.Join(got.Env, ",") != "A=1,B=2,C=3" ||
		got.User != "nobody" || got.WorkingDir != "/tmp" || !got.Privileged || !got.AttachStdout || !got.AttachStderr {
		t.Errorf("exec options: %+v", got)
	}
	for _, want := range []string{"hello", "world", "stderr: warn", "exited 0", `"exit_code":0`} {
		if !actHasProgress(res, want) {
			t.Errorf("progress lacks %q: %q", want, res.Progress)
		}
	}
	b, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	r, _ := out["result"].(map[string]any)
	if out["stdout"] != "hello\nworld\n" || out["stderr"] != "warn\n" || out["exec_id"] != "e1" || out["container_id"] != "web" ||
		len(fmt.Sprint(out["stdout_sha256"])) != 64 || len(fmt.Sprint(out["stderr_sha256"])) != 64 || r == nil || r["pid"] != float64(42) {
		t.Errorf("result file = %s", b)
	}
}

func TestExecActionNonZeroExit(t *testing.T) {
	f, pd := newP3eFake(t)
	actFakeExec(t, f, "step 1\n", "boom: no such file\n", 3, false)
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("false"),
	})
	txt := res.errText()
	if !strings.Contains(txt, "exited with code 3") || !strings.Contains(txt, "stderr: boom: no such file") || !strings.Contains(txt, "step 1") {
		t.Fatalf("error should carry exit code and output tail: %s", txt)
	}
}

func TestExecActionTimeout(t *testing.T) {
	f, pd := newP3eFake(t)
	actFakeExec(t, f, "started\n", "", 0, true)
	start := time.Now()
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("sleep", "100"),
		"timeout":      actTfStr("300ms"),
	})
	if !strings.Contains(res.errText(), "did not finish within 300ms") {
		t.Fatalf("expected timeout error, got %q", res.errText())
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("timeout not enforced: took %s", time.Since(start))
	}
}

func TestExecActionMissingContainer(t *testing.T) {
	f, pd := newP3eFake(t)
	f.on("GET /containers/web/json", func(w http.ResponseWriter, r *http.Request) {
		p3eWriteJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: web"})
	})
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{"container_id": actTfStr("web"), "command": actTfList("true")})
	if !strings.Contains(res.errText(), "No such container") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestExecActionDetach(t *testing.T) {
	f, pd := newP3eFake(t)
	got := actFakeExec(t, f, "", "", 0, false)
	started := false
	f.on("POST /exec/e1/start", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		started = body["Detach"] == true
		w.WriteHeader(http.StatusOK)
	})
	f.json("GET /exec/e1/json", map[string]any{"ID": "e1", "ContainerID": "web", "Running": true, "ExitCode": 0, "Pid": 7})
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("sleep", "60"),
		"detach":       actTfBool(true),
		"tty":          actTfBool(true),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if !started || got.AttachStdout || !got.TTY {
		t.Errorf("detached start=%v options=%+v", started, got)
	}
	if !actHasProgress(res, "started detached") {
		t.Errorf("progress = %q", res.Progress)
	}
}

func TestExecActionBadEnvFile(t *testing.T) {
	f, pd := newP3eFake(t)
	actFakeExec(t, f, "", "", 0, false)
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("true"),
		"env_file":     actTfList(filepath.Join(t.TempDir(), "missing.env")),
	})
	if !strings.Contains(res.errText(), "Cannot read env_file") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestExecActionValidate(t *testing.T) {
	d := actValidate(t, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{}),
		"timeout":      actTfStr("soon"),
	})
	if d.ErrorsCount() != 2 {
		t.Fatalf("want 2 errors (empty command, bad timeout), got %v", d)
	}
}

func TestExecActionService(t *testing.T) {
	f, pd := newP3eFake(t)
	got := actFakeExec(t, f, "seeded\n", "", 0, false)
	var filt string
	f.on("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		filt = r.URL.Query().Get("filters")
		p3eWriteJSON(w, http.StatusOK, []container.Summary{
			{ID: "other", Names: []string{"/shop_db.2.zzz"}},
			{ID: "web", Names: []string{"/shop_db.1.abc"}},
		})
	})
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{"service_id": actTfStr("shop_db"), "command": actTfList("true")})
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
		"both":    {"container_id": actTfStr("web"), "service_id": actTfStr("db"), "command": actTfList("true")},
	} {
		if d := actValidate(t, NewExecAction(), cfg); d.ErrorsCount() != 1 {
			t.Errorf("%s: want 1 error, got %v", name, d)
		}
	}
}

func TestCopReadEnvFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.env")
	t.Setenv("COP_FROM_ENV", "e")
	if err := os.WriteFile(p, []byte("A=1\r\n  # c\nCOP_FROM_ENV\nCOP_UNSET_VAR_XYZ\nB=x=y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := copReadEnvFiles([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "A=1,COP_FROM_ENV=e,B=x=y" {
		t.Errorf("got %q", got)
	}
	if err := os.WriteFile(p, []byte("BAD KEY=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copReadEnvFiles([]string{p}); err == nil {
		t.Error("expected an error for a key with a space")
	}
	if strings.Join(copMergeEnv([]string{"A=1", "B=2"}, map[string]string{"A": "9"}), ",") != "A=9,B=2" {
		t.Error("copMergeEnv: the map should win")
	}
}
