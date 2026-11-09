package provider

// Pass 3 Lane C: docker_plugin (managed Engine plugins, `docker plugin
// install`). Install goes through the client's PluginInstall, which fetches
// GET /plugins/privileges first and hands the list to an
// AcceptPermissionsFunc: the provider grants it only when
// grant_all_permissions is set or every requested privilege is covered by a
// grant_permissions block. The plugin is installed disabled, its env is set
// (PluginSet) and it is then enabled with enable_timeout, so the timeout and
// env apply on the first start.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/plugin"
	"github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/jsonmessage"
)

var (
	_ resource.Resource                = (*pluginResource)(nil)
	_ resource.ResourceWithConfigure   = (*pluginResource)(nil)
	_ resource.ResourceWithImportState = (*pluginResource)(nil)
)

// NewPluginResource is the docker_plugin constructor.
func NewPluginResource() resource.Resource { return &pluginResource{} }

type pluginResource struct {
	pd *providerData
}

type pluginResourceModel struct {
	ID                  types.String       `tfsdk:"id"`
	Name                types.String       `tfsdk:"name"`
	PluginReference     types.String       `tfsdk:"plugin_reference"`
	Alias               types.String       `tfsdk:"alias"`
	Enabled             types.Bool         `tfsdk:"enabled"`
	GrantAllPermissions types.Bool         `tfsdk:"grant_all_permissions"`
	GrantPermissions    []pluginGrantModel `tfsdk:"grant_permissions"`
	Env                 types.Set          `tfsdk:"env"`
	EnableTimeout       types.Int64        `tfsdk:"enable_timeout"`
	ForceDestroy        types.Bool         `tfsdk:"force_destroy"`
	ForceDisable        types.Bool         `tfsdk:"force_disable"`
	contractEnginePlugin
}

type pluginGrantModel struct {
	Name  types.String `tfsdk:"name"`
	Value types.Set    `tfsdk:"value"`
}

// pluginDefaultEnableTimeout is enable_timeout's default, in seconds.
const pluginDefaultEnableTimeout = 60

func (r *pluginResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin"
}

func (r *pluginResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed Docker Engine plugin installed from a registry, like `docker plugin install`. " +
			"The privileges the plugin asks for (`GET /plugins/privileges`) must be granted with `grant_all_permissions` or " +
			"`grant_permissions` blocks, otherwise install fails and lists them. `enabled` and `env` change in place (the plugin is " +
			"disabled for `PluginSet` and re-enabled). With `alias` set, a changed `name` upgrades the plugin in place " +
			"(`docker plugin upgrade`: disable, `PluginUpgrade`, re-enable), keeping its local name and settings; without `alias`, a changed " +
			"`name` forces a new resource, since the local name is derived from it. A changed `alias` always forces a new resource. " +
			"Import by plugin ID or name.",
		Attributes: contractAddResourceAttributes(map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Plugin ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Plugin reference to install from a registry, e.g. `vieux/sshfs:latest` or `ghcr.io/org/plugin:1.0` " +
					"(`registry_auth` credentials apply). With `alias` set, a change upgrades the plugin in place (`PluginUpgrade`); " +
					"without `alias`, changing this forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(pluginNameRequiresReplace,
					"Forces replacement unless alias is set.", "Forces replacement unless `alias` is set.")},
			},
			"plugin_reference": schema.StringAttribute{
				Computed: true, MarkdownDescription: "Fully qualified reference the daemon installed from, e.g. `docker.io/vieux/sshfs:latest`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"alias": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Local name of the plugin (`docker plugin install --alias`), used by volume and network drivers. " +
					"Default: the daemon's name for `name`. Changing this forces a new resource.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether the plugin is enabled. Default `true`. Updated in place.",
			},
			"grant_all_permissions": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Grant every privilege the plugin requests (`--grant-all-permissions`). Conflicts with `grant_permissions`.",
			},
			"env": schema.SetAttribute{
				ElementType: types.StringType, Optional: true, Computed: true,
				MarkdownDescription: "Plugin settings as `KEY=VALUE` (`docker plugin set`). Unset reports the plugin's current env; when set only " +
					"the keys you list are reported. Updated in place: the plugin is disabled, set and re-enabled.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"enable_timeout": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(pluginDefaultEnableTimeout),
				MarkdownDescription: fmt.Sprintf("Seconds the daemon waits for the plugin to start when enabling it. Default `%d`.", pluginDefaultEnableTimeout),
			},
			"force_destroy": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Remove the plugin on destroy even while it is enabled or in use (`docker plugin rm -f`). Default `false`.",
			},
			"force_disable": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Disable the plugin even while it is in use (`docker plugin disable -f`), for env updates, upgrades and destroy. Default `false`.",
			},
		}, "engine:plugin"),
		Blocks: map[string]schema.Block{
			"grant_permissions": schema.SetNestedBlock{
				MarkdownDescription: "Privileges to grant, one block per privilege name, e.g. `name = \"network\"`, `value = [\"host\"]`. " +
					"Install fails when the plugin asks for a privilege or value not listed. Conflicts with `grant_all_permissions`. " +
					"Changing it does not reinstall the plugin.",
				Validators: []validator.Set{setvalidator.ConflictsWith(path.MatchRoot("grant_all_permissions"))},
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{Required: true, MarkdownDescription: "Privilege name, e.g. `network`, `mount`, `device`, `capabilities`."},
					"value": schema.SetAttribute{
						ElementType: types.StringType, Required: true,
						MarkdownDescription: "Granted values, e.g. `[\"host\"]` for `network` or `[\"CAP_SYS_ADMIN\"]` for `capabilities`.",
					},
				}},
			},
		},
	}
}

