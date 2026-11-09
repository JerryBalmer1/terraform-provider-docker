package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func dfTestSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	var sr datasource.SchemaResponse
	(&dockerfileDataSource{}).Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatal(sr.Diagnostics)
	}
	return sr
}

// dfTestRead runs Read with cfg as the configuration (computed values null).
func dfTestRead(t *testing.T, pd *providerData, cfg dockerfileDataSourceModel) (dockerfileDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	sr := dfTestSchema(t)
	cfg.contractDockerfileDataDockerfile.contractSetNull()
	cfg.StageNames = types.ListNull(types.StringType)
	cfg.Labels = types.MapNull(types.StringType)
	dfNullBlockOutputs(&cfg)
	typ := sr.Schema.Type().TerraformType(ctx)
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}
	if d := st.Set(ctx, &cfg); d.HasError() {
		t.Fatalf("building config: %v", d)
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&dockerfileDataSource{pd: pd}).Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: st.Raw}}, &resp)
	var out dockerfileDataSourceModel
	if !resp.Diagnostics.HasError() {
		if d := resp.State.Get(ctx, &out); d.HasError() {
			t.Fatalf("reading state: %v", d)
		}
	}
	return out, resp.Diagnostics
}

func dfTestOK(t *testing.T, diags diag.Diagnostics) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
}

func dfTestErr(t *testing.T, diags diag.Diagnostics, want string) {
	t.Helper()
	if !diags.HasError() {
		t.Fatalf("expected an error containing %q", want)
	}
	for _, d := range diags.Errors() {
		if strings.Contains(d.Summary()+" "+d.Detail(), want) {
			return
		}
	}
	t.Fatalf("no error contains %q: %v", want, diags)
}

// dfTestInstr returns stages[s].instructions[i] as plain values.
func dfTestInstr(t *testing.T, m dockerfileDataSourceModel, s, i int) map[string]attr.Value {
	t.Helper()
	st := m.Stages.Elements()[s].(types.Object).Attributes()
	return st["instructions"].(types.List).Elements()[i].(types.Object).Attributes()
}

func dfTestJSON(t *testing.T, v attr.Value) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(v.(types.String).ValueString()), &out); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, v)
	}
	return out
}

func TestDockerfileDataSourceSchema(t *testing.T) {
	ctx := context.Background()
	sr := dfTestSchema(t)
	if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatal(d)
	}
	var md datasource.MetadataResponse
	NewDockerfileDataSource().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "docker"}, &md)
	if md.TypeName != "docker_dockerfile" {
		t.Fatalf("type name %q", md.TypeName)
	}
	for _, n := range contractIdentityOutputNames(dockerfileItemID) {
		if _, ok := sr.Schema.Attributes[n]; !ok {
			t.Errorf("identity output %s missing", n)
		}
	}
	// Q48: one keyword block per instruction keyword (MAINTAINER excluded, Q29).
	ins := sr.Schema.Blocks["stage"].GetNestedObject().GetBlocks()["instruction"].GetNestedObject()
	for _, kw := range []string{"run", "cmd", "entrypoint", "shell", "label", "expose", "env", "add", "copy", "volume", "user", "workdir", "arg", "stopsignal", "healthcheck"} {
		b, ok := ins.GetBlocks()[kw]
		if !ok {
			t.Errorf("keyword block %s missing", kw)
			continue
		}
		if _, ok := b.GetNestedObject().GetAttributes()["onbuild"]; !ok {
			t.Errorf("%s: onbuild attribute missing (Q30)", kw)
		}
	}
	if _, ok := ins.GetBlocks()["maintainer"]; ok {
		t.Error("maintainer must have no input block (Q29)")
	}
	// The block items' outputs on each instruction block.
	for _, n := range []string{"instruction", "stage_index", "instruction_index", "dockerfile", "content_sha256", "dockerfile_syntax"} {
		if _, ok := ins.GetAttributes()[n]; !ok {
			t.Errorf("instruction block output %s missing", n)
		}
	}
	for _, n := range []string{"syntax", "escape"} {
		if _, ok := sr.Schema.Attributes[n]; !ok {
			t.Errorf("directive attribute %s missing (Q28)", n)
		}
	}
	if _, ok := sr.Schema.Blocks["check"]; !ok {
		t.Error("check block missing (Q28)")
	}
}

