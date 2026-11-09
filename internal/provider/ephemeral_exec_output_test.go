package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// p3eFakeExec wires create/start/inspect of one exec into f. start hijacks
// the connection and writes stdout/stderr multiplexed like the daemon.
func p3eFakeExec(f *p3eFake, ctr, stdout, stderr string, exitCode int, got *client.ExecCreateOptions) {
	f.json("GET /version", map[string]string{"Version": "29.0.0", "ApiVersion": "1.47"})
	f.json("GET /containers/"+ctr+"/json", container.InspectResponse{ID: ctr, Name: "/" + ctr})
	f.on("POST /containers/"+ctr+"/exec", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(got)
		p3eWriteJSON(w, http.StatusCreated, container.ExecCreateResponse{ID: "ex1"})
	})
	f.on("POST /exec/ex1/start", func(w http.ResponseWriter, r *http.Request) {
		// Drain the request first: closing a socket with unread input makes
		// Windows reset the connection before the client reads the output.
		_, _ = io.Copy(io.Discard, r.Body)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() {
			if tc, ok := conn.(*net.TCPConn); ok {
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
	})
	f.json("GET /exec/ex1/json", client.ExecInspectResult{ID: "ex1", Running: false, ExitCode: exitCode})
}

func TestExecOutputEphemeralOpen(t *testing.T) {
	f, pd := newP3eFake(t)
	var got client.ExecCreateOptions
	p3eFakeExec(f, "web", "tok-123\n", "warn: x\n", 0, &got)
	res, diags := p3eOpenEphemeral(t, NewExecOutputEphemeralResource(), pd, map[string]tftypes.Value{
		"container_id": p3eStr("web"),
		"command":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("cat"), p3eStr("/token")}),
		"user":         p3eStr("1000"),
		"env":          p3eStrMap(map[string]string{"B": "2", "A": "1"}),
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	var m execOutputModel
	res.Get(context.Background(), &m)
	if m.Stdout.ValueString() != "tok-123\n" || m.Stderr.ValueString() != "warn: x\n" || m.ExitCode.ValueInt64() != 0 {
		t.Errorf("got stdout=%q stderr=%q code=%d", m.Stdout.ValueString(), m.Stderr.ValueString(), m.ExitCode.ValueInt64())
	}
	if strings.Join(got.Cmd, " ") != "cat /token" || got.User != "1000" || strings.Join(got.Env, ",") != "A=1,B=2" || !got.AttachStdout || !got.AttachStderr {
		t.Errorf("exec options = %+v", got)
	}
}

func TestExecOutputEphemeralNonZeroExit(t *testing.T) {
	f, pd := newP3eFake(t)
	var got client.ExecCreateOptions
	p3eFakeExec(f, "web", "", "cat: /nope: No such file\n", 1, &got)
	_, diags := p3eOpenEphemeral(t, NewExecOutputEphemeralResource(), pd, map[string]tftypes.Value{
		"container_id": p3eStr("web"),
		"command":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("cat"), p3eStr("/nope")}),
	})
	if !diags.HasError() || !strings.Contains(diags[0].Detail(), "exited with code 1") || !strings.Contains(diags[0].Detail(), "No such file") {
		t.Fatalf("diags = %v", diags)
	}
}

func TestExecOutputEphemeralBadTimeout(t *testing.T) {
	_, pd := newP3eFake(t)
	_, diags := p3eOpenEphemeral(t, NewExecOutputEphemeralResource(), pd, map[string]tftypes.Value{
		"container_id": p3eStr("web"),
		"command":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{p3eStr("true")}),
		"timeout":      p3eStr("soon"),
	})
	if !diags.HasError() {
		t.Fatal("expected invalid timeout")
	}
}

func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{max: 4}
	n, _ := b.Write([]byte("abc"))
	n2, _ := b.Write([]byte("def"))
	if b.String() != "abcd" || !b.truncated || n != 3 || n2 != 3 {
		t.Errorf("got %q truncated=%v", b.String(), b.truncated)
	}
}

func TestAccExecOutputEphemeral(t *testing.T) {
	testAccPreCheck(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccEphemeralFactories(),
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_10_0)},
		Steps: []resource.TestStep{{
			Config: `
resource "docker_container" "exec" {
  name    = "tfacc-exec-output"
  image   = "busybox:1.36"
  command = ["sh", "-c", "echo s3cret > /tmp/token && sleep 3600"]
}

ephemeral "docker_exec_output" "token" {
  container_id = docker_container.exec.id
  command      = ["cat", "/tmp/token"]
}

provider "echo" {
  data = trimspace(ephemeral.docker_exec_output.token.stdout)
}

resource "echo" "token" {}
`,
			Check: resource.TestCheckResourceAttr("echo.token", "data", "s3cret"),
		}},
	})
}
