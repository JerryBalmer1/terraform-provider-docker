package provider

// Per-container compose data sources, answered from the Engine by labels:
//   - data.docker_compose_logs (compose:cli-logs; logs contract, source "<service>.<container>")
//   - data.docker_compose_port (compose:cli-port)
//   - data.docker_compose_top  (compose:cli-top)

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// ---------------------------------------------------------------------------
// docker_compose_logs

// NewComposeLogsDataSource returns data.docker_compose_logs.
func NewComposeLogsDataSource() datasource.DataSource { return &composeLogsDataSource{} }

type composeLogsDataSource struct{ composeDSBase }

type composeLogsModel struct {
	contractComposeCliLogs
	Name         types.String `tfsdk:"name"`
	Services     types.List   `tfsdk:"services"`
	Tail         types.Int64  `tfsdk:"tail"`
	Since        types.String `tfsdk:"since"`
	ContainerIDs types.Map    `tfsdk:"container_ids"`
}

func (d *composeLogsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_logs"
}

func (d *composeLogsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Reads the logs of a compose project's containers, like `docker compose logs --timestamps`. Each container's " +
			"last `tail` lines are read, merged in time order into `log_entries` (source `<service>.<container>`) and rendered as `logs`.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"name":          composeDSProjectAttr(),
			"services":      composeDSServicesAttr(),
			"tail":          logTailAttribute(),
			"since":         logSinceAttribute(),
			"container_ids": composeContainerIDsAttr(),
		}, "compose:cli-logs"),
	}
}

func (d *composeLogsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composeLogsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	since, err := logSinceQuery(m.Since.ValueString(), time.Now())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("since"), "Invalid since", err.Error())
		return
	}
	cs, err := composeContainers(ctx, d.pd, composeSelector{Project: m.Name.ValueString(), Services: stringListFrom(ctx, m.Services, &resp.Diagnostics), All: true})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list compose containers", err.Error())
		return
	}
	tail := logTailValue(m.Tail)
	var lists [][]logEntry
	for _, c := range cs {
		insp, err := d.pd.Client.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			resp.Diagnostics.AddError("Cannot inspect container", fmt.Sprintf("%s: %v", composeSummaryName(c), err))
			return
		}
		tty := insp.Container.Config != nil && insp.Container.Config.Tty
		rc, err := d.pd.Client.ContainerLogs(ctx, c.ID, client.ContainerLogsOptions{
			ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: logTailQuery(tail), Since: since,
		})
		if err != nil {
			resp.Diagnostics.AddError("Cannot read container logs", fmt.Sprintf("%s: %v", composeSummaryName(c), err))
			return
		}
		entries, err := logReadEntries(rc, tty, c.Labels[composeLabelService]+"."+composeSummaryName(c))
		_ = rc.Close()
		if err != nil {
			resp.Diagnostics.AddError("Cannot read container logs", fmt.Sprintf("%s: %v", composeSummaryName(c), err))
			return
		}
		lists = append(lists, entries)
	}
	m.ContainerIDs = composeStringListMap(composeContainerIDs(cs))
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, logFillInput(logMerge(lists...)))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_compose_port

// NewComposePortDataSource returns data.docker_compose_port.
func NewComposePortDataSource() datasource.DataSource { return &composePortDataSource{} }

type composePortDataSource struct{ composeDSBase }

type composePortModel struct {
	contractComposeCliPort
	Name          types.String `tfsdk:"name"`
	Service       types.String `tfsdk:"service"`
	PrivatePort   types.Int64  `tfsdk:"private_port"`
	Protocol      types.String `tfsdk:"protocol"`
	Index         types.Int64  `tfsdk:"index"`
	HostIP        types.String `tfsdk:"host_ip"`
	PublishedPort types.Number `tfsdk:"published_port"`
	ContainerID   types.String `tfsdk:"container_id"`
}

func (d *composePortDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_port"
}

func (d *composePortDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Finds the public port of a service's port binding, like `docker compose port <service> <port>`, from the " +
			"container's `NetworkSettings.Ports`.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"name":    composeDSProjectAttr(),
			"service": dschema.StringAttribute{Required: true, MarkdownDescription: "The service name."},
			"private_port": dschema.Int64Attribute{Required: true, MarkdownDescription: "The container port.",
				Validators: []validator.Int64{int64validator.Between(1, 65535)}},
			"protocol": dschema.StringAttribute{Optional: true, MarkdownDescription: "The port protocol: `tcp` (default), `udp` or `sctp`.",
				Validators: []validator.String{stringvalidator.OneOf("tcp", "udp", "sctp")}},
			"index": dschema.Int64Attribute{Optional: true, MarkdownDescription: "The container number when the service has several (CLI `--index`). Default `1`.",
				Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"host_ip": dschema.StringAttribute{Computed: true, MarkdownDescription: "The host IP of the first binding (empty: all interfaces)."},
			"published_port": dschema.NumberAttribute{Computed: true,
				MarkdownDescription: "The host port of the first binding; null when the port is not published."},
			"container_id": dschema.StringAttribute{Computed: true, MarkdownDescription: "The container that was inspected."},
		}, "compose:cli-port"),
	}
}

