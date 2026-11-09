package provider

// Pass 5 Lane E: data docker_buildx_history (`docker buildx history
// inspect`: one build record) and data docker_buildx_history_records
// (`docker buildx history ls`), Q24 as applied in DECISIONS.md. Records
// come from BuildKit's ListenBuildHistory on every running node of the
// builder.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	controlapi "github.com/moby/buildkit/api/services/control"
)

const (
	bxhInspectItemID = "buildx:history-inspect"
	bxhListItemID    = "buildx:history-ls"
)

var (
	_ datasource.DataSourceWithConfigure = (*buildxHistoryDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*buildxHistoryRecordsDataSource)(nil)
)

// bkHistory reads the build records of one node (ref "" = all); a var so
// unit tests can fake BuildKit.
var bkHistory = func(ctx context.Context, pd *providerData, node bkNode, ref string) ([]*controlapi.BuildHistoryRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*bkDialTimeout)
	defer cancel()
	c, err := bkOpen(ctx, pd, node)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stream, err := c.ControlClient().ListenBuildHistory(ctx, &controlapi.BuildHistoryRequest{EarlyExit: true, Ref: ref})
	if err != nil {
		return nil, err
	}
	var out []*controlapi.BuildHistoryRecord
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if ev.GetType() == controlapi.BuildHistoryEventType_DELETED || ev.GetRecord() == nil {
			continue
		}
		out = append(out, ev.GetRecord())
	}
}

// bxhRecord is one record with the node it came from.
type bxhRecord struct {
	Node bkNode
	Rec  *controlapi.BuildHistoryRecord
}

// bxhRecords reads the records of every running node of builder, newest
// first.
func bxhRecords(ctx context.Context, pd *providerData, builder, ref string) ([]bxhRecord, error) {
	nodes, err := bkBuilderNodes(ctx, pd, builder)
	if err != nil {
		return nil, err
	}
	var out []bxhRecord
	var errs []string
	queried := 0
	for _, n := range nodes {
		if n.Status != bkStatusRunning {
			continue
		}
		queried++
		recs, err := bkHistory(ctx, pd, n, ref)
		if err != nil {
			errs = append(errs, fmt.Sprintf("node %s: %v", n.Name, err))
			continue
		}
		for _, r := range recs {
			out = append(out, bxhRecord{Node: n, Rec: r})
		}
	}
	if queried == 0 {
		return nil, fmt.Errorf("builder %q has no running node", builder)
	}
	if len(errs) == queried {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Rec.GetCreatedAt().AsTime().After(out[j].Rec.GetCreatedAt().AsTime())
	})
	return out, nil
}

// bxhStatus is buildx's status word.
func bxhStatus(r *controlapi.BuildHistoryRecord) string {
	switch {
	case r.GetCompletedAt() == nil:
		return "Running"
	case r.GetError() != nil && r.GetError().GetCode() == 1:
		return "Canceled"
	case r.GetError() != nil:
		return "Error"
	}
	return "Completed"
}

// bxhDuration is the build duration (until now while running).
func bxhDuration(r *controlapi.BuildHistoryRecord, now time.Time) time.Duration {
	if r.GetCreatedAt() == nil {
		return 0
	}
	end := now
	if r.GetCompletedAt() != nil {
		end = r.GetCompletedAt().AsTime()
	}
	return end.Sub(r.GetCreatedAt().AsTime())
}

// bxhName mirrors buildx's BuildName.
func bxhName(fattrs map[string]string) string {
	if v := fattrs["build-arg:BUILDKIT_BUILD_NAME"]; v != "" {
		return v
	}
	var res string
	target := fattrs["target"]
	contextPath := fattrs["context"]
	if contextPath == "" {
		if v := fattrs["vcs:localdir:context"]; v != "." {
			contextPath = v
		}
	}
	dockerfile := ""
	if v := fattrs["filename"]; v != "" && v != "Dockerfile" {
		dockerfile = v
	}
	if v := fattrs["vcs:source"]; v != "" {
		if u, err := url.Parse(v); err == nil {
			res = strings.TrimSuffix(path.Base(u.Path), ".git")
		}
	}
	if res == "" && contextPath != "" {
		res = strings.TrimSuffix(path.Base(strings.ReplaceAll(contextPath, "\\", "/")), ".git")
	}
	if dockerfile != "" {
		if res != "" {
			res += "/"
		}
		res += path.Base(strings.ReplaceAll(dockerfile, "\\", "/"))
	}
	if target != "" {
		if res != "" {
			res += " "
		}
		res += target
	}
	return res
}

