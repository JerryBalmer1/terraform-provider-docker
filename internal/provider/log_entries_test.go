package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// logFrame builds one multiplexed frame.
func logFrame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

const (
	logTS1 = "2026-11-08T10:00:00.000000001Z"
	logTS2 = "2026-11-08T10:00:00.000000002Z"
	logTS3 = "2026-11-08T10:00:01.5Z"
)

func TestLogReadEntriesTTY(t *testing.T) {
	in := logTS1 + " hello\r\n" + logTS2 + " world\n" + logTS3 + " partial"
	got, err := logReadEntries(strings.NewReader(in), true, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("entries: %+v", got)
	}
	want := []logEntry{
		{Timestamp: logTS1, Stream: "combined", Source: "web", Message: "hello"},
		{Timestamp: logTS2, Stream: "combined", Source: "web", Message: "world"},
		{Timestamp: logTS3, Stream: "combined", Source: "web", Message: "partial"},
	}
	for i := range want {
		g := got[i]
		g.Time = time.Time{}
		if g != want[i] {
			t.Errorf("%d: got %+v want %+v", i, g, want[i])
		}
	}
	if got[0].Time.IsZero() {
		t.Error("timestamp not parsed")
	}
}

func TestLogReadEntriesMultiplexed(t *testing.T) {
	var b bytes.Buffer
	b.Write(logFrame(1, logTS1+" out one\n"))
	b.Write(logFrame(2, logTS2+" err ")) // a line split across frames
	b.Write(logFrame(1, logTS3+" out two\r\n"))
	b.Write(logFrame(2, "continued\n"))
	b.Write(logFrame(2, logTS3+" err tail")) // partial final line
	got, err := logReadEntries(&b, false, "svc.1")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range got {
		lines = append(lines, e.Stream+"|"+e.Timestamp+"|"+e.Message)
	}
	want := []string{
		"stdout|" + logTS1 + "|out one",
		"stdout|" + logTS3 + "|out two",
		"stderr|" + logTS2 + "|err continued",
		"stderr|" + logTS3 + "|err tail",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if got[0].Source != "svc.1" {
		t.Errorf("source %q", got[0].Source)
	}
}

func TestLogReadEntriesErrors(t *testing.T) {
	if _, err := logReadEntries(bytes.NewReader(logFrame(3, "boom")), false, "x"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("systemerr: %v", err)
	}
	if _, err := logReadEntries(bytes.NewReader([]byte{1, 0, 0}), false, "x"); err == nil {
		t.Error("truncated header must fail")
	}
	if _, err := logReadEntries(bytes.NewReader(logFrame(1, "abc")[:9]), false, "x"); err == nil {
		t.Error("truncated payload must fail")
	}
	got, err := logReadEntries(strings.NewReader("no timestamp here\n"), true, "x")
	if err != nil || got[0].Timestamp != "" || got[0].Message != "no timestamp here" {
		t.Errorf("no timestamp: %+v %v", got, err)
	}
}

func TestLogMergeTailRender(t *testing.T) {
	a, _ := logReadEntries(strings.NewReader(logTS1+" a1\n"+logTS3+" a2\n"), true, "a")
	b, _ := logReadEntries(strings.NewReader(logTS2+" b1\n"+logTS3+" b2\n"), true, "b")
	m := logMerge(a, b)
	var msgs []string
	for _, e := range m {
		msgs = append(msgs, e.Message)
	}
	if strings.Join(msgs, ",") != "a1,b1,a2,b2" {
		t.Errorf("merge (stable on equal timestamps): %v", msgs)
	}
	if got := logApplyTail(m, 2); len(got) != 2 || got[0].Message != "a2" {
		t.Errorf("tail: %+v", got)
	}
	if got := logApplyTail(m, 0); len(got) != 0 {
		t.Error("tail 0")
	}
	if got := logApplyTail(m, 10); len(got) != 4 {
		t.Error("tail larger than entries")
	}
	r := logRender(m[:2])
	if r != logTS1+" combined a a1\n"+logTS2+" combined b b1" {
		t.Errorf("render: %q", r)
	}
}

func TestLogSinceQuery(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"":                            "",
		"42m":                         "1794136680.000000000",
		"2026-11-08T11:00:00Z":        "1794135600.000000000",
		"2026-11-08T11:00:00.5+00:00": "1794135600.500000000",
		"1793962800":                  "1793962800",
		"1793962800.25":               "1793962800.25",
	}
	for in, want := range cases {
		got, err := logSinceQuery(in, now)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"yesterday", "-5m", "0s"} {
		if _, err := logSinceQuery(bad, now); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestLogInputs(t *testing.T) {
	if logTailValue(types.Int64Null()) != 1000 || logTailValue(types.Int64Value(5)) != 5 || logTailQuery(7) != "7" {
		t.Error("tail helpers")
	}
	ta := logTailAttribute()
	if !ta.Optional || !strings.Contains(ta.MarkdownDescription, "Default `1000`") {
		t.Error("tail attribute")
	}
	sa := logSinceAttribute()
	resp := &validator.StringResponse{}
	sa.Validators[0].ValidateString(context.Background(), validator.StringRequest{Path: path.Root("since"), ConfigValue: types.StringValue("nope")}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("since validator")
	}
	resp = &validator.StringResponse{}
	sa.Validators[0].ValidateString(context.Background(), validator.StringRequest{Path: path.Root("since"), ConfigValue: types.StringValue("1h")}, resp)
	if resp.Diagnostics.HasError() {
		t.Error("since 1h")
	}
}

// TestLogFillContainerLogs fills the generated docker_logs contract struct
// and stores it in a data source state.
func TestLogFillContainerLogs(t *testing.T) {
	ctx := context.Background()
	entries, _ := logReadEntries(strings.NewReader(logTS1+" secret=1\n"), true, "web")
	type model struct {
		Tail  types.Int64  `tfsdk:"tail"`
		Since types.String `tfsdk:"since"`
		contractEngineContainerLogs
	}
	var m model
	if d := m.contractFill(ctx, nil, logFillInput(entries)); d.HasError() {
		t.Fatal(d)
	}
	want := logTS1 + " combined web secret=1"
	sum := sha256.Sum256([]byte(want))
	if m.Logs.ValueString() != want || m.ContentSHA256.ValueString() != hex.EncodeToString(sum[:]) {
		t.Errorf("logs %q hash %s", m.Logs.ValueString(), m.ContentSHA256)
	}
	s := schema.Schema{Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
		"tail": logTailAttribute(), "since": logSinceAttribute(),
	}, "engine:container-logs")}
	st := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if d := st.Set(ctx, &m); d.HasError() {
		t.Fatal(d)
	}
	var msg types.String
	if d := st.GetAttribute(ctx, path.Root("log_entries").AtListIndex(0).AtName("message"), &msg); d.HasError() || msg.ValueString() != "secret=1" {
		t.Errorf("message %s %v", msg, d)
	}
}