func (r *pluginResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// pluginAcceptFunc returns the privilege check PluginInstall calls with the
// privileges the plugin requests.
func pluginAcceptFunc(all bool, grants []pluginGrantModel) func(context.Context, plugin.Privileges) (bool, error) {
	granted := map[string]map[string]bool{}
	for _, g := range grants {
		n := g.Name.ValueString()
		if granted[n] == nil {
			granted[n] = map[string]bool{}
		}
		for _, v := range containerSetStrings(g.Value) {
			granted[n][v] = true
		}
	}
	return func(_ context.Context, privs plugin.Privileges) (bool, error) {
		if all {
			return true, nil
		}
		var missing []string
		for _, p := range privs {
			for _, v := range p.Value {
				if !granted[p.Name][v] {
					missing = append(missing, fmt.Sprintf("%s=%s", p.Name, v))
				}
			}
			if len(p.Value) == 0 && granted[p.Name] == nil {
				missing = append(missing, p.Name)
			}
		}
		if len(missing) > 0 {
			return false, fmt.Errorf("the plugin requests privileges that are not granted: %s. Add grant_permissions blocks "+
				"for them or set grant_all_permissions = true (requested: %s)", strings.Join(missing, ", "), pluginPrivilegesString(privs))
		}
		return true, nil
	}
}

func pluginPrivilegesString(privs plugin.Privileges) string {
	parts := make([]string, 0, len(privs))
	for _, p := range privs {
		parts = append(parts, fmt.Sprintf("%s=[%s]", p.Name, strings.Join(p.Value, ",")))
	}
	return strings.Join(parts, " ")
}

// pluginEnvKeys returns the KEY part of KEY=VALUE entries.
func pluginEnvKeys(env []string) map[string]bool {
	out := map[string]bool{}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		out[k] = true
	}
	return out
}

