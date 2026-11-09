package provider

// Lane D: data.docker_dockerfile image mode. The Engine API cannot return a
// Dockerfile, so the instruction list is rebuilt from GET
// /images/{name}/history (CreatedBy, oldest first) plus the image config for
// values the history does not record. The result is one stage on `scratch`
// with fidelity "approximate" (Q49): earlier stages, COPY/ADD sources, RUN
// --mount/--network/--security flags and heredoc structure are not recorded
// in an image.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/image"
)

// dockerfileImageResult is what image mode reads.
type dockerfileImageResult struct {
	Parsed      *dockerfileParsed
	Rendered    string
	ImageID     string
	DigestRef   string
	History     []any
	Labels      map[string]string
	Exposed     []any
	HistoryOf   []int // HistoryOf[i] = history index of instruction i, -1 = from the config
	Items       []image.HistoryResponseItem
	Unconverted int // history entries that carried an instruction the reconstruction could not read
}

// dockerfileFromImage reads image ref through the Engine API and rebuilds
// an approximate Dockerfile.
func dockerfileFromImage(ctx context.Context, pd *providerData, ref string) (*dockerfileImageResult, error) {
	if pd == nil || pd.Client == nil {
		return nil, fmt.Errorf("image mode needs a configured provider (Docker Engine connection)")
	}
	insp, err := pd.Client.ImageInspect(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("inspecting image %q: %w", ref, err)
	}
	hist, err := pd.Client.ImageHistory(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("reading the history of image %q: %w", ref, err)
	}
	res := &dockerfileImageResult{ImageID: insp.ID, Items: hist.Items, Labels: map[string]string{}, Exposed: []any{}, History: []any{}}
	if len(insp.RepoDigests) > 0 {
		res.DigestRef = insp.RepoDigests[0]
	}
	for _, h := range hist.Items {
		tags := append([]string{}, h.Tags...)
		res.History = append(res.History, map[string]any{
			"id": h.ID, "created": h.Created, "created_by": h.CreatedBy, "tags": tags, "size": h.Size, "comment": h.Comment,
		})
	}
	var cfg dockerspec.DockerOCIImageConfig
	if insp.Config != nil {
		cfg = *insp.Config
	}
	for k, v := range cfg.Labels {
		res.Labels[k] = v
	}
	for _, p := range dockerfileSortedKeys(cfg.ExposedPorts) {
		res.Exposed = append(res.Exposed, dockerfilePortValue(p))
	}
	doc, historyOf, skipped := dockerfileReconstruct(ref, insp.ID, hist.Items, cfg)
	res.Unconverted = skipped
	text, err := dockerfileRender(doc)
	if err != nil {
		return nil, fmt.Errorf("rendering the reconstructed Dockerfile: %w", err)
	}
	parsed, err := dockerfileParse(text)
	if err != nil {
		return nil, fmt.Errorf("re-reading the reconstructed Dockerfile: %w", err)
	}
	// Image mode has no source lines.
	parsed.StageLines, parsed.GlobalLines, parsed.FromLines = nil, nil, nil
	res.Parsed, res.Rendered, res.HistoryOf = parsed, text, historyOf
	return res, nil
}

var (
	dockerfileHistRunArgsRe = regexp.MustCompile(`^\|(\d+) `)
	dockerfileHistCopyInRe  = regexp.MustCompile(`^(ADD|COPY) (\S+) in (.+)$`)
	dockerfileHistPortRe    = regexp.MustCompile(`(\d+(?:-\d+)?/[a-zA-Z]+)`)
)

