package provider

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Helpers for the Lane D action unit tests. They reuse the Lane 3 fake
// Engine (swarmNewFakeEngine) and call Invoke directly.

func actTfStr(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
func actTfBool(b bool) tftypes.Value  { return tftypes.NewValue(tftypes.Bool, b) }
func actTfNum(n int64) tftypes.Value {
	return tftypes.NewValue(tftypes.Number, big.NewFloat(float64(n)))
}

func actTfList(ss ...string) tftypes.Value {
	vals := make([]tftypes.Value, len(ss))
	for i, s := range ss {
		vals[i] = actTfStr(s)
	}
	return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, vals)
}

func actTfMap(m map[string]string) tftypes.Value {
	vals := map[string]tftypes.Value{}
	for k, v := range m {
		vals[k] = actTfStr(v)
	}
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, vals)
}

// actTestConfig builds the action's config with vals set and every other
// attribute null.
func actTestConfig(t *testing.T, a action.Action, vals map[string]tftypes.Value) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	var sr action.SchemaResponse
	a.Schema(ctx, action.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics)
	}
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	m := map[string]tftypes.Value{}
	for k, at := range typ.AttributeTypes {
		if v, ok := vals[k]; ok {
			m[k] = v
		} else {
			m[k] = tftypes.NewValue(at, nil)
		}
	}
	for k := range vals {
		if _, ok := typ.AttributeTypes[k]; !ok {
			t.Fatalf("attribute %q is not in the schema", k)
		}
	}
	return tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(typ, m)}
}

// actResult is what one Invoke produced.
type actResult struct {
	Diags    diag.Diagnostics
	Progress []string
}

func (r actResult) errText() string {
	var b strings.Builder
	for _, d := range r.Diags.Errors() {
		b.WriteString(d.Summary() + ": " + d.Detail() + "\n")
	}
	return b.String()
}

// actInvoke configures a against the fake engine and invokes it.
func actInvoke(t *testing.T, f *swarmFakeEngine, a action.Action, vals map[string]tftypes.Value) actResult {
	t.Helper()
	return actInvokePD(t, &providerData{Client: f.client(t)}, a, vals)
}

func actInvokePD(t *testing.T, pd *providerData, a action.Action, vals map[string]tftypes.Value) actResult {
	t.Helper()
	ctx := context.Background()
	if ac, ok := a.(action.ActionWithConfigure); ok {
		var cr action.ConfigureResponse
		ac.Configure(ctx, action.ConfigureRequest{ProviderData: pd}, &cr)
		if cr.Diagnostics.HasError() {
			t.Fatalf("configure: %v", cr.Diagnostics)
		}
	}
	var mu sync.Mutex
	var res actResult
	resp := action.InvokeResponse{SendProgress: func(e action.InvokeProgressEvent) {
		mu.Lock()
		defer mu.Unlock()
		res.Progress = append(res.Progress, e.Message)
	}}
	a.Invoke(ctx, action.InvokeRequest{Config: actTestConfig(t, a, vals)}, &resp)
	res.Diags = resp.Diagnostics
	return res
}

// actValidate runs ValidateConfig.
func actValidate(t *testing.T, a action.Action, vals map[string]tftypes.Value) diag.Diagnostics {
	t.Helper()
	av, ok := a.(action.ActionWithValidateConfig)
	if !ok {
		t.Fatalf("%T has no ValidateConfig", a)
	}
	var resp action.ValidateConfigResponse
	av.ValidateConfig(context.Background(), action.ValidateConfigRequest{Config: actTestConfig(t, a, vals)}, &resp)
	return resp.Diagnostics
}

func actFastPoll(t *testing.T) {
	t.Helper()
	swarmFastPoll(t)
	old := actPollInterval
	actPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { actPollInterval = old })
}

