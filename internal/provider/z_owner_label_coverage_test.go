package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// inventoryRegistered returns the registered names of the inventory items
// of one kind ("resource", "action"), without the "action." prefix.
func inventoryRegistered(t *testing.T, kind string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "reference", "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["layer"]; ok {
				if k, _ := x["kind"].(string); k == kind {
					if r, _ := x["registered"].(string); r != "" {
						out[strings.TrimPrefix(r, kind+".")] = true
					}
				}
				return
			}
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(raw)
	return out
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestOwnerLabelCoverage (Pass 5.3 Lane Z) keeps the owner label from being
// silently missed by a new type. Two lists are matched against the
// inventory and the provider's registrations:
//   - resources: every registered resource is in ownerLabelledTypes (inject +
//     conflict check + adopt) or ownerLabelExclusions (with a reason);
//   - actions: every registered action is in ownerInjectOnlyActions (inject
//     only), ownerLabelExclusions, or ownerNoObjectActions (creates nothing
//     labellable).
//
// Each type sits in exactly one list, and the lists hold the right kind.
func TestOwnerLabelCoverage(t *testing.T) {
	srvRes, srvAct := providerResourceAndActionNames(t)
	invRes, invAct := inventoryRegistered(t, "resource"), inventoryRegistered(t, "action")
	for _, n := range sortedNames(srvRes) {
		if !invRes[n] {
			t.Errorf("resource %s is registered but not in reference/inventory.json", n)
		}
	}
	for _, n := range sortedNames(srvAct) {
		if !invAct[n] {
			t.Errorf("action %s is registered but not in reference/inventory.json", n)
		}
	}

	// Resources: inject + check, or a documented exclusion.
	for _, n := range sortedNames(invRes) {
		_, full := ownerLabelledTypes[n]
		_, excl := ownerLabelExclusions[n]
		switch {
		case full && excl:
			t.Errorf("resource %s is both owner-labelled and excluded", n)
		case !full && !excl:
			t.Errorf("resource %s is in neither ownerLabelledTypes (inject + check + adopt) nor ownerLabelExclusions", n)
		}
	}
	for n := range ownerLabelledTypes {
		if !invRes[n] {
			t.Errorf("ownerLabelledTypes lists %s, which is not a resource in the inventory", n)
		}
	}

	// Actions: inject-only, a documented exclusion, or creates nothing.
	for _, n := range sortedNames(invAct) {
		k := 0
		for _, m := range []map[string]string{ownerInjectOnlyActions, ownerLabelExclusions, ownerNoObjectActions} {
			if _, ok := m[n]; ok {
				k++
			}
		}
		if k != 1 {
			t.Errorf("action %s is in %d of ownerInjectOnlyActions / ownerLabelExclusions / ownerNoObjectActions, want exactly 1", n, k)
		}
	}
	for n := range ownerInjectOnlyActions {
		if !invAct[n] {
			t.Errorf("ownerInjectOnlyActions lists %s, which is not an action in the inventory", n)
		}
	}
	for n := range ownerNoObjectActions {
		if !invAct[n] {
			t.Errorf("ownerNoObjectActions lists %s, which is not an action in the inventory", n)
		}
	}
	for n, why := range ownerLabelExclusions {
		if !invRes[n] && !invAct[n] {
			t.Errorf("ownerLabelExclusions lists %s, which is neither a resource nor an action in the inventory", n)
		}
		if len(why) < 20 {
			t.Errorf("exclusion %s: reason %q is too short", n, why)
		}
	}
}

// TestOwnerLabelExclusionsInContract: every exclusion is documented in
// reference/CONTRACT.md's owner-label section with its reason.
func TestOwnerLabelExclusionsInContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "reference", "CONTRACT.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "## Owner label")
	if i < 0 {
		t.Fatal("reference/CONTRACT.md has no \"## Owner label\" section")
	}
	sec := doc[i:]
	if j := strings.Index(sec[3:], "\n## "); j >= 0 {
		sec = sec[:j+3]
	}
	for n, why := range ownerLabelExclusions {
		if !strings.Contains(sec, "`"+n+"`") || !strings.Contains(sec, why) {
			t.Errorf("CONTRACT.md owner-label section does not list `%s` with its reason %q", n, why)
		}
	}
}

func providerResourceAndActionNames(t *testing.T) (map[string]bool, map[string]bool) {
	t.Helper()
	srv, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	res, act := map[string]bool{}, map[string]bool{}
	for n := range resp.ResourceSchemas {
		res[n] = true
	}
	for n := range resp.ActionSchemas {
		act[n] = true
	}
	return res, act
}