// dockerfileHistoryLine turns one CreatedBy value into a Dockerfile line.
// "" = no instruction; "HEALTHCHECK" alone = take it from the config.
func dockerfileHistoryLine(createdBy string) string {
	s := strings.TrimSpace(createdBy)
	s = strings.TrimSpace(strings.TrimSuffix(s, "# buildkit"))
	if s == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(s, "/bin/sh -c #(nop)"); ok {
		s = strings.TrimSpace(rest)
	} else if strings.HasPrefix(s, "|") || strings.HasPrefix(s, "/bin/sh -c ") || strings.HasPrefix(s, "cmd /S /C ") {
		s = "RUN " + s
	}
	kw, rest, _ := strings.Cut(s, " ")
	kw = strings.ToUpper(kw)
	switch kw {
	case "RUN":
		body := strings.TrimSpace(rest)
		if m := dockerfileHistRunArgsRe.FindStringSubmatch(body); m != nil {
			// "|N k=v ..." lists the N build args in effect (values dropped:
			// the reconstruction does not repeat build-arg values).
			n, _ := strconv.Atoi(m[1])
			fields := strings.SplitN(strings.TrimSpace(body[len(m[0]):]), " ", n+1)
			if len(fields) == n+1 {
				body = fields[n]
			} else {
				body = ""
			}
		}
		for _, sh := range []string{"/bin/sh -c ", "cmd /S /C "} {
			body = strings.TrimPrefix(body, sh)
		}
		if strings.TrimSpace(body) == "" {
			return ""
		}
		return "RUN " + body
	case "EXPOSE":
		if strings.Contains(rest, "map[") {
			ports := dockerfileHistPortRe.FindAllString(rest, -1)
			if len(ports) == 0 {
				return ""
			}
			return "EXPOSE " + strings.Join(ports, " ")
		}
	case "VOLUME":
		r := strings.TrimSpace(rest)
		if strings.HasPrefix(r, "[") {
			var a []string
			if json.Unmarshal([]byte(r), &a) != nil {
				return "VOLUME " + strings.Join(strings.Fields(strings.Trim(r, "[]")), " ")
			}
		}
	case "HEALTHCHECK":
		return "HEALTHCHECK"
	case "ADD", "COPY":
		if m := dockerfileHistCopyInRe.FindStringSubmatch(s); m != nil {
			return m[1] + " " + m[2] + " " + strings.TrimSpace(m[3])
		}
	case "FROM":
		return ""
	}
	if !dockerfileKeywords[kw] {
		return ""
	}
	return s
}

// dockerfileHealthcheckInstruction renders an image config healthcheck.
func dockerfileHealthcheckInstruction(hc *dockerspec.HealthcheckConfig) (seamDockerfileInstruction, bool) {
	ins := seamDockerfileInstruction{Keyword: "HEALTHCHECK"}
	if hc == nil || len(hc.Test) == 0 {
		return ins, false
	}
	switch hc.Test[0] {
	case "NONE":
		ins.Args = []string{"NONE"}
		return ins, true
	case "CMD":
		ins.Args = append([]string{"CMD"}, hc.Test[1:]...)
		ins.JSONForm = true
	case "CMD-SHELL":
		ins.Args = []string{"CMD", strings.Join(hc.Test[1:], " ")}
	default:
		return ins, false
	}
	add := func(name, v string) {
		ins.Flags = append(ins.Flags, seamDockerfileFlag{Name: name, Values: []string{v}})
	}
	if hc.Interval > 0 {
		add("interval", hc.Interval.String())
	}
	if hc.Timeout > 0 {
		add("timeout", hc.Timeout.String())
	}
	if hc.StartPeriod > 0 {
		add("start-period", hc.StartPeriod.String())
	}
	if hc.StartInterval > 0 {
		add("start-interval", hc.StartInterval.String())
	}
	if hc.Retries > 0 {
		add("retries", strconv.Itoa(hc.Retries))
	}
	return ins, true
}

