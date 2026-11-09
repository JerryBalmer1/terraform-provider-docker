package provider

// Pass 5 Lane E: data docker_buildx_disk_usage (`docker buildx du`), Q26:
// a builder's build cache records and totals, separate from
// docker_system_df.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	bkclient "github.com/moby/buildkit/client"
)

const bxduItemID = "buildx:du"

var _ datasource.DataSourceWithConfigure = (*buildxDiskUsageDataSource)(nil)

// bkDiskUsage reads one node's cache records; a var so unit tests can fake
// BuildKit.
var bkDiskUsage = func(ctx context.Context, pd *providerData, node bkNode, filters []string) ([]*bkclient.UsageInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*bkDialTimeout)
	defer cancel()
	c, err := bkOpen(ctx, pd, node)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var opts []bkclient.DiskUsageOption
	if len(filters) > 0 {
		opts = append(opts, bkclient.WithFilter(filters))
	}
	return c.DiskUsage(ctx, opts...)
}

// NewBuildxDiskUsageDataSource is the data docker_buildx_disk_usage constructor.
func NewBuildxDiskUsageDataSource() datasource.DataSource { return &buildxDiskUsageDataSource{} }

type buildxDiskUsageDataSource struct{ pd *providerData }

type bxduModel struct {
	contractBuildxDu
	Builder types.String `tfsdk:"builder"`
	Filters types.Map    `tfsdk:"filters"`
	IDs     types.List   `tfsdk:"ids"`
}

func (d *buildxDiskUsageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_buildx_disk_usage"
}

func (d *buildxDiskUsageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *buildxDiskUsageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"builder": schema.StringAttribute{
			Optional: true, Computed: true,
			MarkdownDescription: "Builder whose cache is read (`--builder`). Default `default`, the daemon's built-in BuildKit (reached over " +
				"the daemon's `/grpc` endpoint); other names are docker-container builders on the provider's daemon.",
		},
		"filters": schema.MapAttribute{
			ElementType: types.StringType, Optional: true,
			MarkdownDescription: "Map of BuildKit cache filter keys to values (`--filter`), e.g. `{ type = \"regular\" }` or " +
				"`{ description = \"~pulled\" }` (a leading `~` matches by substring). Keys: `id`, `parents`, `description`, `type`, " +
				"`inuse`, `mutable`, `shared`, `private`.",
		},
		"ids": schema.ListAttribute{
			ElementType: types.StringType, Computed: true,
			MarkdownDescription: "Cache record ids, largest first, e.g. for a targeted `docker_builder_prune` filter.",
		},
	}
	contractAddDataSourceAttributes(attrs, bxduItemID)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the build cache of a buildx builder, like `docker buildx du --verbose`: one entry per cache record with " +
			"size, type, sharing and usage, plus the shared, private, reclaimable and total sizes printed under the table. " +
			"For the daemon-wide view of images, containers and volumes use `docker_system_df`.",
		Attributes: attrs,
	}
}

// bxduFilters turns the filters map into BuildKit filter expressions.
func bxduFilters(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		if s, ok := strings.CutPrefix(v, "~"); ok {
			out = append(out, k+"~="+s)
		} else {
			out = append(out, k+"=="+v)
		}
	}
	return out
}

// bxduSummary computes buildx du's totals.
func bxduSummary(recs []*bkclient.UsageInfo) map[string]any {
	var shared, private, reclaimable, total int64
	for _, r := range recs {
		if r.Shared {
			shared += r.Size
		} else {
			private += r.Size
		}
		if !r.InUse {
			reclaimable += r.Size
		}
		total += r.Size
	}
	return map[string]any{"shared": shared, "private": private, "reclaimable": reclaimable, "total": total}
}

func (d *buildxDiskUsageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m bxduModel
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
	filters := bxduFilters(stringMapFrom(ctx, m.Filters, &resp.Diagnostics))
	nodes, err := bkBuilderNodes(ctx, d.pd, builder)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("builder"), "Cannot read build cache", err.Error())
		return
	}
	var recs []*bkclient.UsageInfo
	queried := 0
	for _, n := range nodes {
		if n.Status != bkStatusRunning {
			continue
		}
		queried++
		r, err := bkDiskUsage(ctx, d.pd, n, filters)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("builder"), "Cannot read build cache", fmt.Sprintf("builder %q node %s: %v", builder, n.Name, err))
			return
		}
		recs = append(recs, r...)
	}
	if queried == 0 {
		resp.Diagnostics.AddAttributeError(path.Root("builder"), "Cannot read build cache", fmt.Sprintf("builder %q has no running node", builder))
		return
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].Size != recs[j].Size {
			return recs[i].Size > recs[j].Size
		}
		return recs[i].ID < recs[j].ID
	})
	records := make([]any, 0, len(recs))
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		last := ""
		if r.LastUsedAt != nil {
			last = r.LastUsedAt.UTC().Format(time.RFC3339Nano)
		}
		ids = append(ids, r.ID)
		records = append(records, map[string]any{
			"id": r.ID, "parents": bxStrings(r.Parents), "created_at": r.CreatedAt.UTC().Format(time.RFC3339Nano),
			"mutable": r.Mutable, "reclaimable": !r.InUse, "shared": r.Shared, "size": r.Size, "description": r.Description,
			"usage_count": r.UsageCount, "last_used_at": last, "type": string(r.RecordType),
		})
	}
	summary := bxduSummary(recs)
	raw, _ := json.Marshal(map[string]any{"builder": builder, "records": recs, "summary": summary})
	m.Builder = types.StringValue(builder)
	m.IDs = stringListValue(ids, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw:        map[string][]byte{"disk_usage_json": raw},
		Structured: map[string]any{"records": records, "summary": summary},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
