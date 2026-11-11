package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRegistry = `package provider

func genResources() []func() resource.Resource {
	return []func() resource.Resource{
		NewNetworkResource, // resource docker_network
		NewVolumeResource,  // resource docker_volume
	}
}

func genDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewNetworkDataSource, // data_source docker_network
	}
}

func genActions() []func() action.Action {
	return []func() action.Action{
		NewExecAction,        // action docker_exec
		NewSystemPruneAction, // action docker_system_prune
		NewImagePruneAction,  // action docker_image_prune
	}
}

func genEphemeralResources() []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		NewSwarmTokensEphemeralResource, // ephemeral_resource docker_swarm_tokens
	}
}

func genListResources() []func() list.ListResource {
	return []func() list.ListResource{
		NewVolumeListResource, // list_resource docker_volume
	}
}

func genFunctions() []func() function.Function {
	return []func() function.Function{
		NewPortSpecFunction,  // function port_spec
		NewImageRefFunction,  // function image_ref
	}
}
`

func testReg(t *testing.T) Registry {
	t.Helper()
	r, err := ParseRegistry(testRegistry)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseRegistry(t *testing.T) {
	r := testReg(t)
	want := map[string]int{"resource": 2, "data_source": 1, "action": 3, "ephemeral_resource": 1, "list_resource": 1, "function": 2}
	for k, n := range want {
		if len(r[k]) != n {
			t.Errorf("%s: %d names, want %d (%v)", k, len(r[k]), n, r[k])
		}
	}
	if got := strings.Join(r["function"], ","); got != "image_ref,port_spec" {
		t.Errorf("functions not sorted: %s", got)
	}
	if _, err := ParseRegistry("package provider\n"); err == nil {
		t.Error("empty registry: want an error")
	}
	if _, err := ParseRegistry("\t\tNewX, // widget docker_x\n"); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("unknown kind: err = %v", err)
	}
}

func TestBlock(t *testing.T) {
	b := Block(testReg(t))
	for _, s := range []string{
		"Registered: 2 resources, 1 data sources, 3 actions, 1 ephemeral resources, 2 provider functions, 1 list resources.",
		"- resources (2): `docker_network`, `docker_volume`",
		"- ephemeral resources (1): `docker_swarm_tokens`",
		"- provider functions (2): `image_ref`, `port_spec`",
		"- list resources (1): `docker_volume`",
	} {
		if !strings.Contains(b, s) {
			t.Errorf("block lacks %q:\n%s", s, b)
		}
	}
	if strings.Contains(b, "- actions") || strings.Contains(b, "- data sources") {
		t.Errorf("long kinds must be counts only:\n%s", b)
	}
}

func TestCheckAndWrite(t *testing.T) {
	r := testReg(t)
	doc := "# Doc\n\n" + beginLine + "\nstale\n" + endMarker + "\n\nSee `docker_network` and `docker_exec`.\n"
	probs := Check(doc, r, true)
	if len(probs) != 1 || !strings.Contains(probs[0].Msg, "stale") || probs[0].Line != 3 {
		t.Fatalf("stale block: %v", probs)
	}
	out, ok := Write(doc, r)
	if !ok {
		t.Fatal("Write: block not found")
	}
	if p := Check(out, r, true); len(p) != 0 {
		t.Fatalf("after Write: %v\n%s", p, out)
	}
	if again, _ := Write(out, r); again != out {
		t.Error("Write is not idempotent")
	}
	// CRLF documents keep CRLF and still check clean.
	crlf := strings.ReplaceAll(doc, "\n", "\r\n")
	outCRLF, _ := Write(crlf, r)
	if strings.Count(outCRLF, "\n") != strings.Count(outCRLF, "\r\n") {
		t.Error("CRLF document lost its line endings")
	}
	if p := Check(outCRLF, r, true); len(p) != 0 {
		t.Fatalf("CRLF after Write: %v", p)
	}
}

func TestCheckCountsAndNames(t *testing.T) {
	r := testReg(t)
	doc := strings.Join([]string{
		"It registers 3 ephemeral resources and 2 provider functions.", // 1 wrong (ephemeral)
		"Terraform 1.14 actions need nothing.",                         // version, not a count
		"There are 3 actions and 1 list resource.",                     // both right (singular ok)
		"Also `docker_ghost` and `docker_volume`.",                     // docker_ghost is not registered
		"And 5 resources.", // wrong
	}, "\n")
	probs := Check(doc, r, false)
	var msgs []string
	for _, p := range probs {
		msgs = append(msgs, p.String())
	}
	got := strings.Join(msgs, "\n")
	if len(probs) != 3 {
		t.Fatalf("want 3 problems, got:\n%s", got)
	}
	for _, s := range []string{`1: says "3 ephemeral resources", the registry has 1`, "4: `docker_ghost`", `5: says "5 resources", the registry has 2`} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in:\n%s", s, got)
		}
	}
	if p := Check(doc, r, true); len(p) != 4 || !strings.Contains(p[0].Msg, "no generated doccounts block") {
		t.Errorf("requireBlock: %v", p)
	}
	if p := Check(beginLine+"\nno end\n", r, false); len(p) != 1 || !strings.Contains(p[0].Msg, "no end marker") {
		t.Errorf("unterminated block: %v", p)
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "registry_gen.go")
	doc := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(reg, []byte(testRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("x\n"+beginLine+"\n"+endMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := run(reg, []string{doc}, []string{doc}, nil, EvidenceSources{}, false); rc != 1 {
		t.Errorf("check of a stale doc: rc %d, want 1", rc)
	}
	if rc := run(reg, []string{doc}, []string{doc}, nil, EvidenceSources{}, true); rc != 0 {
		t.Errorf("write: rc %d, want 0", rc)
	}
	if rc := run(reg, []string{doc}, []string{doc}, nil, EvidenceSources{}, false); rc != 0 {
		t.Errorf("check after write: rc %d, want 0", rc)
	}
	if rc := run(filepath.Join(dir, "missing.go"), []string{doc}, nil, nil, EvidenceSources{}, false); rc != 2 {
		t.Errorf("missing registry: rc %d, want 2", rc)
	}
}