const dfTestContent = `# syntax=docker/dockerfile:1.7
ARG GO=1.22
FROM golang:${GO} AS build
RUN --mount=type=cache,target=/root/.cache --network=none go build -o /app
FROM alpine:3.20
LABEL a="x y" org.opencontainers.image.authors=me
EXPOSE 80 53/udp
COPY --from=build --link /app /app
ONBUILD RUN echo hi
MAINTAINER old <old@example.com>
CMD ["/app"]
`

func TestDockerfileDataSourceContent(t *testing.T) {
	m, diags := dfTestRead(t, nil, dockerfileDataSourceModel{Content: types.StringValue(dfTestContent)})
	dfTestOK(t, diags)
	if m.SourceType.ValueString() != "content" || m.ID.ValueString() != "content:"+dockerfileSHA256(dfTestContent) {
		t.Errorf("id/source_type: %s %s", m.ID, m.SourceType)
	}
	if m.Fidelity.ValueString() != "exact" {
		t.Errorf("fidelity %s", m.Fidelity)
	}
	if m.SourceDockerfile.ValueString() != dfTestContent || m.SourceDockerfileSHA256.ValueString() != dockerfileSHA256(dfTestContent) {
		t.Error("source_dockerfile / sha256")
	}
	if m.DockerfileSHA256.ValueString() != dockerfileSHA256(m.Dockerfile.ValueString()) {
		t.Error("dockerfile_sha256")
	}
	if m.DockerfileSyntax.ValueString() != "docker/dockerfile:1.7" {
		t.Errorf("dockerfile_syntax %s", m.DockerfileSyntax)
	}
	if !m.EngineAPIVersion.IsNull() || !m.DockerServerVersion.IsNull() {
		t.Error("engine versions must be null outside image mode")
	}
	if got := m.StageNames.String(); got != `["build","1"]` || m.FinalStage.ValueString() != "1" {
		t.Errorf("stage_names %s final %s", got, m.FinalStage)
	}
	if l := m.Labels.Elements(); len(l) != 2 || l["a"].(types.String).ValueString() != "x y" {
		t.Errorf("labels %v", m.Labels)
	}
	if n := len(m.ExposedPorts.Elements()); n != 2 {
		t.Errorf("exposed_ports %v", m.ExposedPorts)
	}
	bi := m.BaseImages.Elements()
	if len(bi) != 2 {
		t.Fatalf("base_images %v", m.BaseImages)
	}
	if ref := bi[1].(types.Object).Attributes()["image_ref"].(types.String).ValueString(); ref != "docker.io/library/alpine:3.20" {
		t.Errorf("image_ref %s", ref)
	}
	if img := bi[0].(types.Object).Attributes()["image"].(types.String).ValueString(); img != "golang:1.22" {
		t.Errorf("global ARG substitution in base image: %s", img)
	}
	run := dfTestJSON(t, dfTestInstr(t, m, 0, 0)["run"])
	if run["network"] != "none" || run["min_syntax"] != "dockerfile:1.3" {
		t.Errorf("run object %v", run)
	}
	mount := run["mount"].([]any)[0].(map[string]any)
	if mount["type"] != "cache" || mount["cache"].(map[string]any)["target"] != "/root/.cache" {
		t.Errorf("mount %v", mount)
	}
	cp := dfTestJSON(t, dfTestInstr(t, m, 1, 2)["copy"])
	if cp["from"] != "build" || cp["link"] != true || cp["destination"] != "/app" {
		t.Errorf("copy %v", cp)
	}
	ob := dfTestInstr(t, m, 1, 3)
	if ob["keyword"].(types.String).ValueString() != "ONBUILD" {
		t.Errorf("onbuild keyword %v", ob["keyword"])
	}
	if tr := dfTestJSON(t, ob["onbuild"])["trigger"].(map[string]any); tr["keyword"] != "RUN" {
		t.Errorf("onbuild trigger %v", tr)
	}
	mt := dfTestInstr(t, m, 1, 4)
	if dfTestJSON(t, mt["maintainer"])["name"] != "old <old@example.com>" || mt["line"].(types.Number).String() != "10" {
		t.Errorf("maintainer %v", mt)
	}
	ga := m.GlobalArgs.Elements()
	if len(ga) != 1 || ga[0].(types.Object).Attributes()["default"].(types.String).ValueString() != "1.22" {
		t.Errorf("global_args %v", m.GlobalArgs)
	}
	if !m.History.IsNull() {
		t.Error("history must be null outside image mode")
	}
}

func TestDockerfileDataSourcePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte("FROM scratch\r\nCOPY a /a\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, diags := dfTestRead(t, nil, dockerfileDataSourceModel{Path: types.StringValue(p)})
	dfTestOK(t, diags)
	if !strings.HasPrefix(m.ID.ValueString(), "path:") || m.SourceType.ValueString() != "path" {
		t.Errorf("id %s", m.ID)
	}
	if m.Dockerfile.ValueString() != "FROM scratch\nCOPY a /a\n" {
		t.Errorf("rendered %q", m.Dockerfile.ValueString())
	}
	if len(m.BaseImages.Elements()) != 0 {
		t.Error("scratch is not a base image")
	}
	_, diags = dfTestRead(t, nil, dockerfileDataSourceModel{Path: types.StringValue(filepath.Join(dir, "missing"))})
	dfTestErr(t, diags, "Cannot read Dockerfile")
}

func TestDockerfileDataSourceURL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Dockerfile":
			_, _ = w.Write([]byte("FROM alpine\nRUN true\n"))
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := dockerfileHTTPClient
	dockerfileHTTPClient = srv.Client()
	defer func() { dockerfileHTTPClient = old }()

	m, diags := dfTestRead(t, nil, dockerfileDataSourceModel{URL: types.StringValue(srv.URL + "/Dockerfile")})
	dfTestOK(t, diags)
	if m.SourceType.ValueString() != "url" || m.ID.ValueString() != "url:"+srv.URL+"/Dockerfile" {
		t.Errorf("id %s", m.ID)
	}
	_, diags = dfTestRead(t, nil, dockerfileDataSourceModel{URL: types.StringValue(srv.URL + "/missing")})
	dfTestErr(t, diags, "404")
	_, diags = dfTestRead(t, nil, dockerfileDataSourceModel{URL: types.StringValue(srv.URL + "/html")})
	dfTestErr(t, diags, "HTML")
	_, diags = dfTestRead(t, nil, dockerfileDataSourceModel{URL: types.StringValue("http://example.com/Dockerfile")})
	dfTestErr(t, diags, "https://")
}

