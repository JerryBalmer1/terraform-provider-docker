package provider

// data.docker_compose_config (compose:cli-config, Q12): `docker compose
// config` without the CLI: parse, interpolate, merge and validate a project
// with compose-go and render it in canonical form. No Engine call.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	aschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure        = (*composeConfigDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*composeConfigDataSource)(nil)
)

// Descriptions of the project source attributes, shared by every type that
// loads a compose model.
const (
	composeSrcContentDesc  = "Compose YAML as a string. Exactly one of `content` and `files` must be set."
	composeSrcFilesDesc    = "Compose file paths, merged in order like repeated `docker compose -f`."
	composeSrcNameDesc     = "The compose project name (compose-spec top-level `name`, CLI `--project-name`). Defaults to the file's top-level `name`, then to the project directory's name."
	composeSrcProfilesDesc = "Profiles to enable (CLI `--profile`)."
	composeSrcEnvDesc      = "Variables for `${VAR}` interpolation. The provider's own environment is never read; values here override `env_file`."
	composeSrcEnvFileDesc  = "Dotenv files read for interpolation variables (CLI `--env-file`), later files winning."
	composeSrcDirDesc      = "The project directory relative paths resolve against (CLI `--project-directory`). Defaults to the first file's directory, or the Terraform working directory for `content`."
)

// composeSourceDataSourceAttributes are the project source attributes of a
// data source. With nameRequired, name is required (content/files optional
// are then not offered: label-based types only need the name).
func composeSourceDataSourceAttributes() map[string]dschema.Attribute {
	return map[string]dschema.Attribute{
		"content": dschema.StringAttribute{Optional: true, MarkdownDescription: composeSrcContentDesc},
		"files": dschema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: composeSrcFilesDesc,
			Validators: []validator.List{listvalidator.SizeAtLeast(1)}},
		"name":              dschema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: composeSrcNameDesc},
		"profiles":          dschema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: composeSrcProfilesDesc},
		"env":               dschema.MapAttribute{ElementType: types.StringType, Optional: true, Sensitive: true, MarkdownDescription: composeSrcEnvDesc},
		"env_file":          dschema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: composeSrcEnvFileDesc},
		"project_directory": dschema.StringAttribute{Optional: true, MarkdownDescription: composeSrcDirDesc},
	}
}

// composeSourceActionAttributes are the project source attributes of an
// action (name optional: it defaults like the loader's).
func composeSourceActionAttributes() map[string]aschema.Attribute {
	return map[string]aschema.Attribute{
		"content": aschema.StringAttribute{Optional: true, MarkdownDescription: composeSrcContentDesc},
		"files": aschema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: composeSrcFilesDesc,
			Validators: []validator.List{listvalidator.SizeAtLeast(1)}},
		"name":              aschema.StringAttribute{Optional: true, MarkdownDescription: composeSrcNameDesc},
		"profiles":          aschema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: composeSrcProfilesDesc},
		"env":               aschema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: composeSrcEnvDesc},
		"env_file":          aschema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: composeSrcEnvFileDesc},
		"project_directory": aschema.StringAttribute{Optional: true, MarkdownDescription: composeSrcDirDesc},
	}
}

// NewComposeConfigDataSource returns data.docker_compose_config.
func NewComposeConfigDataSource() datasource.DataSource { return &composeConfigDataSource{} }

type composeConfigDataSource struct{ pd *providerData }

type composeConfigModel struct {
	contractComposeCliConfig
	composeSourceModel
	ServiceNames types.List `tfsdk:"service_names"`
	ImageRefs    types.Map  `tfsdk:"image_refs"`
}

func (d *composeConfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_config"
}

func (d *composeConfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := composeSourceDataSourceAttributes()
	attrs["service_names"] = dschema.ListAttribute{ElementType: types.StringType, Computed: true,
		MarkdownDescription: "The enabled services of the project, sorted."}
	attrs["image_refs"] = dschema.MapAttribute{ElementType: types.StringType, Computed: true,
		MarkdownDescription: "Map of service name to the image it runs after interpolation (`image`, or `<project>-<service>` for a build-only service)."}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Renders a compose project the way `docker compose config` does, without the CLI and without contacting the daemon. " +
			"The project is parsed, interpolated (only with `env` and `env_file`), merged and validated by compose-go; `compose_yaml` is the " +
			"canonical YAML, `project` the same model as a structured object, and `content_sha256` detects change.",
		Attributes: contractAddDataSourceAttributes(attrs, "compose:cli-config"),
	}
}

func (d *composeConfigDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("content"), path.MatchRoot("files")),
	}
}

func (d *composeConfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *composeConfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m composeConfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p := composeLoadSource(ctx, m.composeSourceModel, &resp.Diagnostics)
	if p == nil {
		return
	}
	yaml, err := seamComposeImpl.Normalize(p)
	if err != nil {
		resp.Diagnostics.AddError("Cannot render compose project", err.Error())
		return
	}
	m.Name = types.StringValue(p.Name)
	m.ServiceNames = stringListValue(composeSortedKeys(p.Services), false)
	images := map[string]string{}
	for _, s := range p.Services {
		images[s.Name] = composeImageName(p.Name, s)
	}
	m.ImageRefs = stringMapValue(images, false)
	resp.Diagnostics.Append(m.contractFill(ctx, d.pd, contractFillInput{
		Raw:        map[string][]byte{"compose_yaml": yaml},
		Structured: map[string]any{"project": composeProjectValue(p)},
	})...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
