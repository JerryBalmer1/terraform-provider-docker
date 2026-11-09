package provider

// Pass 5 Lane E: action docker_buildx_bake (Q20), `docker buildx bake`:
// read a bake definition (files and/or inline content), resolve groups,
// targets, inherits, matrix and variables (buildx_bake_parse.go), then
// build every selected target through BuildKit on one builder connection
// and report one result object.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	units "github.com/docker/go-units"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const bakeItemID = "buildx:bake"

var (
	_ action.ActionWithConfigure      = (*buildxBakeAction)(nil)
	_ action.ActionWithValidateConfig = (*buildxBakeAction)(nil)
)

// NewBuildxBakeAction returns the docker_buildx_bake action.
func NewBuildxBakeAction() action.Action { return &buildxBakeAction{} }

type buildxBakeAction struct{ actBase }

type bakeActionModel struct {
	Files      types.List   `tfsdk:"files"`
	Content    types.String `tfsdk:"content"`
	Targets    types.List   `tfsdk:"targets"`
	Variables  types.Map    `tfsdk:"variables"`
	Set        types.Map    `tfsdk:"set"`
	WorkingDir types.String `tfsdk:"working_dir"`
	Builder    types.String `tfsdk:"builder"`
	Push       types.Bool   `tfsdk:"push"`
	Load       types.Bool   `tfsdk:"load"`
	NoCache    types.Bool   `tfsdk:"no_cache"`
	Pull       types.Bool   `tfsdk:"pull"`
	Provenance types.String `tfsdk:"provenance"`
	SBOM       types.Bool   `tfsdk:"sbom"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *buildxBakeAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_bake"
}

func (a *buildxBakeAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Builds one or more targets from a bake definition, like `docker buildx bake`. The definition comes from HCL, " +
			"JSON or compose files and/or inline `content`; groups, targets, `inherits`, `matrix`, variables (overridden by `variables`), " +
			"user functions and the bake standard library are resolved as the buildx bake reference describes, then every target is built " +
			"through BuildKit on the selected builder. The result object holds the resolved definition (`bake --print`), the per-target " +
			"metadata (`--metadata-file`), tags, digests and build refs. Images are not tracked in state; use `docker_image` for images " +
			"Terraform should own.",
		Attributes: map[string]schema.Attribute{
			"files": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Bake files to read, in merge order (`--file`): `.hcl`, `.json` or compose `.yml`/`.yaml`. Without `files` " +
					"and `content`, the files bake looks for (`compose.yaml` ... `docker-bake.override.hcl`) are read from `working_dir`.",
			},
			"content": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Inline bake definition, HCL or JSON (detected by a leading `{`), merged after `files`.",
			},
			"targets": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Targets or groups to build. Default `[\"default\"]` (the only target when the definition has one and no `default`).",
			},
			"variables": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Map of variable name to override value, where bake reads environment variables: primitives as text, " +
					"lists and sets as CSV, other types as JSON (or JSON for any typed variable under `<NAME>_JSON`). Every key must be a " +
					"declared variable. The provider's environment is never read.",
			},
			"set": schema.MapAttribute{
				ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Map of target attribute overrides (`--set`), keyed `<target>.<attribute>[.<key>]`; `<target>` may be `*` or a " +
					"glob. Examples: `\"*.platform\" = \"linux/amd64,linux/arm64\"`, `\"app.args.VERSION\" = \"1.2\"`, `\"*.push\" = \"true\"`. " +
					"List values take several entries separated by `;`.",
			},
			"working_dir": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Directory that relative bake file paths, contexts, secret and SSH paths are resolved against. Default: " +
					"Terraform's working directory, as buildx uses its current directory. Use `path.module` to resolve from the module.",
			},
			"builder": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Builder to build on (`--builder`): `default` (the daemon's BuildKit, the default) or the name of a " +
					"docker-container builder, e.g. `docker_buildx_builder.ci.name`.",
			},
			"push": schema.BoolAttribute{Optional: true, MarkdownDescription: "Whether to push every target's tags (`--push`, `*.output=type=registry`)."},
			"load": schema.BoolAttribute{Optional: true, MarkdownDescription: "Whether to load every target's image into the daemon (`--load`, `*.output=type=docker`)."},
			"no_cache": schema.BoolAttribute{
				Optional: true, MarkdownDescription: "Whether to build without the build cache (`--no-cache`).",
			},
			"pull": schema.BoolAttribute{Optional: true, MarkdownDescription: "Whether to always pull referenced images (`--pull`)."},
			"provenance": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Provenance attestation for every target (`--provenance`): `min`, `max` or `false`.",
				Validators:          []validator.String{stringvalidator.OneOf("min", "max", "false", "true")},
			},
			"sbom":            schema.BoolAttribute{Optional: true, MarkdownDescription: "Whether to add an SBOM attestation to every target (`--sbom`)."},
			actResultPathAttr: actResultPathAttribute(),
		},
	}
}