func TestDockerfileDataSourceImage(t *testing.T) {
	f, pd := newP3eFake(t)
	f.json("GET /images/app:1/json", map[string]any{
		"Id": "sha256:abc", "RepoDigests": []string{"app@sha256:def"},
		"Config": map[string]any{
			"Env": []string{"PATH=/usr/bin", "MODE=prod"}, "Cmd": []string{"/app"}, "ExposedPorts": map[string]any{"8080/tcp": map[string]any{}},
			"Labels": map[string]string{"team": "core"}, "Healthcheck": map[string]any{"Test": []string{"CMD-SHELL", "curl -f localhost"}, "Interval": 30000000000, "Retries": 3},
			"WorkingDir": "/srv",
		},
	})
	f.json("GET /images/app:1/history", []map[string]any{
		{"Id": "sha256:4", "Created": 4, "CreatedBy": `CMD ["/app"]`, "Tags": []string{"app:1"}, "Size": 0, "Comment": "buildkit.dockerfile.v0"},
		{"Id": "<missing>", "Created": 3, "CreatedBy": "HEALTHCHECK &{[\"CMD-SHELL\" \"curl -f localhost\"] \"30s\" \"0s\" \"0s\" \"0s\" '\\x03'}", "Size": 0},
		{"Id": "<missing>", "Created": 3, "CreatedBy": "EXPOSE map[8080/tcp:{}]", "Size": 0},
		{"Id": "<missing>", "Created": 2, "CreatedBy": "RUN |1 MODE=prod /bin/sh -c make build # buildkit", "Size": 1234},
		{"Id": "<missing>", "Created": 2, "CreatedBy": "WORKDIR /srv", "Size": 0},
		{"Id": "<missing>", "Created": 1, "CreatedBy": "/bin/sh -c #(nop)  CMD [\"sh\"]", "Size": 0},
		{"Id": "<missing>", "Created": 1, "CreatedBy": "/bin/sh -c #(nop) ADD file:1234 in / ", "Size": 7000000},
		{"Id": "<missing>", "Created": 0, "CreatedBy": "something unknown", "Size": 0},
	})
	f.json("GET /version", map[string]any{"Version": "28.0.0", "ApiVersion": "1.47"})
	m, diags := dfTestRead(t, pd, dockerfileDataSourceModel{Image: types.StringValue("app:1")})
	dfTestOK(t, diags)
	if m.Fidelity.ValueString() != "approximate" || m.SourceType.ValueString() != "image" || m.ID.ValueString() != "image:sha256:abc" {
		t.Errorf("fidelity/source/id: %s %s %s", m.Fidelity, m.SourceType, m.ID)
	}
	if m.ImageDigestRef.ValueString() != "app@sha256:def" {
		t.Errorf("image_digest_ref %s", m.ImageDigestRef)
	}
	if !m.DockerfileSyntax.IsNull() || !m.SourceDockerfile.IsNull() {
		t.Error("dockerfile_syntax and source_dockerfile are null in image mode")
	}
	if m.EngineAPIVersion.IsNull() {
		t.Error("engine_api_version set in image mode")
	}
	want := []string{"ADD file:1234 /", `CMD ["sh"]`, "WORKDIR /srv", "RUN make build", "EXPOSE 8080/tcp", "HEALTHCHECK --interval=30s --retries=3 CMD curl -f localhost", `CMD ["/app"]`, "ENV PATH=/usr/bin MODE=prod", "LABEL team=core"}
	got := m.Dockerfile.ValueString()
	pos := 0
	for _, w := range want {
		i := strings.Index(got[pos:], w)
		if i < 0 {
			t.Fatalf("rendered reconstruction lacks %q in order:\n%s", w, got)
		}
		pos += i
	}
	if len(m.History.Elements()) != 8 {
		t.Errorf("history %d", len(m.History.Elements()))
	}
	run := dfTestInstr(t, m, 0, 3)
	if run["layer_size"].(types.Number).String() != "1234" || run["line"].IsNull() == false {
		t.Errorf("run history extras %v", run)
	}
	if m.Labels.Elements()["team"].(types.String).ValueString() != "core" {
		t.Error("labels from config")
	}
	if diags.WarningsCount() == 0 {
		t.Error("expected a warning for the unconverted history entry")
	}
	_, diags = dfTestRead(t, pd, dockerfileDataSourceModel{Image: types.StringValue("missing:1")})
	dfTestErr(t, diags, "Cannot reconstruct")
	_, diags = dfTestRead(t, nil, dockerfileDataSourceModel{Image: types.StringValue("app:1")})
	dfTestErr(t, diags, "configured provider")
}

func dfStr(s string) types.String { return types.StringValue(s) }

func dfStrs(s ...string) []types.String {
	out := []types.String{}
	for _, v := range s {
		out = append(out, types.StringValue(v))
	}
	return out
}

