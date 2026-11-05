package provider

// docker_compose: a compose project (YAML content or files) applied through
// the Engine API, either as containers/networks/volumes on one daemon
// (mode "local") or as a Swarm stack (mode "swarm"). No compose CLI plugin
// is involved.

import (
	"context"
	"fmt"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                     = (*composeResource)(nil)
	_ resource.ResourceWithConfigure        = (*composeResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*composeResource)(nil)
	_ resource.ResourceWithImportState      = (*composeResource)(nil)
	_ resource.ResourceWithConfigValidators = (*composeResource)(nil)
)

const (
	composeModeLocal          = "local"
	composeModeSwarm          = "swarm"
	composeDefaultWaitTimeout = "2m"
	// composeDriftPrefix starts spec_hash when Read found drift.
	composeDriftPrefix = "drifted: "
)

// NewComposeResource returns the docker_compose resource.
func NewComposeResource() resource.Resource { return &composeResource{} }

type composeResource struct {
	pd *providerData
}

type composeResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Content     types.String `tfsdk:"content"`
	Files       types.List   `tfsdk:"files"`
	ProjectName types.String `tfsdk:"project_name"`
	Profiles    types.List   `tfsdk:"profiles"`
	Env         types.Map    `tfsdk:"env"`
	EnvFile     types.List   `tfsdk:"env_file"`
	WorkingDir  types.String `tfsdk:"working_dir"`
	Mode        types.String `tfsdk:"mode"`
	Wait        types.Bool   `tfsdk:"wait"`
	WaitTimeout types.String `tfsdk:"wait_timeout"`
	KeepVolumes types.Bool   `tfsdk:"keep_volumes"`
	SpecHash    types.String `tfsdk:"spec_hash"`
	Services    types.Map    `tfsdk:"services"`
	Networks    types.Map    `tfsdk:"networks"`
	Volumes     types.Map    `tfsdk:"volumes"`
}

var composeServicesType = types.MapType{ElemType: types.ListType{ElemType: types.StringType}}

func (r *composeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose"
}

func (r *composeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Docker Compose project applied through the Engine API, with no compose CLI plugin involved. " +
			"The project is parsed with compose-go (the library `docker compose` uses) from `content` or `files`, with " +
			"interpolation limited to `env` and `env_file`. In `mode = \"local\"` every service becomes containers named " +
			"`<project>-<service>-<n>` (or `container_name`), every network `<project>_<name>` and every volume " +
			"`<project>_<name>`, all labelled `com.docker.compose.project` like `docker compose up` does, so `docker compose ps` " +
			"shows them. In `mode = \"swarm\"` the project becomes a stack (services, overlay networks, secrets and configs " +
			"labelled `com.docker.stack.namespace`) like `docker stack deploy`. Refresh compares the live objects with the " +
			"parsed project (a `com.docker.compose.config-hash` label on each object, plus container state or service " +
			"image/replicas) and marks `spec_hash` as drifted so the next plan shows an update. An update creates what is " +
			"missing, recreates what changed and removes objects of the project that are no longer in it. Destroy removes " +
			"everything labelled with the project. `build` is not supported: build images with `docker_image`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "The project name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"content": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Compose YAML as a string. Exactly one of `content` and `files` must be set; relative paths in it resolve against `working_dir` (default: the Terraform working directory)."},
			"files": schema.ListAttribute{ElementType: types.StringType, Optional: true,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				MarkdownDescription: "Compose file paths, merged in order like repeated `docker compose -f`. Relative paths in them resolve against `working_dir` or, when unset, the first file's directory."},
			"project_name": schema.StringAttribute{Optional: true, Computed: true,
				MarkdownDescription: "Project name used to name and label every object. Defaults to the file's top-level `name`, then to the working directory's name; changing it forces a new resource.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()}},
			"profiles": schema.ListAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Profiles to enable. Services with a `profiles` list run only when one of them is enabled; services without one always run."},
			"env": schema.MapAttribute{ElementType: types.StringType, Optional: true, Sensitive: true,
				MarkdownDescription: "Variables for `${VAR}` interpolation and `environment:`-sourced secrets and configs. The provider's own environment is never read; values here override `env_file`."},
			"env_file": schema.ListAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Dotenv files read for interpolation variables, later files winning. Relative paths resolve like compose file paths."},
			"working_dir": schema.StringAttribute{Optional: true,
				MarkdownDescription: "Project directory that relative paths resolve against. Defaults to the first file's directory, or the Terraform working directory for `content`."},
			"mode": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(composeModeLocal),
				MarkdownDescription: "Deployment target: `local` (default) or `swarm`. `local` creates containers on the provider's daemon; `swarm` creates a stack and needs a Swarm manager. Changing it forces a new resource.",
				Validators:          []validator.String{stringvalidator.OneOf(composeModeLocal, composeModeSwarm)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"wait": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Wait for the project to be ready after create and update (default `true`). Local mode waits for containers to run (and be healthy when they have a healthcheck) or to exit 0; swarm mode waits for services to converge."},
			"wait_timeout": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(composeDefaultWaitTimeout),
				MarkdownDescription: "Maximum time for each wait, as a Go duration (default `2m`). It also bounds `depends_on` conditions in local mode.",
				Validators:          []validator.String{svcDurationStringValidator{}}},
			"keep_volumes": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Keep the project's named volumes on destroy and when they leave the project (default `false`, which removes them)."},
			"spec_hash": schema.StringAttribute{Computed: true,
				MarkdownDescription: "Digest of the parsed project. Refresh replaces it with `drifted: <reasons>` when the live objects no longer match, which makes the next plan show an update."},
			"services": schema.MapAttribute{ElementType: types.ListType{ElemType: types.StringType}, Computed: true,
				MarkdownDescription: "Map of service name to the IDs of its containers (local, in container-number order) or to its Swarm service ID (swarm)."},
			"networks": schema.MapAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Map of network key to the ID of the network the project created. External networks are not listed."},
			"volumes": schema.MapAttribute{ElementType: types.StringType, Computed: true,
				MarkdownDescription: "Map of volume key to the volume name. In swarm mode volumes are created by each node on first use, so the names come from the services' mounts."},
		},
	}
}