func actHasProgress(r actResult, sub string) bool {
	for _, p := range r.Progress {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

// TestActionsRegistered checks every implemented action is served with a valid
// schema and a standalone first sentence (TestRegistryGen checks the names).
func TestActionsRegistered(t *testing.T) {
	ctx := context.Background()
	for _, f := range genActions() {
		a := f()
		if isPass5Stub(a) {
			continue // TestRegistryGen checks stub names; stubs have no schema to check yet
		}
		var mr action.MetadataResponse
		a.Metadata(ctx, action.MetadataRequest{ProviderTypeName: "docker"}, &mr)
		var sr action.SchemaResponse
		a.Schema(ctx, action.SchemaRequest{}, &sr)
		if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s: %v", mr.TypeName, d)
		}
		desc := sr.Schema.MarkdownDescription
		if i := strings.Index(desc, ". "); i < 20 {
			t.Errorf("%s: first sentence too short or missing: %q", mr.TypeName, desc)
		}
		for name, at := range sr.Schema.Attributes {
			if at.GetMarkdownDescription() == "" {
				t.Errorf("%s.%s has no MarkdownDescription", mr.TypeName, name)
			}
		}
		if _, ok := a.(action.ActionWithConfigure); !ok {
			t.Errorf("%s does not implement Configure", mr.TypeName)
		}
	}
}

// TestActionsUnconfigured checks Invoke fails cleanly without provider data.
func TestActionsUnconfigured(t *testing.T) {
	a := NewImagePruneAction()
	var resp action.InvokeResponse
	a.Invoke(context.Background(), action.InvokeRequest{Config: actTestConfig(t, a, nil)}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error without provider data")
	}
}

func TestActLineWriter(t *testing.T) {
	var got, tail []string
	w := &actLineWriter{prefix: "> ", emit: func(s string) { got = append(got, s) }, tail: &tail}
	_, _ = w.Write([]byte("a\r\nb"))
	_, _ = w.Write([]byte("c\n"))
	_, _ = w.Write([]byte("partial"))
	w.Flush()
	if strings.Join(got, "|") != "> a|> bc|> partial" {
		t.Fatalf("got %q", got)
	}
	for i := 0; i < 30; i++ {
		_, _ = w.Write([]byte("x\n"))
	}
	if len(tail) != actTailLines {
		t.Fatalf("tail len %d", len(tail))
	}
}

// TestActionsServedOverProtocol checks the framework serves every action
// schema through GetProviderSchema without diagnostics.
func TestActionsServedOverProtocol(t *testing.T) {
	srv, err := testAccProtoV6ProviderFactories["docker"]()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		t.Errorf("%s: %s", d.Summary, d.Detail)
	}
	if got, want := len(resp.ActionSchemas), len(genActions()); got != want {
		t.Errorf("%d action schemas served, want %d", got, want)
	}
	for _, n := range []string{"docker_exec", "docker_service_rollback", "docker_node_drain"} {
		if resp.ActionSchemas[n] == nil {
			t.Errorf("%s not served", n)
		}
	}
}

// TestActionExamples parses examples/actions/<name>/action.tf: each must be
// valid HCL with an action block of its type, an action_trigger that
// references it, and the -invoke comment.
func TestActionExamples(t *testing.T) {
	ctx := context.Background()
	for _, f := range genActions() {
		if isPass5Stub(f()) {
			continue
		}
		var mr action.MetadataResponse
		f().Metadata(ctx, action.MetadataRequest{ProviderTypeName: "docker"}, &mr)
		name := mr.TypeName
		p := filepath.Join("..", "..", "examples", "actions", name, "action.tf")
		src, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		file, diags := hclsyntax.ParseConfig(src, p, hcl.InitialPos)
		if diags.HasErrors() {
			t.Errorf("%s: %s", name, diags.Error())
			continue
		}
		var label string
		for _, b := range file.Body.(*hclsyntax.Body).Blocks {
			if b.Type == "action" && len(b.Labels) == 2 && b.Labels[0] == name {
				label = b.Labels[1]
			}
		}
		if label == "" {
			t.Errorf("%s: no action %q block", name, name)
			continue
		}
		ref := "action." + name + "." + label
		s := string(src)
		if !strings.Contains(s, "action_trigger") || !strings.Contains(s, "after_create, after_update") || !strings.Contains(s, "actions = ["+ref+"]") {
			t.Errorf("%s: no lifecycle action_trigger on after_create/after_update for %s", name, ref)
		}
		if !strings.Contains(s, "terraform apply -invoke="+ref) {
			t.Errorf("%s: missing the -invoke comment", name)
		}
	}
}