// pluginEnvRead reports the daemon's env. With a known prior only keys in
// prior are reported (the daemon lists every settable variable with its
// default).
func pluginEnvRead(prior types.Set, got []string) types.Set {
	if !containerKnown(prior) {
		return stringSetValue(append([]string{}, got...), false)
	}
	keep := pluginEnvKeys(containerSetStrings(prior))
	out := []string{}
	for _, e := range got {
		k, _, _ := strings.Cut(e, "=")
		if keep[k] {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return stringSetValue(out, false)
}

// pluginDrain reads an install/pull progress stream and returns its error.
func pluginDrain(rc io.ReadCloser) error {
	defer rc.Close()
	return jsonmessage.DisplayJSONMessagesStream(rc, io.Discard, 0, false, nil)
}

// pluginFamiliar returns the familiar spelling of a plugin reference with
// the default tag ("docker.io/vieux/sshfs" -> "vieux/sshfs:latest").
func pluginFamiliar(ref string) string {
	n, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ref
	}
	return reference.FamiliarString(reference.TagNameOnly(n))
}

func (r *pluginResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pluginResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	ref := plan.Name.ValueString()
	auth, err := r.pd.registryAuthFor(ref)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid plugin reference", err.Error())
		return
	}
	env := []string{}
	if containerKnown(plan.Env) {
		env = containerSetStrings(plan.Env)
	}
	alias, _ := knownString(plan.Alias)
	opts := client.PluginInstallOptions{
		RemoteRef:             ref,
		RegistryAuth:          auth,
		Disabled:              true, // enabled below with enable_timeout
		AcceptAllPermissions:  plan.GrantAllPermissions.ValueBool(),
		AcceptPermissionsFunc: pluginAcceptFunc(plan.GrantAllPermissions.ValueBool(), plan.GrantPermissions),
		Args:                  env,
	}
	rc, err := r.pd.Client.PluginInstall(ctx, alias, opts)
	if err == nil {
		err = pluginDrain(rc)
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot install plugin", fmt.Sprintf("%s: %v", ref, err))
		return
	}
	lookup := alias
	if lookup == "" {
		lookup = ref
	}
	pRes, err := r.pd.Client.PluginInspect(ctx, lookup, client.PluginInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect installed plugin", fmt.Sprintf("%s: %v", lookup, err))
		return
	}
	p := pRes.Plugin
	plan.ID = types.StringValue(p.ID)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	if plan.Enabled.ValueBool() {
		if _, err := r.pd.Client.PluginEnable(ctx, p.ID, client.PluginEnableOptions{Timeout: int(plan.EnableTimeout.ValueInt64())}); err != nil {
			resp.Diagnostics.AddError("Cannot enable plugin", fmt.Sprintf("%s: %v", ref, err))
			return
		}
	}
	r.readInto(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// readInto refreshes m (and its contract outputs) from PluginInspect. It
// returns false when the plugin is gone.
func (r *pluginResource) readInto(ctx context.Context, m *pluginResourceModel, diags *diag.Diagnostics) bool {
	pRes, err := r.pd.Client.PluginInspect(ctx, m.ID.ValueString(), client.PluginInspectOptions{})
	p := pRes.Plugin
	if isNotFound(err) {
		return false
	}
	if err != nil {
		diags.AddError("Cannot inspect plugin", err.Error())
		return false
	}
	pluginApply(m, &p)
	diags.Append(m.contractFill(ctx, r.pd, contractFillInput{Raw: map[string][]byte{"inspect_json": rEngineBody(pRes.Raw)}})...)
	return true
}

// pluginNameRequiresReplace: a changed name upgrades in place only when the
// local name is pinned by alias.
func pluginNameRequiresReplace(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	var alias types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("alias"), &alias)...)
	resp.RequiresReplace = alias.IsNull()
}

// pluginUpgrade runs PluginUpgrade for a changed name. The plugin must be
// disabled; the caller re-enables it.
func (r *pluginResource) pluginUpgrade(ctx context.Context, id string, plan pluginResourceModel) error {
	ref := plan.Name.ValueString()
	auth, err := r.pd.registryAuthFor(ref)
	if err != nil {
		return err
	}
	rc, err := r.pd.Client.PluginUpgrade(ctx, id, client.PluginUpgradeOptions{
		RemoteRef:             ref,
		RegistryAuth:          auth,
		AcceptAllPermissions:  plan.GrantAllPermissions.ValueBool(),
		AcceptPermissionsFunc: pluginAcceptFunc(plan.GrantAllPermissions.ValueBool(), plan.GrantPermissions),
	})
	if err != nil {
		return err
	}
	return pluginDrain(rc)
}

// pluginApply maps an inspected plugin onto m.
func pluginApply(m *pluginResourceModel, p *plugin.Plugin) {
	m.ID = types.StringValue(p.ID)
	// The daemon stores an alias with the default tag ("sshfs" -> "sshfs:latest").
	if a, ok := knownString(m.Alias); !ok || pluginFamiliar(a) != pluginFamiliar(p.Name) {
		m.Alias = types.StringValue(p.Name)
	}
	m.PluginReference = stringOrNull(p.PluginReference)
	m.Enabled = types.BoolValue(p.Enabled)
	m.Env = pluginEnvRead(m.Env, p.Settings.Env)
	if !containerKnown(m.Name) {
		ref := p.PluginReference
		if ref == "" {
			ref = p.Name
		}
		m.Name = types.StringValue(pluginFamiliar(ref))
	}
	if !containerKnown(m.EnableTimeout) {
		m.EnableTimeout = types.Int64Value(pluginDefaultEnableTimeout)
	}
	if !containerKnown(m.ForceDestroy) {
		m.ForceDestroy = types.BoolValue(false)
	}
	if !containerKnown(m.ForceDisable) {
		m.ForceDisable = types.BoolValue(false)
	}
}

