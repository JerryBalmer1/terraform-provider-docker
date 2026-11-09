package provider

// Pass 5 Lane 0: the generic types behind the stub constructors in
// stubs_pass5_<lane>.go (written by tools/registrygen -stubs). A stub
// registers its final type name with a minimal schema so the registration
// matches the inventory, and every use of it fails with a "not implemented
// (Pass 5)" diagnostic. Lanes replace stubs with real constructors; they do
// not edit this file. Invoke-Build StubReport lists the stubs left.

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/action"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	ephschema "github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// pass5Stub carries what every stub kind needs.
type pass5Stub struct {
	suffix  string // type name without "docker_"
	item    string // inventory item id
	summary string // inventory summary
}

func (s pass5Stub) typeName(provider string) string { return provider + "_" + s.suffix }

func (s pass5Stub) description() string {
	return fmt.Sprintf("Not implemented yet: a Pass 5 placeholder for inventory item `%s`. %s", s.item, s.summary)
}

func (s pass5Stub) fail(diags *diag.Diagnostics) {
	diags.AddError(fmt.Sprintf("docker_%s is not implemented (Pass 5)", s.suffix),
		fmt.Sprintf("This build registers docker_%s as a Pass 5 placeholder (inventory item %s). It has no implementation yet; "+
			"use a provider build where the type is implemented.", s.suffix, s.item))
}

// ---- resource

type pass5StubResource struct{ pass5Stub }

var (
	_ resource.Resource                   = (*pass5StubResource)(nil)
	_ resource.ResourceWithValidateConfig = (*pass5StubResource)(nil)
)

func newPass5StubResource(suffix, item, summary string) resource.Resource {
	return &pass5StubResource{pass5Stub{suffix, item, summary}}
}

func (r *pass5StubResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = r.typeName(req.ProviderTypeName)
}

func (r *pass5StubResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		MarkdownDescription: r.description(),
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{Computed: true, MarkdownDescription: "Placeholder identifier; always unknown until the type is implemented."},
		},
	}
}

func (r *pass5StubResource) ValidateConfig(_ context.Context, _ resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	r.fail(&resp.Diagnostics)
}

func (r *pass5StubResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	r.fail(&resp.Diagnostics)
}

func (r *pass5StubResource) Read(_ context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	r.fail(&resp.Diagnostics)
}

func (r *pass5StubResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.fail(&resp.Diagnostics)
}

func (r *pass5StubResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	r.fail(&resp.Diagnostics)
}

// ---- data source

type pass5StubDataSource struct{ pass5Stub }

var (
	_ datasource.DataSource                   = (*pass5StubDataSource)(nil)
	_ datasource.DataSourceWithValidateConfig = (*pass5StubDataSource)(nil)
)

func newPass5StubDataSource(suffix, item, summary string) datasource.DataSource {
	return &pass5StubDataSource{pass5Stub{suffix, item, summary}}
}

func (d *pass5StubDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = d.typeName(req.ProviderTypeName)
}

func (d *pass5StubDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dsschema.Schema{
		MarkdownDescription: d.description(),
		Attributes: map[string]dsschema.Attribute{
			"id": dsschema.StringAttribute{Computed: true, MarkdownDescription: "Placeholder identifier; never set until the type is implemented."},
		},
	}
}

func (d *pass5StubDataSource) ValidateConfig(_ context.Context, _ datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	d.fail(&resp.Diagnostics)
}

func (d *pass5StubDataSource) Read(_ context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	d.fail(&resp.Diagnostics)
}

// ---- action

type pass5StubAction struct{ pass5Stub }

var (
	_ action.Action                   = (*pass5StubAction)(nil)
	_ action.ActionWithValidateConfig = (*pass5StubAction)(nil)
)

func newPass5StubAction(suffix, item, summary string) action.Action {
	return &pass5StubAction{pass5Stub{suffix, item, summary}}
}

func (a *pass5StubAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = a.typeName(req.ProviderTypeName)
}

func (a *pass5StubAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = actionschema.Schema{MarkdownDescription: a.description()}
}

func (a *pass5StubAction) ValidateConfig(_ context.Context, _ action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	a.fail(&resp.Diagnostics)
}

func (a *pass5StubAction) Invoke(_ context.Context, _ action.InvokeRequest, resp *action.InvokeResponse) {
	a.fail(&resp.Diagnostics)
}

// ---- ephemeral resource

type pass5StubEphemeralResource struct{ pass5Stub }

var (
	_ ephemeral.EphemeralResource                   = (*pass5StubEphemeralResource)(nil)
	_ ephemeral.EphemeralResourceWithValidateConfig = (*pass5StubEphemeralResource)(nil)
)

func newPass5StubEphemeralResource(suffix, item, summary string) ephemeral.EphemeralResource {
	return &pass5StubEphemeralResource{pass5Stub{suffix, item, summary}}
}

func (e *pass5StubEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = e.typeName(req.ProviderTypeName)
}

func (e *pass5StubEphemeralResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = ephschema.Schema{
		MarkdownDescription: e.description(),
		Attributes: map[string]ephschema.Attribute{
			"id": ephschema.StringAttribute{Computed: true, MarkdownDescription: "Placeholder identifier; never set until the type is implemented."},
		},
	}
}

func (e *pass5StubEphemeralResource) ValidateConfig(_ context.Context, _ ephemeral.ValidateConfigRequest, resp *ephemeral.ValidateConfigResponse) {
	e.fail(&resp.Diagnostics)
}

func (e *pass5StubEphemeralResource) Open(_ context.Context, _ ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	e.fail(&resp.Diagnostics)
}

// isPass5Stub reports whether v (a resource, data source, action or
// ephemeral resource) is a Pass 5 stub, so registry-wide tests can skip it.
func isPass5Stub(v any) bool {
	switch v.(type) {
	case *pass5StubResource, *pass5StubDataSource, *pass5StubAction, *pass5StubEphemeralResource:
		return true
	}
	return false
}
