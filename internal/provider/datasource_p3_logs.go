package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/client"
)

var (
	_ datasource.DataSource              = (*p3LogsDS)(nil)
	_ datasource.DataSourceWithConfigure = (*p3LogsDS)(nil)
)

// NewContainerLogsDataSource returns the docker_container_logs data source
// (DECISIONS Q6; logs contract: logs, log_entries, tail, since).
func NewContainerLogsDataSource() datasource.DataSource { return &p3LogsDS{} }

type p3LogsDS struct {
	pd *providerData
}

type p3LogsModel struct {
	Container   types.String `tfsdk:"container"`
	Tail        types.Int64  `tfsdk:"tail"`
	Since       types.String `tfsdk:"since"`
	Until       types.String `tfsdk:"until"`
	Details     types.Bool   `tfsdk:"details"`
	Stdout      types.Bool   `tfsdk:"stdout"`
	Stderr      types.Bool   `tfsdk:"stderr"`
	ID          types.String `tfsdk:"id"`
	ContainerID types.String `tfsdk:"container_id"`
	contractEngineContainerLogs
}

func (d *p3LogsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_logs"
}

func (d *p3LogsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	until := logSinceAttribute()
	until.MarkdownDescription = "Only lines before this time (Engine `until`): an RFC 3339 timestamp, a UNIX timestamp or a duration before now such as `10m`."
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a container's logs (`GET /containers/{id}/logs`), like `docker container logs --timestamps`. " +
			"`log_entries` holds one object per line (`timestamp`, `stream`, `source`, `message`) and `logs` the same lines as text. " +
			"Output of containers without a TTY is demultiplexed into `stdout` / `stderr` entries; a TTY container's lines have stream `combined`. " +
			"The read does not follow the log and keeps the last `tail` lines (default 1000).",
		Attributes: contractAddDataSourceAttributes(map[string]schema.Attribute{
			"container":    schema.StringAttribute{Required: true, MarkdownDescription: "The container whose logs are read, by ID or name."},
			"tail":         logTailAttribute(),
			"since":        logSinceAttribute(),
			"until":        until,
			"details":      schema.BoolAttribute{Optional: true, MarkdownDescription: "Add the extra attributes the log driver records (for example labels) to each message (Engine `details`). Default `false`."},
			"stdout":       schema.BoolAttribute{Optional: true, MarkdownDescription: "Include standard output (Engine `stdout`). Default `true`."},
			"stderr":       schema.BoolAttribute{Optional: true, MarkdownDescription: "Include standard error (Engine `stderr`). Default `true`."},
			"id":           schema.StringAttribute{Computed: true, MarkdownDescription: "Container ID (the same value as `container_id`)."},
			"container_id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID of the container the logs were read from."},
		}, "engine:container-logs"),
	}
}

func (d *p3LogsDS) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func p3BoolOr(v types.Bool, def bool) bool {
	if v.IsNull() || v.IsUnknown() {
		return def
	}
	return v.ValueBool()
}

func (d *p3LogsDS) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "The docker provider was not configured.")
		return
	}
	var m p3LogsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	now := time.Now()
	since, err := logSinceQuery(m.Since.ValueString(), now)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("since"), "Invalid since", err.Error())
		return
	}
	until, err := logSinceQuery(m.Until.ValueString(), now)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("until"), "Invalid until", err.Error())
		return
	}
	tail := logTailValue(m.Tail)
	opts := client.ContainerLogsOptions{
		ShowStdout: p3BoolOr(m.Stdout, true),
		ShowStderr: p3BoolOr(m.Stderr, true),
		Since:      since,
		Until:      until,
		Timestamps: true,
		Details:    p3BoolOr(m.Details, false),
		Tail:       logTailQuery(tail),
	}
	if !opts.ShowStdout && !opts.ShowStderr {
		resp.Diagnostics.AddAttributeError(path.Root("stdout"), "No log stream selected", "At least one of stdout and stderr must be true.")
		return
	}
	inspRes, err := d.pd.Client.ContainerInspect(ctx, m.Container.ValueString(), client.ContainerInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect container", fmt.Sprintf("%s: %v", m.Container.ValueString(), err))
		return
	}
	insp := inspRes.Container
	rc, err := d.pd.Client.ContainerLogs(ctx, insp.ID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read container logs", fmt.Sprintf("%s: %v", m.Container.ValueString(), err))
		return
	}
	defer rc.Close()
	tty := insp.Config != nil && insp.Config.Tty
	entries, err := logReadEntries(rc, tty, copName(insp.Name))
	if err != nil {
		resp.Diagnostics.AddError("Cannot read container logs", err.Error())
		return
	}
	entries = logApplyTail(entries, tail)
	m.ID = types.StringValue(insp.ID)
	m.ContainerID = types.StringValue(insp.ID)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, logFillInput(entries))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
