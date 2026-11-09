package provider

// Lane D: the Dockerfile render / parse seam (seams.go seamDockerfile).
//
// Parsing uses github.com/moby/buildkit/frontend/dockerfile/parser (the
// same lexer BuildKit's frontend uses: line continuations, comments, parser
// directives, heredocs, JSON forms). Rendering is ours. The renderer writes
// every instruction on one line (continuations are joined, which the parser
// does anyway), heredoc bodies after their instruction, parser directives
// first and one blank line before each FROM. Render(Parse(x)) re-parses to
// the same document (golden tests in dockerfile_seam_test.go).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

func init() { seamDockerfileImpl = dockerfileSeam{} }

// dockerfileSeam implements seamDockerfile.
type dockerfileSeam struct{}

func (dockerfileSeam) Render(doc seamDockerfileDoc) (string, error) { return dockerfileRender(doc) }

func (dockerfileSeam) Parse(text string) (seamDockerfileDoc, error) {
	p, err := dockerfileParse(text)
	if err != nil {
		return seamDockerfileDoc{}, err
	}
	return p.Doc, nil
}

// dockerfileKeywords are the instructions of the Dockerfile reference
// (frontend 1.28), upper case.
var dockerfileKeywords = map[string]bool{
	"ADD": true, "ARG": true, "CMD": true, "COPY": true, "ENTRYPOINT": true, "ENV": true, "EXPOSE": true,
	"FROM": true, "HEALTHCHECK": true, "LABEL": true, "MAINTAINER": true, "ONBUILD": true, "RUN": true,
	"SHELL": true, "STOPSIGNAL": true, "USER": true, "VOLUME": true, "WORKDIR": true,
}

// dockerfileParsed is a parse result with the positions the seam document
// does not carry.
type dockerfileParsed struct {
	Doc seamDockerfileDoc
	// GlobalLines[i] is the 1-based start line of Doc.GlobalArgs[i].
	GlobalLines []int
	// FromLines[s] is the line of stage s's FROM; StageLines[s][i] the line
	// of Doc.Stages[s].Instructions[i].
	FromLines  []int
	StageLines [][]int
	// Warnings are the parser's warnings (empty continuation lines).
	Warnings []string
}

// dockerfileParse parses Dockerfile text into the seam document.
func dockerfileParse(text string) (*dockerfileParsed, error) {
	data := bytes.TrimPrefix([]byte(text), []byte{0xEF, 0xBB, 0xBF})
	out := &dockerfileParsed{}
	var dp parser.DirectiveParser
	dirs, err := dp.ParseAll(data)
	if err != nil {
		return nil, fmt.Errorf("dockerfile: %w", err)
	}
	for _, d := range dirs {
		switch d.Name {
		case "syntax":
			out.Doc.Syntax = d.Value
		case "escape":
			out.Doc.Escape = d.Value
		case "check":
			c, err := dockerfileParseCheck(d.Value)
			if err != nil {
				return nil, err
			}
			out.Doc.Check = c
		}
	}
	res, err := parser.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("dockerfile: %w", err)
	}
	for _, w := range res.Warnings {
		out.Warnings = append(out.Warnings, w.Short)
	}
	for _, n := range res.AST.Children {
		kw := strings.ToUpper(n.Value)
		if !dockerfileKeywords[kw] {
			return nil, fmt.Errorf("dockerfile line %d: unknown instruction %q", n.StartLine, n.Value)
		}
		if kw == "FROM" {
			st, err := dockerfileStageFromNode(n)
			if err != nil {
				return nil, err
			}
			out.Doc.Stages = append(out.Doc.Stages, st)
			out.FromLines = append(out.FromLines, n.StartLine)
			out.StageLines = append(out.StageLines, nil)
			continue
		}
		ins, err := dockerfileInstructionFromNode(n)
		if err != nil {
			return nil, err
		}
		if len(out.Doc.Stages) == 0 {
			if kw != "ARG" {
				return nil, fmt.Errorf("dockerfile line %d: %s before the first FROM (only ARG may precede FROM)", n.StartLine, kw)
			}
			out.Doc.GlobalArgs = append(out.Doc.GlobalArgs, ins)
			out.GlobalLines = append(out.GlobalLines, n.StartLine)
			continue
		}
		s := len(out.Doc.Stages) - 1
		out.Doc.Stages[s].Instructions = append(out.Doc.Stages[s].Instructions, ins)
		out.StageLines[s] = append(out.StageLines[s], n.StartLine)
	}
	return out, nil
}

