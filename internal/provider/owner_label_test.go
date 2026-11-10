package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

type fakePrivate map[string][]byte

func (f fakePrivate) SetKey(_ context.Context, k string, v []byte) diag.Diagnostics {
	f[k] = v
	return nil
}

func (f fakePrivate) GetKey(_ context.Context, k string) ([]byte, diag.Diagnostics) { return f[k], nil }

func TestOwnerInjectStrip(t *testing.T) {
	off := &providerData{}
	in := map[string]string{"a": "1"}
	if got := off.ownerInject(in); len(got) != 1 {
		t.Errorf("owner unset must not label: %v", got)
	}
	if got := off.ownerInject(nil); got != nil {
		t.Errorf("nil stays nil: %v", got)
	}
	on := &providerData{Owner: "team-a"}
	got := on.ownerInject(in)
	if got[ownerLabelKey] != "team-a" || got["a"] != "1" || len(in) != 1 {
		t.Errorf("inject = %v (input %v)", got, in)
	}
	if s := ownerStrip(got); len(s) != 1 || s["a"] != "1" {
		t.Errorf("strip = %v", s)
	}
}

func TestOwnerCreateCheck(t *testing.T) {
	on := &providerData{Owner: "team-a"}
	if err := ownerCreateCheck(&providerData{}, "network", "n", map[string]string{ownerLabelKey: "team-b"}); err != nil {
		t.Errorf("owner unset: %v", err)
	}
	if err := ownerCreateCheck(on, "network", "n", map[string]string{ownerLabelKey: "team-a"}); err != nil {
		t.Errorf("same owner: %v", err)
	}
	if err := ownerCreateCheck(on, "network", "n", map[string]string{"x": "y"}); err != nil {
		t.Errorf("unlabelled existing object: %v", err)
	}
	err := ownerCreateCheck(on, "network", "n", map[string]string{ownerLabelKey: "team-b"})
	if err == nil || !strings.Contains(err.Error(), `owned by "team-b"`) || !strings.Contains(err.Error(), "adopt = true") {
		t.Errorf("conflict error = %v", err)
	}
}

func TestOwnerPlanCheck(t *testing.T) {
	ctx := context.Background()
	on := &providerData{Owner: "team-a"}
	priv := fakePrivate{}
	if d := ownerRecord(ctx, priv, map[string]string{ownerLabelKey: "team-b"}); d.HasError() {
		t.Fatal(d)
	}
	if d := ownerPlanCheck(ctx, on, priv, "volume", "v", types.BoolNull()); !d.HasError() {
		t.Error("a foreign owner without adopt must fail the plan")
	}
	if d := ownerPlanCheck(ctx, on, priv, "volume", "v", types.BoolValue(true)); d.HasError() {
		t.Errorf("adopt = true: %v", d)
	}
	if d := ownerPlanCheck(ctx, &providerData{}, priv, "volume", "v", types.BoolNull()); d.HasError() {
		t.Errorf("owner unset: %v", d)
	}
	same := fakePrivate{}
	_ = ownerRecord(ctx, same, map[string]string{ownerLabelKey: "team-a"})
	if d := ownerPlanCheck(ctx, on, same, "volume", "v", types.BoolNull()); d.HasError() {
		t.Errorf("own object: %v", d)
	}
	if d := ownerPlanCheck(ctx, on, fakePrivate{}, "volume", "v", types.BoolNull()); d.HasError() {
		t.Errorf("nothing recorded: %v", d)
	}
}

// TestOwnerLabelledTypesRegistered keeps the three owner-label lists
// honest: every listed type is registered (docker_container_run arrives with
// Lane A), each lane is one of this pass's lanes, no type is in two lists,
// and every exclusion has a reason. Lane Z adds TestOwnerLabelCoverage, which
// also requires every registered resource and action to be in one list.
func TestOwnerLabelledTypesRegistered(t *testing.T) {
	names := registeredTypeNames(t)
	seen := map[string]string{}
	check := func(list string, m map[string]string, lanes bool) {
		for typ, v := range m {
			if prev, dup := seen[typ]; dup {
				t.Errorf("%s is in both %s and %s", typ, prev, list)
			}
			seen[typ] = list
			if !names[typ] && typ != "docker_container_run" {
				t.Errorf("%s lists %s, which is not registered", list, typ)
			}
			if lanes && (len(v) != 1 || !strings.Contains("ARSKBZ", v)) {
				t.Errorf("%s: %s: lane %q", list, typ, v)
			}
			if !lanes && len(v) < 20 {
				t.Errorf("%s: %s: reason %q is too short", list, typ, v)
			}
		}
	}
	check("ownerLabelledTypes", ownerLabelledTypes, true)
	check("ownerInjectOnlyActions", ownerInjectOnlyActions, true)
	check("ownerLabelExclusions", ownerLabelExclusions, false)
}

// registeredTypeNames returns every resource, action and ephemeral resource
// type name the provider registers.
func registeredTypeNames(t *testing.T) map[string]bool {
	t.Helper()
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for n := range resp.ResourceSchemas {
		out[n] = true
	}
	for n := range resp.ActionSchemas {
		out[n] = true
	}
	for n := range resp.EphemeralResourceSchemas {
		out[n] = true
	}
	return out
}