func TestDockerfileDataSourceBuilder(t *testing.T) {
	cfg := dockerfileDataSourceModel{
		Syntax: dfStr("docker/dockerfile:1"),
		Check:  &dfCheckModel{Skip: dfStrs("JSONArgsRecommended", "SomeFutureRule"), Error: types.BoolValue(true)},
		GlobalArg: []dfGlobalArgModel{
			{Name: dfStr("BASE"), Default: dfStr("alpine:3.20")},
		},
		Stage: []dfStageModel{
			{
				Name: dfStr("build"), From: dfStr("golang:1.22"), Platform: dfStr("$BUILDPLATFORM"),
				Instruction: []dfInstructionModel{
					{Workdir: &dfWorkdirModel{Path: dfStr("/src")}},
					{Run: &dfRunModel{
						ShellForm: dfStr("go build -o /out/app ."),
						Mount:     []dfMountModel{{Type: dfStr("cache"), Target: dfStr("/root/.cache"), Sharing: dfStr("locked")}, {Type: dfStr("secret"), ID: dfStr("tok"), Env: dfStr("TOKEN"), Required: types.BoolValue(true)}},
						Network:   dfStr("none"),
					}},
					{Run: &dfRunModel{Heredocs: []dfHeredocModel{{Content: dfStr("set -e\necho \"$HOME\""), Expand: types.BoolValue(false)}}}},
				},
			},
			{
				From: dfStr("${BASE}"),
				Instruction: []dfInstructionModel{
					{Comments: dfStrs("metadata"), Label: &dfLabelModel{Labels: map[string]types.String{"org.opencontainers.image.authors": dfStr("Ops <ops@example.com>")}}},
					{Env: &dfEnvModel{Env: map[string]types.String{"B": dfStr("2"), "A": dfStr("one two")}}},
					{Copy: &dfCopyModel{From: dfStr("build"), Sources: dfStrs("/out/app"), Destination: dfStr("/usr/local/bin/app"), Link: types.BoolValue(true), Chmod: dfStr("0755")}},
					{Copy: &dfCopyModel{Destination: dfStr("/etc/"), Heredocs: []dfHeredocModel{{Delimiter: dfStr("app.conf"), Content: dfStr("x=1\n")}}}},
					{Add: &dfAddModel{Sources: dfStrs("https://example.com/a b.tgz"), Destination: dfStr("/opt/"), Checksum: dfStr("sha256:00")}},
					{Expose: &dfExposeModel{Ports: []dfPortModel{{Port: dfStr("8080")}, {Port: dfStr("53"), Protocol: dfStr("udp")}}}},
					{Volume: &dfVolumeModel{Paths: dfStrs("/data")}},
					{User: &dfUserModel{User: dfStr("app"), Group: dfStr("app")}},
					{Arg: &dfArgModel{Args: []dfArgItemModel{{Name: dfStr("V")}, {Name: dfStr("W"), Default: dfStr("w")}}}},
					{Healthcheck: &dfHealthcheckModel{Interval: dfStr("10s"), Retries: types.Int64Value(2), ExecForm: dfStrs("/app", "health")}},
					{Stopsignal: &dfStopsignalModel{Signal: dfStr("SIGQUIT")}},
					{Shell: &dfShellModel{Shell: dfStrs("/bin/sh", "-c")}},
					{Run: &dfRunModel{ShellForm: dfStr("echo child"), OnBuild: types.BoolValue(true)}},
					{Entrypoint: &dfCmdModel{ExecForm: dfStrs("/usr/local/bin/app")}},
					{Cmd: &dfCmdModel{ShellForm: dfStr("--serve")}},
				},
			},
		},
	}
	m, diags := dfTestRead(t, nil, cfg)
	dfTestOK(t, diags)
	if diags.WarningsCount() != 1 || !strings.Contains(diags.Warnings()[0].Detail(), "SomeFutureRule") {
		t.Errorf("Q32 warning expected: %v", diags)
	}
	want := `# syntax=docker/dockerfile:1
# check=skip=JSONArgsRecommended,SomeFutureRule;error=true

ARG BASE=alpine:3.20

FROM --platform=$BUILDPLATFORM golang:1.22 AS build
WORKDIR /src
RUN --mount=type=cache,target=/root/.cache,sharing=locked --mount=type=secret,id=tok,env=TOKEN,required=true --network=none go build -o /out/app .
RUN <<"EOF"
set -e
echo "$HOME"
EOF

FROM ${BASE}
# metadata
LABEL org.opencontainers.image.authors="Ops <ops@example.com>"
ENV A="one two" B=2
COPY --from=build --chmod=0755 --link /out/app /usr/local/bin/app
COPY <<app.conf /etc/
x=1
app.conf
ADD --checksum=sha256:00 ["https://example.com/a b.tgz", "/opt/"]
EXPOSE 8080 53/udp
VOLUME /data
USER app:app
ARG V W=w
HEALTHCHECK --interval=10s --retries=2 CMD ["/app", "health"]
STOPSIGNAL SIGQUIT
SHELL ["/bin/sh", "-c"]
ONBUILD RUN echo child
ENTRYPOINT ["/usr/local/bin/app"]
CMD -- --serve
`
	if got := m.Dockerfile.ValueString(); got != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
	if m.SourceType.ValueString() != "blocks" || !strings.HasPrefix(m.ID.ValueString(), "blocks:") || !m.SourceDockerfile.IsNull() {
		t.Errorf("builder identity %s %s", m.SourceType, m.ID)
	}
	if got := m.StageNames.String(); got != `["build","1"]` {
		t.Errorf("stage_names %s", got)
	}
	// Block outputs.
	ins := m.Stage[1].Instruction[12]
	if ins.Instruction.ValueString() != "RUN" || ins.Dockerfile.ValueString() != "ONBUILD RUN echo child\n" ||
		ins.StageIndex.String() != "1" || ins.InstructionIndex.String() != "13" ||
		ins.ContentSHA256.ValueString() != dockerfileSHA256("ONBUILD RUN echo child\n") || ins.DockerfileSyntax.ValueString() != "docker/dockerfile:1" {
		t.Errorf("instruction block outputs %+v", ins)
	}
	if s := m.Stage[0]; s.StageName.ValueString() != "build" || s.InstructionIndex.String() != "0" || s.Dockerfile.ValueString() != "FROM --platform=$BUILDPLATFORM golang:1.22 AS build\n" {
		t.Errorf("stage block outputs %+v", s.dfBlockOutputs)
	}
	if g := m.GlobalArg[0]; g.Instruction.ValueString() != "ARG" || !g.StageIndex.IsNull() || g.Dockerfile.ValueString() != "ARG BASE=alpine:3.20\n" {
		t.Errorf("global_arg outputs %+v", g)
	}
	if base := m.Stages.Elements()[1].(types.Object).Attributes()["base_image"].(types.String).ValueString(); base != "alpine:3.20" {
		t.Errorf("base_image %s", base)
	}
	dir := m.Directives.Attributes()
	if dir["syntax"].(types.String).ValueString() != "docker/dockerfile:1" || !dir["check"].(types.Object).Attributes()["error"].(types.Bool).ValueBool() {
		t.Errorf("directives %v", m.Directives)
	}
}