// dockerfileParseCheck parses the value of `# check=skip=a,b;error=true`.
func dockerfileParseCheck(v string) (seamDockerfileCheck, error) {
	var c seamDockerfileCheck
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			return c, fmt.Errorf("dockerfile: check directive: %q is not key=value", part)
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "skip":
			for _, s := range strings.Split(val, ",") {
				if s = strings.TrimSpace(s); s != "" {
					c.Skip = append(c.Skip, s)
				}
			}
		case "error":
			switch strings.ToLower(strings.TrimSpace(val)) {
			case "true":
				c.Error = true
			case "false":
				c.Error = false
			default:
				return c, fmt.Errorf("dockerfile: check directive: error=%q is not true or false", val)
			}
		default:
			return c, fmt.Errorf("dockerfile: check directive: unknown key %q (skip, error)", k)
		}
	}
	return c, nil
}

func dockerfileStageFromNode(n *parser.Node) (seamDockerfileStage, error) {
	st := seamDockerfileStage{Comments: dockerfileComments(n.PrevComment)}
	for _, f := range n.Flags {
		name, val, _ := strings.Cut(strings.TrimPrefix(f, "--"), "=")
		if name != "platform" {
			return st, fmt.Errorf("dockerfile line %d: unknown FROM flag --%s", n.StartLine, name)
		}
		st.Platform = val
	}
	words := dockerfileNodeValues(n.Next)
	switch {
	case len(words) == 1:
		st.From = words[0]
	case len(words) == 3 && strings.EqualFold(words[1], "AS"):
		st.From, st.Name = words[0], words[2]
	default:
		return st, fmt.Errorf("dockerfile line %d: FROM needs <image> [AS <name>], got %q", n.StartLine, strings.Join(words, " "))
	}
	return st, nil
}

func dockerfileComments(c []string) []string {
	if len(c) == 0 {
		return nil
	}
	return append([]string(nil), c...)
}

// dockerfileNodeValues returns the values of a Next chain.
func dockerfileNodeValues(n *parser.Node) []string {
	var out []string
	for ; n != nil; n = n.Next {
		out = append(out, n.Value)
	}
	return out
}

// dockerfileInstructionFromNode converts one AST child (not FROM).
func dockerfileInstructionFromNode(n *parser.Node) (seamDockerfileInstruction, error) {
	ins := seamDockerfileInstruction{Comments: dockerfileComments(n.PrevComment)}
	inner := n
	if strings.EqualFold(n.Value, "ONBUILD") {
		if n.Next == nil || len(n.Next.Children) == 0 {
			return ins, fmt.Errorf("dockerfile line %d: ONBUILD needs an instruction", n.StartLine)
		}
		inner = n.Next.Children[0]
		ins.OnBuild = true
		kw := strings.ToUpper(inner.Value)
		if kw == "ONBUILD" || kw == "FROM" || kw == "MAINTAINER" || !dockerfileKeywords[kw] {
			return ins, fmt.Errorf("dockerfile line %d: ONBUILD %s is not allowed", n.StartLine, inner.Value)
		}
	}
	ins.Keyword = strings.ToUpper(inner.Value)
	for _, f := range inner.Flags {
		name, val, hasVal := strings.Cut(strings.TrimPrefix(f, "--"), "=")
		fl := seamDockerfileFlag{Name: name}
		if hasVal {
			fl.Values = dockerfileSplitCSV(val)
		}
		ins.Flags = append(ins.Flags, fl)
	}
	ins.JSONForm = inner.Attributes["json"]
	switch ins.Keyword {
	case "ENV", "LABEL":
		for k := inner.Next; k != nil; {
			v := k.Next
			if v == nil {
				break
			}
			sep := ""
			if v.Next != nil {
				sep = v.Next.Value
			}
			if sep == "" {
				ins.Args = append(ins.Args, k.Value, v.Value)
			} else {
				ins.Args = append(ins.Args, k.Value+sep+v.Value)
			}
			if v.Next == nil {
				break
			}
			k = v.Next.Next
		}
	default:
		ins.Args = dockerfileNodeValues(inner.Next)
	}
	for _, h := range n.Heredocs {
		hd := seamDockerfileHeredoc{Name: h.Name, Content: h.Content, StripTabs: h.Chomp, Quoted: !h.Expand}
		if (ins.Keyword == "COPY" || ins.Keyword == "ADD") && len(ins.Args) > 0 {
			hd.Target = ins.Args[len(ins.Args)-1]
		}
		ins.Heredocs = append(ins.Heredocs, hd)
	}
	return ins, nil
}

