package provider

// Lane D: the builder mode of data.docker_dockerfile. The configuration
// writes a Dockerfile with nested blocks:
//
//	stage {                       # FROM ... AS name (Q31), in order
//	  instruction { run { ... } } # one keyword block per instruction block
//	  instruction { copy { ... } }
//	}
//
// Order: Terraform keeps the order of the blocks of ONE block type, but not
// the relative order of different block types. So every instruction is an
// `instruction` block (one ordered list) holding exactly one typed keyword
// block (`run`, `copy`, `env` ... the lowercase keyword, Q48); the keyword
// block is the discriminator. ONBUILD is the `onbuild = true` attribute of
// each keyword block (Q30); MAINTAINER has no block (Q29).

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// dfBlockOutputs are the computed outputs of one block (the
// dockerfile:<KEYWORD> items: position, rendered text, hash, syntax).
type dfBlockOutputs struct {
	StageIndex       types.Number `tfsdk:"stage_index"`
	InstructionIndex types.Number `tfsdk:"instruction_index"`
	Dockerfile       types.String `tfsdk:"dockerfile"`
	ContentSHA256    types.String `tfsdk:"content_sha256"`
	DockerfileSyntax types.String `tfsdk:"dockerfile_syntax"`
}

type dfCheckModel struct {
	Skip  []types.String `tfsdk:"skip"`
	Error types.Bool     `tfsdk:"error"`
}

type dfGlobalArgModel struct {
	Name        types.String `tfsdk:"name"`
	Default     types.String `tfsdk:"default"`
	Instruction types.String `tfsdk:"instruction"`
	dfBlockOutputs
}

type dfStageModel struct {
	Name        types.String         `tfsdk:"name"`
	From        types.String         `tfsdk:"from"`
	Platform    types.String         `tfsdk:"platform"`
	Comments    []types.String       `tfsdk:"comments"`
	Instruction []dfInstructionModel `tfsdk:"instruction"`
	StageName   types.String         `tfsdk:"stage_name"`
	dfBlockOutputs
}

type dfInstructionModel struct {
	Comments    []types.String      `tfsdk:"comments"`
	Run         *dfRunModel         `tfsdk:"run"`
	Cmd         *dfCmdModel         `tfsdk:"cmd"`
	Entrypoint  *dfCmdModel         `tfsdk:"entrypoint"`
	Shell       *dfShellModel       `tfsdk:"shell"`
	Label       *dfLabelModel       `tfsdk:"label"`
	Expose      *dfExposeModel      `tfsdk:"expose"`
	Env         *dfEnvModel         `tfsdk:"env"`
	Add         *dfAddModel         `tfsdk:"add"`
	Copy        *dfCopyModel        `tfsdk:"copy"`
	Volume      *dfVolumeModel      `tfsdk:"volume"`
	User        *dfUserModel        `tfsdk:"user"`
	Workdir     *dfWorkdirModel     `tfsdk:"workdir"`
	Arg         *dfArgModel         `tfsdk:"arg"`
	Stopsignal  *dfStopsignalModel  `tfsdk:"stopsignal"`
	Healthcheck *dfHealthcheckModel `tfsdk:"healthcheck"`
	Instruction types.String        `tfsdk:"instruction"`
	dfBlockOutputs
}

type dfHeredocModel struct {
	Delimiter types.String `tfsdk:"delimiter"`
	Content   types.String `tfsdk:"content"`
	StripTabs types.Bool   `tfsdk:"strip_tabs"`
	Expand    types.Bool   `tfsdk:"expand"`
}

type dfMountModel struct {
	Type     types.String `tfsdk:"type"`
	Target   types.String `tfsdk:"target"`
	Source   types.String `tfsdk:"source"`
	From     types.String `tfsdk:"from"`
	RW       types.Bool   `tfsdk:"rw"`
	ID       types.String `tfsdk:"id"`
	RO       types.Bool   `tfsdk:"ro"`
	Sharing  types.String `tfsdk:"sharing"`
	Mode     types.String `tfsdk:"mode"`
	UID      types.Int64  `tfsdk:"uid"`
	GID      types.Int64  `tfsdk:"gid"`
	Size     types.String `tfsdk:"size"`
	Env      types.String `tfsdk:"env"`
	Required types.Bool   `tfsdk:"required"`
}