func (r *pluginResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state pluginResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if !r.readInto(ctx, &state, &resp.Diagnostics) {
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *pluginResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state pluginResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	id := state.ID.ValueString()
	plan.ID = state.ID
	enabled := state.Enabled.ValueBool()
	upgrade := plan.Name.ValueString() != state.Name.ValueString() && pluginFamiliar(plan.Name.ValueString()) != pluginFamiliar(state.Name.ValueString())
	envChanged := containerKnown(plan.Env) && !plan.Env.Equal(state.Env)
	if upgrade {
		if enabled {
			if _, err := r.pd.Client.PluginDisable(ctx, id, client.PluginDisableOptions{Force: plan.ForceDisable.ValueBool()}); err != nil {
				resp.Diagnostics.AddError("Cannot disable plugin for upgrade", err.Error())
				return
			}
			enabled = false
		}
		if err := r.pluginUpgrade(ctx, id, plan); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Cannot upgrade plugin",
				fmt.Sprintf("%s -> %s: %v (the plugin is left disabled)", state.Name.ValueString(), plan.Name.ValueString(), err))
			return
		}
	}
	if envChanged {
		if enabled {
			if _, err := r.pd.Client.PluginDisable(ctx, id, client.PluginDisableOptions{Force: plan.ForceDisable.ValueBool()}); err != nil {
				resp.Diagnostics.AddError("Cannot disable plugin for update", err.Error())
				return
			}
			enabled = false
		}
		if _, err := r.pd.Client.PluginSet(ctx, id, client.PluginSetOptions{Args: containerSetStrings(plan.Env)}); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("env"), "Cannot set plugin env", err.Error())
			return
		}
	}
	switch want := plan.Enabled.ValueBool(); {
	case want && !enabled:
		if _, err := r.pd.Client.PluginEnable(ctx, id, client.PluginEnableOptions{Timeout: int(plan.EnableTimeout.ValueInt64())}); err != nil {
			resp.Diagnostics.AddError("Cannot enable plugin", err.Error())
			return
		}
	case !want && enabled:
		if _, err := r.pd.Client.PluginDisable(ctx, id, client.PluginDisableOptions{Force: plan.ForceDisable.ValueBool()}); err != nil {
			resp.Diagnostics.AddError("Cannot disable plugin", err.Error())
			return
		}
	}
	if !r.readInto(ctx, &plan, &resp.Diagnostics) {
		if !resp.Diagnostics.HasError() {
			resp.Diagnostics.AddError("Plugin disappeared", "The plugin was removed during update.")
		}
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *pluginResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pluginResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || r.pd == nil {
		return
	}
	if err := pluginRemove(ctx, r.pd, state); err != nil {
		resp.Diagnostics.AddError("Cannot remove plugin", err.Error())
	}
}

// pluginRemove disables (unless force_destroy) and removes the plugin.
func pluginRemove(ctx context.Context, pd *providerData, m pluginResourceModel) error {
	id := m.ID.ValueString()
	pRes, err := pd.Client.PluginInspect(ctx, id, client.PluginInspectOptions{})
	p := pRes.Plugin
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	force := m.ForceDestroy.ValueBool()
	if p.Enabled && !force {
		if _, err := pd.Client.PluginDisable(ctx, id, client.PluginDisableOptions{Force: m.ForceDisable.ValueBool()}); err != nil && !isNotFound(err) {
			return fmt.Errorf("disabling %s: %w", p.Name, err)
		}
	}
	_, err = pd.Client.PluginRemove(ctx, id, client.PluginRemoveOptions{Force: force})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("removing %s: %w", p.Name, err)
	}
	return nil
}

func (r *pluginResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if r.pd == nil {
		resp.Diagnostics.AddError("Provider not configured", "Cannot import before the provider is configured.")
		return
	}
	pRes, err := r.pd.Client.PluginInspect(ctx, req.ID, client.PluginInspectOptions{})
	if err != nil {
		resp.Diagnostics.AddError("Cannot find plugin", fmt.Sprintf("%q: %v", req.ID, err))
		return
	}
	p := pRes.Plugin
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), p.ID)...)
}