// bxhRFC3339 formats a record time ("" when unset).
func bxhTime(t interface{ AsTime() time.Time }, set bool) string {
	if !set {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339Nano)
}

// bxhPlatforms splits the platform frontend attribute.
func bxhPlatforms(fattrs map[string]string) []string {
	out := []string{}
	for _, p := range strings.Split(fattrs["platform"], ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// bxhAttachments lists attestation descriptors per platform.
func bxhAttachments(r *controlapi.BuildHistoryRecord) []map[string]any {
	out := []map[string]any{}
	add := func(platform string, res *controlapi.BuildResultInfo) {
		if res == nil {
			return
		}
		for _, d := range res.GetAttestations() {
			typ := d.GetAnnotations()["in-toto.io/predicate-type"]
			if typ == "" {
				typ = d.GetMediaType()
			}
			out = append(out, map[string]any{"digest": d.GetDigest(), "type": typ, "platform": platform})
		}
	}
	add("", r.GetResult())
	keys := make([]string, 0, len(r.GetResults()))
	for k := range r.GetResults() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k, r.GetResults()[k])
	}
	return out
}

// bxhSyntax is the frontend a record ran, when not the built-in one.
func bxhSyntax(r *controlapi.BuildHistoryRecord) string {
	fa := r.GetFrontendAttrs()
	if v := fa["build-arg:BUILDKIT_SYNTAX"]; v != "" {
		return v
	}
	if r.GetFrontend() == "gateway.v0" {
		return fa["source"]
	}
	return ""
}

// bxhInspect is the `history inspect --format json` object and the
// contract `record` object for one record.
func bxhInspect(r bxhRecord, now time.Time) (map[string]any, map[string]any) {
	rec := r.Rec
	fa := rec.GetFrontendAttrs()
	keepGit, _ := strconv.ParseBool(fa["build-arg:BUILDKIT_CONTEXT_KEEP_GIT_DIR"])
	dockerfile := fa["filename"]
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	ctxPath := fa["context"]
	if ctxPath == "" {
		ctxPath = fa["vcs:localdir:context"]
	}
	dur := bxhDuration(rec, now)
	status := bxhStatus(rec)
	started := bxhTime(rec.GetCreatedAt(), rec.GetCreatedAt() != nil)
	completed := bxhTime(rec.GetCompletedAt(), rec.GetCompletedAt() != nil)
	platforms := bxhPlatforms(fa)
	attachments := bxhAttachments(rec)
	buildArgs := []map[string]any{}
	labels := map[string]string{}
	keys := make([]string, 0, len(fa))
	for k := range fa {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if n, ok := strings.CutPrefix(k, "build-arg:"); ok {
			buildArgs = append(buildArgs, map[string]any{"Name": n, "Value": fa[k]})
		}
		if n, ok := strings.CutPrefix(k, "label:"); ok {
			labels[n] = fa[k]
		}
	}
	var errMsg string
	if rec.GetError() != nil {
		errMsg = rec.GetError().GetMessage()
	}
	raw := map[string]any{
		"Name": bxhName(fa), "Ref": rec.GetRef(), "Context": ctxPath, "Dockerfile": dockerfile,
		"VCSRepository": fa["vcs:source"], "VCSRevision": fa["vcs:revision"], "Target": fa["target"],
		"Platform": platforms, "KeepGitDir": keepGit, "StartedAt": started, "CompletedAt": completed,
		"Duration": int64(dur), "Status": status, "Error": errMsg,
		"NumCompletedSteps": rec.GetNumCompletedSteps(), "NumTotalSteps": rec.GetNumTotalSteps(), "NumCachedSteps": rec.GetNumCachedSteps(),
		"BuildArgs": buildArgs, "Labels": labels,
		"Config":      map[string]any{"ImageResolveMode": fa["image-resolve-mode"]},
		"Materials":   []any{},
		"Attachments": attachments,
		"Builder":     r.Node.Builder, "Node": r.Node.Name,
	}
	structured := map[string]any{
		"name": raw["Name"], "ref": rec.GetRef(), "context": ctxPath, "dockerfile": dockerfile,
		"vcs_repository": fa["vcs:source"], "vcs_revision": fa["vcs:revision"], "target": fa["target"],
		"platform": platforms, "keep_git_dir": keepGit, "started_at": started, "completed_at": completed,
		"duration": int64(dur), "status": status,
		"num_completed_steps": rec.GetNumCompletedSteps(), "num_total_steps": rec.GetNumTotalSteps(), "num_cached_steps": rec.GetNumCachedSteps(),
		"config":      map[string]any{"image_resolve_mode": fa["image-resolve-mode"]},
		"materials":   []any{},
		"attachments": attachments,
	}
	return raw, structured
}