func (a *buildxBakeAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m bakeActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Set.IsUnknown() && !m.Set.IsNull() {
		for k := range m.Set.Elements() {
			if !strings.Contains(k, ".") {
				resp.Diagnostics.AddAttributeError(path.Root("set").AtMapKey(k), "Invalid set key", fmt.Sprintf("%q: keys are <target>.<attribute>[.<key>].", k))
			}
		}
	}
	if c, ok := knownString(m.Content); ok && strings.TrimSpace(c) == "" {
		resp.Diagnostics.AddAttributeError(path.Root("content"), "Empty content", "content must hold a bake definition when set.")
	}
}

// bakeInputs collects the definition files.
func bakeInputs(files []string, content, wd string) ([]bakeFile, error) {
	var out []bakeFile
	for _, f := range files {
		p := expandHome(f)
		if !filepath.IsAbs(p) {
			p = filepath.Join(wd, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading bake file: %w", err)
		}
		out = append(out, bakeFile{Name: p, Data: b})
	}
	if content != "" {
		name := filepath.Join(wd, "inline.docker-bake.hcl")
		if strings.HasPrefix(strings.TrimSpace(content), "{") {
			name = filepath.Join(wd, "inline.docker-bake.json")
		}
		out = append(out, bakeFile{Name: name, Data: []byte(content)})
	}
	if len(out) == 0 {
		for _, n := range bakeDefaultFiles {
			p := filepath.Join(wd, n)
			b, err := os.ReadFile(p)
			if err == nil {
				out = append(out, bakeFile{Name: p, Data: b})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no bake definition: set files or content, or put one of %s in %s", strings.Join(bakeDefaultFiles, ", "), wd)
		}
	}
	return out, nil
}

// bakeComposeLoader reads a compose file into bake targets: one per
// service with a build section, all in the default group.
func bakeComposeLoader(ctx context.Context, wd string) func(bakeFile) (*bakeDef, error) {
	return func(f bakeFile) (*bakeDef, error) {
		proj, err := seamComposeImpl.Load(ctx, seamComposeLoadRequest{Content: string(f.Data), WorkingDir: filepath.Dir(f.Name)})
		if err != nil {
			return nil, fmt.Errorf("loading compose file: %w", err)
		}
		def := &bakeDef{Groups: map[string][]string{}, Targets: map[string]*bakeTarget{}}
		var names []string
		for name := range proj.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		var built []string
		for _, name := range names {
			svc := proj.Services[name]
			b := svc.Build
			if b == nil {
				continue
			}
			t := &bakeTarget{Name: name}
			sp := func(s string) *string {
				if s == "" {
					return nil
				}
				return &s
			}
			t.Context = sp(b.Context)
			t.Dockerfile = sp(b.Dockerfile)
			t.DockerfileInline = sp(b.DockerfileInline)
			t.Target = sp(b.Target)
			t.Network = sp(b.Network)
			if len(b.Args) > 0 {
				t.Args = map[string]*string{}
				for k, v := range b.Args {
					t.Args[k] = v
				}
			}
			if len(b.Labels) > 0 {
				t.Labels = map[string]*string{}
				for k, v := range b.Labels {
					vv := v
					t.Labels[k] = &vv
				}
			}
			if len(b.AdditionalContexts) > 0 {
				t.Contexts = map[string]string(b.AdditionalContexts)
			}
			tags := append([]string(nil), b.Tags...)
			if svc.Image != "" {
				tags = append([]string{svc.Image}, tags...)
			}
			if len(tags) > 0 {
				t.Tags = tags
			}
			if len(b.Platforms) > 0 {
				t.Platforms = append([]string(nil), b.Platforms...)
			}
			if len(b.CacheFrom) > 0 {
				t.CacheFrom = append([]string(nil), b.CacheFrom...)
			}
			if len(b.CacheTo) > 0 {
				t.CacheTo = append([]string(nil), b.CacheTo...)
			}
			if len(b.NoCacheFilter) > 0 {
				t.NoCacheFilter = append([]string(nil), b.NoCacheFilter...)
			}
			if len(b.Entitlements) > 0 {
				t.Entitlements = append([]string(nil), b.Entitlements...)
			}
			for host, ips := range b.ExtraHosts {
				for _, ip := range ips {
					t.ExtraHosts = append(t.ExtraHosts, host+"="+ip)
				}
			}
			sort.Strings(t.ExtraHosts)
			for _, s := range b.Secrets {
				spec := "id=" + s.Source
				if sec, ok := proj.Secrets[s.Source]; ok {
					switch {
					case sec.File != "":
						spec += ",src=" + sec.File
					case sec.Environment != "":
						spec += ",type=env,env=" + sec.Environment
					}
				}
				t.Secrets = append(t.Secrets, spec)
			}
			for _, k := range b.SSH {
				if k.Path != "" {
					t.SSH = append(t.SSH, k.ID+"="+k.Path)
				} else {
					t.SSH = append(t.SSH, k.ID)
				}
			}
			for n, u := range b.Ulimits {
				if u == nil {
					continue
				}
				if u.Single != 0 {
					t.Ulimits = append(t.Ulimits, fmt.Sprintf("%s=%d", n, u.Single))
				} else {
					t.Ulimits = append(t.Ulimits, fmt.Sprintf("%s=%d:%d", n, u.Soft, u.Hard))
				}
			}
			sort.Strings(t.Ulimits)
			if b.NoCache {
				v := true
				t.NoCache = &v
			}
			if b.Pull {
				v := true
				t.Pull = &v
			}
			if b.ShmSize > 0 {
				s := fmt.Sprint(int64(b.ShmSize))
				t.ShmSize = &s
			}
			if b.Provenance != "" {
				t.Attest = append(t.Attest, "type=provenance,"+bakeComposeAttest(b.Provenance))
			}
			if b.SBOM != "" {
				t.Attest = append(t.Attest, "type=sbom,"+bakeComposeAttest(b.SBOM))
			}
			def.Targets[name] = t
			def.TargetOrder = append(def.TargetOrder, name)
			built = append(built, name)
		}
		def.Groups["default"] = built
		def.GroupOrder = []string{"default"}
		return def, nil
	}
}

// bakeComposeAttest maps a compose provenance/sbom value to attest options.
func bakeComposeAttest(v string) string {
	switch v {
	case "true":
		return "disabled=false"
	case "false":
		return "disabled=true"
	}
	if strings.Contains(v, "=") {
		return v
	}
	return "mode=" + v
}

// bakeGlobals are the action-wide flags.
type bakeGlobals struct {
	push, load, noCache, pull, sbom bool
	provenance                      string
}

// bakeRequest turns a resolved target into a build request.
func bakeRequest(t *bakeTarget, wd string, g bakeGlobals) (seamBuildRequest, *bkExtra, error) {
	var req seamBuildRequest
	extra := &bkExtra{Contexts: map[string]string{}}
	abs := func(p string) string {
		p = expandHome(p)
		if p == "" || filepath.IsAbs(p) || bkRemoteContext(p) {
			return p
		}
		return filepath.Join(wd, p)
	}
	if t.Context != nil {
		req.ContextDir = abs(*t.Context)
	}
	if t.DockerfileInline != nil {
		req.DockerfileInline = *t.DockerfileInline
	} else if t.Dockerfile != nil {
		req.Dockerfile = *t.Dockerfile
	}
	if t.Target != nil {
		req.Target = *t.Target
	}
	req.Platforms = append([]string(nil), t.Platforms...)
	if len(t.Args) > 0 {
		req.BuildArgs = map[string]string{}
		for k, v := range t.Args {
			if v != nil {
				req.BuildArgs[k] = *v
			}
		}
	}
	if len(t.Labels) > 0 {
		req.Labels = map[string]string{}
		for k, v := range t.Labels {
			if v != nil {
				req.Labels[k] = *v
			}
		}
	}
	for _, s := range t.Secrets {
		sec, err := bakeParseSecret(s)
		if err != nil {
			return req, nil, fmt.Errorf("target %q: secret %q: %w", t.Name, s, err)
		}
		if sec.Src != "" {
			sec.Src = abs(sec.Src)
		}
		req.Secrets = append(req.Secrets, sec)
	}
	for _, s := range t.SSH {
		ssh := bakeParseSSH(s)
		for i := range ssh.Paths {
			ssh.Paths[i] = abs(ssh.Paths[i])
		}
		req.SSH = append(req.SSH, ssh)
	}
	req.CacheFrom = append([]string(nil), t.CacheFrom...)
	req.CacheTo = append([]string(nil), t.CacheTo...)
	req.Outputs.Names = append([]string(nil), t.Tags...)
	for _, o := range t.Outputs {
		if strings.Contains(o, "=") {
			if kv, err := bkParseCSV(o); err == nil {
				if d := kv["dest"]; d != "" && d != "-" && !filepath.IsAbs(expandHome(d)) {
					o = strings.Replace(o, "dest="+d, "dest="+abs(d), 1)
				}
			}
		} else if o != "" && o != "-" {
			o = "type=local,dest=" + abs(o)
		}
		req.Outputs.Raw = append(req.Outputs.Raw, o)
	}
	req.Outputs.Push = g.push
	req.Outputs.Load = g.load
	if t.Network != nil {
		req.Network = *t.Network
	}
	req.NoCache = g.noCache || (t.NoCache != nil && *t.NoCache)
	req.NoCacheFilter = append([]string(nil), t.NoCacheFilter...)
	req.Pull = g.pull || (t.Pull != nil && *t.Pull)
	req.ExtraHosts = append([]string(nil), t.ExtraHosts...)
	if t.ShmSize != nil && *t.ShmSize != "" {
		n, err := units.RAMInBytes(*t.ShmSize)
		if err != nil {
			return req, nil, fmt.Errorf("target %q: shm-size %q: %w", t.Name, *t.ShmSize, err)
		}
		req.ShmSize = n
	}
	req.Ulimits = append([]string(nil), t.Ulimits...)
	req.Attestations.Extra = append([]string(nil), t.Attest...)
	if g.provenance != "" {
		req.Attestations.Provenance = g.provenance
		if g.provenance == "false" {
			req.Attestations.Extra = append(req.Attestations.Extra, "type=provenance,disabled=true")
		}
	}
	req.Attestations.SBOM = g.sbom
	for k, v := range t.Contexts {
		if !strings.Contains(v, "://") && !strings.HasPrefix(v, "target:") {
			v = abs(v)
		}
		extra.Contexts[k] = v
	}
	extra.Annotations = append([]string(nil), t.Annotations...)
	if t.Call != nil {
		extra.Call = *t.Call
	}
	extra.Entitlements = append([]string(nil), t.Entitlements...)
	return req, extra, nil
}

// bakeParseSecret parses a bake secret entry (buildx --secret syntax).
func bakeParseSecret(s string) (seamBuildSecret, error) {
	kv, err := bkParseCSV(s)
	if err != nil {
		return seamBuildSecret{}, err
	}
	sec := seamBuildSecret{ID: kv["id"]}
	src := kv["src"]
	if src == "" {
		src = kv["source"]
	}
	switch kv["type"] {
	case "env":
		sec.Env = kv["env"]
		if sec.Env == "" {
			sec.Env = src
		}
		if sec.Env == "" {
			sec.Env = sec.ID
		}
	case "", "file":
		if kv["env"] != "" {
			sec.Env = kv["env"]
		} else if src != "" {
			sec.Src = src
		} else {
			sec.Env = sec.ID
		}
	default:
		return sec, fmt.Errorf("unsupported secret type %q (file, env)", kv["type"])
	}
	if sec.ID == "" {
		if sec.Src != "" {
			sec.ID = filepath.Base(sec.Src)
		} else {
			return sec, errors.New("id is required")
		}
	}
	return sec, nil
}

// bakeParseSSH parses "id[=path[,path]]".
func bakeParseSSH(s string) seamBuildSSH {
	id, rest, ok := strings.Cut(s, "=")
	out := seamBuildSSH{ID: id}
	if ok && rest != "" {
		out.Paths = strings.Split(rest, ",")
	}
	return out
}

// bakeBuild builds one target; a var so unit tests can skip BuildKit.
var bakeBuild = bkBuild

// bakeConnect opens the builder; a var so unit tests can skip BuildKit.
var bakeConnect = bkConnect

// bakeTargetResult is what one target produced.
type bakeTargetResult struct {
	target *bakeTarget
	res    *seamBuildResult
	pushed bool
}

func (a *buildxBakeAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m bakeActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if a.client(&resp.Diagnostics) == nil || resp.Diagnostics.HasError() {
		return
	}
	wd := "."
	if v, ok := knownString(m.WorkingDir); ok && v != "" {
		wd = expandHome(v)
	}
	if abs, err := filepath.Abs(wd); err == nil {
		wd = abs
	}
	files, err := bakeInputs(stringListFrom(ctx, m.Files, &resp.Diagnostics), m.Content.ValueString(), wd)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("files"), "Cannot read bake definition", err.Error())
		return
	}
	def, err := bakeParse(files, stringMapFrom(ctx, m.Variables, &resp.Diagnostics), bakeComposeLoader(ctx, wd))
	if err != nil {
		resp.Diagnostics.AddError("Invalid bake definition", err.Error())
		return
	}
	resolved, err := def.resolve(stringListFrom(ctx, m.Targets, &resp.Diagnostics), stringMapFrom(ctx, m.Set, &resp.Diagnostics))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("targets"), "Cannot resolve bake targets", err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	g := bakeGlobals{push: m.Push.ValueBool(), load: m.Load.ValueBool(), noCache: m.NoCache.ValueBool(), pull: m.Pull.ValueBool(),
		sbom: m.SBOM.ValueBool(), provenance: m.Provenance.ValueString()}
	conn, err := bakeConnect(ctx, a.pd, seamBuilderRef{Name: m.Builder.ValueString()})
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("builder"), "Cannot connect to the builder", err.Error())
		return
	}
	defer conn.Close()

	var results []bakeTargetResult
	var failures []string
	for _, t := range resolved.Targets {
		breq, extra, err := bakeRequest(t, wd, g)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if len(t.Resources) > 0 || len(t.Policy) > 0 {
			actProgress(ctx, resp, "target %s: resources and policy are not applied by this provider", t.Name)
		}
		name := t.Name
		breq.Progress = func(line string) { tflog.Debug(ctx, line, map[string]any{"target": name}) }
		actProgress(ctx, resp, "building target %s on builder %s", t.Name, conn.Node.Builder)
		res, err := bakeBuild(ctx, a.pd, conn, breq, extra)
		if err != nil {
			failures = append(failures, fmt.Sprintf("target %q: %v", t.Name, err))
			continue
		}
		pushed := breq.Outputs.Push
		for _, o := range breq.Outputs.Raw {
			if kv, err := bkParseCSV(o); err == nil && (kv["type"] == "registry" || kv["push"] == "true") {
				pushed = true
			}
		}
		actProgress(ctx, resp, "built target %s", t.Name)
		results = append(results, bakeTargetResult{target: t, res: res, pushed: pushed})
	}
	if len(failures) > 0 {
		resp.Diagnostics.AddError("docker_buildx_bake failed", strings.Join(failures, "\n\n"))
		return
	}
	actFinishResult(ctx, resp, a.pd, bakeItemID, bakeResultValues(resolved, results), m.ResultPath)
}

