package provider

// Lane D: the structured outputs of data.docker_dockerfile (stages,
// directives, global_args, exposed_ports, base_images, labels) and the
// per-keyword objects of the 21 dockerfile_block items (Q48: the object of
// each instruction is named by its lowercase keyword).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
)

// dockerfileCheckRules are the build-check rule names of the pinned frontend
// (reference/upstream/dockerfile/frontend/dockerfile/docs/rules, 1.28.0).
// Q32: a name outside this list is a warning, never an error.
var dockerfileCheckRules = []string{
	"ConsistentInstructionCasing", "CopyIgnoredFile", "DuplicateStageName", "ExposeInvalidFormat",
	"ExposeProtoCasing", "FromAsCasing", "FromPlatformFlagConstDisallowed", "InvalidDefaultArgInFrom",
	"InvalidDefinitionDescription", "JSONArgsRecommended", "LegacyKeyValueFormat", "MaintainerDeprecated",
	"MultipleInstructionsDisallowed", "NoEmptyContinuation", "RedundantTargetPlatform", "ReservedStageName",
	"SecretsUsedInArgOrEnv", "StageNameCasing", "UndefinedArgInFrom", "UndefinedVar", "WorkdirRelativePath",
}

// dockerfileUnknownCheckRules returns the skip names that are neither `all`
// nor a pinned rule.
func dockerfileUnknownCheckRules(skip []string) []string {
	known := map[string]bool{"all": true}
	for _, r := range dockerfileCheckRules {
		known[strings.ToLower(r)] = true
	}
	var out []string
	for _, s := range skip {
		if !known[strings.ToLower(s)] {
			out = append(out, s)
		}
	}
	return out
}

// Built-in ARG sets (reference.md #predefined-args,
// #automatic-platform-args-in-the-global-scope, #buildkit-built-in-build-args).
var dockerfileBuiltinArgs = func() map[string]string {
	m := map[string]string{}
	for _, n := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "FTP_PROXY", "ftp_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy"} {
		m[n] = "predefined"
	}
	for _, n := range []string{"TARGETPLATFORM", "TARGETOS", "TARGETARCH", "TARGETVARIANT", "BUILDPLATFORM", "BUILDOS", "BUILDARCH", "BUILDVARIANT"} {
		m[n] = "platform"
	}
	for _, n := range []string{"BUILDKIT_BUILD_NAME", "BUILDKIT_CACHE_MOUNT_NS", "BUILDKIT_CONTEXT_KEEP_GIT_DIR", "BUILDKIT_GIT_ADVICE", "BUILDKIT_INLINE_CACHE", "BUILDKIT_MULTI_PLATFORM", "BUILDKIT_SANDBOX_HOSTNAME", "BUILDKIT_SYNTAX", "SOURCE_DATE_EPOCH"} {
		m[n] = "buildkit"
	}
	return m
}()

// dockerfileFlagMinSyntax is the "Minimum Dockerfile version" of each flag
// (reference.md tables), keyed "KEYWORD --flag".
var dockerfileFlagMinSyntax = map[string]string{
	"RUN --mount": "1.2", "RUN --network": "1.3", "RUN --security": "1.20", "RUN --device": "1.27",
	"ADD --keep-git-dir": "1.1", "ADD --checksum": "1.6", "ADD --chmod": "1.2", "ADD --link": "1.4",
	"ADD --unpack": "1.17", "ADD --exclude": "1.19",
	"COPY --chmod": "1.2", "COPY --link": "1.4", "COPY --parents": "1.20", "COPY --exclude": "1.19",
}

// dockerfileMaxSyntax returns the higher of two "1.x" versions.
func dockerfileMaxSyntax(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	minor := func(v string) int {
		_, m, _ := strings.Cut(v, ".")
		n, _ := strconv.Atoi(m)
		return n
	}
	if minor(b) > minor(a) {
		return b
	}
	return a
}

type dockerfileEnv struct{}

func (dockerfileEnv) Get(string) (string, bool) { return "", false }
func (dockerfileEnv) Keys() []string            { return nil }

// dockerfileUnquote removes shell quoting from a word, keeping $VAR
// references as written.
func dockerfileUnquote(word, esc string) string {
	r := '\\'
	if esc == "`" {
		r = '`'
	}
	lex := shell.NewLex(r)
	lex.SkipUnsetEnv = true
	out, _, err := lex.ProcessWord(word, dockerfileEnv{})
	if err != nil {
		return word
	}
	return out
}

