package provider

// Lane D: data "docker_dockerfile" (inventory dockerfile:data-dockerfile and
// the 21 dockerfile_block items, Q27-Q32, Q47-Q49).

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*dockerfileDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*dockerfileDataSource)(nil)
)

const dockerfileItemID = "dockerfile:data-dockerfile"

// NewDockerfileDataSource is the data "docker_dockerfile" constructor.
func NewDockerfileDataSource() datasource.DataSource { return &dockerfileDataSource{} }

type dockerfileDataSource struct {
	pd *providerData
}

type dockerfileDataSourceModel struct {
	contractDockerfileDataDockerfile

	Content   types.String       `tfsdk:"content"`
	Path      types.String       `tfsdk:"path"`
	URL       types.String       `tfsdk:"url"`
	Image     types.String       `tfsdk:"image"`
	Syntax    types.String       `tfsdk:"syntax"`
	Escape    types.String       `tfsdk:"escape"`
	Check     *dfCheckModel      `tfsdk:"check"`
	GlobalArg []dfGlobalArgModel `tfsdk:"global_arg"`
	Stage     []dfStageModel     `tfsdk:"stage"`

	ID             types.String `tfsdk:"id"`
	SourceType     types.String `tfsdk:"source_type"`
	GitCommit      types.String `tfsdk:"git_commit"`
	ImageDigestRef types.String `tfsdk:"image_digest_ref"`
	StageNames     types.List   `tfsdk:"stage_names"`
	FinalStage     types.String `tfsdk:"final_stage"`
	Labels         types.Map    `tfsdk:"labels"`
}

func (d *dockerfileDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dockerfile"
}

func (d *dockerfileDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// Schema text helpers.
func dfOnBuildAttr() schema.BoolAttribute {
	return schema.BoolAttribute{Optional: true, MarkdownDescription: "Wraps the instruction in `ONBUILD` (Q30): it runs when a later build uses this image as its base."}
}

func dfShellFormAttr(kw string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, MarkdownDescription: "The command in shell form (`" + kw + " <command>`), run through the stage's SHELL. One line; conflicts with `exec_form`."}
}

func dfExecFormAttr(kw string) schema.ListAttribute {
	return schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "The command in exec form (`" + kw + " [\"executable\", \"param\"]`), run without a shell. Conflicts with `shell_form`."}
}

func dfHeredocsAttr(copy bool) schema.ListNestedAttribute {
	desc := "Here-documents (`<<EOF`) attached to the instruction, in order. Each becomes a `<<DELIM` word on the instruction line followed by its body."
	if copy {
		desc = "Here-documents (`<<name`) copied as files into `destination`, in order; the delimiter is the file name."
	}
	return schema.ListNestedAttribute{
		Optional: true, MarkdownDescription: desc,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"delimiter":  schema.StringAttribute{Optional: true, MarkdownDescription: "The delimiter word. Default `EOF` (`EOF1`, `EOF2` ... when several)."},
			"content":    schema.StringAttribute{Required: true, MarkdownDescription: "The body up to the delimiter; a trailing newline is added when missing."},
			"strip_tabs": schema.BoolAttribute{Optional: true, MarkdownDescription: "Use `<<-` so leading tabs are stripped from the body."},
			"expand":     schema.BoolAttribute{Optional: true, MarkdownDescription: "Expand build variables in the body. Default true; false quotes the delimiter (`<<\"EOF\"`)."},
		}},
	}
}

