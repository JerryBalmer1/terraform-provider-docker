package main

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	pschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// fakeResource is a resource whose only interesting method is Schema.
type fakeResource struct{}

func (fakeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_widget"
}

func (fakeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = fakeSchema()
}

func (fakeResource) Create(context.Context, resource.CreateRequest, *resource.CreateResponse) {}
func (fakeResource) Read(context.Context, resource.ReadRequest, *resource.ReadResponse)       {}
func (fakeResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {}
func (fakeResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

type fakeProvider struct{}

func (fakeProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "docker"
}
func (fakeProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = pschema.Schema{}
}
func (fakeProvider) Configure(context.Context, provider.ConfigureRequest, *provider.ConfigureResponse) {
}
func (fakeProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }
func (fakeProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return fakeResource{} }}
}

// customReplace is a hand-written modifier whose description matches the
// framework's wording; it must be detected by the description fallback.
type customReplace struct{}

func (customReplace) Description(context.Context) string {
	return "Terraform will destroy and recreate the resource when this changes."
}
func (customReplace) MarkdownDescription(ctx context.Context) string { return "" }
func (customReplace) PlanModifyString(context.Context, planmodifier.StringRequest, *planmodifier.StringResponse) {
}

func fakeSchema() rschema.Schema {
	return rschema.Schema{
		MarkdownDescription: "A widget.",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.StringAttribute{Computed: true, MarkdownDescription: "ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": rschema.StringAttribute{Required: true, MarkdownDescription: "Name. Forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"driver": rschema.StringAttribute{Optional: true, MarkdownDescription: "Driver, no sentence.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()}},
			"custom": rschema.StringAttribute{Optional: true, MarkdownDescription: "Custom.",
				PlanModifiers: []planmodifier.String{customReplace{}}},
			"size": rschema.Int64Attribute{Optional: true, Description: "Size. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIf(
					func(context.Context, planmodifier.Int64Request, *int64planmodifier.RequiresReplaceIfFuncResponse) {}, "x", "x")}},
			"labels": rschema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Labels.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()}},
			"free": rschema.StringAttribute{Optional: true, MarkdownDescription: "Updates in place."},
			"meta": rschema.SingleNestedAttribute{Optional: true, MarkdownDescription: "Meta.",
				Attributes: map[string]rschema.Attribute{
					"k": rschema.StringAttribute{Optional: true, MarkdownDescription: "Key.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
				}},
		},
		Blocks: map[string]rschema.Block{
			"build": rschema.ListNestedBlock{
				MarkdownDescription: "Build.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: rschema.NestedBlockObject{
					Attributes: map[string]rschema.Attribute{
						"context": rschema.StringAttribute{Required: true, MarkdownDescription: "Context. Forces replacement.",
							PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
					},
					Blocks: map[string]rschema.Block{
						"secret": rschema.SingleNestedBlock{
							MarkdownDescription: "Secret. Forces replacement.",
							PlanModifiers:       []planmodifier.Object{objectplanmodifier.RequiresReplace()},
							Attributes: map[string]rschema.Attribute{
								"id": rschema.StringAttribute{Optional: true, MarkdownDescription: "Secret id."},
							},
						},
					},
				},
			},
		},
	}
}

