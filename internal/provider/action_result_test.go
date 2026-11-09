package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// actResultTestItem is an action shaped like the Q35 rule: one `result`,
// identity, a sensitive raw with its hash, and versions.
func actResultTestItem() *contractItem {
	return &contractItem{
		ID: "test:action", Kind: "action", Name: "docker_test_action",
		Outputs: []contractOutput{
			{Name: "container_id", Category: contractIdentity, TFType: "string"},
			{Name: "result", Category: contractResult, TFType: "object({...})", Fields: []contractField{
				{Name: "exit_code", Upstream: "ExitCode", TFType: "number"},
				{Name: "env", Upstream: "Env", TFType: "list(string)", Sensitive: true},
				{Name: "items", Upstream: "Items", TFType: "list(object({...}))", Fields: []contractField{
					{Name: "id", Upstream: "ID", TFType: "string"},
					{Name: "secret", Upstream: "Secret", TFType: "string", Sensitive: true},
				}},
			}},
			{Name: "stdout", Category: contractRaw, TFType: "string", Sensitive: true},
			{Name: "stdout_sha256", Category: contractHash, TFType: "string", HashOf: "stdout"},
			{Name: "compose_go_version", Category: contractVersion, TFType: "string"},
			{Name: "engine_api_version", Category: contractVersion, TFType: "string"},
		},
	}
}

type actResultTestValue struct {
	ExitCode int
	Env      []string
	Items    []struct{ ID, Secret string }
}

func TestActFinishResult(t *testing.T) {
	ctx := context.Background()
	contractTestItem(t, actResultTestItem())
	dir := t.TempDir()
	out := filepath.Join(dir, "sub", "result.json")
	var msgs []string
	resp := &action.InvokeResponse{SendProgress: func(e action.InvokeProgressEvent) { msgs = append(msgs, e.Message) }}
	val := actResultTestValue{ExitCode: 3, Env: []string{"A=1"}, Items: []struct{ ID, Secret string }{{"i1", "pw"}}}
	obj := actFinishResult(ctx, resp, nil, "test:action", map[string]any{
		"container_id": "abc",
		"result":       val,
		"stdout":       []byte("hello\n"),
	}, types.StringValue(out))
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() > 0 {
		t.Fatalf("diags: %v", resp.Diagnostics)
	}
	sum := sha256.Sum256([]byte("hello\n"))
	if obj["stdout_sha256"] != hex.EncodeToString(sum[:]) || obj["compose_go_version"] != contractComposeGoVersion || obj["engine_api_version"] != nil {
		t.Errorf("computed values: %v", obj)
	}

	// Progress: one message, sensitive values redacted, the rest readable.
	if len(msgs) != 1 {
		t.Fatalf("progress messages: %v", msgs)
	}
	m := msgs[0]
	if !strings.HasPrefix(m, "docker_test_action result: {") || strings.Contains(m, "hello") || strings.Contains(m, "pw") || strings.Contains(m, "A=1") {
		t.Errorf("progress not redacted: %s", m)
	}
	if !strings.Contains(m, `"exit_code":3`) || !strings.Contains(m, `"id":"i1"`) || !strings.Contains(m, `"secret":"(sensitive)"`) {
		t.Errorf("progress lost readable fields: %s", m)
	}

	// File: full values, sorted keys, 2-space indent, trailing newline.
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasSuffix(s, "}\n") || !strings.Contains(s, "\n  \"compose_go_version\"") || !strings.Contains(s, `"stdout": "hello\n"`) || !strings.Contains(s, `"secret": "pw"`) {
		t.Errorf("file:\n%s", s)
	}
	keys := []string{`"compose_go_version"`, `"container_id"`, `"engine_api_version"`, `"result"`, `"stdout"`, `"stdout_sha256"`}
	last := -1
	for _, k := range keys {
		i := strings.Index(s, k)
		if i < last {
			t.Errorf("keys not sorted at %s:\n%s", k, s)
		}
		last = i
	}
	if strings.Index(s, `"env"`) > strings.Index(s, `"exit_code"`) || strings.Index(s, `"exit_code"`) > strings.Index(s, `"items"`) {
		t.Errorf("nested keys not sorted:\n%s", s)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil || back["container_id"] != "abc" {
		t.Errorf("round trip: %v %v", err, back)
	}
}

func TestActFinishResultStructValuesAndUnknownKey(t *testing.T) {
	ctx := context.Background()
	contractTestItem(t, actResultTestItem())
	resp := &action.InvokeResponse{}
	type vals struct {
		ContainerID string             `json:"container_id"`
		Result      actResultTestValue `json:"result"`
		Stdout      string             `json:"stdout"`
		Bogus       string             `json:"bogus"`
	}
	obj := actFinishResult(ctx, resp, nil, "test:action", vals{ContainerID: "x", Stdout: "out"}, types.StringNull())
	if obj["container_id"] != "x" || obj["stdout"] != "out" {
		t.Errorf("obj %v", obj)
	}
	if resp.Diagnostics.WarningsCount() != 1 || !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "bogus") {
		t.Errorf("want a warning for bogus: %v", resp.Diagnostics)
	}
	if _, ok := obj["bogus"]; ok {
		t.Error("unknown key kept")
	}
}

