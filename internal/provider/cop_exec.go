package provider

// Pass 5 Lane A (WP cop): the exec run shared by action docker_exec and
// ephemeral docker_exec_output (Q33 stdout + stderr, Q34 two hashes, Q35 one
// result object = GET /exec/{id}/json).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// copExecSpec is one exec to run.
type copExecSpec struct {
	Container string // ID or name
	Opts      client.ExecCreateOptions
	Detach    bool
	Timeout   time.Duration
	// MaxBytes caps each captured stream (0 = unlimited).
	MaxBytes int
	// Stdout / Stderr, when set, also receive the streams as they arrive.
	Stdout, Stderr io.Writer
	// Stdin, when non-nil, is written to the command's stdin (AttachStdin),
	// which is then closed so the command sees end of file. Ignored with
	// Detach.
	Stdin *string
}

// copExecOutcome is what a finished (or detached) exec produced.
type copExecOutcome struct {
	ExecID   string
	ExitCode int
	Running  bool
	Stdout   string
	Stderr   string
	// Inspect is the GET /exec/{id}/json body (decoded), or a reconstruction
	// from the typed client result when the raw body cannot be read.
	Inspect map[string]any
}

// errCopExecTimeout marks a run that did not finish in time.
var errCopExecTimeout = errors.New("exec timed out")

// copExecRun creates, starts (attached unless Detach) and inspects one exec.
// Captured output is returned even when the run fails.
func copExecRun(ctx context.Context, c *client.Client, s copExecSpec) (copExecOutcome, error) {
	var out copExecOutcome
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	opts := s.Opts
	opts.AttachStdout, opts.AttachStderr = !s.Detach, !s.Detach
	opts.AttachStdin = s.Stdin != nil && !s.Detach
	created, err := c.ExecCreate(ctx, s.Container, opts)
	if err != nil {
		return out, fmt.Errorf("creating exec: %w", err)
	}
	out.ExecID = created.ID
	if s.Detach {
		if _, err := c.ExecStart(ctx, created.ID, client.ExecStartOptions{Detach: true, TTY: opts.TTY, ConsoleSize: opts.ConsoleSize}); err != nil {
			return out, fmt.Errorf("starting exec %s: %w", created.ID, err)
		}
		out.Inspect, out.ExitCode, out.Running = copExecInspect(ctx, c, created.ID, opts)
		return out, nil
	}
	hj, err := c.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: opts.TTY, ConsoleSize: opts.ConsoleSize})
	if err != nil {
		return out, fmt.Errorf("starting exec %s: %w", created.ID, err)
	}
	stop := context.AfterFunc(ctx, hj.Close)
	defer stop()
	defer hj.Close()
	if opts.AttachStdin {
		if _, ok := hj.Conn.(client.CloseWriter); !ok {
			return out, fmt.Errorf("exec %s: the connection to the daemon (%T) cannot half-close, so stdin would never reach end of file", created.ID, hj.Conn)
		}
		go copExecWriteStdin(&hj.HijackedResponse, *s.Stdin)
	}
	outBuf := &cappedBuffer{max: s.MaxBytes}
	errBuf := &cappedBuffer{max: s.MaxBytes}
	if s.MaxBytes <= 0 {
		outBuf.max, errBuf.max = int(^uint(0)>>1), int(^uint(0)>>1)
	}
	var ow, ew io.Writer = outBuf, errBuf
	if s.Stdout != nil {
		ow = io.MultiWriter(outBuf, s.Stdout)
	}
	if s.Stderr != nil {
		ew = io.MultiWriter(errBuf, s.Stderr)
	}
	if opts.TTY {
		_, err = io.Copy(ow, hj.Reader)
	} else {
		_, err = stdcopy.StdCopy(ow, ew, hj.Reader)
	}
	out.Stdout, out.Stderr = outBuf.String(), errBuf.String()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%w after %s", errCopExecTimeout, s.Timeout)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("reading exec output: %w", err)
	}
	// The exit code can lag the stream end by a moment.
	err = waitFor(ctx, s.Timeout, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
		insp, err := c.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return false, err
		}
		return !insp.Running, nil
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return out, fmt.Errorf("%w: exit code not available within %s", errCopExecTimeout, s.Timeout)
		}
		return out, fmt.Errorf("inspecting exec %s: %w", created.ID, err)
	}
	out.Inspect, out.ExitCode, out.Running = copExecInspect(ctx, c, created.ID, opts)
	return out, nil
}

