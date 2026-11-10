package provider

// Pass 5.3 Lane B: bake `target:<name>` named contexts (bake-reference.md
// "target.contexts": "Use another target as base"). buildx builds the
// referenced target first and hands its result to the dependent build as a
// named context. The provider does the same inside one BuildKit gateway
// build (bkclient.Client.Build): each dependency is solved with the
// dockerfile frontend, its result reference becomes a frontend input
// (`context:<name>` = `input:<name>`, with its image config passed as
// `input-metadata:<name>`, buildkit frontend/dockerui/namedcontext.go
// case "input"), and only the last target's result is exported. Everything
// stays in one session, so the dependencies' local directories are served
// under their own local names ("tpd-<target>-context" ...), fed to the
// frontend as its "context" / "dockerfile" inputs (dockerui/context.go
// initContext reads both from the frontend inputs).
//
// Multi-platform dependencies hand one input per platform
// (`context:<name>::<platform>`, dockerui/config.go names them so).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
)

// bkLinkStep is one target of a linked build: its request and options.
// ID is the bake target name; Contexts may refer to earlier steps with
// `target:<id>`.
type bkLinkStep struct {
	ID    string
	Req   seamBuildRequest
	Extra *bkExtra
}

// bkTargetRef returns the target a `target:` context names ("" = not one).
func bkTargetRef(v string) string {
	if t, ok := strings.CutPrefix(v, "target:"); ok {
		return t
	}
	return ""
}

// bkHasTargetContexts reports whether extra names any `target:` context.
func bkHasTargetContexts(extra *bkExtra) bool {
	if extra == nil {
		return false
	}
	for _, v := range extra.Contexts {
		if bkTargetRef(v) != "" {
			return true
		}
	}
	return false
}

// bkSplitTargetContexts returns extra without its `target:` contexts, and
// those contexts (context name -> target name).
func bkSplitTargetContexts(extra *bkExtra) (*bkExtra, map[string]string) {
	if extra == nil {
		return &bkExtra{}, nil
	}
	out := *extra
	out.Contexts = map[string]string{}
	refs := map[string]string{}
	for k, v := range extra.Contexts {
		if t := bkTargetRef(v); t != "" {
			refs[k] = t
			continue
		}
		out.Contexts[k] = v
	}
	return &out, refs
}

// bkLinkedPrepared is one prepared step.
type bkLinkedPrepared struct {
	id    string
	attrs map[string]string
	// locals are the frontend inputs served from local directories
	// (input name -> local name and options); the session ID is added in
	// the gateway callback.
	locals  map[string]bkLocalInput
	targets map[string]string // context name -> step id
}

// bkLocalInput is a local directory fed to the frontend as an input.
type bkLocalInput struct {
	local string
	opts  []llb.LocalOption
}

// bkLocalName is the local name a dependency's directory is served under.
func bkLocalName(id, name string) string {
	return "tpd-" + strings.ToLower(bakeSanitize(id)) + "-" + name
}

func bakeSanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// bkMergeSessionRequests merges the secrets and SSH entries of every step
// into the last one (one session serves the whole linked build). The same
// id with two different sources is an error.
func bkMergeSessionRequests(steps []bkLinkStep) (seamBuildRequest, error) {
	main := steps[len(steps)-1].Req
	secrets := map[string]seamBuildSecret{}
	var secOrder []string
	ssh := map[string]seamBuildSSH{}
	var sshOrder []string
	for _, st := range steps {
		for _, s := range st.Req.Secrets {
			if old, ok := secrets[s.ID]; ok {
				if old != s {
					return main, fmt.Errorf("target %q: build secret %q has a different source than in another target of the same build", st.ID, s.ID)
				}
				continue
			}
			secrets[s.ID] = s
			secOrder = append(secOrder, s.ID)
		}
		for _, s := range st.Req.SSH {
			if old, ok := ssh[s.ID]; ok {
				if strings.Join(old.Paths, ",") != strings.Join(s.Paths, ",") {
					return main, fmt.Errorf("target %q: ssh %q has different paths than in another target of the same build", st.ID, s.ID)
				}
				continue
			}
			ssh[s.ID] = s
			sshOrder = append(sshOrder, s.ID)
		}
	}
	main.Secrets = nil
	for _, id := range secOrder {
		main.Secrets = append(main.Secrets, secrets[id])
	}
	main.SSH = nil
	for _, id := range sshOrder {
		main.SSH = append(main.SSH, ssh[id])
	}
	return main, nil
}