func (r *composeResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("content"), path.MatchRoot("files")),
	}
}

func (r *composeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// ---------------------------------------------------------------------------
// Desired state

// composeDesired is a parsed and expanded project.
type composeDesired struct {
	Project *ctypes.Project
	Local   *composeLocalPlan
	Swarm   *composeSwarmPlan
	Hash    string
}

func (d *composeDesired) warnings() []string {
	if d.Local != nil {
		return d.Local.Warnings
	}
	return d.Swarm.Warnings
}

func composeInputsFrom(ctx context.Context, m *composeResourceModel, diags *diag.Diagnostics) composeInputs {
	in := composeInputs{
		Content:     m.Content.ValueString(),
		Files:       stringListFrom(ctx, m.Files, diags),
		Profiles:    stringListFrom(ctx, m.Profiles, diags),
		Env:         stringMapFrom(ctx, m.Env, diags),
		EnvFiles:    stringListFrom(ctx, m.EnvFile, diags),
		WorkingDir:  m.WorkingDir.ValueString(),
		ProjectName: m.ProjectName.ValueString(),
	}
	return in
}

// composeInputsKnown reports whether every input attribute is known.
func composeInputsKnown(m *composeResourceModel) bool {
	for _, v := range []attr.Value{m.Content, m.Files, m.Profiles, m.Env, m.EnvFile, m.WorkingDir, m.Mode} {
		if v.IsUnknown() {
			return false
		}
	}
	return true
}

// composeBuildDesired parses and expands the project m describes. An
// unknown project_name falls back to the file's own name.
func composeBuildDesired(ctx context.Context, m *composeResourceModel) (*composeDesired, error) {
	var diags diag.Diagnostics
	in := composeInputsFrom(ctx, m, &diags)
	if m.ProjectName.IsUnknown() {
		in.ProjectName = ""
	}
	if diags.HasError() {
		return nil, fmt.Errorf("reading attributes: %v", diags)
	}
	p, err := composeLoad(ctx, in)
	if err != nil {
		return nil, err
	}
	d := &composeDesired{Project: p}
	type obj struct{ Kind, Name, Hash string }
	var objs []obj
	mode := m.Mode.ValueString()
	if mode == "" {
		mode = composeModeLocal
	}
	if mode == composeModeSwarm {
		if d.Swarm, err = composeSwarmExpand(p); err != nil {
			return nil, err
		}
		for _, n := range d.Swarm.Networks {
			objs = append(objs, obj{"network", n.Name, n.Hash})
		}
		for _, b := range d.Swarm.Secrets {
			objs = append(objs, obj{"secret", b.Name, b.Hash})
		}
		for _, b := range d.Swarm.Configs {
			objs = append(objs, obj{"config", b.Name, b.Hash})
		}
		for _, s := range d.Swarm.Services {
			objs = append(objs, obj{"service", s.Name, s.Hash})
		}
	} else {
		if d.Local, err = composeLocalExpand(p); err != nil {
			return nil, err
		}
		for _, n := range d.Local.Networks {
			objs = append(objs, obj{"network", n.Name, n.Hash})
		}
		for _, v := range d.Local.Volumes {
			objs = append(objs, obj{"volume", v.Name, v.Hash})
		}
		for _, c := range d.Local.Containers {
			objs = append(objs, obj{"container", c.Name, c.Hash})
		}
	}
	d.Hash = composeHash(struct {
		Mode    string
		Project string
		Objects []obj
	}{mode, p.Name, objs})
	return d, nil
}

// ---------------------------------------------------------------------------
// Plan

func (r *composeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan composeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !composeInputsKnown(&plan) {
		return
	}
	var state *composeResourceModel
	if !req.State.Raw.IsNull() {
		state = &composeResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	d, err := composeBuildDesired(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid compose project", err.Error())
		return
	}
	for _, w := range d.warnings() {
		resp.Diagnostics.AddWarning("Compose attribute ignored", w)
	}
	plan.ProjectName = types.StringValue(d.Project.Name)
	plan.ID = plan.ProjectName
	plan.SpecHash = types.StringValue(d.Hash)
	if state != nil && !state.ProjectName.IsNull() && state.ProjectName.ValueString() != d.Project.Name {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("project_name"))
	}
	if state != nil && state.SpecHash.ValueString() == d.Hash && state.ProjectName.ValueString() == d.Project.Name {
		plan.Services, plan.Networks, plan.Volumes = state.Services, state.Networks, state.Volumes
	} else {
		plan.Services = types.MapUnknown(composeServicesType.ElemType)
		plan.Networks = types.MapUnknown(types.StringType)
		plan.Volumes = types.MapUnknown(types.StringType)
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// ---------------------------------------------------------------------------
// Apply / refresh helpers

func composeTimeout(m *composeResourceModel) (time.Duration, error) {
	return parseDurationOr(m.WaitTimeout.ValueString(), 2*time.Minute)
}

func (r *composeResource) apply(ctx context.Context, m *composeResourceModel, diags *diag.Diagnostics) {
	d, err := composeBuildDesired(ctx, m)
	if err != nil {
		diags.AddError("Invalid compose project", err.Error())
		return
	}
	timeout, err := composeTimeout(m)
	if err != nil {
		diags.AddAttributeError(path.Root("wait_timeout"), "Invalid wait_timeout", err.Error())
		return
	}
	warn := func(summary, detail string) { diags.AddWarning(summary, detail) }
	if d.Swarm != nil {
		err = composeSwarmApply(ctx, r.pd, d.Swarm, m.Wait.ValueBool(), timeout, warn)
	} else {
		err = composeLocalApply(ctx, r.pd, d.Local, composeLocalApplyOpts{Wait: m.Wait.ValueBool(), WaitTimeout: timeout, KeepVolumes: m.KeepVolumes.ValueBool()}, warn)
	}
	if err != nil {
		diags.AddError("Cannot apply compose project", fmt.Sprintf("%s: %v", d.Project.Name, err))
	}
}

// refresh lists the project's objects into m's computed maps. With
// checkDrift it also re-parses the configuration in m and replaces
// spec_hash when the daemon no longer matches. It returns false when the
// project has no objects left.
func (r *composeResource) refresh(ctx context.Context, m *composeResourceModel, checkDrift bool, diags *diag.Diagnostics) bool {
	name := m.ProjectName.ValueString()
	var d *composeDesired
	if checkDrift && (!m.Content.IsNull() || !m.Files.IsNull()) {
		var err error
		if d, err = composeBuildDesired(ctx, m); err != nil {
			diags.AddWarning("Cannot check docker_compose drift", fmt.Sprintf("parsing the project failed, so drift is not checked: %v", err))
			d = nil
		}
	}
	var svcs map[string][]string
	var nets, vols map[string]string
	var drift []string
	var empty bool
	if m.Mode.ValueString() == composeModeSwarm {
		ex, err := composeSwarmList(ctx, r.pd, name)
		if err != nil {
			diags.AddError("Cannot list compose project", err.Error())
			return true
		}
		empty = ex.empty()
		var sp *composeSwarmPlan
		if d != nil {
			sp = d.Swarm
			drift = composeSwarmDrift(sp, ex)
		}
		svcs, nets, vols = composeSwarmIDs(sp, name, ex)
	} else {
		ex, err := composeLocalList(ctx, r.pd, name)
		if err != nil {
			diags.AddError("Cannot list compose project", err.Error())
			return true
		}
		empty = ex.empty()
		if d != nil {
			drift = composeLocalDrift(d.Local, ex)
		}
		svcs, nets, vols = composeLocalIDs(ex)
	}
	if empty && checkDrift {
		return false
	}
	sv := map[string]attr.Value{}
	for k, ids := range svcs {
		sv[k] = stringListValue(ids, false)
	}
	m.Services = types.MapValueMust(composeServicesType.ElemType, sv)
	m.Networks = stringMapValue(nets, false)
	m.Volumes = stringMapValue(vols, false)
	if len(drift) > 0 {
		const maxReasons = 5
		more := ""
		if len(drift) > maxReasons {
			more = fmt.Sprintf(" (and %d more)", len(drift)-maxReasons)
			drift = drift[:maxReasons]
		}
		m.SpecHash = types.StringValue(composeDriftPrefix + strings.Join(drift, "; ") + more)
	} else if d != nil {
		m.SpecHash = types.StringValue(d.Hash)
	}
	return true
}

// ---------------------------------------------------------------------------
// CRUD

func (r *composeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan composeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	r.apply(ctx, &plan, &resp.Diagnostics)
	failed := resp.Diagnostics.HasError()
	var rd diag.Diagnostics
	r.refresh(ctx, &plan, false, &rd)
	if !failed {
		resp.Diagnostics.Append(rd...)
	}
	// Saved even on failure so a retry or destroy cleans up what was made.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *composeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state composeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if !r.refresh(ctx, &state, true, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *composeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan composeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	r.apply(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	r.refresh(ctx, &plan, false, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *composeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state composeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	name := state.ProjectName.ValueString()
	warn := func(summary, detail string) { resp.Diagnostics.AddWarning(summary, detail) }
	var err error
	if state.Mode.ValueString() == composeModeSwarm {
		err = composeSwarmDestroy(ctx, r.pd, name, state.KeepVolumes.ValueBool(), warn)
	} else {
		err = composeLocalDestroy(ctx, r.pd, name, state.KeepVolumes.ValueBool())
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot remove compose project", fmt.Sprintf("%s: %v", name, err))
	}
}

// ImportState takes "<project>" (local) or "<project>:swarm". The
// configuration's content/files are adopted on the next apply: objects
// whose config-hash label matches are kept, others are recreated.
func (r *composeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name, mode, _ := strings.Cut(req.ID, ":")
	if mode == "" {
		mode = composeModeLocal
	}
	if name == "" || (mode != composeModeLocal && mode != composeModeSwarm) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("want \"<project>\" or \"<project>:swarm\", got %q", req.ID))
		return
	}
	var empty bool
	if mode == composeModeSwarm {
		ex, err := composeSwarmList(ctx, r.pd, name)
		if err != nil {
			resp.Diagnostics.AddError("Cannot import compose project", err.Error())
			return
		}
		empty = ex.empty()
	} else {
		ex, err := composeLocalList(ctx, r.pd, name)
		if err != nil {
			resp.Diagnostics.AddError("Cannot import compose project", err.Error())
			return
		}
		empty = ex.empty()
	}
	if empty {
		resp.Diagnostics.AddError("Cannot import compose project", fmt.Sprintf("no objects are labelled for project %q in %s mode", name, mode))
		return
	}
	for attrName, v := range map[string]any{
		"id": name, "project_name": name, "mode": mode,
		"wait": true, "wait_timeout": composeDefaultWaitTimeout, "keep_volumes": false,
	} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attrName), v)...)
	}
}