// bxhPickRef resolves a ref argument: "" or "^N" (N-th newest, 0 =
// newest), a plain ref, or builder/node/ref as build_ref outputs give it.
func bxhPickRef(recs []bxhRecord, ref string) (bxhRecord, error) {
	if len(recs) == 0 {
		return bxhRecord{}, errors.New("the builder has no build records")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return recs[0], nil
	}
	if n, ok := strings.CutPrefix(ref, "^"); ok {
		i, err := strconv.Atoi(n)
		if err != nil || i < 0 {
			return bxhRecord{}, fmt.Errorf("invalid ref %q: ^N needs a non-negative number", ref)
		}
		if i >= len(recs) {
			return bxhRecord{}, fmt.Errorf("ref %q: the builder has only %d record(s)", ref, len(recs))
		}
		return recs[i], nil
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	var match []bxhRecord
	for _, r := range recs {
		if r.Rec.GetRef() == ref {
			return r, nil
		}
		if strings.HasPrefix(r.Rec.GetRef(), ref) {
			match = append(match, r)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return bxhRecord{}, fmt.Errorf("no build record %q on the builder", ref)
	}
	return bxhRecord{}, fmt.Errorf("ref %q is ambiguous (%d records start with it)", ref, len(match))
}

// ---------------------------------------------------------------------------
// docker_buildx_history

// NewBuildxHistoryDataSource is the data docker_buildx_history constructor.
func NewBuildxHistoryDataSource() datasource.DataSource { return &buildxHistoryDataSource{} }

type buildxHistoryDataSource struct{ pd *providerData }

type bxhInspectModel struct {
	contractBuildxHistoryInspect
	Builder     types.String `tfsdk:"builder"`
	Ref         types.String `tfsdk:"ref"`
	Name        types.String `tfsdk:"name"`
	Platforms   types.List   `tfsdk:"platforms"`
	VCSRevision types.String `tfsdk:"vcs_revision"`
}

func (d *buildxHistoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_history"
}

func (d *buildxHistoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func bxhBuilderAttr() schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true, Computed: true,
		MarkdownDescription: "Builder whose records are read (`--builder`). Default `default`, the daemon's built-in BuildKit; other names are " +
			"docker-container builders on the provider's daemon (see `docker_buildx_builder`).",
	}
}

func (d *buildxHistoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"builder": bxhBuilderAttr(),
		"ref": schema.StringAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Build record to read: its ref (a unique prefix is enough), a `builder/node/ref` value as `docker_image` and " +
				"`docker_buildx_bake` report it, or `^N` for the N-th newest record (`^0` = newest). Default: the newest record.",
		},
		"name": schema.StringAttribute{Computed: true, MarkdownDescription: "Build name, as `docker buildx history ls` shows it."},
		"platforms": schema.ListAttribute{
			ElementType: types.StringType, Computed: true, MarkdownDescription: "Platforms the build produced (empty for the builder's default).",
		},
		"vcs_revision": schema.StringAttribute{Computed: true, MarkdownDescription: "Source revision the build was made from, when BuildKit recorded one."},
	}
	contractAddDataSourceAttributes(attrs, bxhInspectItemID)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads one build record of a buildx builder, like `docker buildx history inspect`: context, Dockerfile, target, " +
			"platforms, status, timing, step counts and attestation attachments. Records come from BuildKit's build history API on the " +
			"builder's running nodes. Materials and attachment bodies are not read (only attachment digests and types).",
		Attributes: attrs,
	}
}