// copExecWriteStdin writes data to the attached stdin of an exec and closes
// the write half, which the daemon turns into end of file on the command's
// stdin (it closes the process stdin when the attached stream ends, as
// `docker exec -i` does when its input ends). A write error (the command
// exited or closed stdin without reading everything) is not an error of the
// run: the exit code decides, as with a shell pipe.
func copExecWriteStdin(hj *client.HijackedResponse, data string) {
	if data != "" {
		_, _ = io.WriteString(hj.Conn, data)
	}
	_ = hj.CloseWrite()
}

// copExecInspect returns the GET /exec/{id}/json body. The typed client
// result drops ProcessConfig and the Open* flags, so the body is read over
// the client's own dialer; if that fails, the object is rebuilt from the
// typed result and the create options.
func copExecInspect(ctx context.Context, c *client.Client, id string, opts client.ExecCreateOptions) (map[string]any, int, bool) {
	if b, err := copEngineGet(ctx, c, "/exec/"+id+"/json"); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			code, _ := m["ExitCode"].(float64)
			running, _ := m["Running"].(bool)
			return m, int(code), running
		}
	}
	insp, err := c.ExecInspect(ctx, id, client.ExecInspectOptions{})
	if err != nil {
		return map[string]any{"ID": id}, 0, false
	}
	pc := map[string]any{"privileged": opts.Privileged, "user": opts.User, "tty": opts.TTY}
	if len(opts.Cmd) > 0 {
		pc["entrypoint"] = opts.Cmd[0]
		pc["arguments"] = opts.Cmd[1:]
	}
	return map[string]any{
		"ID": insp.ID, "ContainerID": insp.ContainerID, "Running": insp.Running, "ExitCode": insp.ExitCode, "Pid": insp.PID,
		"OpenStdin": opts.AttachStdin, "OpenStdout": opts.AttachStdout, "OpenStderr": opts.AttachStderr,
		"DetachKeys": opts.DetachKeys, "ProcessConfig": pc,
	}, insp.ExitCode, insp.Running
}

// copEngineGet performs a GET against the Engine API through the client's
// dialer (plain HTTP over the dialed connection, TLS when the client has
// it) and returns the body of a 200 response.
func copEngineGet(ctx context.Context, c *client.Client, p string) ([]byte, error) {
	dial := c.Dialer()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	u := "http://docker"
	if v := c.ClientVersion(); v != "" {
		u += "/v" + v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+p, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", p, resp.StatusCode)
	}
	return b, nil
}

// copReadEnvFiles reads docker --env-file files: KEY=VALUE lines, # comments
// and blank lines skipped, a bare KEY taken from the environment of the
// Terraform process (skipped when unset).
func copReadEnvFiles(paths []string) ([]string, error) {
	var env []string
	for _, p := range paths {
		f, err := os.Open(expandHome(p))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		n := 0
		for sc.Scan() {
			n++
			line := strings.TrimLeft(sc.Text(), " \t")
			line = strings.TrimSuffix(line, "\r")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if k == "" || strings.ContainsAny(k, " \t") {
				f.Close()
				return nil, fmt.Errorf("%s:%d: invalid variable name %q", p, n, k)
			}
			if !ok {
				val, set := os.LookupEnv(k)
				if !set {
					continue
				}
				v = val
			}
			env = append(env, k+"="+v)
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	return env, nil
}

// copMergeEnv merges env-file entries with the env map (the map wins) into
// a sorted KEY=VALUE list.
func copMergeEnv(fileEnv []string, m map[string]string) []string {
	all := map[string]string{}
	for _, kv := range fileEnv {
		k, v, _ := strings.Cut(kv, "=")
		all[k] = v
	}
	for k, v := range m {
		all[k] = v
	}
	out := make([]string, 0, len(all))
	for k, v := range all {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
