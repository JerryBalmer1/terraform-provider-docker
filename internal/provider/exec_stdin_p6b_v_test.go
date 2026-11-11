package provider

// Pass 6b Lane V: the stdin input of docker_exec and docker_exec_output
// (ContainerExec AttachStdin, hijacked attach, write, CloseWrite).

import (
	"context"
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
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// execStdinFake wires container "web" and one exec whose process behaves
// like `cat`: the start handler reads the hijacked stream until the client
// closes its write side, then echoes everything it read on stdout. eof
// reports whether the read ended with end of file (CloseWrite) rather than
// a deadline.
type execStdinFake struct {
	got  client.ExecCreateOptions
	read chan string
	eof  chan bool
}

func newExecStdinFake(t *testing.T, f *p3eFake) *execStdinFake {
	t.Helper()
	x := &execStdinFake{read: make(chan string, 1), eof: make(chan bool, 1)}
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /containers/web/json", container.InspectResponse{ID: "web", Name: "/web"})
	f.on("POST /containers/web/exec", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&x.got); err != nil {
			t.Error(err)
		}
		p3eWriteJSON(w, http.StatusCreated, container.ExecCreateResponse{ID: "e1"})
	})
	f.on("POST /exec/e1/start", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(buf, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		_ = buf.Flush()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		in, err := io.ReadAll(buf.Reader)
		x.eof <- err == nil
		x.read <- string(in)
		_, _ = newTestStdWriter(buf, stdcopy.Stdout).Write([]byte("echo:" + string(in)))
		_ = buf.Flush()
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	})
	f.json("GET /exec/e1/json", map[string]any{"ID": "e1", "ContainerID": "web", "Running": false, "ExitCode": 0, "OpenStdin": true})
	return x
}

func (x *execStdinFake) check(t *testing.T, want string) {
	t.Helper()
	if !x.got.AttachStdin || !x.got.AttachStdout || !x.got.AttachStderr {
		t.Errorf("exec create: AttachStdin=%v AttachStdout=%v AttachStderr=%v, want all true", x.got.AttachStdin, x.got.AttachStdout, x.got.AttachStderr)
	}
	select {
	case eof := <-x.eof:
		if !eof {
			t.Error("the provider did not close stdin: the fake read until its deadline instead of end of file")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exec start was never called")
	}
	if got := <-x.read; got != want {
		t.Errorf("stdin bytes = %q, want %q", got, want)
	}
}

func TestExecActionStdinP6bV(t *testing.T) {
	f, pd := newP3eFake(t)
	x := newExecStdinFake(t, f)
	resultPath := filepath.Join(t.TempDir(), "exec.json")
	in := "line one\nline two\n"
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("cat"),
		"stdin":        actTfStr(in),
		"result_path":  actTfStr(resultPath),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	x.check(t, in)
	b, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["stdout"] != "echo:"+in {
		t.Errorf("stdout = %q", out["stdout"])
	}
	if strings.Contains(string(b), `"stdin"`) {
		t.Errorf("result file carries stdin: %s", b)
	}
}

// An empty stdin still attaches and closes it at once (the command sees
// end of file immediately rather than no stdin at all).
func TestExecActionEmptyStdinP6bV(t *testing.T) {
	f, pd := newP3eFake(t)
	x := newExecStdinFake(t, f)
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("cat"),
		"stdin":        actTfStr(""),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	x.check(t, "")
}

func TestExecActionStdinValidateP6bV(t *testing.T) {
	for name, extra := range map[string]map[string]tftypes.Value{
		"tty":    {"tty": actTfBool(true)},
		"detach": {"detach": actTfBool(true)},
	} {
		vals := map[string]tftypes.Value{
			"container_id": actTfStr("web"),
			"command":      actTfList("cat"),
			"stdin":        actTfStr("x"),
		}
		for k, v := range extra {
			vals[k] = v
		}
		d := actValidate(t, NewExecAction(), vals)
		if d.ErrorsCount() != 1 || !strings.Contains(d.Errors()[0].Summary(), "stdin") {
			t.Errorf("%s: want one stdin error, got %v", name, d)
		}
	}
}

func TestExecOutputEphemeralStdinP6bV(t *testing.T) {
	f, pd := newP3eFake(t)
	x := newExecStdinFake(t, f)
	res, diags := p3eOpenEphemeral(t, NewExecOutputEphemeralResource(), pd, map[string]tftypes.Value{
		"container_id": p3eStr("web"),
		"command":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("cat")}),
		"stdin":        p3eStr("s3cret"),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	x.check(t, "s3cret")
	var m execOutputModel
	res.Get(context.Background(), &m)
	if m.Stdout.ValueString() != "echo:s3cret" {
		t.Errorf("stdout = %q", m.Stdout.ValueString())
	}
}

// Without stdin nothing is attached (unchanged behaviour).
func TestExecOutputEphemeralNoStdinP6bV(t *testing.T) {
	f, pd := newP3eFake(t)
	var got client.ExecCreateOptions
	p3eFakeExec(f, "web", "x", "", 0, &got)
	_, diags := p3eOpenEphemeral(t, NewExecOutputEphemeralResource(), pd, map[string]tftypes.Value{
		"container_id": p3eStr("web"),
		"command":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("true")}),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if got.AttachStdin {
		t.Error("AttachStdin set without stdin")
	}
}

// TestAccExecStdinP6bV pipes stdin into `sh -c 'read x; ...'` and `cat`
// through the action (result_path) and the ephemeral resource.
func TestAccExecStdinP6bV(t *testing.T) {
	testAccPreCheck(t)
	name := acctest.RandomWithPrefix("tfacc-exec-stdin")
	resultPath := filepath.Join(t.TempDir(), "exec.json")
	cfg := actAccContainer(name, fmt.Sprintf(`
action "docker_exec" "stdin" {
  config {
    container_id = docker_container.c.name
    command      = ["sh", "-c", "read first; echo got-$first; cat"]
    stdin        = "hello\nrest of input\n"
    result_path  = %q
  }
}

resource "terraform_data" "stdin" {
  input = docker_container.c.id
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_exec.stdin]
    }
  }
}
`, filepath.ToSlash(resultPath)))
	hAccTest(t, resource.TestCase{
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_14_0)},
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: func(*terraform.State) error {
				b, err := os.ReadFile(resultPath)
				if err != nil {
					return fmt.Errorf("result_path not written: %w", err)
				}
				var out map[string]any
				if err := json.Unmarshal(b, &out); err != nil {
					return err
				}
				if out["stdout"] != "got-hello\nrest of input\n" {
					return fmt.Errorf("stdout = %q, want the first line read by sh and the rest echoed by cat", out["stdout"])
				}
				return nil
			},
		}},
	})
}

func TestAccExecOutputEphemeralStdinP6bV(t *testing.T) {
	testAccPreCheck(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccEphemeralFactories(),
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{{
			Config: `
resource "docker_container" "exec" {
  name    = "tfacc-exec-output-stdin"
  image   = "busybox:1.36"
  command = ["sh", "-c", "sleep 3600"]
}

ephemeral "docker_exec_output" "upper" {
  container_id = docker_container.exec.id
  command      = ["tr", "a-z", "A-Z"]
  stdin        = "piped s3cret"
}

provider "echo" {
  data = ephemeral.docker_exec_output.upper.stdout
}

resource "echo" "upper" {}
`,
			Check: resource.TestCheckResourceAttr("echo.upper", "data", "PIPED S3CRET"),
		}},
	})
}