// dockerfileReconstruct builds the approximate document. historyOf maps each
// instruction to its history index (-1 = from the config).
func dockerfileReconstruct(ref, id string, items []image.HistoryResponseItem, cfg dockerspec.DockerOCIImageConfig) (seamDockerfileDoc, []int, int) {
	stage := seamDockerfileStage{
		From:     "scratch",
		Comments: []string{fmt.Sprintf("Reconstructed from the history of image %s (%s); fidelity approximate, the base image is not recorded.", ref, id)},
	}
	var historyOf []int
	seen := map[string]bool{}
	skipped := 0
	for i := len(items) - 1; i >= 0; i-- {
		line := dockerfileHistoryLine(items[i].CreatedBy)
		if line == "" {
			if strings.TrimSpace(items[i].CreatedBy) != "" {
				skipped++ // not an instruction this reconstruction can read
			}
			continue
		}
		if line == "HEALTHCHECK" {
			if ins, ok := dockerfileHealthcheckInstruction(cfg.Healthcheck); ok {
				stage.Instructions = append(stage.Instructions, ins)
				historyOf = append(historyOf, i)
				seen["HEALTHCHECK"] = true
			} else {
				skipped++
			}
			continue
		}
		p, err := dockerfileParse("FROM scratch\n" + line + "\n")
		if err != nil || len(p.Doc.Stages) != 1 || len(p.Doc.Stages[0].Instructions) != 1 {
			skipped++
			continue
		}
		ins := p.Doc.Stages[0].Instructions[0]
		if _, err := dockerfileInstructionLine(ins, `\`); err != nil {
			skipped++
			continue
		}
		stage.Instructions = append(stage.Instructions, ins)
		historyOf = append(historyOf, i)
		seen[ins.Keyword] = true
	}
	// Config values the history did not record (images built by other tools).
	add := func(ins seamDockerfileInstruction) {
		if seen[ins.Keyword] {
			return
		}
		stage.Instructions = append(stage.Instructions, ins)
		historyOf = append(historyOf, -1)
	}
	if len(cfg.Env) > 0 {
		var args []string
		for _, e := range cfg.Env {
			k, v, _ := strings.Cut(e, "=")
			args = append(args, k+"="+dockerfileQuoteWord(v, `\`))
		}
		add(seamDockerfileInstruction{Keyword: "ENV", Args: args})
	}
	if len(cfg.Labels) > 0 {
		var args []string
		for _, k := range dockerfileSortedKeys(cfg.Labels) {
			args = append(args, dockerfileQuoteWord(k, `\`)+"="+dockerfileQuoteWord(cfg.Labels[k], `\`))
		}
		add(seamDockerfileInstruction{Keyword: "LABEL", Args: args})
	}
	if len(cfg.ExposedPorts) > 0 {
		add(seamDockerfileInstruction{Keyword: "EXPOSE", Args: dockerfileSortedKeys(cfg.ExposedPorts)})
	}
	if len(cfg.Volumes) > 0 {
		add(seamDockerfileInstruction{Keyword: "VOLUME", Args: dockerfileSortedKeys(cfg.Volumes), JSONForm: true})
	}
	if cfg.WorkingDir != "" {
		add(seamDockerfileInstruction{Keyword: "WORKDIR", Args: []string{cfg.WorkingDir}})
	}
	if cfg.User != "" {
		add(seamDockerfileInstruction{Keyword: "USER", Args: []string{cfg.User}})
	}
	if len(cfg.Shell) > 0 {
		add(seamDockerfileInstruction{Keyword: "SHELL", Args: append([]string{}, cfg.Shell...), JSONForm: true})
	}
	if cfg.StopSignal != "" {
		add(seamDockerfileInstruction{Keyword: "STOPSIGNAL", Args: []string{cfg.StopSignal}})
	}
	if ins, ok := dockerfileHealthcheckInstruction(cfg.Healthcheck); ok {
		add(ins)
	}
	if len(cfg.Entrypoint) > 0 {
		add(seamDockerfileInstruction{Keyword: "ENTRYPOINT", Args: append([]string{}, cfg.Entrypoint...), JSONForm: true})
	}
	if len(cfg.Cmd) > 0 {
		add(seamDockerfileInstruction{Keyword: "CMD", Args: append([]string{}, cfg.Cmd...), JSONForm: true})
	}
	return seamDockerfileDoc{Stages: []seamDockerfileStage{stage}}, historyOf, skipped
}
