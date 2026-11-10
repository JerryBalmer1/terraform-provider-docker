package provider

// Engine API call logging (Pass 5.3 Lane 0): one structured tflog entry per
// Docker Engine API call, with request_id (per call), method, path (object
// IDs kept, secret query values redacted), status, duration_ms and
// api_version.
//
// Where it hooks in: the moby client wraps its transport itself and clones
// any *http.Client it is given (client_options.go WithHTTPClient), and it
// needs the base *http.Transport it built for SSH dialing, TLS hijacking
// (exec / attach) and idle-connection cleanup (client.go baseTransport,
// dialerFromTransport). A RoundTripper wrapped around that transport would
// break all three, so apiLogger rides the client's own transport hooks
// instead (WithHTTPRequestHook / WithHTTPResponseHook, client_hook.go
// hookTransport, which also covers hijacked requests). The request hook
// starts the per-call record and carries it in the request context; the
// response hook writes the entry. A call that fails before any response (a
// dial error) is logged from the httptrace ConnectDone callback, so every
// call is logged exactly once.
//
// The entries go to the Terraform log at DEBUG under the "docker_api"
// subsystem: TF_LOG_PROVIDER=DEBUG shows them.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/moby/moby/client"
)

const apiLogSubsystem = "docker_api"

type apiCallKey struct{}

// apiCall is the per-call record the request hook puts in the context.
type apiCall struct {
	id     string
	method string
	path   string
	apiVer string
	start  time.Time
	logged atomic.Bool
	now    func() time.Time
}

// apiLogger holds the clock (tests replace it) and the id source.
type apiLogger struct {
	now   func() time.Time
	newID func() string
}

func newAPILogger() *apiLogger {
	return &apiLogger{now: time.Now, newID: randomRequestID}
}

func randomRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// apiLogOpts returns the client options that install the hooks.
func apiLogOpts() []client.Opt {
	l := newAPILogger()
	return []client.Opt{client.WithHTTPRequestHook(l.requestHook), client.WithHTTPResponseHook(l.responseHook)}
}

// requestHook starts the call record. It replaces the request's context (the
// moby hook transport sends the hook's copy of the request, metadata
// included).
func (l *apiLogger) requestHook(req *http.Request) error {
	path, ver := apiLogPath(req.URL)
	call := &apiCall{id: l.newID(), method: req.Method, path: path, apiVer: ver, start: l.now(), now: l.now}
	ctx := context.WithValue(req.Context(), apiCallKey{}, call)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				call.log(ctx, 0, err)
			}
		},
	})
	*req = *req.WithContext(ctx)
	return nil
}

func (l *apiLogger) responseHook(resp *http.Response) {
	if resp == nil || resp.Request == nil {
		return
	}
	call, _ := resp.Request.Context().Value(apiCallKey{}).(*apiCall)
	if call == nil {
		return
	}
	call.log(resp.Request.Context(), resp.StatusCode, nil)
}

func (c *apiCall) log(ctx context.Context, status int, err error) {
	if !c.logged.CompareAndSwap(false, true) {
		return
	}
	fields := map[string]any{
		"request_id":  c.id,
		"method":      c.method,
		"path":        c.path,
		"status":      status,
		"duration_ms": c.now().Sub(c.start).Milliseconds(),
		"api_version": c.apiVer,
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	ctx = tflog.NewSubsystem(ctx, apiLogSubsystem)
	tflog.SubsystemDebug(ctx, apiLogSubsystem, "docker engine api call", fields)
}

// apiLogSecretKeys are query parameter names (lowercased substrings) whose
// values are redacted: build args carry secrets often enough, and any
// auth/password/token/secret/key/credential parameter is redacted outright.
var apiLogSecretKeys = []string{"auth", "pass", "secret", "token", "key", "cred", "buildargs", "registryconfig", "cert"}

// apiLogPath returns the request path with the /v1.NN prefix removed and the
// query re-encoded with secret values replaced by "REDACTED", plus the API
// version from the prefix ("" when the path has none).
func apiLogPath(u *url.URL) (string, string) {
	p := u.Path
	ver := ""
	if strings.HasPrefix(p, "/v") {
		rest := p[2:]
		v, tail, ok := strings.Cut(rest, "/")
		if ok && apiVersionPattern.MatchString(v) {
			ver = v
			p = "/" + tail
		}
	}
	q := u.Query()
	if len(q) == 0 {
		return p, ver
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		redact := false
		lk := strings.ToLower(k)
		for _, s := range apiLogSecretKeys {
			if strings.Contains(lk, s) {
				redact = true
				break
			}
		}
		for j, v := range q[k] {
			if i > 0 || j > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			if redact {
				b.WriteString("REDACTED")
			} else {
				b.WriteString(url.QueryEscape(v))
			}
		}
	}
	return p + "?" + b.String(), ver
}
