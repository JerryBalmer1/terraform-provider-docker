package provider

import (
	"strings"
	"testing"
)

// Pass 6b Lane Z: `target.<name>.<attr>` sees every bake target attribute,
// null when the target leaves it unset, as buildx v0.38.2 stores the whole
// decoded Target struct (bake/hclparser/hclparser.go:558-580,
// bake/bake.go:895-929). inherits are not applied to the referenced value.
const bakeUnsetRefHCL = `
target "_common" {
  args = { VERSION = "1.4" }
}

target "app" {
  inherits = ["_common"]
  context  = "app"
}

target "worker" {
  context = "worker"
  # unset attributes of app are null, not missing
  args    = target.app.args == null ? { FROM_APP = "none" } : target.app.args
  tags    = [target.app.dockerfile == null ? "w:default-dockerfile" : "w:other"]
  labels  = {
    version     = target._common.args.VERSION
    app-context = target.app.context
    no-cache    = target.app.no-cache == null ? "unset" : "set"
    desc        = "[${target.app.description}]"
  }
  platforms = target.app.platforms == null ? ["linux/amd64"] : target.app.platforms
}
`

func TestBakeTargetRefUnsetAttrsP6bZ2(t *testing.T) {
	def, err := bakeParse([]bakeFile{{Name: "docker-bake.hcl", Data: []byte(bakeUnsetRefHCL)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.resolve([]string{"worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := res.Targets[0]
	if w.Args["FROM_APP"] == nil || *w.Args["FROM_APP"] != "none" {
		t.Errorf("args = %v", w.printMap())
	}
	if strings.Join(w.Tags, ",") != "w:default-dockerfile" {
		t.Errorf("tags = %v", w.Tags)
	}
	for k, want := range map[string]string{"version": "1.4", "app-context": "app", "no-cache": "unset", "desc": "[]"} {
		if w.Labels[k] == nil || *w.Labels[k] != want {
			t.Errorf("label %s = %v, want %q", k, w.Labels[k], want)
		}
	}
	if strings.Join(w.Platforms, ",") != "linux/amd64" {
		t.Errorf("platforms = %v", w.Platforms)
	}

	// Indexing an unset (null) map fails, as in buildx: app's args come
	// only from inherits, which a reference does not see.
	_, err = bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(`
target "_common" {
  args = { VERSION = "1" }
}
target "app" {
  inherits = ["_common"]
}
target "worker" {
  labels = { v = target.app.args.VERSION }
}`)}}, nil, nil)
	if err == nil || strings.Contains(err.Error(), "does not have an attribute named") {
		t.Errorf("want a null-value error, got %v", err)
	}

	// Every attribute buildx exposes is present on the referenced object.
	for k := range bakeTargetAttrNulls {
		hcl := "target \"a\" {\n  context = \".\"\n}\ntarget \"b\" {\n  description = target.a[\"" + k + "\"] == null ? \"null\" : \"set\"\n}\n"
		if _, err := bakeParse([]bakeFile{{Name: "b.hcl", Data: []byte(hcl)}}, nil, nil); err != nil {
			t.Errorf("target.a[%q]: %v", k, err)
		}
	}
}
