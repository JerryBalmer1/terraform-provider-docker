package provider

// data.docker_stacks (cli-only:stacks, Q21/Q51) and data.docker_stack
// (cli-only:stack-services, Q21/Q50): `docker stack ls` and `docker stack
// services` through the Engine API. Tasks are not listed (Q50): use
// data.docker_tasks filtered by the com.docker.stack.namespace label.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// NewStacksDataSource returns data.docker_stacks.
func NewStacksDataSource() datasource.DataSource { return &stacksDataSource{} }

type stacksDataSource struct{ composeDSBase }

type stacksModel struct {
	contractCliOnlyStacks
	Names types.List `tfsdk:"names"`
}

func (d *stacksDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stacks"
}

func (d *stacksDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Lists the Swarm stacks, like `docker stack ls`: services grouped by the `com.docker.stack.namespace` label, " +
			"with the number of services in each (`services_count`). Needs a Swarm manager.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"names": dschema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "The stack names, sorted."},
		}, "cli-only:stacks"),
	}
}

func (d *stacksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m stacksModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	rows, err := stackServiceRows(ctx, d.pd.Client, "")
	if err != nil {
		resp.Diagnostics.AddError("Cannot list stacks", err.Error())
		return
	}
	count := map[string]int{}
	for _, r := range rows {
		count[r.NamespaceLabels]++
	}
	names := composeSortedKeys(count)
	list := make([]map[string]any, 0, len(names))
	for _, n := range names {
		list = append(list, map[string]any{"Name": n, "Namespace": n, "Orchestrator": "Swarm", "Services": count[n]})
	}
	m.Names = stringListValue(names, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw: map[string][]byte{"stacks_json": composeJSON(list)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// NewStackDataSource returns data.docker_stack.
func NewStackDataSource() datasource.DataSource { return &stackDataSource{} }

type stackDataSource struct{ composeDSBase }

type stackDSModel struct {
	contractCliOnlyStackServices
	Name       types.String `tfsdk:"name"`
	ServiceIDs types.Map    `tfsdk:"service_ids"`
	Images     types.Map    `tfsdk:"images"`
}

func (d *stackDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stack"
}

func (d *stackDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Reads the services of one Swarm stack, like `docker stack services`: mode, `running/desired` replicas, " +
			"image and published ports per service. Tasks are not listed; use `data.docker_tasks` with the `com.docker.stack.namespace` label.",
		Attributes: contractAddDataSourceAttributes(map[string]dschema.Attribute{
			"name": dschema.StringAttribute{Required: true, MarkdownDescription: "The stack namespace."},
			"service_ids": dschema.MapAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Map of short service name (without `<stack>_`) to service ID."},
			"images": dschema.MapAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Map of short service name to its image (digest pin removed, as the CLI shows it)."},
		}, "cli-only:stack-services"),
	}
}

func (d *stackDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m stackDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || !d.ready(resp) {
		return
	}
	rows, err := stackServiceRows(ctx, d.pd.Client, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot list stack services", err.Error())
		return
	}
	ids, images := map[string]string{}, map[string]string{}
	list := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		ids[r.Short] = r.ID
		images[r.Short] = r.Image
		list = append(list, stackServiceStatus(r))
	}
	m.ServiceIDs = stringMapValue(ids, false)
	m.Images = stringMapValue(images, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw: map[string][]byte{"services_json": composeJSON(list)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