func TestDockerfileDataSourceErrors(t *testing.T) {
	cases := map[string]struct {
		cfg  dockerfileDataSourceModel
		want string
	}{
		"none":            {dockerfileDataSourceModel{}, "exactly one"},
		"two":             {dockerfileDataSourceModel{Content: dfStr("FROM a\n"), Image: dfStr("a")}, "exactly one"},
		"directives":      {dockerfileDataSourceModel{Content: dfStr("FROM a\n"), Syntax: dfStr("x")}, "need stage blocks"},
		"global_arg":      {dockerfileDataSourceModel{Content: dfStr("FROM a\n"), GlobalArg: []dfGlobalArgModel{{Name: dfStr("A")}}}, "need stage blocks"},
		"parse":           {dockerfileDataSourceModel{Content: dfStr("RUN x\n")}, "before the first FROM"},
		"unknown keyword": {dockerfileDataSourceModel{Content: dfStr("FROM a\nFOO bar\n")}, "unknown instruction"},
		"two keywords": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Run: &dfRunModel{ShellForm: dfStr("x")}, Cmd: &dfCmdModel{ShellForm: dfStr("y")}}}}}}, "exactly one keyword block"},
		"no keyword": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{{}}}}}, "exactly one keyword block"},
		"both forms": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Cmd: &dfCmdModel{ShellForm: dfStr("x"), ExecForm: dfStrs("y")}}}}}}, "not both"},
		"heredoc exec": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Run: &dfRunModel{ExecForm: dfStrs("y"), Heredocs: []dfHeredocModel{{Content: dfStr("x")}}}}}}}}, "shell form"},
		"bad escape": {dockerfileDataSourceModel{Escape: dfStr("x"), Stage: []dfStageModel{{From: dfStr("a")}}}, "escape must be"},
		"bad mount": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Run: &dfRunModel{ShellForm: dfStr("x"), Mount: []dfMountModel{{Type: dfStr("nfs")}}}}}}}}, "mount type"},
		"healthcheck none": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Healthcheck: &dfHealthcheckModel{None: types.BoolValue(true), ShellForm: dfStr("x")}}}}}}, "none = true"},
		"multiline env": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Env: &dfEnvModel{Env: map[string]types.String{"A": dfStr("1\n2")}}}}}}}, "one line"},
		"from with space": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a b")}}}, "from must be"},
		"copy no source": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Copy: &dfCopyModel{Destination: dfStr("/x")}}}}}}, "sources"},
		"heredoc delimiter in body": {dockerfileDataSourceModel{Stage: []dfStageModel{{From: dfStr("a"), Instruction: []dfInstructionModel{
			{Run: &dfRunModel{Heredocs: []dfHeredocModel{{Content: dfStr("a\nEOF\nb")}}}}}}}}, "own delimiter"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := dfTestRead(t, nil, c.cfg)
			dfTestErr(t, diags, c.want)
		})
	}
}