// bkBuildLinked builds steps in order in one gateway build; the last step
// is the requested target and the only one exported.
func bkBuildLinked(ctx context.Context, pd *providerData, conn *bkConn, steps []bkLinkStep) (*seamBuildResult, error) {
	if len(steps) == 0 {
		return nil, errors.New("nothing to build")
	}
	mainReq, err := bkMergeSessionRequests(steps)
	if err != nil {
		return nil, err
	}
	last := steps[len(steps)-1]
	mainExtra, mainRefs := bkSplitTargetContexts(last.Extra)
	prep, err := bkPrepare(ctx, pd, conn, mainReq, mainExtra)
	if prep != nil {
		defer prep.close()
	}
	if err != nil {
		return nil, err
	}
	// Dependencies: prepared like any build, then served under their own
	// local names in the main session.
	var deps []bkLinkedPrepared
	for _, st := range steps[:len(steps)-1] {
		dreq := st.Req
		dreq.Outputs = seamBuildOutputs{}
		dreq.CacheTo = nil
		dreq.Secrets, dreq.SSH = nil, nil
		dextra, drefs := bkSplitTargetContexts(st.Extra)
		dextra.Call = ""
		dp, err := bkPrepare(ctx, pd, conn, dreq, dextra)
		if dp != nil {
			defer dp.close()
		}
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", st.ID, err)
		}
		lp := bkLinkedPrepared{id: st.ID, attrs: maps.Clone(dp.opt.FrontendAttrs), locals: map[string]bkLocalInput{}, targets: drefs}
		for name, fs := range dp.opt.LocalMounts {
			local := bkLocalName(st.ID, name)
			prep.opt.LocalMounts[local] = fs
			switch name {
			case "context":
				opts := []llb.LocalOption{llb.SharedKeyHint(dp.opt.SharedKey)}
				if pats := bkReadDockerignoreFor(dreq.ContextDir); len(pats) > 0 {
					opts = append(opts, llb.ExcludePatterns(pats))
				}
				lp.locals["context"] = bkLocalInput{local: local, opts: opts}
			case "dockerfile":
				lp.locals["dockerfile"] = bkLocalInput{local: local, opts: []llb.LocalOption{
					llb.FollowPaths([]string{lp.attrs["filename"], lp.attrs["filename"] + ".dockerignore"}),
				}}
			default:
				if lp.attrs["context:"+name] == "local:"+name {
					lp.attrs["context:"+name] = "local:" + local
				}
			}
		}
		deps = append(deps, lp)
	}
	mainLP := bkLinkedPrepared{id: last.ID, targets: mainRefs}

	progress := newBKProgress(mainReq.Progress)
	ch := make(chan *bkclient.SolveStatus)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for st := range ch {
			progress.add(st)
		}
	}()
	tflog.Info(ctx, "BuildKit linked build", map[string]any{"builder": conn.Node.Builder, "target": last.ID, "dependencies": len(deps)})
	resp, err := conn.Client.Build(ctx, prep.opt, "", func(ctx context.Context, gw gateway.Client) (*gateway.Result, error) {
		sid := gw.BuildOpts().SessionID
		base := gw.BuildOpts().Opts
		results := map[string]*gateway.Result{}
		solve := func(lp bkLinkedPrepared, attrs map[string]string) (*gateway.Result, error) {
			fi := map[string]*pb.Definition{}
			for name, li := range lp.locals {
				st := llb.Local(li.local, append([]llb.LocalOption{llb.SessionID(sid)}, li.opts...)...)
				def, err := st.Marshal(ctx)
				if err != nil {
					return nil, err
				}
				fi[name] = def.ToPB()
			}
			if err := bkLinkInputs(lp, results, attrs, fi); err != nil {
				return nil, err
			}
			return gw.Solve(ctx, gateway.SolveRequest{Frontend: bkFrontend, FrontendOpt: attrs, FrontendInputs: fi})
		}
		for _, d := range deps {
			attrs := maps.Clone(d.attrs)
			for k, v := range base {
				if k == "cache-imports" || k == "cache-from" {
					attrs[k] = v
				}
			}
			res, err := solve(d, attrs)
			if err != nil {
				return nil, fmt.Errorf("target %q: %w", d.id, err)
			}
			results[d.id] = res
		}
		return solve(mainLP, maps.Clone(base))
	}, ch)
	<-done
	progress.flush()
	for _, l := range prep.loads {
		if lerr := l.wait(); lerr != nil && err == nil {
			err = fmt.Errorf("loading the built image into the daemon: %w", lerr)
		}
	}
	if err != nil {
		return &seamBuildResult{BuildLog: progress.log()}, progress.wrap(err)
	}
	res := &seamBuildResult{BuildLog: progress.log(), Platforms: append([]string(nil), mainReq.Platforms...)}
	res.Metadata = bkMetadata(resp.ExporterResponse, conn.Node, prep.opt.Ref, progress.warnings)
	res.ImageID = resp.ExporterResponse["containerimage.config.digest"]
	res.Digest = resp.ExporterResponse["containerimage.digest"]
	if res.ImageID == "" {
		res.ImageID = res.Digest
	}
	if mainReq.Outputs.Push && res.Digest != "" && len(mainReq.Outputs.Names) > 0 {
		res.ImageDigestRef = bkDigestRef(mainReq.Outputs.Names[0], res.Digest)
	}
	return res, nil
}