type dfDeviceModel struct {
	Name     types.String `tfsdk:"name"`
	Required types.Bool   `tfsdk:"required"`
}

type dfRunModel struct {
	ShellForm types.String     `tfsdk:"shell_form"`
	ExecForm  []types.String   `tfsdk:"exec_form"`
	Heredocs  []dfHeredocModel `tfsdk:"heredocs"`
	Mount     []dfMountModel   `tfsdk:"mount"`
	Network   types.String     `tfsdk:"network"`
	Security  types.String     `tfsdk:"security"`
	Device    []dfDeviceModel  `tfsdk:"device"`
	OnBuild   types.Bool       `tfsdk:"onbuild"`
}

type dfCmdModel struct {
	ShellForm types.String   `tfsdk:"shell_form"`
	ExecForm  []types.String `tfsdk:"exec_form"`
	OnBuild   types.Bool     `tfsdk:"onbuild"`
}

type dfShellModel struct {
	Shell   []types.String `tfsdk:"shell"`
	OnBuild types.Bool     `tfsdk:"onbuild"`
}

type dfLabelModel struct {
	Labels  map[string]types.String `tfsdk:"labels"`
	OnBuild types.Bool              `tfsdk:"onbuild"`
}

type dfPortModel struct {
	Port     types.String `tfsdk:"port"`
	Protocol types.String `tfsdk:"protocol"`
}

type dfExposeModel struct {
	Ports   []dfPortModel `tfsdk:"ports"`
	OnBuild types.Bool    `tfsdk:"onbuild"`
}

type dfEnvModel struct {
	Env     map[string]types.String `tfsdk:"env"`
	OnBuild types.Bool              `tfsdk:"onbuild"`
}

type dfAddModel struct {
	Sources     []types.String `tfsdk:"sources"`
	Destination types.String   `tfsdk:"destination"`
	KeepGitDir  types.Bool     `tfsdk:"keep_git_dir"`
	Checksum    types.String   `tfsdk:"checksum"`
	Chmod       types.String   `tfsdk:"chmod"`
	Chown       types.String   `tfsdk:"chown"`
	Link        types.Bool     `tfsdk:"link"`
	Unpack      types.Bool     `tfsdk:"unpack"`
	Exclude     []types.String `tfsdk:"exclude"`
	OnBuild     types.Bool     `tfsdk:"onbuild"`
}

type dfCopyModel struct {
	Sources     []types.String   `tfsdk:"sources"`
	Destination types.String     `tfsdk:"destination"`
	Heredocs    []dfHeredocModel `tfsdk:"heredocs"`
	From        types.String     `tfsdk:"from"`
	Chmod       types.String     `tfsdk:"chmod"`
	Chown       types.String     `tfsdk:"chown"`
	Link        types.Bool       `tfsdk:"link"`
	Parents     types.Bool       `tfsdk:"parents"`
	Exclude     []types.String   `tfsdk:"exclude"`
	OnBuild     types.Bool       `tfsdk:"onbuild"`
}

type dfVolumeModel struct {
	Paths   []types.String `tfsdk:"paths"`
	OnBuild types.Bool     `tfsdk:"onbuild"`
}

type dfUserModel struct {
	User    types.String `tfsdk:"user"`
	Group   types.String `tfsdk:"group"`
	OnBuild types.Bool   `tfsdk:"onbuild"`
}

type dfWorkdirModel struct {
	Path    types.String `tfsdk:"path"`
	OnBuild types.Bool   `tfsdk:"onbuild"`
}

type dfArgItemModel struct {
	Name    types.String `tfsdk:"name"`
	Default types.String `tfsdk:"default"`
}

type dfArgModel struct {
	Args    []dfArgItemModel `tfsdk:"args"`
	OnBuild types.Bool       `tfsdk:"onbuild"`
}

type dfStopsignalModel struct {
	Signal  types.String `tfsdk:"signal"`
	OnBuild types.Bool   `tfsdk:"onbuild"`
}

type dfHealthcheckModel struct {
	None          types.Bool     `tfsdk:"none"`
	Interval      types.String   `tfsdk:"interval"`
	Timeout       types.String   `tfsdk:"timeout"`
	StartPeriod   types.String   `tfsdk:"start_period"`
	StartInterval types.String   `tfsdk:"start_interval"`
	Retries       types.Int64    `tfsdk:"retries"`
	ShellForm     types.String   `tfsdk:"shell_form"`
	ExecForm      []types.String `tfsdk:"exec_form"`
	OnBuild       types.Bool     `tfsdk:"onbuild"`
}

