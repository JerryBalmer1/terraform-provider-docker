package provider

// Pass 5 Lane 0: the log_entries builder shared by every logs item
// (docker_logs, docker_service_logs, docker_task_logs, docker_compose_logs),
// per reference/CONTRACT.md "Logs":
//   - structured `log_entries`: list(object({timestamp, stream, source,
//     message})), only `message` sensitive;
//   - raw `logs`: one line per entry "<timestamp> <stream> <source> <message>",
//     joined with "\n" (no trailing newline), hashed as content_sha256;
//   - inputs `tail` (default 1000 lines per container / service / task)
//     and `since` (RFC 3339 or a duration such as 42m) (rule L1).
//
// Lane usage (data source):
//
//	opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true,
//		Tail: logTailQuery(logTailValue(m.Tail))}
//	if opts.Since, err = logSinceQuery(m.Since.ValueString(), time.Now()); err != nil { ... }
//	rc, err := c.ContainerLogs(ctx, id, opts)
//	entries, err := logReadEntries(rc, tty, name)
//	m.contractFill(ctx, pd, logFillInput(entries))
//
// Several sources (a service's tasks, a compose project's containers):
// read each with its own source and tail, then logMerge them.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// logDefaultTail is the tail input's default (lines per source).
const logDefaultTail int64 = 1000

// Stream values of a log entry.
const (
	logStreamStdout   = "stdout"
	logStreamStderr   = "stderr"
	logStreamCombined = "combined" // TTY: the Engine merges the streams at the source
)

// logEntry is one log line.
type logEntry struct {
	// Timestamp is the RFC 3339 nano prefix as the Engine sent it
	// (timestamps=true), or "" when the line had none.
	Timestamp string
	// Time is Timestamp parsed (zero when absent); logMerge sorts on it.
	Time    time.Time
	Stream  string // stdout, stderr or combined
	Source  string // container name, service name, task ID or "<compose service>.<container>"
	Message string // the line without timestamp, trailing newline or CR
}

// logReadEntries reads one log stream of one source. tty selects the raw
// (TTY, stream "combined") format; otherwise the stream is the Engine's
// multiplexed format (8-byte headers: stream type, 3 zero bytes, uint32
// big-endian payload size). Lines may span frames; a final line without
// a newline is kept; a trailing CR is dropped (CRLF). A frame of stream 3
// (systemerr) becomes an error.
func logReadEntries(r io.Reader, tty bool, source string) ([]logEntry, error) {
	var out []logEntry
	emit := func(stream, line string) {
		out = append(out, logParseLine(stream, source, line))
	}
	if tty {
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				emit(logStreamCombined, line)
			}
			if err == io.EOF {
				return out, nil
			}
			if err != nil {
				return out, err
			}
		}
	}
	bufs := map[string]*strings.Builder{logStreamStdout: {}, logStreamStderr: {}}
	feed := func(stream string, b []byte) {
		sb := bufs[stream]
		for len(b) > 0 {
			i := bytes.IndexByte(b, '\n')
			if i < 0 {
				sb.Write(b)
				return
			}
			sb.Write(b[:i+1])
			emit(stream, sb.String())
			sb.Reset()
			b = b[i+1:]
		}
	}
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF {
				break
			}
			if err == io.ErrUnexpectedEOF {
				return out, errors.New("log stream ended inside a frame header")
			}
			return out, err
		}
		size := binary.BigEndian.Uint32(hdr[4:8])
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			return out, fmt.Errorf("log stream ended inside a frame: %w", err)
		}
		switch hdr[0] {
		case 0, 1: // stdin (never sent for logs) or stdout
			feed(logStreamStdout, payload)
		case 2:
			feed(logStreamStderr, payload)
		case 3:
			return out, fmt.Errorf("daemon error in log stream: %s", strings.TrimSpace(string(payload)))
		default:
			return out, fmt.Errorf("unknown log stream type %d", hdr[0])
		}
	}
	for _, s := range []string{logStreamStdout, logStreamStderr} {
		if bufs[s].Len() > 0 {
			emit(s, bufs[s].String())
		}
	}
	return out, nil
}

// logParseLine splits a line into timestamp and message.
func logParseLine(stream, source, line string) logEntry {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	e := logEntry{Stream: stream, Source: source, Message: line}
	if ts, rest, ok := strings.Cut(line, " "); ok || ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			e.Timestamp, e.Time, e.Message = ts, t, rest
		}
	}
	return e
}

