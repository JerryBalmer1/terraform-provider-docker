package provider

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*p3LogsDS)(nil)
	_ datasource.DataSourceWithConfigure = (*p3LogsDS)(nil)
)

// p3LogsMaxBytes caps how much log output one read keeps.
const p3LogsMaxBytes = 16 << 20

// NewP3LogsDataSource returns the docker_logs data source.
func NewP3LogsDataSource() datasource.DataSource { return &p3LogsDS{} }

type p3LogsDS struct {
	pd *providerData
}

type p3LogsModel struct {
	Container      types.String `tfsdk:"container"`
	Tail           types.String `tfsdk:"tail"`
	Since          types.String `tfsdk:"since"`
	Until          types.String `tfsdk:"until"`
	Timestamps     types.Bool   `tfsdk:"timestamps"`
	Details        types.Bool   `tfsdk:"details"`
	ShowStdout     types.Bool   `tfsdk:"show_stdout"`
	ShowStderr     types.Bool   `tfsdk:"show_stderr"`
	ID             types.String `tfsdk:"id"`
	LogsListString types.List   `tfsdk:"logs_list_string"`
}

func (d *p3LogsDS) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_logs"
}

func (d *p3LogsDS) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a container's logs (`GET /containers/{id}/logs`) as a list of lines. " +
			"Output of containers without a TTY is demultiplexed, so lines carry no stream headers; stdout and stderr are interleaved in the order the daemon returns them. " +
			"The read does not follow the log, and keeps at most 16 MiB.",
		Attributes: map[string]schema.Attribute{
			"container": schema.StringAttribute{Required: true, MarkdownDescription: "Container name or ID."},
			"tail": schema.StringAttribute{
				Optional:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^(all|[0-9]+)$`), "must be `all` or a number of lines")},
				MarkdownDescription: "Number of lines to read from the end of the log, or `all`. Default `all`.",
			},
			"since": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Only lines after this time: RFC 3339 timestamp, Unix timestamp, or a duration relative to now such as `10m`.",
			},
			"until": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Only lines before this time, same formats as `since`.",
			},
			"timestamps":  schema.BoolAttribute{Optional: true, MarkdownDescription: "Prefix every line with its RFC 3339 timestamp. Default `false`."},
			"details":     schema.BoolAttribute{Optional: true, MarkdownDescription: "Add the extra attributes the log driver records (for example labels) to each line. Default `false`."},
			"show_stdout": schema.BoolAttribute{Optional: true, MarkdownDescription: "Include standard output. Default `true`."},
			"show_stderr": schema.BoolAttribute{Optional: true, MarkdownDescription: "Include standard error. Default `true`."},
			"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Container ID."},
			"logs_list_string": schema.ListAttribute{
				ElementType:         types.StringType,
				Computed:            true,
				MarkdownDescription: "Log lines without their trailing newline.",
			},
		},
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
	opts := container.LogsOptions{
		ShowStdout: p3BoolOr(m.ShowStdout, true),
		ShowStderr: p3BoolOr(m.ShowStderr, true),
		Since:      m.Since.ValueString(),
		Until:      m.Until.ValueString(),
		Timestamps: p3BoolOr(m.Timestamps, false),
		Details:    p3BoolOr(m.Details, false),
		Tail:       m.Tail.ValueString(),
	}
	if !opts.ShowStdout && !opts.ShowStderr {
		resp.Diagnostics.AddAttributeError(path.Root("show_stdout"), "No log stream selected", "At least one of show_stdout and show_stderr must be true.")
		return
	}
	insp, err := d.pd.Client.ContainerInspect(ctx, m.Container.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect container", fmt.Sprintf("%s: %v", m.Container.ValueString(), err))
		return
	}
	rc, err := d.pd.Client.ContainerLogs(ctx, insp.ID, opts)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read container logs", fmt.Sprintf("%s: %v", m.Container.ValueString(), err))
		return
	}
	defer rc.Close()
	tty := insp.Config != nil && insp.Config.Tty
	text, err := p3ReadLogs(rc, tty)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read container logs", err.Error())
		return
	}
	lines := p3SplitLines(text)
	vals := make([]attr.Value, 0, len(lines))
	for _, l := range lines {
		vals = append(vals, types.StringValue(l))
	}
	m.ID = types.StringValue(insp.ID)
	m.LogsListString = types.ListValueMust(types.StringType, vals)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// p3ReadLogs reads a log stream: raw for TTY containers, otherwise the
// multiplexed stdout/stderr frames demuxed into one buffer in order.
func p3ReadLogs(r io.Reader, tty bool) (string, error) {
	buf := &cappedBuffer{max: p3LogsMaxBytes}
	var err error
	if tty {
		_, err = io.Copy(buf, r)
	} else {
		_, err = stdcopy.StdCopy(buf, buf, r)
	}
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// p3SplitLines splits on newlines, dropping a trailing empty line and CRs.
func p3SplitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}