func dfStrings(in []types.String) []string {
	var out []string
	for _, v := range in {
		out = append(out, v.ValueString())
	}
	return out
}

func dfSet(v types.String) bool { return !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" }

func dfTrue(v types.Bool) bool { return !v.IsNull() && !v.IsUnknown() && v.ValueBool() }

// dfBoolFlag appends --name (true) or --name=false; null adds nothing.
func dfBoolFlag(ins *seamDockerfileInstruction, name string, v types.Bool) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	if v.ValueBool() {
		ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: name})
		return
	}
	ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: name, Values: []string{"false"}})
}

func dfStrFlag(ins *seamDockerfileInstruction, name string, v types.String) {
	if dfSet(v) {
		ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: name, Values: []string{v.ValueString()}})
	}
}

// dfHeredocs converts heredoc models; the second result is the
// `<<[-]["]NAME["]` words for the instruction line.
func dfHeredocs(in []dfHeredocModel) ([]seamDockerfileHeredoc, []string, error) {
	var hs []seamDockerfileHeredoc
	var words []string
	for i, h := range in {
		name := h.Delimiter.ValueString()
		if name == "" {
			name = "EOF"
			if len(in) > 1 {
				name = fmt.Sprintf("EOF%d", i+1)
			}
		}
		if strings.ContainsAny(name, " \t\"'<") {
			return nil, nil, fmt.Errorf("heredoc delimiter %q must be one word without quotes", name)
		}
		content := h.Content.ValueString()
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		for _, l := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
			if strings.TrimLeft(l, "\t") == name {
				return nil, nil, fmt.Errorf("heredoc content contains its own delimiter line %q", name)
			}
		}
		hd := seamDockerfileHeredoc{Name: name, Content: content, StripTabs: dfTrue(h.StripTabs), Quoted: !h.Expand.IsNull() && !h.Expand.ValueBool()}
		w := "<<"
		if hd.StripTabs {
			w += "-"
		}
		if hd.Quoted {
			w += `"` + name + `"`
		} else {
			w += name
		}
		hs = append(hs, hd)
		words = append(words, w)
	}
	return hs, words, nil
}

// dfMountValue renders one --mount value.
func dfMountValue(m dfMountModel) (string, error) {
	typ := m.Type.ValueString()
	if typ == "" {
		typ = "bind"
	}
	switch typ {
	case "bind", "cache", "tmpfs", "secret", "ssh":
	default:
		return "", fmt.Errorf("mount type %q is not bind, cache, tmpfs, secret or ssh", typ)
	}
	q := func(v string) string {
		if strings.ContainsAny(v, `,"`) {
			return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
		}
		return v
	}
	parts := []string{"type=" + typ}
	str := func(k string, v types.String) {
		if dfSet(v) {
			parts = append(parts, k+"="+q(v.ValueString()))
		}
	}
	boolean := func(k string, v types.Bool) {
		if !v.IsNull() && !v.IsUnknown() {
			parts = append(parts, fmt.Sprintf("%s=%t", k, v.ValueBool()))
		}
	}
	num := func(k string, v types.Int64) {
		if !v.IsNull() && !v.IsUnknown() {
			parts = append(parts, fmt.Sprintf("%s=%d", k, v.ValueInt64()))
		}
	}
	str("id", m.ID)
	str("target", m.Target)
	str("source", m.Source)
	str("from", m.From)
	str("sharing", m.Sharing)
	str("size", m.Size)
	str("env", m.Env)
	boolean("rw", m.RW)
	boolean("ro", m.RO)
	boolean("required", m.Required)
	str("mode", m.Mode)
	num("uid", m.UID)
	num("gid", m.GID)
	if typ != "secret" && typ != "ssh" && !dfSet(m.Target) {
		return "", fmt.Errorf("mount type=%s needs target", typ)
	}
	return strings.Join(parts, ","), nil
}