// TestDockerfileDataSourceUnknownCheckRuleWarns: Q32 in source modes too.
func TestDockerfileDataSourceUnknownCheckRuleWarns(t *testing.T) {
	m, diags := dfTestRead(t, nil, dockerfileDataSourceModel{Content: dfStr("# check=skip=all,NotARule\nFROM a\n")})
	dfTestOK(t, diags)
	if diags.WarningsCount() != 1 {
		t.Fatalf("want one warning, got %v", diags)
	}
	skip := m.Directives.Attributes()["check"].(types.Object).Attributes()["skip"].(types.List)
	if len(skip.Elements()) != 2 {
		t.Errorf("skip %v", skip)
	}
}

// TestDockerfileKeywordBlocksNoRequired: an instruction block sets one
// keyword block and leaves the others absent; the framework validates the
// Required attributes of an absent SingleNestedBlock, so a keyword block
// must have none (dfInstruction checks them when the block is set).
func TestDockerfileKeywordBlocksNoRequired(t *testing.T) {
	for kw, b := range dfInstructionBlocks() {
		for name, a := range b.(dschema.SingleNestedBlock).Attributes {
			if a.IsRequired() {
				t.Errorf("%s.%s is Required", kw, name)
			}
		}
	}
	var m dfInstructionModel
	m.User = &dfUserModel{User: types.StringNull(), Group: types.StringNull(), OnBuild: types.BoolNull()}
	if _, err := dfInstruction(m); err == nil || !strings.Contains(err.Error(), "USER: user is required") {
		t.Errorf("set user block without user: %v", err)
	}
}