// dfBlockOutputAttrs are the computed attributes of each builder block (the
// dockerfile:<KEYWORD> items' outputs; the typed object of each instruction
// is in `stages[*].instructions[*].<keyword>`).
func dfBlockOutputAttrs(attrs map[string]schema.Attribute, withInstruction bool) map[string]schema.Attribute {
	attrs["stage_index"] = schema.NumberAttribute{Computed: true, MarkdownDescription: "0-based index of the build stage holding the block; null for a `global_arg`."}
	attrs["instruction_index"] = schema.NumberAttribute{Computed: true, MarkdownDescription: "0-based position of the block inside its stage (FROM is 0, so instructions start at 1); for a `global_arg`, its position among the global ARGs."}
	attrs["dockerfile"] = schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "This block rendered as Dockerfile text (instruction line and heredoc bodies). Sensitive: ENV values and ARG defaults appear inline."}
	attrs["content_sha256"] = schema.StringAttribute{Computed: true, MarkdownDescription: "The sha256 (hex) of this block's `dockerfile` text."}
	attrs["dockerfile_syntax"] = schema.StringAttribute{Computed: true, MarkdownDescription: "The `# syntax=` value in effect for the Dockerfile holding this block, else the default frontend (`" + contractDockerfileDefaultSyntax + "`)."}
	if withInstruction {
		attrs["instruction"] = schema.StringAttribute{Computed: true, MarkdownDescription: "The instruction keyword of this block in upper case (`RUN`, `COPY`, ...); ONBUILD shows in `dockerfile`, not here."}
	}
	return attrs
}

// dfKeywordBlock builds one keyword block. Each `instruction` block sets
// exactly one keyword block and leaves the others absent, but the framework
// validates the Required attributes of an absent SingleNestedBlock as if it
// were present ("Missing Configuration for Required Attribute" for every
// unused keyword). So the keyword's own required arguments are declared
// Optional here; dfInstruction rejects a set block that lacks them.
func dfKeywordBlock(desc string, attrs map[string]schema.Attribute) schema.SingleNestedBlock {
	const req = " Required in this block."
	for k, a := range attrs {
		switch v := a.(type) {
		case schema.StringAttribute:
			if v.Required {
				v.Required, v.Optional, v.MarkdownDescription = false, true, v.MarkdownDescription+req
				attrs[k] = v
			}
		case schema.ListAttribute:
			if v.Required {
				v.Required, v.Optional, v.MarkdownDescription = false, true, v.MarkdownDescription+req
				attrs[k] = v
			}
		case schema.MapAttribute:
			if v.Required {
				v.Required, v.Optional, v.MarkdownDescription = false, true, v.MarkdownDescription+req
				attrs[k] = v
			}
		case schema.ListNestedAttribute:
			if v.Required {
				v.Required, v.Optional, v.MarkdownDescription = false, true, v.MarkdownDescription+req
				attrs[k] = v
			}
		}
	}
	attrs["onbuild"] = dfOnBuildAttr()
	return schema.SingleNestedBlock{MarkdownDescription: desc, Attributes: attrs}
}