// dfCommand fills Args/JSONForm from shell_form / exec_form.
func dfCommand(ins *seamDockerfileInstruction, kw string, shellForm types.String, execForm []types.String) error {
	switch {
	case dfSet(shellForm) && execForm != nil:
		return fmt.Errorf("%s: set shell_form or exec_form, not both", kw)
	case execForm != nil:
		ins.Args, ins.JSONForm = dfStrings(execForm), true
	case dfSet(shellForm):
		if strings.Contains(shellForm.ValueString(), "\n") {
			return fmt.Errorf("%s: shell_form must be one line (use a heredoc for scripts)", kw)
		}
		ins.Args = []string{shellForm.ValueString()}
	default:
		return fmt.Errorf("%s: shell_form or exec_form is required", kw)
	}
	return nil
}

// dfWordsJSON reports whether a source/destination list needs the JSON form.
func dfWordsJSON(words []string) bool {
	for _, w := range words {
		if w == "" || strings.ContainsAny(w, " \t") {
			return true
		}
	}
	return false
}

// dfInstruction converts one instruction block.
func dfInstruction(m dfInstructionModel) (seamDockerfileInstruction, error) {
	ins := seamDockerfileInstruction{Comments: dfStrings(m.Comments)}
	n := 0
	for _, set := range []bool{m.Run != nil, m.Cmd != nil, m.Entrypoint != nil, m.Shell != nil, m.Label != nil, m.Expose != nil,
		m.Env != nil, m.Add != nil, m.Copy != nil, m.Volume != nil, m.User != nil, m.Workdir != nil, m.Arg != nil,
		m.Stopsignal != nil, m.Healthcheck != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return ins, fmt.Errorf("an instruction block needs exactly one keyword block (run, copy, env, ...), found %d", n)
	}
	var err error
	switch {
	case m.Run != nil:
		r := m.Run
		ins.Keyword, ins.OnBuild = "RUN", dfTrue(r.OnBuild)
		for _, mt := range r.Mount {
			v, err := dfMountValue(mt)
			if err != nil {
				return ins, fmt.Errorf("RUN: %w", err)
			}
			ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: "mount", Values: []string{v}})
		}
		dfStrFlag(&ins, "network", r.Network)
		dfStrFlag(&ins, "security", r.Security)
		for _, d := range r.Device {
			v := d.Name.ValueString()
			if v == "" {
				return ins, fmt.Errorf("RUN: device needs a name")
			}
			if dfTrue(d.Required) {
				v += ",required=true"
			}
			ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: "device", Values: []string{v}})
		}
		hs, words, herr := dfHeredocs(r.Heredocs)
		if herr != nil {
			return ins, fmt.Errorf("RUN: %w", herr)
		}
		ins.Heredocs = hs
		switch {
		case len(hs) > 0 && r.ExecForm != nil:
			return ins, fmt.Errorf("RUN: heredocs need the shell form, not exec_form")
		case len(hs) > 0 && !dfSet(r.ShellForm):
			ins.Args = []string{strings.Join(words, " ")}
		case len(hs) > 0:
			s := r.ShellForm.ValueString()
			if !strings.Contains(s, "<<") {
				s = strings.Join(words, " ") + " " + s
			}
			ins.Args = []string{s}
		default:
			err = dfCommand(&ins, "RUN", r.ShellForm, r.ExecForm)
		}
	case m.Cmd != nil:
		ins.Keyword, ins.OnBuild = "CMD", dfTrue(m.Cmd.OnBuild)
		err = dfCommand(&ins, "CMD", m.Cmd.ShellForm, m.Cmd.ExecForm)
	case m.Entrypoint != nil:
		ins.Keyword, ins.OnBuild = "ENTRYPOINT", dfTrue(m.Entrypoint.OnBuild)
		err = dfCommand(&ins, "ENTRYPOINT", m.Entrypoint.ShellForm, m.Entrypoint.ExecForm)
	case m.Shell != nil:
		ins.Keyword, ins.OnBuild = "SHELL", dfTrue(m.Shell.OnBuild)
		if len(m.Shell.Shell) == 0 {
			return ins, fmt.Errorf("SHELL: shell needs at least the executable")
		}
		ins.Args, ins.JSONForm = dfStrings(m.Shell.Shell), true
	case m.Label != nil:
		ins.Keyword, ins.OnBuild = "LABEL", dfTrue(m.Label.OnBuild)
		ins.Args, err = dfKVWords("LABEL", m.Label.Labels)
	case m.Env != nil:
		ins.Keyword, ins.OnBuild = "ENV", dfTrue(m.Env.OnBuild)
		ins.Args, err = dfKVWords("ENV", m.Env.Env)
	case m.Expose != nil:
		ins.Keyword, ins.OnBuild = "EXPOSE", dfTrue(m.Expose.OnBuild)
		for _, p := range m.Expose.Ports {
			w := p.Port.ValueString()
			if w == "" || strings.ContainsAny(w, " \t") {
				return ins, fmt.Errorf("EXPOSE: invalid port %q", w)
			}
			if dfSet(p.Protocol) {
				w += "/" + p.Protocol.ValueString()
			}
			ins.Args = append(ins.Args, w)
		}
		if len(ins.Args) == 0 {
			return ins, fmt.Errorf("EXPOSE: ports is required")
		}
	case m.Add != nil:
		a := m.Add
		ins.Keyword, ins.OnBuild = "ADD", dfTrue(a.OnBuild)
		dfStrFlag(&ins, "chown", a.Chown)
		dfStrFlag(&ins, "chmod", a.Chmod)
		dfBoolFlag(&ins, "link", a.Link)
		dfBoolFlag(&ins, "keep-git-dir", a.KeepGitDir)
		dfStrFlag(&ins, "checksum", a.Checksum)
		dfBoolFlag(&ins, "unpack", a.Unpack)
		for _, e := range a.Exclude {
			ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: "exclude", Values: []string{e.ValueString()}})
		}
		err = dfCopyArgs(&ins, dfStrings(a.Sources), nil, a.Destination)
	case m.Copy != nil:
		c := m.Copy
		ins.Keyword, ins.OnBuild = "COPY", dfTrue(c.OnBuild)
		dfStrFlag(&ins, "from", c.From)
		dfStrFlag(&ins, "chown", c.Chown)
		dfStrFlag(&ins, "chmod", c.Chmod)
		dfBoolFlag(&ins, "link", c.Link)
		dfBoolFlag(&ins, "parents", c.Parents)
		for _, e := range c.Exclude {
			ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: "exclude", Values: []string{e.ValueString()}})
		}
		hs, words, herr := dfHeredocs(c.Heredocs)
		if herr != nil {
			return ins, fmt.Errorf("COPY: %w", herr)
		}
		ins.Heredocs = hs
		err = dfCopyArgs(&ins, dfStrings(c.Sources), words, c.Destination)
		for i := range ins.Heredocs {
			ins.Heredocs[i].Target = c.Destination.ValueString()
		}
	case m.Volume != nil:
		ins.Keyword, ins.OnBuild = "VOLUME", dfTrue(m.Volume.OnBuild)
		ins.Args = dfStrings(m.Volume.Paths)
		if len(ins.Args) == 0 {
			return ins, fmt.Errorf("VOLUME: paths is required")
		}
		ins.JSONForm = dfWordsJSON(ins.Args)
	case m.User != nil:
		ins.Keyword, ins.OnBuild = "USER", dfTrue(m.User.OnBuild)
		u := m.User.User.ValueString()
		if u == "" {
			return ins, fmt.Errorf("USER: user is required")
		}
		if dfSet(m.User.Group) {
			u += ":" + m.User.Group.ValueString()
		}
		ins.Args = []string{u}
	case m.Workdir != nil:
		ins.Keyword, ins.OnBuild = "WORKDIR", dfTrue(m.Workdir.OnBuild)
		if !dfSet(m.Workdir.Path) {
			return ins, fmt.Errorf("WORKDIR: path is required")
		}
		ins.Args = []string{m.Workdir.Path.ValueString()}
	case m.Arg != nil:
		ins.Keyword, ins.OnBuild = "ARG", dfTrue(m.Arg.OnBuild)
		if len(m.Arg.Args) == 0 {
			return ins, fmt.Errorf("ARG: args is required")
		}
		for _, a := range m.Arg.Args {
			w, werr := dfArgWord(a.Name, a.Default)
			if werr != nil {
				return ins, werr
			}
			ins.Args = append(ins.Args, w)
		}
	case m.Stopsignal != nil:
		ins.Keyword, ins.OnBuild = "STOPSIGNAL", dfTrue(m.Stopsignal.OnBuild)
		if !dfSet(m.Stopsignal.Signal) {
			return ins, fmt.Errorf("STOPSIGNAL: signal is required")
		}
		ins.Args = []string{m.Stopsignal.Signal.ValueString()}
	case m.Healthcheck != nil:
		h := m.Healthcheck
		ins.Keyword, ins.OnBuild = "HEALTHCHECK", dfTrue(h.OnBuild)
		if dfTrue(h.None) {
			if dfSet(h.ShellForm) || h.ExecForm != nil || dfSet(h.Interval) || dfSet(h.Timeout) || dfSet(h.StartPeriod) || dfSet(h.StartInterval) || !h.Retries.IsNull() {
				return ins, fmt.Errorf("HEALTHCHECK: none = true takes no command and no options")
			}
			ins.Args = []string{"NONE"}
			break
		}
		dfStrFlag(&ins, "interval", h.Interval)
		dfStrFlag(&ins, "timeout", h.Timeout)
		dfStrFlag(&ins, "start-period", h.StartPeriod)
		dfStrFlag(&ins, "start-interval", h.StartInterval)
		if !h.Retries.IsNull() && !h.Retries.IsUnknown() {
			ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: "retries", Values: []string{fmt.Sprint(h.Retries.ValueInt64())}})
		}
		var cmd seamDockerfileInstruction
		if err = dfCommand(&cmd, "HEALTHCHECK", h.ShellForm, h.ExecForm); err == nil {
			ins.Args, ins.JSONForm = append([]string{"CMD"}, cmd.Args...), cmd.JSONForm
		}
	}
	return ins, err
}