func (d *composePortDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composePortModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	proto := m.Protocol.ValueString()
	if proto == "" {
		proto = "tcp"
	}
	index := int64(1)
	if !m.Index.IsNull() {
		index = m.Index.ValueInt64()
	}
	cs, err := composeContainers(ctx, d.pd, composeSelector{Project: m.Name.ValueString(), Services: []string{m.Service.ValueString()}, All: true})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list compose containers", err.Error())
		return
	}
	var id string
	for _, c := range cs {
		if c.Labels[composeLabelNumber] == strconv.FormatInt(index, 10) {
			id = c.ID
		}
	}
	if id == "" {
		resp.Diagnostics.AddError("No such service container", fmt.Sprintf("project %s has no container %d of service %s", m.Name.ValueString(), index, m.Service.ValueString()))
		return
	}
	insp, err := d.pd.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect container", err.Error())
		return
	}
	port, err := network.ParsePort(fmt.Sprintf("%d/%s", m.PrivatePort.ValueInt64(), proto))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("private_port"), "Invalid port", err.Error())
		return
	}
	var bindings []network.PortBinding
	if ns := insp.Container.NetworkSettings; ns != nil {
		bindings = ns.Ports[port]
	}
	list := []any{}
	for _, b := range bindings {
		ip := ""
		if b.HostIP.IsValid() {
			ip = b.HostIP.String()
		}
		list = append(list, map[string]any{"HostIp": ip, "HostPort": b.HostPort})
	}
	m.ContainerID = types.StringValue(id)
	m.HostIP = types.StringNull()
	m.PublishedPort = types.NumberNull()
	if len(list) > 0 {
		first := list[0].(map[string]any)
		m.HostIP = types.StringValue(first["HostIp"].(string))
		if n, err := strconv.ParseInt(first["HostPort"].(string), 10, 64); err == nil {
			m.PublishedPort = types.NumberValue(newBigFloatInt(n))
		}
	}
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{Structured: map[string]any{"bindings": list}})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ---------------------------------------------------------------------------
// docker_compose_top

// NewComposeTopDataSource returns data.docker_compose_top.
func NewComposeTopDataSource() datasource.DataSource { return &composeTopDataSource{} }

type composeTopDataSource struct{ composeDSBase }

type composeTopModel struct {
	contractComposeCliTop
	Name         types.String `tfsdk:"name"`
	Services     types.List   `tfsdk:"services"`
	PsArgs       types.String `tfsdk:"ps_args"`
	ContainerIDs types.Map    `tfsdk:"container_ids"`
}

func (d *composeTopDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_top"
}

func (d *composeTopDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Lists the processes running in a compose project's containers, like `docker compose top`. " +
			"`processes` is keyed by container name.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"name":     composeDSProjectAttr(),
			"services": composeDSServicesAttr(),
			"ps_args": dschema.StringAttribute{Optional: true,
				MarkdownDescription: "Arguments for `ps` inside each container (the Engine's `ps_args`), e.g. `-ef`. Default: the Engine's `-ef`."},
			"container_ids": composeContainerIDsAttr(),
		}, "compose:cli-top"),
	}
}

func (d *composeTopDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composeTopModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	cs, err := composeContainers(ctx, d.pd, composeSelector{Project: m.Name.ValueString(), Services: stringListFrom(ctx, m.Services, &resp.Diagnostics)})
	if err != nil {
		resp.Diagnostics.AddError("Cannot list compose containers", err.Error())
		return
	}
	var args []string
	if a := strings.TrimSpace(m.PsArgs.ValueString()); a != "" {
		args = strings.Fields(a)
	}
	procs := map[string]any{}
	for _, c := range cs {
		top, err := d.pd.Client.ContainerTop(ctx, c.ID, client.ContainerTopOptions{Arguments: args})
		if err != nil {
			resp.Diagnostics.AddError("Cannot list processes", fmt.Sprintf("%s: %v", composeSummaryName(c), err))
			return
		}
		ps := top.Processes
		if ps == nil {
			ps = [][]string{}
		}
		procs[composeSummaryName(c)] = map[string]any{"service": c.Labels[composeLabelService], "Titles": top.Titles, "Processes": ps}
	}
	m.ContainerIDs = composeStringListMap(composeContainerIDs(cs))
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Structured: map[string]any{"processes": procs},
		Raw:        map[string][]byte{"top_json": composeJSON(procs)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