func dfInstructionBlocks() map[string]schema.Block {
	return map[string]schema.Block{
		"run": dfKeywordBlock("`RUN`: execute a command at build time and commit the result as a new layer. Set `shell_form`, `exec_form` or `heredocs`.", map[string]schema.Attribute{
			"shell_form": dfShellFormAttr("RUN"),
			"exec_form":  dfExecFormAttr("RUN"),
			"heredocs":   dfHeredocsAttr(false),
			"mount": schema.ListNestedAttribute{
				Optional: true, MarkdownDescription: "One `--mount` flag per entry (Dockerfile 1.2): a bind, cache, tmpfs, secret or ssh mount for this RUN only.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"type":     schema.StringAttribute{Optional: true, MarkdownDescription: "`bind` (default), `cache`, `tmpfs`, `secret` or `ssh`."},
					"target":   schema.StringAttribute{Optional: true, MarkdownDescription: "Mount path (required for bind, cache and tmpfs)."},
					"source":   schema.StringAttribute{Optional: true, MarkdownDescription: "Path in `from` (bind, cache)."},
					"from":     schema.StringAttribute{Optional: true, MarkdownDescription: "Build stage, context or image to mount from (bind, cache)."},
					"rw":       schema.BoolAttribute{Optional: true, MarkdownDescription: "Allow writes to a bind mount (discarded after the RUN)."},
					"id":       schema.StringAttribute{Optional: true, MarkdownDescription: "Cache id, or secret / ssh id."},
					"ro":       schema.BoolAttribute{Optional: true, MarkdownDescription: "Read-only cache mount."},
					"sharing":  schema.StringAttribute{Optional: true, MarkdownDescription: "Cache sharing mode: `shared` (default), `private` or `locked`."},
					"mode":     schema.StringAttribute{Optional: true, MarkdownDescription: "File mode in octal (cache 0755, secret 0400, ssh 0600 by default)."},
					"uid":      schema.Int64Attribute{Optional: true, MarkdownDescription: "User id of the mount (default 0)."},
					"gid":      schema.Int64Attribute{Optional: true, MarkdownDescription: "Group id of the mount (default 0)."},
					"size":     schema.StringAttribute{Optional: true, MarkdownDescription: "Size limit of a tmpfs mount."},
					"env":      schema.StringAttribute{Optional: true, MarkdownDescription: "Expose a secret as this environment variable (Dockerfile 1.10)."},
					"required": schema.BoolAttribute{Optional: true, MarkdownDescription: "Fail when the secret or ssh agent is not available (default false)."},
				}},
			},
			"network":  schema.StringAttribute{Optional: true, MarkdownDescription: "`--network` (Dockerfile 1.3): `default`, `none` or `host` (needs the network.host entitlement)."},
			"security": schema.StringAttribute{Optional: true, MarkdownDescription: "`--security` (Dockerfile 1.20): `sandbox` (default) or `insecure` (needs the security.insecure entitlement)."},
			"device": schema.ListNestedAttribute{
				Optional: true, MarkdownDescription: "One `--device` flag per entry (Dockerfile 1.27, BuildKit 0.20+): a CDI device for this RUN.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":     schema.StringAttribute{Required: true, MarkdownDescription: "CDI device name: `vendor/device`, `vendor/device=name`, `vendor/device=*` or a class."},
					"required": schema.BoolAttribute{Optional: true, MarkdownDescription: "Fail when the device is not available."},
				}},
			},
		}),
		"cmd": dfKeywordBlock("`CMD`: the default command (or default ENTRYPOINT arguments) of the image.", map[string]schema.Attribute{
			"shell_form": dfShellFormAttr("CMD"), "exec_form": dfExecFormAttr("CMD"),
		}),
		"entrypoint": dfKeywordBlock("`ENTRYPOINT`: the executable the container runs.", map[string]schema.Attribute{
			"shell_form": dfShellFormAttr("ENTRYPOINT"), "exec_form": dfExecFormAttr("ENTRYPOINT"),
		}),
		"shell": dfKeywordBlock("`SHELL`: the shell used for the shell form of later instructions.", map[string]schema.Attribute{
			"shell": schema.ListAttribute{ElementType: types.StringType, Required: true, MarkdownDescription: "Executable and parameters, e.g. `[\"/bin/bash\", \"-c\"]` (JSON form only)."},
		}),
		"label": dfKeywordBlock("`LABEL`: image metadata; use `org.opencontainers.image.authors` instead of the deprecated MAINTAINER (Q29).", map[string]schema.Attribute{
			"labels": schema.MapAttribute{ElementType: types.StringType, Required: true, MarkdownDescription: "Label keys and values, rendered sorted by key and quoted where needed."},
		}),
		"expose": dfKeywordBlock("`EXPOSE`: document the ports the container listens on.", map[string]schema.Attribute{
			"ports": schema.ListNestedAttribute{
				Required: true, MarkdownDescription: "Ports in order.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"port":     schema.StringAttribute{Required: true, MarkdownDescription: "Port number or range (may be a $VAR)."},
					"protocol": schema.StringAttribute{Optional: true, MarkdownDescription: "`tcp` (default when omitted) or `udp`."},
				}},
			},
		}),
		"env": dfKeywordBlock("`ENV`: environment variables for later instructions and the container.", map[string]schema.Attribute{
			"env": schema.MapAttribute{ElementType: types.StringType, Required: true, Sensitive: true, MarkdownDescription: "Variables and values, rendered sorted by name and quoted where needed. Sensitive."},
		}),
		"add": dfKeywordBlock("`ADD`: copy files, URLs, Git repositories or local tar archives (unpacked) into the image.", map[string]schema.Attribute{
			"sources":      schema.ListAttribute{ElementType: types.StringType, Required: true, MarkdownDescription: "Sources in order: context paths or patterns, URLs, Git URLs or local tar archives."},
			"destination":  schema.StringAttribute{Required: true, MarkdownDescription: "Destination path in the image."},
			"keep_git_dir": schema.BoolAttribute{Optional: true, MarkdownDescription: "`--keep-git-dir` (Dockerfile 1.1): keep the `.git` directory of a Git source."},
			"checksum":     schema.StringAttribute{Optional: true, MarkdownDescription: "`--checksum` (Dockerfile 1.6): `sha256:<hash>` of an HTTP source or a Git commit SHA."},
			"chmod":        schema.StringAttribute{Optional: true, MarkdownDescription: "`--chmod` (Dockerfile 1.2): octal mode, or symbolic notation (Dockerfile 1.14)."},
			"chown":        schema.StringAttribute{Optional: true, MarkdownDescription: "`--chown`: `<user>[:<group>]` or `<uid>[:<gid>]`."},
			"link":         schema.BoolAttribute{Optional: true, MarkdownDescription: "`--link` (Dockerfile 1.4): copy into an independent layer."},
			"unpack":       schema.BoolAttribute{Optional: true, MarkdownDescription: "`--unpack` (Dockerfile 1.17): whether to unpack archives (local archives unpack by default)."},
			"exclude":      schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "`--exclude` patterns (Dockerfile 1.19), one flag each."},
		}),
		"copy": dfKeywordBlock("`COPY`: copy files from the build context, a stage, an image or a named context into the image.", map[string]schema.Attribute{
			"sources":     schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Sources in order (paths or patterns). Required unless `heredocs` is set."},
			"destination": schema.StringAttribute{Required: true, MarkdownDescription: "Destination path in the image."},
			"heredocs":    dfHeredocsAttr(true),
			"from":        schema.StringAttribute{Optional: true, MarkdownDescription: "`--from`: a build stage, image or named context to copy from."},
			"chmod":       schema.StringAttribute{Optional: true, MarkdownDescription: "`--chmod` (Dockerfile 1.2): octal mode, or symbolic notation (Dockerfile 1.14)."},
			"chown":       schema.StringAttribute{Optional: true, MarkdownDescription: "`--chown`: `<user>[:<group>]` or `<uid>[:<gid>]`."},
			"link":        schema.BoolAttribute{Optional: true, MarkdownDescription: "`--link` (Dockerfile 1.4): copy into an independent layer."},
			"parents":     schema.BoolAttribute{Optional: true, MarkdownDescription: "`--parents` (Dockerfile 1.20): keep the parent directories of the sources."},
			"exclude":     schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "`--exclude` patterns (Dockerfile 1.19), one flag each."},
		}),
		"volume": dfKeywordBlock("`VOLUME`: mount points for externally mounted volumes.", map[string]schema.Attribute{
			"paths": schema.ListAttribute{ElementType: types.StringType, Required: true, MarkdownDescription: "Mount point paths (the JSON form is used when one holds whitespace)."},
		}),
		"user": dfKeywordBlock("`USER`: the user (and group) for later instructions and the container.", map[string]schema.Attribute{
			"user":  schema.StringAttribute{Required: true, MarkdownDescription: "User name or UID."},
			"group": schema.StringAttribute{Optional: true, MarkdownDescription: "Group name or GID."},
		}),
		"workdir": dfKeywordBlock("`WORKDIR`: the working directory for later instructions and the container.", map[string]schema.Attribute{
			"path": schema.StringAttribute{Required: true, MarkdownDescription: "Absolute path, or relative to the previous WORKDIR; may hold $VAR."},
		}),
		"arg": dfKeywordBlock("`ARG` inside a stage: build arguments the stage can use (ARGs before the first FROM are `global_arg` blocks).", map[string]schema.Attribute{
			"args": schema.ListNestedAttribute{
				Required: true, MarkdownDescription: "Arguments in order.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":    schema.StringAttribute{Required: true, MarkdownDescription: "Argument name."},
					"default": schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Default value. Sensitive: it is stored in the image history."},
				}},
			},
		}),
		"stopsignal": dfKeywordBlock("`STOPSIGNAL`: the signal that stops the container.", map[string]schema.Attribute{
			"signal": schema.StringAttribute{Required: true, MarkdownDescription: "`SIG<NAME>` or an unsigned number."},
		}),
		"healthcheck": dfKeywordBlock("`HEALTHCHECK`: how to test that the container still works (`none = true` disables an inherited check).", map[string]schema.Attribute{
			"none":           schema.BoolAttribute{Optional: true, MarkdownDescription: "`HEALTHCHECK NONE`: disable any healthcheck inherited from the base image."},
			"interval":       schema.StringAttribute{Optional: true, MarkdownDescription: "`--interval` duration (default 30s)."},
			"timeout":        schema.StringAttribute{Optional: true, MarkdownDescription: "`--timeout` duration (default 30s)."},
			"start_period":   schema.StringAttribute{Optional: true, MarkdownDescription: "`--start-period` duration (default 0s)."},
			"start_interval": schema.StringAttribute{Optional: true, MarkdownDescription: "`--start-interval` duration (default 5s; Docker Engine 25.0+)."},
			"retries":        schema.Int64Attribute{Optional: true, MarkdownDescription: "`--retries` count (default 3)."},
			"shell_form":     dfShellFormAttr("HEALTHCHECK CMD"),
			"exec_form":      dfExecFormAttr("HEALTHCHECK CMD"),
		}),
	}
}