func dfArgWord(name, def types.String) (string, error) {
	n := name.ValueString()
	if n == "" || strings.ContainsAny(n, " \t=\"'") {
		return "", fmt.Errorf("ARG: invalid name %q", n)
	}
	if def.IsNull() || def.IsUnknown() {
		return n, nil
	}
	if strings.Contains(def.ValueString(), "\n") {
		return "", fmt.Errorf("ARG %s: the default must be one line", n)
	}
	return n + "=" + dockerfileQuoteWord(def.ValueString(), `\`), nil
}

// dfKVWords renders a LABEL / ENV map as key=value words (sorted keys).
func dfKVWords(kw string, m map[string]types.String) ([]string, error) {
	if len(m) == 0 {
		return nil, fmt.Errorf("%s: at least one key is required", kw)
	}
	var out []string
	for _, k := range dockerfileSortedKeys(m) {
		v := m[k].ValueString()
		if strings.Contains(v, "\n") || strings.Contains(k, "\n") {
			return nil, fmt.Errorf("%s %s: values must be one line", kw, k)
		}
		if k == "" || strings.Contains(k, "=") {
			return nil, fmt.Errorf("%s: invalid key %q", kw, k)
		}
		out = append(out, dockerfileQuoteWord(k, `\`)+"="+dockerfileQuoteWord(v, `\`))
	}
	return out, nil
}

// dfCopyArgs sets the COPY / ADD argument list.
func dfCopyArgs(ins *seamDockerfileInstruction, sources, heredocWords []string, dest types.String) error {
	if !dfSet(dest) {
		return fmt.Errorf("%s: destination is required", ins.Keyword)
	}
	if len(sources) == 0 && len(heredocWords) == 0 {
		return fmt.Errorf("%s: sources (or heredocs) required", ins.Keyword)
	}
	words := append(append([]string{}, sources...), dest.ValueString())
	if len(heredocWords) == 0 && dfWordsJSON(words) {
		ins.Args, ins.JSONForm = words, true
		return nil
	}
	if dfWordsJSON(words) {
		return fmt.Errorf("%s: paths with whitespace need the JSON form, which heredocs cannot use", ins.Keyword)
	}
	ins.Args = append(append(append([]string{}, sources...), heredocWords...), dest.ValueString())
	return nil
}

// dfBuildDoc converts the builder configuration into a document.
func dfBuildDoc(m *dockerfileDataSourceModel) (seamDockerfileDoc, error) {
	doc := seamDockerfileDoc{Syntax: m.Syntax.ValueString(), Escape: m.Escape.ValueString()}
	if doc.Escape != "" && doc.Escape != `\` && doc.Escape != "`" {
		return doc, fmt.Errorf("escape must be \\ or ` (got %q)", doc.Escape)
	}
	if m.Check != nil {
		doc.Check = seamDockerfileCheck{Skip: dfStrings(m.Check.Skip), Error: dfTrue(m.Check.Error)}
	}
	for i, g := range m.GlobalArg {
		w, err := dfArgWord(g.Name, g.Default)
		if err != nil {
			return doc, fmt.Errorf("global_arg %d: %w", i, err)
		}
		doc.GlobalArgs = append(doc.GlobalArgs, seamDockerfileInstruction{Keyword: "ARG", Args: []string{w}})
	}
	for si, s := range m.Stage {
		st := seamDockerfileStage{Name: s.Name.ValueString(), From: s.From.ValueString(), Platform: s.Platform.ValueString(), Comments: dfStrings(s.Comments)}
		if strings.ContainsAny(st.From, " \t") || st.From == "" {
			return doc, fmt.Errorf("stage %d: from must be one image reference or stage name", si)
		}
		if strings.ContainsAny(st.Name, " \t") {
			return doc, fmt.Errorf("stage %d: name must be one word", si)
		}
		for ii, im := range s.Instruction {
			ins, err := dfInstruction(im)
			if err != nil {
				return doc, fmt.Errorf("stage %d instruction %d: %w", si, ii, err)
			}
			st.Instructions = append(st.Instructions, ins)
		}
		doc.Stages = append(doc.Stages, st)
	}
	return doc, nil
}