// bakeResultValues builds the action's outputs.
func bakeResultValues(resolved *bakeResolved, results []bakeTargetResult) map[string]any {
	metadata := map[string]any{}
	imageRefs := map[string]any{}
	digestRefs := map[string]any{}
	digests := map[string]any{}
	platforms := map[string]any{}
	buildRefs := map[string]any{}
	targets := []string{}
	for _, r := range results {
		n := r.target.Name
		targets = append(targets, n)
		metadata[n] = r.res.Metadata
		imageRefs[n] = bxStrings(r.target.Tags)
		platforms[n] = bxStrings(r.target.Platforms)
		digests[n] = r.res.Digest
		if s, ok := r.res.Metadata["buildx.build.ref"].(string); ok {
			buildRefs[n] = s
		}
		refs := []string{}
		if r.pushed && r.res.Digest != "" {
			for _, tag := range r.target.Tags {
				refs = append(refs, bkDigestRef(tag, r.res.Digest))
			}
		}
		digestRefs[n] = refs
	}
	print := resolved.printObject()
	metaJSON, _ := actJSON(metadata, true)
	return map[string]any{
		"bake_hcl":          resolved.printHCL(),
		"bake_json":         resolved.printJSON(),
		"metadata_json":     metaJSON,
		"result":            map[string]any{"bake": print, "metadata": metadata},
		"targets":           targets,
		"image_refs":        imageRefs,
		"image_digest_refs": digestRefs,
		"digests":           digests,
		"platforms":         platforms,
		"build_refs":        buildRefs,
	}
}