func (d *dockerfileDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"content": schema.StringAttribute{Optional: true, MarkdownDescription: "Dockerfile text to parse (source `content`). Exactly one of `content`, `path`, `url`, `image` or `stage` blocks."},
		"path":    schema.StringAttribute{Optional: true, MarkdownDescription: "Local Dockerfile to read and parse (source `path`), relative to the working directory."},
		"url":     schema.StringAttribute{Optional: true, MarkdownDescription: "`https://` URL of a raw Dockerfile to download and parse (source `url`; pure Go HTTPS, no git)."},
		"image":   schema.StringAttribute{Optional: true, MarkdownDescription: "Local image (name, tag or ID) whose history is turned into an approximate Dockerfile through the Engine API (source `image`, `fidelity = \"approximate\"`)."},
		"syntax":  schema.StringAttribute{Optional: true, MarkdownDescription: "Builder mode: the `# syntax=` parser directive, e.g. `docker/dockerfile:1` (needs `stage` blocks)."},
		"escape":  schema.StringAttribute{Optional: true, MarkdownDescription: "Builder mode: the `# escape=` parser directive, `\\` (default) or a backtick (needs `stage` blocks)."},

		"id":               schema.StringAttribute{Computed: true, MarkdownDescription: "Source locator: `content:<sha256>`, `path:<absolute path>`, `url:<url>`, `image:<image id>` or `blocks:<sha256 of the rendered text>`."},
		"source_type":      schema.StringAttribute{Computed: true, MarkdownDescription: "Which source was read: `content`, `path`, `url`, `image` or `blocks`."},
		"git_commit":       schema.StringAttribute{Computed: true, MarkdownDescription: "Always null: Git sources are not read (use `url` with a raw file URL pinned to a commit)."},
		"image_digest_ref": schema.StringAttribute{Computed: true, MarkdownDescription: "Image mode only: the first `repo@sha256:...` of the image read; null otherwise."},
		"stage_names":      schema.ListAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Stage names in order (unnamed stages as their index), the targets for `build.target`."},
		"final_stage":      schema.StringAttribute{Computed: true, MarkdownDescription: "Name (or index) of the last stage, the default build target."},
		"labels":           schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: "Union of the LABEL instructions of the final stage (image mode: the image config labels)."},
	}
	contractAddDataSourceAttributes(attrs, dockerfileItemID)
	stageAttrs := dfBlockOutputAttrs(map[string]schema.Attribute{
		"name":       schema.StringAttribute{Optional: true, MarkdownDescription: "Stage name (`FROM ... AS <name>`), usable by `COPY --from`, `RUN --mount=from=` and `build.target`."},
		"from":       schema.StringAttribute{Required: true, MarkdownDescription: "Base image (`image`, `image:tag`, `image@digest`, `scratch`), an earlier stage name, or a `$GLOBAL_ARG` reference."},
		"platform":   schema.StringAttribute{Optional: true, MarkdownDescription: "`FROM --platform`, e.g. `linux/amd64` or `$BUILDPLATFORM`."},
		"comments":   schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Comment lines written above the FROM line."},
		"stage_name": schema.StringAttribute{Computed: true, MarkdownDescription: "The `AS <name>` stage name; null when unnamed."},
	}, false)
	instrAttrs := dfBlockOutputAttrs(map[string]schema.Attribute{
		"comments": schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Comment lines written above the instruction."},
	}, true)
	globalArgAttrs := dfBlockOutputAttrs(map[string]schema.Attribute{
		"name":    schema.StringAttribute{Required: true, MarkdownDescription: "Argument name."},
		"default": schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Default value. Sensitive: it is stored in the image history."},
	}, true)
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads, parses and renders one Dockerfile: from `content`, a local `path`, an https `url`, an `image` history (approximate), or written with ordered `stage` / `instruction` blocks. " +
			"Every source yields the same outputs: the re-rendered `dockerfile` (+ `dockerfile_sha256`), the text as read in `source_dockerfile` (+ `source_dockerfile_sha256`), " +
			"and structured `stages` (each instruction with all its flags as the object named by its lowercase keyword, JSON-encoded), `directives`, `global_args`, `exposed_ports`, `base_images`.\n\n" +
			"Builder mode keeps instruction order with one `instruction` block per instruction holding exactly one keyword block (`run`, `copy`, `env`, ...): Terraform keeps the order of blocks of one type, not across types. " +
			"`onbuild = true` on a keyword block writes `ONBUILD <instruction>`. MAINTAINER is read from existing Dockerfiles but cannot be written: use `label { labels = { \"org.opencontainers.image.authors\" = \"...\" } }`. " +
			"`check.skip` names outside the pinned rule list (frontend 1.28) give a warning, not an error, because a newer `syntax` may add rules.",
		Attributes: attrs,
		Blocks: map[string]schema.Block{
			"check": schema.SingleNestedBlock{
				MarkdownDescription: "Builder mode: the `# check=` parser directive (Dockerfile 1.8) configuring build checks.",
				Attributes: map[string]schema.Attribute{
					"skip":  schema.ListAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Build-check rule names to skip, or `all`; unknown names warn."},
					"error": schema.BoolAttribute{Optional: true, MarkdownDescription: "Fail the build on check violations (`error=true`)."},
				},
			},
			"global_arg": schema.ListNestedBlock{
				MarkdownDescription: "Builder mode: `ARG` instructions before the first FROM, in order (usable in `stage.from`).",
				NestedObject:        schema.NestedBlockObject{Attributes: globalArgAttrs},
			},
			"stage": schema.ListNestedBlock{
				MarkdownDescription: "Builder mode: build stages in order (`FROM [--platform=...] <from> [AS <name>]`, Q31) and their instructions.",
				NestedObject: schema.NestedBlockObject{
					Attributes: stageAttrs,
					Blocks: map[string]schema.Block{
						"instruction": schema.ListNestedBlock{
							MarkdownDescription: "One instruction of the stage, in order. Holds exactly one keyword block.",
							NestedObject:        schema.NestedBlockObject{Attributes: instrAttrs, Blocks: dfInstructionBlocks()},
						},
					},
				},
			},
		},
	}
}