// dockerfileFlagMap groups an instruction's flags by name (values joined
// back with commas, one entry per occurrence).
func dockerfileFlagMap(ins seamDockerfileInstruction) map[string][]string {
	m := map[string][]string{}
	for _, f := range ins.Flags {
		if f.Values == nil {
			m[f.Name] = append(m[f.Name], "")
			continue
		}
		m[f.Name] = append(m[f.Name], strings.Join(f.Values, ","))
	}
	return m
}

// dockerfileFlagBool reads a boolean flag: bare (--link) is true.
func dockerfileFlagBool(ins seamDockerfileInstruction, name string) any {
	for _, f := range ins.Flags {
		if f.Name != name {
			continue
		}
		if f.Values == nil {
			return true
		}
		v := strings.Join(f.Values, ",")
		b, err := strconv.ParseBool(v)
		if err != nil {
			return true
		}
		return b
	}
	return nil
}

func dockerfileFlagString(m map[string][]string, name string) any {
	if v, ok := m[name]; ok && len(v) > 0 {
		return v[len(v)-1]
	}
	return nil
}

// dockerfileMinSyntax is the highest min syntax among the flags used.
func dockerfileMinSyntax(kw string, ins seamDockerfileInstruction, extra string) any {
	v := extra
	for _, f := range ins.Flags {
		v = dockerfileMaxSyntax(v, dockerfileFlagMinSyntax[kw+" --"+f.Name])
	}
	if v == "" {
		return nil
	}
	return "dockerfile:" + v
}

func dockerfileHeredocsValue(ins seamDockerfileInstruction, copyTarget bool) []any {
	out := []any{}
	for _, h := range ins.Heredocs {
		hd := map[string]any{"delimiter": h.Name, "strip_tabs": h.StripTabs, "expand": !h.Quoted, "content": h.Content, "target": nil}
		if copyTarget && h.Target != "" {
			hd["target"] = h.Target
		}
		out = append(out, hd)
	}
	return out
}

// dockerfileCommandValue is the {form, shell_form, exec_form} triple.
func dockerfileCommandValue(json bool, args []string) map[string]any {
	if json {
		return map[string]any{"form": "exec", "shell_form": nil, "exec_form": append([]string{}, args...)}
	}
	return map[string]any{"form": "shell", "shell_form": strings.Join(args, " "), "exec_form": nil}
}

// dockerfileKV is one key[=value] option of a --mount value.
type dockerfileKV struct {
	Key, Value string
	Eq         bool
}

// dockerfileParseKV parses a comma-separated key[=value] list.
func dockerfileParseKV(v string) []dockerfileKV {
	var out []dockerfileKV
	for _, p := range dockerfileSplitCSV(v) {
		k, val, ok := strings.Cut(p, "=")
		out = append(out, dockerfileKV{Key: strings.ToLower(strings.TrimSpace(k)), Value: strings.Trim(val, `"'`), Eq: ok})
	}
	return out
}