// bkLinkInputs adds the `target:` contexts of lp as frontend inputs from
// earlier results: one `input:` context per result reference, plus its
// image config.
func bkLinkInputs(lp bkLinkedPrepared, results map[string]*gateway.Result, attrs map[string]string, fi map[string]*pb.Definition) error {
	names := make([]string, 0, len(lp.targets))
	for k := range lp.targets {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		tgt := lp.targets[name]
		res, ok := results[tgt]
		if !ok {
			return fmt.Errorf("named context %s=target:%s: target %q is not built before %q", name, tgt, tgt, lp.id)
		}
		add := func(key string, ref gateway.Reference, cfg []byte) error {
			st := llb.Scratch()
			if ref != nil {
				var err error
				if st, err = ref.ToState(); err != nil {
					return err
				}
			}
			def, err := st.Marshal(context.Background())
			if err != nil {
				return err
			}
			fi[key] = def.ToPB()
			attrs["context:"+key] = "input:" + key
			if len(cfg) > 0 {
				md, _ := json.Marshal(map[string][]byte{"containerimage.config": cfg})
				attrs["input-metadata:"+key] = string(md)
			}
			return nil
		}
		if res.Ref != nil || len(res.Refs) == 0 {
			if err := add(name, res.Ref, res.Metadata["containerimage.config"]); err != nil {
				return err
			}
			continue
		}
		plats := make([]string, 0, len(res.Refs))
		for p := range res.Refs {
			plats = append(plats, p)
		}
		sort.Strings(plats)
		for _, p := range plats {
			if err := add(name+"::"+p, res.Refs[p], res.Metadata["containerimage.config/"+p]); err != nil {
				return err
			}
		}
	}
	return nil
}

// bkReadDockerignoreFor is imgReadDockerignore for a context directory that
// may be relative.
func bkReadDockerignoreFor(dir string) []string {
	abs, err := filepath.Abs(expandHome(dir))
	if err != nil {
		return nil
	}
	if _, err := os.Stat(abs); err != nil {
		return nil
	}
	p, _ := imgReadDockerignore(abs)
	return p
}