// dockerfileRead is the result of reading one source.
type dockerfileRead struct {
	parsed     *dockerfileParsed
	rendered   string
	source     *string
	sourceType string
	id         string
	image      *dockerfileImageResult
}

func (d *dockerfileDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m dockerfileDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r, diags := dockerfileReadSource(ctx, d.pd, &m)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(dockerfileFillModel(ctx, d.pd, &m, r)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// dockerfileReadSource validates the source choice and reads it.
func dockerfileReadSource(ctx context.Context, pd *providerData, m *dockerfileDataSourceModel) (*dockerfileRead, diag.Diagnostics) {
	var diags diag.Diagnostics
	var set []string
	for name, v := range map[string]types.String{"content": m.Content, "path": m.Path, "url": m.URL, "image": m.Image} {
		if !v.IsNull() {
			set = append(set, name)
		}
	}
	if len(m.Stage) > 0 {
		set = append(set, "stage")
	}
	if len(set) != 1 {
		diags.AddError("Invalid docker_dockerfile source",
			fmt.Sprintf("Set exactly one of content, path, url, image or stage blocks (found: %s).", strings.Join(dockerfileSorted(set), ", ")))
		return nil, diags
	}
	if set[0] != "stage" && (!m.Syntax.IsNull() || !m.Escape.IsNull() || m.Check != nil || len(m.GlobalArg) > 0) {
		diags.AddError("Invalid docker_dockerfile builder arguments",
			"syntax, escape, check and global_arg write a Dockerfile and need stage blocks; with "+set[0]+" the directives come from the source.")
		return nil, diags
	}
	r := &dockerfileRead{sourceType: set[0]}
	var text string
	switch set[0] {
	case "content":
		text = m.Content.ValueString()
		r.id = "content:" + dockerfileSHA256(text)
	case "path":
		t, abs, err := dockerfileReadPath(m.Path.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("path"), "Cannot read Dockerfile", err.Error())
			return nil, diags
		}
		text, r.id = t, "path:"+abs
	case "url":
		t, err := dockerfileFetchURL(ctx, m.URL.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("url"), "Cannot fetch Dockerfile", err.Error())
			return nil, diags
		}
		text, r.id = t, "url:"+m.URL.ValueString()
	case "image":
		img, err := dockerfileFromImage(ctx, pd, m.Image.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("image"), "Cannot reconstruct a Dockerfile from the image", err.Error())
			return nil, diags
		}
		if img.Unconverted > 0 {
			diags.AddWarning("Image history only partly converted",
				fmt.Sprintf("%d history entr(y/ies) of %s could not be read back as instructions; they are listed in `history` only.", img.Unconverted, m.Image.ValueString()))
		}
		r.image, r.parsed, r.rendered, r.id = img, img.Parsed, img.Rendered, "image:"+img.ImageID
		return r, diags
	case "stage":
		doc, err := dfBuildDoc(m)
		if err != nil {
			diags.AddError("Invalid docker_dockerfile blocks", err.Error())
			return nil, diags
		}
		dockerfileWarnCheck(&diags, doc.Check.Skip)
		rendered, err := dockerfileRender(doc)
		if err != nil {
			diags.AddError("Cannot render Dockerfile", err.Error())
			return nil, diags
		}
		p, err := dockerfileParse(rendered)
		if err != nil {
			diags.AddError("Rendered Dockerfile does not parse", fmt.Sprintf("%v\n\n%s", err, rendered))
			return nil, diags
		}
		r.parsed, r.rendered, r.id = p, rendered, "blocks:"+dockerfileSHA256(rendered)
		return r, diags
	}
	p, err := dockerfileParse(strings.ReplaceAll(text, "\r\n", "\n"))
	if err != nil {
		diags.AddAttributeError(path.Root(set[0]), "Cannot parse Dockerfile", err.Error())
		return nil, diags
	}
	for _, w := range p.Warnings {
		diags.AddAttributeWarning(path.Root(set[0]), "Dockerfile parser warning", w)
	}
	dockerfileWarnCheck(&diags, p.Doc.Check.Skip)
	rendered, err := dockerfileRender(p.Doc)
	if err != nil {
		diags.AddAttributeError(path.Root(set[0]), "Cannot render Dockerfile", err.Error())
		return nil, diags
	}
	r.parsed, r.rendered, r.source = p, rendered, &text
	return r, diags
}