func TestActFinishResultWriteFailure(t *testing.T) {
	ctx := context.Background()
	contractTestItem(t, actResultTestItem())
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := &action.InvokeResponse{}
	actFinishResult(ctx, resp, nil, "test:action", nil, types.StringValue(filepath.Join(blocker, "result.json")))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error when result_path cannot be written")
	}
	if d, ok := resp.Diagnostics.Errors()[0].(interface{ Path() path.Path }); !ok || !d.Path().Equal(path.Root("result_path")) {
		t.Errorf("error not on result_path: %v", resp.Diagnostics)
	}
}

func TestActFinishResultUnknownItem(t *testing.T) {
	resp := &action.InvokeResponse{}
	if actFinishResult(context.Background(), resp, nil, "nope:nope", nil, types.StringNull()) != nil || !resp.Diagnostics.HasError() {
		t.Error("unknown item must fail")
	}
}

func TestActResultPathAttribute(t *testing.T) {
	a := actResultPathAttribute()
	if !a.Optional || !strings.HasPrefix(a.MarkdownDescription, "Local file the action writes its result object to after a successful run.") {
		t.Errorf("attribute: %+v", a)
	}
	dir := t.TempDir()
	cases := map[string]bool{"": false, "  ": false, "out/": false, dir: false, filepath.Join(dir, "r.json"): true}
	for p, ok := range cases {
		req := validator.StringRequest{Path: path.Root("result_path"), ConfigValue: types.StringValue(p)}
		resp := &validator.StringResponse{}
		a.Validators[0].ValidateString(context.Background(), req, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%q: error=%v", p, resp.Diagnostics)
		}
	}
	resp := &validator.StringResponse{}
	a.Validators[0].ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringUnknown()}, resp)
	if resp.Diagnostics.HasError() {
		t.Error("unknown must pass")
	}
}

// TestActFinishResultRealItem runs the finish function on a real inventory
// action (image prune) with a moby-shaped response.
func TestActFinishResultRealItem(t *testing.T) {
	resp := &action.InvokeResponse{}
	obj := actFinishResult(context.Background(), resp, nil, "engine:image-prune", map[string]any{
		"result": map[string]any{"ImagesDeleted": []any{map[string]any{"Deleted": "sha256:1"}}, "SpaceReclaimed": 42},
	}, types.StringNull())
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	r, _ := obj["result"].(map[string]any)
	if r == nil || r["space_reclaimed"] != json.Number("42") {
		t.Errorf("result: %v", obj["result"])
	}
}
