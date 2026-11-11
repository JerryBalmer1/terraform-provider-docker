package provider

// Pass 6c Lane F (open finding 1): docker_exec.stdin_path, a local file read
// at invoke time, so a secret passed to the command's stdin never shows in
// plan output (action arguments cannot be marked sensitive).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestExecActionStdinPathP6cF(t *testing.T) {
	f, pd := newP3eFake(t)
	x := newExecStdinFake(t, f)
	dir := t.TempDir()
	secret := "s3cr3t-value\n"
	in := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(in, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "exec.json")
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("cat"),
		"stdin_path":   actTfStr(in),
		"result_path":  actTfStr(resultPath),
	})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	x.check(t, secret)
	b, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"stdin`) {
		t.Errorf("result file carries stdin: %s", b)
	}
}

func TestExecActionStdinPathMissingP6cF(t *testing.T) {
	f, pd := newP3eFake(t)
	_ = newExecStdinFake(t, f)
	missing := filepath.Join(t.TempDir(), "nope.txt")
	res := actInvokePD(t, pd, NewExecAction(), map[string]tftypes.Value{
		"container_id": actTfStr("web"),
		"command":      actTfList("cat"),
		"stdin_path":   actTfStr(missing),
	})
	if !res.Diags.HasError() || !strings.Contains(res.errText(), "Cannot read stdin_path") {
		t.Fatalf("want a stdin_path read error, got %s", res.errText())
	}
}

func TestExecActionStdinPathValidateP6cF(t *testing.T) {
	for name, tc := range map[string]struct {
		extra map[string]tftypes.Value
		want  string
	}{
		"both":   {map[string]tftypes.Value{"stdin": actTfStr("x")}, "mutually exclusive"},
		"tty":    {map[string]tftypes.Value{"tty": actTfBool(true)}, "stdin_path needs tty = false"},
		"detach": {map[string]tftypes.Value{"detach": actTfBool(true)}, "stdin_path cannot be used with detach"},
	} {
		vals := map[string]tftypes.Value{
			"container_id": actTfStr("web"),
			"command":      actTfList("cat"),
			"stdin_path":   actTfStr("/some/file"),
		}
		for k, v := range tc.extra {
			vals[k] = v
		}
		d := actValidate(t, NewExecAction(), vals)
		if d.ErrorsCount() != 1 || !strings.Contains(d.Errors()[0].Summary(), tc.want) {
			t.Errorf("%s: want one %q error, got %v", name, tc.want, d)
		}
	}
}

func TestExecActionStdinPathDescriptionP6cF(t *testing.T) {
	var sr action.SchemaResponse
	NewExecAction().Schema(context.Background(), action.SchemaRequest{}, &sr)
	for _, a := range []string{"stdin", "stdin_path"} {
		d := sr.Schema.Attributes[a].GetMarkdownDescription()
		if !strings.Contains(d, "plan output") || !strings.Contains(d, "secret") || (a == "stdin" && !strings.Contains(d, "`stdin_path`")) {
			t.Errorf("%s description does not point secrets at stdin_path: %s", a, d)
		}
	}
}