func dockerfileSorted(s []string) []string {
	m := map[string]bool{}
	for _, v := range s {
		m[v] = true
	}
	return dockerfileSortedKeys(m)
}

// dockerfileWarnCheck implements Q32: unknown rule names warn.
func dockerfileWarnCheck(diags *diag.Diagnostics, skip []string) {
	if unknown := dockerfileUnknownCheckRules(skip); len(unknown) > 0 {
		diags.AddWarning("Unknown build-check rule name",
			fmt.Sprintf("check skip names %s are not in the pinned rule list (Dockerfile frontend %s): %s. They are kept; a newer `# syntax=` frontend may know them.",
				strings.Join(unknown, ", "), strings.TrimPrefix(contractDockerfileDefaultSyntax, "docker/dockerfile:"), strings.Join(dockerfileCheckRules, ", ")))
	}
}

// dockerfileFillModel writes every computed value.
func dockerfileFillModel(ctx context.Context, pd *providerData, m *dockerfileDataSourceModel, r *dockerfileRead) diag.Diagnostics {
	var diags diag.Diagnostics
	var extra func(int, int, map[string]any)
	if r.image != nil {
		img := r.image
		extra = func(_ int, ii int, im map[string]any) {
			if ii >= len(img.HistoryOf) || img.HistoryOf[ii] < 0 {
				return
			}
			h := img.Items[img.HistoryOf[ii]]
			im["created_by"], im["created"], im["comment"], im["layer_size"] = h.CreatedBy, h.Created, dockerfileNilEmpty(h.Comment), h.Size
		}
	}
	out := dockerfileBuildOutputs(r.parsed, extra)
	in := contractFillInput{
		Raw: map[string][]byte{"dockerfile": []byte(r.rendered)},
		Structured: map[string]any{
			"stages": out.Stages, "directives": out.Directives, "global_args": out.GlobalArgs,
			"exposed_ports": out.ExposedPorts, "base_images": out.BaseImages, "history": nil,
		},
		Result:           map[string]any{"fidelity": "exact"},
		DockerfileSyntax: r.parsed.Doc.Syntax,
	}
	if r.source != nil {
		in.Raw["source_dockerfile"] = []byte(*r.source)
	}
	var fillPD *providerData
	labels := out.Labels
	if r.image != nil {
		fillPD = pd
		in.Result["fidelity"] = "approximate"
		in.Structured["history"] = r.image.History
		in.Structured["exposed_ports"] = r.image.Exposed
		labels = r.image.Labels
	}
	diags.Append(m.contractDockerfileDataDockerfile.contractFill(ctx, fillPD, in)...)
	if r.image != nil {
		m.DockerfileSyntax = types.StringNull()
	}
	m.ID = types.StringValue(r.id)
	m.SourceType = types.StringValue(r.sourceType)
	m.GitCommit = types.StringNull()
	m.ImageDigestRef = types.StringNull()
	if r.image != nil && r.image.DigestRef != "" {
		m.ImageDigestRef = types.StringValue(r.image.DigestRef)
	}
	names := make([]attr.Value, 0, len(out.StageNames))
	for _, n := range out.StageNames {
		names = append(names, types.StringValue(n))
	}
	m.StageNames = types.ListValueMust(types.StringType, names)
	m.FinalStage = types.StringValue(out.FinalStage)
	lv := map[string]attr.Value{}
	for k, v := range labels {
		lv[k] = types.StringValue(v)
	}
	m.Labels = types.MapValueMust(types.StringType, lv)
	if r.sourceType == "stage" {
		m.SourceType = types.StringValue("blocks")
		dfFillBlockOutputs(m, r.parsed.Doc)
	} else {
		dfNullBlockOutputs(m)
	}
	return diags
}