func (d *buildxHistoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m bxhInspectModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.pd == nil || d.pd.Client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured. This is a provider bug.")
		return
	}
	builder := bkDefaultBuilder
	if v, ok := knownString(m.Builder); ok && v != "" {
		builder = v
	}
	recs, err := bxhRecords(ctx, d.pd, builder, "")
	if err != nil {
		resp.Diagnostics.AddAttributeError(tfpath.Root("builder"), "Cannot read build history", err.Error())
		return
	}
	r, err := bxhPickRef(recs, m.Ref.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(tfpath.Root("ref"), "Cannot find build record", err.Error())
		return
	}
	raw, structured := bxhInspect(r, time.Now())
	b, _ := json.Marshal(raw)
	m.Builder = types.StringValue(builder)
	m.Ref = types.StringValue(r.Rec.GetRef())
	m.Name = types.StringValue(raw["Name"].(string))
	m.Platforms = stringListValue(raw["Platform"].([]string), false)
	m.VCSRevision = stringOrNull(r.Rec.GetFrontendAttrs()["vcs:revision"])
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw:              map[string][]byte{"inspect_json": b},
		Structured:       map[string]any{"record": structured},
		DockerfileSyntax: bxhSyntax(r.Rec),
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_buildx_history_records

// NewBuildxHistoryRecordsDataSource is the data docker_buildx_history_records constructor.
func NewBuildxHistoryRecordsDataSource() datasource.DataSource {
	return &buildxHistoryRecordsDataSource{}
}

type buildxHistoryRecordsDataSource struct{ pd *providerData }

type bxhListModel struct {
	contractBuildxHistoryLs
	Builder types.String `tfsdk:"builder"`
	Limit   types.Int64  `tfsdk:"limit"`
	Refs    types.List   `tfsdk:"refs"`
}

func (d *buildxHistoryRecordsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_history_records"
}

func (d *buildxHistoryRecordsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *buildxHistoryRecordsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"builder": bxhBuilderAttr(),
		"limit": schema.Int64Attribute{
			Optional:            true,
			MarkdownDescription: "Maximum number of records, newest first. Default: all.",
			Validators:          []validator.Int64{int64validator.AtLeast(1)},
		},
		"refs": schema.ListAttribute{
			ElementType: types.StringType, Computed: true,
			MarkdownDescription: "Build record refs, newest first; each is a valid `ref` for `docker_buildx_history`.",
		},
	}
	contractAddDataSourceAttributes(attrs, bxhListItemID)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the build records of a buildx builder, like `docker buildx history ls`: ref, name, status, start time and " +
			"duration of each build, newest first. Records come from BuildKit's build history API on the builder's running nodes.",
		Attributes: attrs,
	}
}

func (d *buildxHistoryRecordsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m bxhListModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.pd == nil || d.pd.Client == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured. This is a provider bug.")
		return
	}
	builder := bkDefaultBuilder
	if v, ok := knownString(m.Builder); ok && v != "" {
		builder = v
	}
	recs, err := bxhRecords(ctx, d.pd, builder, "")
	if err != nil {
		resp.Diagnostics.AddAttributeError(tfpath.Root("builder"), "Cannot read build history", err.Error())
		return
	}
	if n, ok := knownInt64(m.Limit); ok && int(n) < len(recs) {
		recs = recs[:n]
	}
	now := time.Now()
	raw := []map[string]any{}
	structured := []any{}
	refs := []string{}
	for _, r := range recs {
		rec := r.Rec
		name := bxhName(rec.GetFrontendAttrs())
		status := bxhStatus(rec)
		created := bxhTime(rec.GetCreatedAt(), rec.GetCreatedAt() != nil)
		dur := bxhDuration(rec, now).Round(100 * time.Millisecond).String()
		refs = append(refs, rec.GetRef())
		raw = append(raw, map[string]any{
			"ref": rec.GetRef(), "name": name, "status": status, "created_at": created,
			"completed_at": bxhTime(rec.GetCompletedAt(), rec.GetCompletedAt() != nil), "duration": dur,
			"total_steps": rec.GetNumTotalSteps(), "completed_steps": rec.GetNumCompletedSteps(), "cached_steps": rec.GetNumCachedSteps(),
			"builder": r.Node.Builder, "node": r.Node.Name,
		})
		structured = append(structured, map[string]any{"id": rec.GetRef(), "name": name, "status": status, "created_at": created, "duration": dur})
	}
	b, _ := json.Marshal(raw)
	m.Builder = types.StringValue(builder)
	m.Refs = stringListValue(refs, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw:        map[string][]byte{"history_json": b},
		Structured: map[string]any{"records": structured},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
