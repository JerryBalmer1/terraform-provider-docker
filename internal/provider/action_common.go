package provider

// Pass 3 Lane D: helpers shared by the docker_* actions. Every identifier is
// prefixed with "act" so it cannot collide with other lanes.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	units "github.com/docker/go-units"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// actPollInterval is how often waiting actions poll the daemon. A var so
// unit tests can shorten it.
var actPollInterval = 2 * time.Second

// actTailLines is how many output lines an error carries.
const actTailLines = 20

// actBase carries the provider data every action needs.
type actBase struct {
	pd *providerData
}

// Configure stores the provider data. It is nil during validation.
func (a *actBase) Configure(_ context.Context, req action.ConfigureRequest, resp *action.ConfigureResponse) {
	a.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// client returns the Docker client, or adds an error when the provider was
// not configured.
func (a *actBase) client(diags *diag.Diagnostics) *client.Client {
	if a.pd == nil || a.pd.Client == nil {
		diags.AddError("Provider not configured", "The docker provider was not configured before the action was invoked. This is a provider bug.")
		return nil
	}
	return a.pd.Client
}

// actProgress logs msg and sends it to Terraform as a progress event.
func actProgress(ctx context.Context, resp *action.InvokeResponse, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	tflog.Info(ctx, msg)
	if resp != nil && resp.SendProgress != nil {
		resp.SendProgress(action.InvokeProgressEvent{Message: msg})
	}
}

// actDuration validates a duration attribute in ValidateConfig. Unknown and
// null values pass.
func actDuration(v types.String, attr string, diags *diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	d, err := time.ParseDuration(v.ValueString())
	if err != nil || d <= 0 {
		diags.AddAttributeError(path.Root(attr), "Invalid duration",
			fmt.Sprintf("%s must be a positive Go duration such as \"30s\" or \"5m\", got %q.", attr, v.ValueString()))
	}
}

// actTimeout parses a duration attribute, returning def when it is unset.
func actTimeout(v types.String, def time.Duration) time.Duration {
	d, err := parseDurationOr(v.ValueString(), def)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// actBool returns the value of an optional bool, or def when unset.
func actBool(v types.Bool, def bool) bool {
	if b, ok := knownBool(v); ok {
		return b
	}
	return def
}

// actHumanSize formats a byte count like the docker CLI does.
func actHumanSize(n uint64) string {
	return units.HumanSize(float64(n))
}

// ---------------------------------------------------------------------------
// Prune filters

// actPruneFilterKeys are the filter keys the prune actions accept. Each
// prune endpoint takes a different subset; actPruneFilters routes them.
var actPruneFilterKeys = []string{"label", "label!", "until"}

// actValidatePruneFilters rejects filter keys no prune endpoint accepts.
func actValidatePruneFilters(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]string {
	if m.IsUnknown() {
		return nil
	}
	f := stringMapFrom(ctx, m, diags)
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !swarmContains(actPruneFilterKeys, k) {
			diags.AddAttributeError(path.Root("filters").AtMapKey(k), "Unsupported prune filter",
				fmt.Sprintf("filter %q is not supported; use one of %s.", k, strings.Join(actPruneFilterKeys, ", ")))
		}
	}
	return f
}

// actPruneFilters builds filters.Args with only the keys in allowed.
func actPruneFilters(f map[string]string, allowed ...string) filters.Args {
	args := filters.NewArgs()
	for k, v := range f {
		if swarmContains(allowed, k) {
			args.Add(k, v)
		}
	}
	return args
}

// ---------------------------------------------------------------------------
// Files

// actWriteFile streams r into path atomically: it writes a temporary file in
// the same directory and renames it into place. Parent directories are
// created. progress, when non-nil, is called about every 64 MiB.
func actWriteFile(p string, r io.Reader, progress func(written int64)) (int64, error) {
	p = expandHome(p)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("creating directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("creating temporary file in %s: %w", dir, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	w := &actCountingWriter{w: tmp, every: 64 << 20, progress: progress}
	n, err := io.Copy(w, r)
	if err != nil {
		return n, fmt.Errorf("writing %s: %w", p, err)
	}
	if err := tmp.Close(); err != nil {
		return n, fmt.Errorf("writing %s: %w", p, err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		// Windows cannot rename over an existing file that is open
		// elsewhere; report the error rather than leave a partial file.
		return n, fmt.Errorf("moving %s into place: %w", p, err)
	}
	keep = true
	return n, nil
}

type actCountingWriter struct {
	w        io.Writer
	n, next  int64
	every    int64
	progress func(int64)
}

func (c *actCountingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	if c.progress != nil && c.n >= c.next+c.every {
		c.next = c.n
		c.progress(c.n)
	}
	return n, err
}

// actLineWriter splits written bytes into lines and hands each complete line
// to emit. Flush emits a trailing partial line. Tail keeps the last lines.
type actLineWriter struct {
	prefix string
	emit   func(string)
	buf    []byte
	tail   *[]string
}

func (l *actLineWriter) Write(b []byte) (int, error) {
	l.buf = append(l.buf, b...)
	for {
		i := strings.IndexByte(string(l.buf), '\n')
		if i < 0 {
			break
		}
		l.line(strings.TrimRight(string(l.buf[:i]), "\r"))
		l.buf = l.buf[i+1:]
	}
	return len(b), nil
}

func (l *actLineWriter) Flush() {
	if len(l.buf) > 0 {
		l.line(strings.TrimRight(string(l.buf), "\r"))
		l.buf = nil
	}
}

func (l *actLineWriter) line(s string) {
	s = l.prefix + s
	if l.tail != nil {
		*l.tail = append(*l.tail, s)
		if len(*l.tail) > actTailLines {
			*l.tail = (*l.tail)[len(*l.tail)-actTailLines:]
		}
	}
	if l.emit != nil {
		l.emit(s)
	}
}