func dockerfileBoolOpt(v string, bare bool) bool {
	if bare {
		return true
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

func dockerfileNumOpt(v string) any {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return n
}

// dockerfileMountValue maps one --mount value onto {type, bind|cache|tmpfs|secret|ssh}.
func dockerfileMountValue(v string) (map[string]any, bool) {
	kv := dockerfileParseKV(v)
	typ := "bind"
	for _, p := range kv {
		if p.Key == "type" {
			typ = strings.ToLower(p.Value)
		}
	}
	sub := map[string]any{}
	usesEnv := false
	for _, p := range kv {
		k, val, bare := p.Key, p.Value, !p.Eq
		switch k {
		case "type":
		case "target", "dst", "destination":
			sub["target"] = val
		case "source", "src":
			sub["source"] = val
		case "from":
			sub["from"] = val
		case "rw", "readwrite":
			sub["rw"] = dockerfileBoolOpt(val, bare)
		case "ro", "readonly":
			sub["ro"] = dockerfileBoolOpt(val, bare)
		case "required":
			sub["required"] = dockerfileBoolOpt(val, bare)
		case "uid", "gid":
			sub[k] = dockerfileNumOpt(val)
		case "env":
			sub["env"] = val
			usesEnv = true
		default: // id, sharing, mode, size
			sub[k] = val
		}
	}
	out := map[string]any{"type": typ, "bind": nil, "cache": nil, "tmpfs": nil, "secret": nil, "ssh": nil}
	if _, ok := out[typ]; ok {
		out[typ] = sub
	}
	return out, usesEnv
}

// dockerfileKeywordValue is the structured object of one instruction, as
// the dockerfile:<KEYWORD> item's `<keyword>` output (Q48). scope is
// "global" or "stage" (ARG only).
func dockerfileKeywordValue(ins seamDockerfileInstruction, esc, scope string) map[string]any {
	if ins.OnBuild {
		inner := ins
		inner.OnBuild = false
		usesFrom := false
		for _, f := range ins.Flags {
			if f.Name == "from" {
				usesFrom = true
			}
			if f.Name == "mount" && strings.Contains(strings.Join(f.Values, ","), "from=") {
				usesFrom = true
			}
		}
		ms := dockerfileMinSyntax(ins.Keyword, ins, "")
		if usesFrom {
			cur, _ := ms.(string)
			ms = "dockerfile:" + dockerfileMaxSyntax(strings.TrimPrefix(cur, "dockerfile:"), "1.11")
		}
		return map[string]any{
			"trigger":    map[string]any{"keyword": ins.Keyword, "block": dockerfileKeywordValue(inner, esc, scope), "uses_from": usesFrom},
			"min_syntax": ms,
		}
	}
	fm := dockerfileFlagMap(ins)
	switch ins.Keyword {
	case "RUN":
		v := dockerfileCommandValue(ins.JSONForm, ins.Args)
		v["heredocs"] = dockerfileHeredocsValue(ins, false)
		mounts := []any{}
		extra := ""
		for _, m := range fm["mount"] {
			mv, env := dockerfileMountValue(m)
			if env {
				extra = "1.10"
			}
			mounts = append(mounts, mv)
		}
		v["mount"] = mounts
		v["network"] = dockerfileFlagString(fm, "network")
		v["security"] = dockerfileFlagString(fm, "security")
		devices := []any{}
		for _, d := range fm["device"] {
			dev := map[string]any{"name": nil, "required": false}
			for i, p := range dockerfileSplitCSV(d) {
				k, val, has := strings.Cut(p, "=")
				switch {
				case strings.EqualFold(k, "required"):
					dev["required"] = dockerfileBoolOpt(val, !has)
				case strings.EqualFold(k, "name") && has:
					dev["name"] = val
				case i == 0:
					dev["name"] = p
				}
			}
			devices = append(devices, dev)
		}
		v["device"] = devices
		v["min_syntax"] = dockerfileMinSyntax("RUN", ins, extra)
		return v
	case "CMD", "ENTRYPOINT":
		return dockerfileCommandValue(ins.JSONForm, ins.Args)
	case "SHELL":
		return map[string]any{"shell": append([]string{}, ins.Args...)}
	case "LABEL":
		return map[string]any{"labels": dockerfileKVArgs(ins, esc)}
	case "ENV":
		legacy := len(ins.Args) == 2 && !strings.Contains(ins.Args[0], "=")
		return map[string]any{"env": dockerfileKVArgs(ins, esc), "legacy_form": legacy}
	case "MAINTAINER":
		return map[string]any{"name": strings.Join(ins.Args, " ")}
	case "EXPOSE":
		ports := []any{}
		for _, p := range ins.Args {
			ports = append(ports, dockerfilePortValue(p))
		}
		return map[string]any{"ports": ports}
	case "ADD", "COPY":
		var sources []string
		dest := any(nil)
		if n := len(ins.Args); n > 0 {
			sources = append([]string{}, ins.Args[:n-1]...)
			dest = ins.Args[n-1]
		}
		form := "plain"
		if ins.JSONForm {
			form = "json"
		}
		v := map[string]any{
			"sources": sources, "destination": dest, "form": form,
			"chmod": dockerfileFlagString(fm, "chmod"), "chown": dockerfileFlagString(fm, "chown"),
			"link": dockerfileFlagBool(ins, "link"), "exclude": fm["exclude"],
		}
		extra := ""
		if c, ok := v["chmod"].(string); ok && c != "" && !strings.HasPrefix(c, "$") {
			if _, err := strconv.ParseUint(c, 8, 32); err != nil {
				extra = "1.14" // symbolic notation
			}
		}
		if ins.Keyword == "COPY" {
			v["heredocs"] = dockerfileHeredocsValue(ins, true)
			v["from"] = dockerfileFlagString(fm, "from")
			v["parents"] = dockerfileFlagBool(ins, "parents")
		} else {
			v["keep_git_dir"] = dockerfileFlagBool(ins, "keep-git-dir")
			v["checksum"] = dockerfileFlagString(fm, "checksum")
			v["unpack"] = dockerfileFlagBool(ins, "unpack")
		}
		v["min_syntax"] = dockerfileMinSyntax(ins.Keyword, ins, extra)
		return v
	case "VOLUME":
		form := "plain"
		if ins.JSONForm {
			form = "json"
		}
		return map[string]any{"paths": append([]string{}, ins.Args...), "form": form}
	case "USER":
		u, g, ok := strings.Cut(strings.Join(ins.Args, " "), ":")
		v := map[string]any{"user": u, "group": nil}
		if ok {
			v["group"] = g
		}
		return v
	case "WORKDIR":
		return map[string]any{"path": strings.Join(ins.Args, " ")}
	case "ARG":
		args := []any{}
		for _, w := range ins.Args {
			name, def, hasDef := strings.Cut(w, "=")
			a := map[string]any{"name": name, "default": nil, "builtin": nil}
			if hasDef {
				a["default"] = dockerfileUnquote(def, esc)
			}
			if b, ok := dockerfileBuiltinArgs[name]; ok {
				a["builtin"] = b
			}
			args = append(args, a)
		}
		return map[string]any{"args": args, "scope": scope}
	case "STOPSIGNAL":
		return map[string]any{"signal": strings.Join(ins.Args, " ")}
	case "HEALTHCHECK":
		v := map[string]any{
			"none": false, "interval": dockerfileFlagString(fm, "interval"), "timeout": dockerfileFlagString(fm, "timeout"),
			"start_period": dockerfileFlagString(fm, "start-period"), "start_interval": dockerfileFlagString(fm, "start-interval"),
			"retries": nil, "cmd": nil,
		}
		if r, ok := dockerfileFlagString(fm, "retries").(string); ok {
			v["retries"] = dockerfileNumOpt(r)
		}
		if len(ins.Args) > 0 && strings.EqualFold(ins.Args[0], "NONE") {
			v["none"] = true
		} else if len(ins.Args) > 1 {
			v["cmd"] = dockerfileCommandValue(ins.JSONForm, ins.Args[1:])
		}
		return v
	}
	return map[string]any{}
}

// dockerfileKVArgs maps ENV / LABEL arguments to key -> unquoted value.
func dockerfileKVArgs(ins seamDockerfileInstruction, esc string) map[string]any {
	out := map[string]any{}
	if len(ins.Args) == 2 && !strings.Contains(ins.Args[0], "=") {
		out[dockerfileUnquote(ins.Args[0], esc)] = ins.Args[1]
		return out
	}
	for _, w := range ins.Args {
		k, v, _ := strings.Cut(w, "=")
		out[dockerfileUnquote(k, esc)] = dockerfileUnquote(v, esc)
	}
	return out
}

func dockerfilePortValue(p string) map[string]any {
	port, proto, ok := strings.Cut(p, "/")
	if !ok || proto == "" {
		proto = "tcp"
	}
	return map[string]any{"port": port, "protocol": proto}
}

// dockerfileSHA256 is the hex sha256 of s.
func dockerfileSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// dockerfileInstructionText renders one instruction (heredoc bodies
// included, comments excluded): the block's raw `dockerfile` output.
func dockerfileInstructionText(ins seamDockerfileInstruction, esc string) string {
	ins.Comments = nil
	var b strings.Builder
	if err := dockerfileRenderInstruction(&b, ins, esc); err != nil {
		return ""
	}
	return b.String()
}

// dockerfileFromText renders a stage's FROM line.
func dockerfileFromText(st seamDockerfileStage) string {
	s := "FROM "
	if st.Platform != "" {
		s += "--platform=" + st.Platform + " "
	}
	s += st.From
	if st.Name != "" {
		s += " AS " + st.Name
	}
	return s + "\n"
}

// dockerfileOutputs are the structured and identity values of a document.
type dockerfileOutputs struct {
	Stages       []any
	Directives   map[string]any
	GlobalArgs   []any
	ExposedPorts []any
	BaseImages   []any
	Labels       map[string]string
	StageNames   []string
	FinalStage   string
}

var dockerfileVarRe = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}?`)

// dockerfileSubstGlobal substitutes global ARG defaults in a FROM value
// where they are known; ok is false when an unresolved reference remains.
func dockerfileSubstGlobal(v string, globals map[string]string) (string, bool) {
	ok := true
	out := dockerfileVarRe.ReplaceAllStringFunc(v, func(m string) string {
		sm := dockerfileVarRe.FindStringSubmatch(m)
		if g, found := globals[sm[1]]; found {
			return g
		}
		if sm[2] != "" {
			return sm[3]
		}
		ok = false
		return m
	})
	return out, ok
}

// dockerfileBuildOutputs computes the structured outputs. lines may be nil
// (image mode); extra holds per-instruction additions keyed [stage][index].
func dockerfileBuildOutputs(p *dockerfileParsed, extra func(stage, idx int, m map[string]any)) dockerfileOutputs {
	doc := p.Doc
	esc := doc.Escape
	if esc == "" {
		esc = `\`
	}
	out := dockerfileOutputs{Labels: map[string]string{}, StageNames: []string{}, Stages: []any{}, GlobalArgs: []any{}, ExposedPorts: []any{}, BaseImages: []any{}}
	check := map[string]any{"skip": nil, "error": nil}
	if len(doc.Check.Skip) > 0 || doc.Check.Error {
		check = map[string]any{"skip": append([]string{}, doc.Check.Skip...), "error": doc.Check.Error}
	}
	out.Directives = map[string]any{"syntax": dockerfileNilEmpty(doc.Syntax), "escape": dockerfileNilEmpty(doc.Escape), "check": check}
	globals := map[string]string{}
	for _, a := range doc.GlobalArgs {
		for _, w := range a.Args {
			name, def, hasDef := strings.Cut(w, "=")
			ga := map[string]any{"name": name, "default": nil}
			if hasDef {
				d := dockerfileUnquote(def, esc)
				ga["default"] = d
				globals[name] = d
			}
			out.GlobalArgs = append(out.GlobalArgs, ga)
		}
	}
	names := map[string]bool{}
	for si, st := range doc.Stages {
		name := st.Name
		if name == "" {
			name = strconv.Itoa(si)
		}
		out.StageNames = append(out.StageNames, name)
		base, _ := dockerfileSubstGlobal(st.From, globals)
		stage := map[string]any{
			"index": si, "name": dockerfileNilEmpty(st.Name), "base_image": base, "platform": dockerfileNilEmpty(st.Platform),
			"base_stage": nil, "base_image_ref": nil, "base_image_digest": nil,
		}
		switch {
		case names[strings.ToLower(base)]:
			stage["base_stage"] = base
		case strings.EqualFold(base, "scratch"):
		default:
			bi := map[string]any{"stage": name, "image": base, "image_ref": nil, "digest": nil, "platform": dockerfileNilEmpty(st.Platform)}
			if !strings.Contains(base, "$") {
				if named, err := reference.ParseNormalizedNamed(base); err == nil {
					if c, ok := named.(reference.Canonical); ok {
						stage["base_image_digest"] = c.Digest().String()
						bi["digest"] = c.Digest().String()
					}
					if t, ok := reference.TagNameOnly(named).(reference.Tagged); ok {
						ref := reference.FamiliarName(named) + ":" + t.Tag()
						if full, err := reference.ParseNormalizedNamed(ref); err == nil {
							ref = full.String()
						}
						stage["base_image_ref"] = ref
						bi["image_ref"] = ref
					} else if _, isDigest := named.(reference.Canonical); !isDigest {
						stage["base_image_ref"] = named.String()
					}
				}
			}
			out.BaseImages = append(out.BaseImages, bi)
		}
		if st.Name != "" {
			names[strings.ToLower(st.Name)] = true
		}
		instrs := []any{}
		final := si == len(doc.Stages)-1
		for ii, ins := range st.Instructions {
			kwName := strings.ToLower(ins.Keyword)
			if ins.OnBuild {
				kwName = "onbuild"
			}
			keyword := strings.ToUpper(kwName)
			m := map[string]any{
				"index": ii + 1, "keyword": keyword, "line": nil, "dockerfile": dockerfileInstructionText(ins, esc),
				"created_by": nil, "created": nil, "comment": nil, "layer_size": nil,
			}
			if si < len(p.StageLines) && ii < len(p.StageLines[si]) && p.StageLines[si][ii] > 0 {
				m["line"] = p.StageLines[si][ii]
			}
			if len(ins.Comments) > 0 {
				m["comment"] = strings.Join(ins.Comments, "\n")
			}
			m[kwName] = dockerfileKeywordValue(ins, esc, "stage")
			if extra != nil {
				extra(si, ii, m)
			}
			instrs = append(instrs, m)
			if final && !ins.OnBuild {
				switch ins.Keyword {
				case "LABEL":
					for k, v := range dockerfileKVArgs(ins, esc) {
						out.Labels[k] = fmt.Sprint(v)
					}
				case "EXPOSE":
					for _, a := range ins.Args {
						out.ExposedPorts = append(out.ExposedPorts, dockerfilePortValue(a))
					}
				}
			}
		}
		stage["instructions"] = instrs
		out.Stages = append(out.Stages, stage)
	}
	if n := len(out.StageNames); n > 0 {
		out.FinalStage = out.StageNames[n-1]
	}
	return out
}

func dockerfileNilEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// dockerfileSortedKeys returns m's keys sorted.
func dockerfileSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