func TestFrameworkWalk(t *testing.T) {
	ctx := context.Background()
	byType, err := frameworkResources(ctx, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok := byType["docker_widget"]
	if !ok {
		t.Fatalf("docker_widget not walked: %v", byType)
	}
	got := map[string]fwNode{}
	var paths []string
	for _, n := range nodes {
		got[n.Path] = n
		paths = append(paths, n.Path)
	}
	want := "build,build.context,build.secret,build.secret.id,custom,driver,free,id,labels,meta,meta.k,name,size"
	if strings.Join(paths, ",") != want {
		t.Fatalf("paths = %s\nwant    %s", strings.Join(paths, ","), want)
	}
	for p, rr := range map[string]bool{
		"id": false, "free": false, "meta": false, "build.secret.id": false,
		"name": true, "driver": true, "custom": true, "size": true, "labels": true,
		"meta.k": true, "build": true, "build.context": true, "build.secret": true,
	} {
		if got[p].RequiresReplace != rr {
			t.Errorf("%s: RequiresReplace = %v, want %v", p, got[p].RequiresReplace, rr)
		}
	}
	if !got["build"].Block || !got["build.secret"].Block || got["name"].Block {
		t.Error("block flag wrong")
	}
	if got["size"].Description != "Size. Changing this forces a new resource." {
		t.Errorf("plain Description not used as fallback: %q", got["size"].Description)
	}

	set, vs := frameworkReplacements(byType)
	var vstr []string
	for _, v := range vs {
		vstr = append(vstr, v.String())
	}
	wantV := "docker_widget.build (block),docker_widget.custom (attribute),docker_widget.driver (attribute),docker_widget.labels (attribute),docker_widget.meta.k (attribute)"
	if strings.Join(vstr, ",") != wantV {
		t.Errorf("violations = %s\nwant         %s", strings.Join(vstr, ","), wantV)
	}
	if !set.has("docker_widget", "driver") || set.has("docker_widget", "free") || set.has("docker_other", "name") {
		t.Errorf("replace set wrong: %v", set)
	}
	var nilSet replaceSet
	if nilSet.has("docker_widget", "name") {
		t.Error("nil set has entries")
	}
}

func TestIsRequiresReplace(t *testing.T) {
	ctx := context.Background()
	if !isRequiresReplace(ctx, stringplanmodifier.RequiresReplace()) {
		t.Error("stringplanmodifier.RequiresReplace not detected")
	}
	if isRequiresReplace(ctx, stringplanmodifier.UseStateForUnknown()) {
		t.Error("UseStateForUnknown detected as replace")
	}
	if !isRequiresReplace(ctx, customReplace{}) {
		t.Error("description fallback not detected")
	}
	if isRequiresReplace(ctx, nil) {
		t.Error("nil detected")
	}
}

// TestMarkReplacementsIntoSkill feeds framework flags into the protocol-based
// render: an attribute whose text lacks the sentence still lands in the
// replacement table, and a flagged block is marked.
func TestMarkReplacementsIntoSkill(t *testing.T) {
	resp := &tfprotov6.GetProviderSchemaResponse{
		Provider: &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{}},
		ResourceSchemas: map[string]*tfprotov6.Schema{"docker_widget": {Block: &tfprotov6.SchemaBlock{
			Description: "A widget.",
			Attributes: []*tfprotov6.SchemaAttribute{
				{Name: "driver", Type: tftypes.String, Optional: true, Description: "Driver, no sentence."},
				{Name: "free", Type: tftypes.String, Optional: true, Description: "Updates in place."},
			},
			BlockTypes: []*tfprotov6.SchemaNestedBlock{{TypeName: "build", Nesting: tfprotov6.SchemaNestedBlockNestingModeList,
				Block: &tfprotov6.SchemaBlock{Description: "Build.", Attributes: []*tfprotov6.SchemaAttribute{
					{Name: "context", Type: tftypes.String, Required: true, Description: "Context."},
				}}}},
		}}},
	}
	set := replaceSet{"docker_widget": {"driver": true, "build": true, "build.context": true}}
	files, _, _, err := render(resp, renderOpts{Replace: set})
	if err != nil {
		t.Fatal(err)
	}
	skill := string(files["SKILL.md"])
	for _, s := range []string{
		"| `docker_widget` | `driver` | Driver, no sentence. |",
		"| `docker_widget` | `build` (block) | Build. |",
		"| `docker_widget` | `build.context` | Context. |",
	} {
		if !strings.Contains(skill, s) {
			t.Errorf("SKILL.md lacks %q", s)
		}
	}
	if strings.Contains(skill, "`free`") {
		t.Error("free listed as replacement")
	}
	ref := string(files["reference/docker_widget.md"])
	if !strings.Contains(ref, "`build` (list of blocks, forces replacement)") {
		t.Errorf("block not marked in reference:\n%s", ref)
	}
	if !strings.Contains(ref, "| `driver` | `string` | optional | yes |") {
		t.Errorf("driver not marked in reference:\n%s", ref)
	}
}