// logMerge merges entries of several sources, sorted by time. The sort is
// stable, so lines with equal (or missing) timestamps keep their order;
// lines without a timestamp sort first.
func logMerge(lists ...[]logEntry) []logEntry {
	var out []logEntry
	for _, l := range lists {
		out = append(out, l...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// logApplyTail keeps the last tail entries (tail < 0 keeps all). The
// Engine already applies tail per container; use this when one source's
// entries come from several reads.
func logApplyTail(entries []logEntry, tail int64) []logEntry {
	if tail < 0 || int64(len(entries)) <= tail {
		return entries
	}
	return entries[int64(len(entries))-tail:]
}

// logRender renders the raw `logs` text: "<timestamp> <stream> <source>
// <message>" per entry, joined with "\n". A missing timestamp leaves its
// field empty (the line then starts with a space).
func logRender(entries []logEntry) string {
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Timestamp)
		b.WriteByte(' ')
		b.WriteString(e.Stream)
		b.WriteByte(' ')
		b.WriteString(e.Source)
		b.WriteByte(' ')
		b.WriteString(e.Message)
	}
	return b.String()
}

// logStructured is the structured log_entries value (snake_case keys).
func logStructured(entries []logEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{"timestamp": e.Timestamp, "stream": e.Stream, "source": e.Source, "message": e.Message})
	}
	return out
}

// logFillInput is the contractFillInput of a logs item: raw `logs` and
// structured `log_entries` (content_sha256 and the versions are computed
// by the fill).
func logFillInput(entries []logEntry) contractFillInput {
	return contractFillInput{
		Raw:        map[string][]byte{"logs": []byte(logRender(entries))},
		Structured: map[string]any{"log_entries": logStructured(entries)},
	}
}

// ---------------------------------------------------------------------------
// tail / since inputs

// logTailAttribute is the `tail` input of a logs data source.
func logTailAttribute() dschema.Int64Attribute {
	return dschema.Int64Attribute{
		Optional: true,
		MarkdownDescription: "Lines to keep from the end of the logs of each container, service or task (the Engine's `tail`). " +
			"Default `1000`, so state cannot grow without bound; `0` reads none, and there is no \"all\".",
		Validators: []validator.Int64{int64validator.AtLeast(0)},
	}
}

// logSinceAttribute is the `since` input of a logs data source.
func logSinceAttribute() dschema.StringAttribute {
	return dschema.StringAttribute{
		Optional: true,
		MarkdownDescription: "Only lines at or after this time. Takes an RFC 3339 timestamp (e.g. `2026-01-02T15:04:05Z`) or a duration " +
			"before now such as `42m` or `1h30m`, sent as the Engine's UNIX-timestamp `since`. Combined with `tail` when both are set.",
		Validators: []validator.String{logSinceValidator{}},
	}
}

// logTailValue returns the tail input, logDefaultTail when null or unknown.
func logTailValue(v types.Int64) int64 {
	if v.IsNull() || v.IsUnknown() || v.ValueInt64() < 0 {
		return logDefaultTail
	}
	return v.ValueInt64()
}

// logTailQuery is the Engine `tail` query value of a tail input.
func logTailQuery(tail int64) string {
	return strconv.FormatInt(tail, 10)
}

var logUnixRe = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,9})?$`)

// logSinceQuery turns a since input into the Engine's `since` query value
// ("<seconds>.<nanoseconds>"), relative to now for a duration. "" returns
// "". A UNIX timestamp passes through unchanged.
func logSinceQuery(since string, now time.Time) (string, error) {
	s := strings.TrimSpace(since)
	if s == "" {
		return "", nil
	}
	if logUnixRe.MatchString(s) {
		return s, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond()), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return "", fmt.Errorf("since %q is neither an RFC 3339 timestamp nor a positive duration such as 42m", since)
	}
	t := now.Add(-d)
	return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond()), nil
}

// logSinceValidator checks the since input. Unknown and null pass.
type logSinceValidator struct{}

func (logSinceValidator) Description(context.Context) string {
	return "must be an RFC 3339 timestamp, a UNIX timestamp or a positive duration such as 42m"
}

func (v logSinceValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (logSinceValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := logSinceQuery(req.ConfigValue.ValueString(), time.Now()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid since", err.Error())
	}
}