func dfNumber(i int) types.Number { return types.NumberValue(big.NewFloat(float64(i))) }

func dfOutputs(text string, stageIndex *int, idx int, syntax string) dfBlockOutputs {
	si := types.NumberNull()
	if stageIndex != nil {
		si = dfNumber(*stageIndex)
	}
	return dfBlockOutputs{
		StageIndex: si, InstructionIndex: dfNumber(idx), Dockerfile: types.StringValue(text),
		ContentSHA256: types.StringValue(dockerfileSHA256(text)), DockerfileSyntax: types.StringValue(syntax),
	}
}

// dfFillBlockOutputs sets the computed values of the builder blocks from
// the rendered-and-reparsed document (same shape as the configuration).
func dfFillBlockOutputs(m *dockerfileDataSourceModel, doc seamDockerfileDoc) {
	esc := doc.Escape
	if esc == "" {
		esc = `\`
	}
	syntax := doc.Syntax
	if syntax == "" {
		syntax = contractDockerfileDefaultSyntax
	}
	for i := range m.GlobalArg {
		if i < len(doc.GlobalArgs) {
			m.GlobalArg[i].Instruction = types.StringValue("ARG")
			m.GlobalArg[i].dfBlockOutputs = dfOutputs(dockerfileInstructionText(doc.GlobalArgs[i], esc), nil, i, syntax)
		}
	}
	for si := range m.Stage {
		if si >= len(doc.Stages) {
			continue
		}
		st := doc.Stages[si]
		s := si
		m.Stage[si].StageName = types.StringNull()
		if st.Name != "" {
			m.Stage[si].StageName = types.StringValue(st.Name)
		}
		m.Stage[si].dfBlockOutputs = dfOutputs(dockerfileFromText(st), &s, 0, syntax)
		if m.Stage[si].Instruction == nil {
			m.Stage[si].Instruction = []dfInstructionModel{}
		}
		for ii := range m.Stage[si].Instruction {
			if ii >= len(st.Instructions) {
				continue
			}
			ins := st.Instructions[ii]
			m.Stage[si].Instruction[ii].Instruction = types.StringValue(ins.Keyword)
			m.Stage[si].Instruction[ii].dfBlockOutputs = dfOutputs(dockerfileInstructionText(ins, esc), &s, ii+1, syntax)
		}
	}
}

// dfNullBlockOutputs sets every computed block value to null (error paths).
func dfNullBlockOutputs(m *dockerfileDataSourceModel) {
	null := dfBlockOutputs{StageIndex: types.NumberNull(), InstructionIndex: types.NumberNull(), Dockerfile: types.StringNull(), ContentSHA256: types.StringNull(), DockerfileSyntax: types.StringNull()}
	for i := range m.GlobalArg {
		m.GlobalArg[i].Instruction, m.GlobalArg[i].dfBlockOutputs = types.StringNull(), null
	}
	for si := range m.Stage {
		m.Stage[si].StageName, m.Stage[si].dfBlockOutputs = types.StringNull(), null
		for ii := range m.Stage[si].Instruction {
			m.Stage[si].Instruction[ii].Instruction, m.Stage[si].Instruction[ii].dfBlockOutputs = types.StringNull(), null
		}
	}
}