// dockerfileSplitCSV splits a flag value on commas outside quotes. Values are
// kept as written (quotes included), so joining with "," restores the text.
func dockerfileSplitCSV(v string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	for _, r := range v {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
			cur.WriteRune(r)
		case r == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(out, cur.String())
}

var dockerfileDirectiveLikeRe = regexp.MustCompile(`^(?i)(syntax|escape|check)\s*=`)

// dockerfileRender prints doc as Dockerfile text.
func dockerfileRender(doc seamDockerfileDoc) (string, error) {
	esc := doc.Escape
	if esc == "" {
		esc = `\`
	}
	if esc != `\` && esc != "`" {
		return "", fmt.Errorf("dockerfile: escape must be \\ or ` (got %q)", doc.Escape)
	}
	var b strings.Builder
	directives := false
	if doc.Syntax != "" {
		fmt.Fprintf(&b, "# syntax=%s\n", doc.Syntax)
		directives = true
	}
	if doc.Escape != "" {
		fmt.Fprintf(&b, "# escape=%s\n", doc.Escape)
		directives = true
	}
	if c := dockerfileRenderCheck(doc.Check); c != "" {
		fmt.Fprintf(&b, "# check=%s\n", c)
		directives = true
	}
	// A comment that looks like a directive must not become one: a blank
	// line ends the directive block.
	first := firstComment(doc)
	if directives || dockerfileDirectiveLikeRe.MatchString(first) {
		b.WriteString("\n")
	}
	for _, a := range doc.GlobalArgs {
		if a.Keyword != "ARG" {
			return "", fmt.Errorf("dockerfile: global instruction %s: only ARG may precede the first FROM", a.Keyword)
		}
		if err := dockerfileRenderInstruction(&b, a, esc); err != nil {
			return "", err
		}
	}
	if len(doc.Stages) == 0 {
		return "", fmt.Errorf("dockerfile: at least one stage (FROM) is required")
	}
	for i, st := range doc.Stages {
		if i > 0 || len(doc.GlobalArgs) > 0 {
			b.WriteString("\n")
		}
		if strings.TrimSpace(st.From) == "" {
			return "", fmt.Errorf("dockerfile: stage %d: from is required", i)
		}
		dockerfileRenderComments(&b, st.Comments)
		b.WriteString("FROM ")
		if st.Platform != "" {
			b.WriteString("--platform=" + dockerfileQuoteFlag(st.Platform, esc) + " ")
		}
		b.WriteString(st.From)
		if st.Name != "" {
			b.WriteString(" AS " + st.Name)
		}
		b.WriteString("\n")
		for _, ins := range st.Instructions {
			if err := dockerfileRenderInstruction(&b, ins, esc); err != nil {
				return "", fmt.Errorf("stage %d: %w", i, err)
			}
		}
	}
	return b.String(), nil
}

func firstComment(doc seamDockerfileDoc) string {
	if len(doc.GlobalArgs) > 0 {
		if len(doc.GlobalArgs[0].Comments) > 0 {
			return doc.GlobalArgs[0].Comments[0]
		}
		return ""
	}
	if len(doc.Stages) > 0 && len(doc.Stages[0].Comments) > 0 {
		return doc.Stages[0].Comments[0]
	}
	return ""
}

func dockerfileRenderCheck(c seamDockerfileCheck) string {
	var parts []string
	if len(c.Skip) > 0 {
		parts = append(parts, "skip="+strings.Join(c.Skip, ","))
	}
	if c.Error {
		parts = append(parts, "error=true")
	}
	return strings.Join(parts, ";")
}

func dockerfileRenderComments(b *strings.Builder, comments []string) {
	for _, c := range comments {
		for _, l := range strings.Split(c, "\n") {
			b.WriteString("# " + l + "\n")
		}
	}
}

// dockerfileInstructionLine renders the instruction line without heredoc
// bodies or comments.
func dockerfileInstructionLine(ins seamDockerfileInstruction, esc string) (string, error) {
	kw := strings.ToUpper(ins.Keyword)
	if !dockerfileKeywords[kw] || kw == "FROM" || kw == "ONBUILD" {
		return "", fmt.Errorf("dockerfile: %q is not an instruction keyword here", ins.Keyword)
	}
	var b strings.Builder
	if ins.OnBuild {
		if kw == "MAINTAINER" {
			return "", fmt.Errorf("dockerfile: ONBUILD MAINTAINER is not allowed")
		}
		b.WriteString("ONBUILD ")
	}
	b.WriteString(kw)
	for _, f := range ins.Flags {
		if f.Name == "" {
			return "", fmt.Errorf("dockerfile: %s: flag without a name", kw)
		}
		b.WriteString(" --" + f.Name)
		if f.Values != nil {
			b.WriteString("=" + dockerfileQuoteFlag(strings.Join(f.Values, ","), esc))
		}
	}
	args, err := dockerfileRenderArgs(kw, ins)
	if err != nil {
		return "", err
	}
	if args != "" {
		if strings.HasPrefix(args, "--") {
			// "--" ends the flags, so an argument that starts with "--" is
			// not read back as a flag (CMD -- --param).
			b.WriteString(" --")
		}
		b.WriteString(" " + args)
	}
	return b.String(), nil
}

func dockerfileRenderArgs(kw string, ins seamDockerfileInstruction) (string, error) {
	if kw == "HEALTHCHECK" {
		if len(ins.Args) == 0 {
			return "", fmt.Errorf("dockerfile: HEALTHCHECK needs NONE or CMD")
		}
		if ins.JSONForm {
			j, err := dockerfileJSONArray(ins.Args[1:])
			return ins.Args[0] + " " + j, err
		}
		return strings.Join(ins.Args, " "), nil
	}
	if ins.JSONForm {
		return dockerfileJSONArray(ins.Args)
	}
	if kw == "SHELL" {
		return "", fmt.Errorf("dockerfile: SHELL takes the JSON (exec) form only")
	}
	return strings.Join(ins.Args, " "), nil
}

// dockerfileJSONArray renders a JSON string array without HTML escaping.
func dockerfileJSONArray(a []string) (string, error) {
	if a == nil {
		a = []string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(a); err != nil {
		return "", err
	}
	s := strings.TrimSuffix(buf.String(), "\n")
	// Dockerfile style: a space after each comma.
	return strings.ReplaceAll(s, `","`, `", "`), nil
}

func dockerfileRenderInstruction(b *strings.Builder, ins seamDockerfileInstruction, esc string) error {
	line, err := dockerfileInstructionLine(ins, esc)
	if err != nil {
		return err
	}
	dockerfileRenderComments(b, ins.Comments)
	b.WriteString(line + "\n")
	for _, h := range ins.Heredocs {
		if h.Name == "" {
			return fmt.Errorf("dockerfile: %s: heredoc without a delimiter", ins.Keyword)
		}
		b.WriteString(h.Content)
		if h.Content != "" && !strings.HasSuffix(h.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(h.Name + "\n")
	}
	return nil
}

// dockerfileQuoteFlag quotes a flag value when the flag lexer would change
// it (whitespace, quotes or the escape token).
func dockerfileQuoteFlag(v, esc string) string {
	if v != "" && !strings.ContainsAny(v, " \t\"'"+esc) {
		return v
	}
	if v == "" {
		return ""
	}
	r := strings.NewReplacer(esc, esc+esc, `"`, esc+`"`)
	return `"` + r.Replace(v) + `"`
}

// dockerfileQuoteWord quotes a shell-form word (LABEL / ENV value, ARG
// default) when it holds whitespace, quotes or the escape token, keeping
// $VAR references live (double quotes).
func dockerfileQuoteWord(v, esc string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n\"'"+esc) {
		return v
	}
	r := strings.NewReplacer(esc, esc+esc, `"`, esc+`"`, "\n", esc+"n")
	return `"` + r.Replace(v) + `"`
}
