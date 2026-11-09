package provider

import (
	"reflect"
	"strings"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
)

func TestDockerfileSeamRegistered(t *testing.T) {
	if _, ok := seamDockerfileImpl.(dockerfileSeam); !ok {
		t.Fatalf("seamDockerfileImpl is %T, want dockerfileSeam", seamDockerfileImpl)
	}
	doc, err := seamDockerfileImpl.Parse("FROM a AS b\nRUN x\n")
	if err != nil || len(doc.Stages) != 1 || doc.Stages[0].Name != "b" {
		t.Fatalf("parse: %v %+v", err, doc)
	}
}

func TestDockerfileRenderErrors(t *testing.T) {
	cases := map[string]seamDockerfileDoc{
		"no stage":     {},
		"bad escape":   {Escape: "x", Stages: []seamDockerfileStage{{From: "a"}}},
		"global run":   {GlobalArgs: []seamDockerfileInstruction{{Keyword: "RUN", Args: []string{"x"}}}, Stages: []seamDockerfileStage{{From: "a"}}},
		"empty from":   {Stages: []seamDockerfileStage{{}}},
		"bad keyword":  {Stages: []seamDockerfileStage{{From: "a", Instructions: []seamDockerfileInstruction{{Keyword: "NOPE"}}}}},
		"shell plain":  {Stages: []seamDockerfileStage{{From: "a", Instructions: []seamDockerfileInstruction{{Keyword: "SHELL", Args: []string{"sh"}}}}}},
		"onbuild main": {Stages: []seamDockerfileStage{{From: "a", Instructions: []seamDockerfileInstruction{{Keyword: "MAINTAINER", Args: []string{"x"}, OnBuild: true}}}}},
		"heredoc name": {Stages: []seamDockerfileStage{{From: "a", Instructions: []seamDockerfileInstruction{{Keyword: "RUN", Args: []string{"<<EOF"}, Heredocs: []seamDockerfileHeredoc{{}}}}}}},
	}
	for name, doc := range cases {
		if _, err := seamDockerfileImpl.Render(doc); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDockerfileParseErrors(t *testing.T) {
	for name, src := range map[string]string{
		"empty":          "",
		"before from":    "RUN x\nFROM a\n",
		"unknown":        "FROM a\nBOGUS x\n",
		"from flag":      "FROM --foo=bar a\n",
		"from words":     "FROM a b c d\n",
		"check key":      "# check=foo=bar\nFROM a\n",
		"check error":    "# check=error=maybe\nFROM a\n",
		"onbuild from":   "FROM a\nONBUILD FROM b\n",
		"unterminated":   "FROM a\nRUN <<EOF\nx\n",
		"bad escape tok": "# escape=x\nFROM a\n",
	} {
		if _, err := seamDockerfileImpl.Parse(src); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDockerfileDirectivesAndComments(t *testing.T) {
	src := "# syntax = docker/dockerfile:1.4\n# check=skip=StageNameCasing, FromAsCasing;error=true\n# a comment\nFROM a\n# above run\n#\n# second\nRUN x\n"
	p, err := dockerfileParse(src)
	if err != nil {
		t.Fatal(err)
	}
	d := p.Doc
	if d.Syntax != "docker/dockerfile:1.4" || !reflect.DeepEqual(d.Check.Skip, []string{"StageNameCasing", "FromAsCasing"}) || !d.Check.Error {
		t.Errorf("directives %+v", d)
	}
	if !reflect.DeepEqual(d.Stages[0].Comments, []string{"a comment"}) || !reflect.DeepEqual(d.Stages[0].Instructions[0].Comments, []string{"second"}) {
		t.Errorf("comments %+v", d.Stages[0])
	}
	if p.FromLines[0] != 4 || p.StageLines[0][0] != 8 {
		t.Errorf("lines %v %v", p.FromLines, p.StageLines)
	}
	// A leading comment that looks like a directive stays a comment.
	doc := seamDockerfileDoc{Stages: []seamDockerfileStage{{From: "a", Comments: []string{"syntax=not-a-directive"}}}}
	text := dockerfileAssertRoundTrip(t, "directive-like comment", doc)
	if !strings.HasPrefix(text, "\n# syntax=") {
		t.Errorf("rendered %q", text)
	}
}

func TestDockerfileSplitAndQuote(t *testing.T) {
	if got := dockerfileSplitCSV(`type=secret,id="a,b",required`); !reflect.DeepEqual(got, []string{"type=secret", `id="a,b"`, "required"}) {
		t.Errorf("split %q", got)
	}
	if got := dockerfileQuoteFlag("a b", `\`); got != `"a b"` {
		t.Errorf("quote flag %s", got)
	}
	if got := dockerfileQuoteWord(`say "hi" \o/`, `\`); got != `"say \"hi\" \\o/"` {
		t.Errorf("quote word %s", got)
	}
	if got := dockerfileUnquote(`"say \"hi\" $HOME"`, `\`); got != `say "hi" $HOME` {
		t.Errorf("unquote %s", got)
	}
	// Flags with quoted values round-trip.
	doc := seamDockerfileDoc{Stages: []seamDockerfileStage{{From: "a", Instructions: []seamDockerfileInstruction{
		{Keyword: "COPY", Flags: []seamDockerfileFlag{{Name: "chown", Values: []string{"a b"}}, {Name: "link"}, {Name: "exclude", Values: []string{""}}}, Args: []string{"x", "y"}},
	}}}}
	dockerfileAssertRoundTrip(t, "quoted flags", doc)
}

func TestDockerfileKeywordValues(t *testing.T) {
	p, err := dockerfileParse(`FROM a
RUN --device=nvidia.com/gpu=all,required --mount=type=ssh,required --mount=type=tmpfs,target=/t,size=10m --security=insecure ["make"]
COPY --parents --chmod=u=rwX --exclude=*.tmp --exclude=*.log a b /dst/
ADD --unpack=false --keep-git-dir https://x/y.git /src
ARG TARGETOS HTTP_PROXY SOURCE_DATE_EPOCH mine=1
HEALTHCHECK --start-interval=2s --start-period=1s --timeout=3s CMD ["probe"]
USER 1000
VOLUME ["/a", "/b"]
ENV LEGACY a b c
`)
	if err != nil {
		t.Fatal(err)
	}
	ins := p.Doc.Stages[0].Instructions
	run := dockerfileKeywordValue(ins[0], `\`, "stage")
	if run["form"] != "exec" || run["security"] != "insecure" || run["min_syntax"] != "dockerfile:1.27" {
		t.Errorf("run %v", run)
	}
	dev := run["device"].([]any)[0].(map[string]any)
	if dev["name"] != "nvidia.com/gpu=all" || dev["required"] != true {
		t.Errorf("device %v", dev)
	}
	mounts := run["mount"].([]any)
	if ssh := mounts[0].(map[string]any)["ssh"].(map[string]any); ssh["required"] != true {
		t.Errorf("ssh mount %v", mounts[0])
	}
	if tmp := mounts[1].(map[string]any)["tmpfs"].(map[string]any); tmp["size"] != "10m" || tmp["target"] != "/t" {
		t.Errorf("tmpfs mount %v", mounts[1])
	}
	cp := dockerfileKeywordValue(ins[1], `\`, "stage")
	if cp["parents"] != true || cp["min_syntax"] != "dockerfile:1.20" || !reflect.DeepEqual(cp["exclude"], []string{"*.tmp", "*.log"}) || !reflect.DeepEqual(cp["sources"], []string{"a", "b"}) {
		t.Errorf("copy %v", cp)
	}
	add := dockerfileKeywordValue(ins[2], `\`, "stage")
	if add["unpack"] != false || add["keep_git_dir"] != true || add["min_syntax"] != "dockerfile:1.17" {
		t.Errorf("add %v", add)
	}
	arg := dockerfileKeywordValue(ins[3], `\`, "stage")["args"].([]any)
	want := []string{"platform", "predefined", "buildkit", ""}
	for i, a := range arg {
		b, _ := a.(map[string]any)["builtin"].(string)
		if b != want[i] {
			t.Errorf("arg %d builtin %q want %q", i, b, want[i])
		}
	}
	hc := dockerfileKeywordValue(ins[4], `\`, "stage")
	if hc["start_interval"] != "2s" || hc["cmd"].(map[string]any)["form"] != "exec" {
		t.Errorf("healthcheck %v", hc)
	}
	if u := dockerfileKeywordValue(ins[5], `\`, "stage"); u["user"] != "1000" || u["group"] != nil {
		t.Errorf("user %v", u)
	}
	if v := dockerfileKeywordValue(ins[6], `\`, "stage"); v["form"] != "json" {
		t.Errorf("volume %v", v)
	}
	if e := dockerfileKeywordValue(ins[7], `\`, "stage"); e["legacy_form"] != true || e["env"].(map[string]any)["LEGACY"] != "a b c" {
		t.Errorf("env %v", e)
	}
}

func TestDockerfileHistoryLine(t *testing.T) {
	for in, want := range map[string]string{
		"":                                          "",
		"/bin/sh -c #(nop)  CMD [\"sh\"]":           `CMD ["sh"]`,
		"/bin/sh -c #(nop) ADD file:ab in / ":       "ADD file:ab /",
		"/bin/sh -c apk add curl":                   "RUN apk add curl",
		"|2 A=1 B=2 /bin/sh -c make":                "RUN make",
		"RUN |1 X=y /bin/sh -c go build # buildkit": "RUN go build",
		"EXPOSE map[80/tcp:{} 443/tcp:{}]":          "EXPOSE 80/tcp 443/tcp",
		"VOLUME [/data /logs]":                      "VOLUME /data /logs",
		"HEALTHCHECK &{...}":                        "HEALTHCHECK",
		"COPY . /app # buildkit":                    "COPY . /app",
		"FROM alpine":                               "",
		"bogus":                                     "",
		"cmd /S /C dir":                             "RUN dir",
	} {
		if got := dockerfileHistoryLine(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestDockerfileHealthcheckInstruction(t *testing.T) {
	ins, ok := dockerfileHealthcheckInstruction(&dockerspec.HealthcheckConfig{Test: []string{"CMD", "a", "b"}, Timeout: 3 * time.Second, StartPeriod: time.Second, StartInterval: 2 * time.Second})
	if !ok || !ins.JSONForm || len(ins.Flags) != 3 {
		t.Errorf("%+v", ins)
	}
	if ins, ok := dockerfileHealthcheckInstruction(&dockerspec.HealthcheckConfig{Test: []string{"NONE"}}); !ok || ins.Args[0] != "NONE" {
		t.Errorf("%+v", ins)
	}
	if _, ok := dockerfileHealthcheckInstruction(nil); ok {
		t.Error("nil healthcheck")
	}
	if _, ok := dockerfileHealthcheckInstruction(&dockerspec.HealthcheckConfig{Test: []string{"WHAT"}}); ok {
		t.Error("unknown test kind")
	}
}

func TestDockerfileUnknownCheckRules(t *testing.T) {
	if got := dockerfileUnknownCheckRules([]string{"all", "jsonargsrecommended", "Nope"}); !reflect.DeepEqual(got, []string{"Nope"}) {
		t.Errorf("%v", got)
	}
	if len(dockerfileCheckRules) != 21 {
		t.Errorf("pinned rule list has %d names, want 21", len(dockerfileCheckRules))
	}
}

func TestDockerfileMaxSyntax(t *testing.T) {
	if dockerfileMaxSyntax("1.4", "1.20") != "1.20" || dockerfileMaxSyntax("1.20", "1.4") != "1.20" || dockerfileMaxSyntax("", "1.2") != "1.2" {
		t.Error("max syntax")
	}
}
