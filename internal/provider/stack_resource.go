package provider

// docker_stack: a set of services, networks, secrets and configs managed
// together under the com.docker.stack.namespace label, built from the same
// API primitives as the standalone resources (no compose file, no CLI).

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
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
	_ resource.Resource                = (*stackResource)(nil)
	_ resource.ResourceWithConfigure   = (*stackResource)(nil)
	_ resource.ResourceWithImportState = (*stackResource)(nil)
)

// stackRemoveTimeout bounds waits for removed services' tasks to go and for
// in-use networks/secrets/configs to become removable.
const stackRemoveTimeout = 2 * time.Minute

// stackVXLANOption is added by the daemon to overlay networks; it is not
// user configuration.
const stackVXLANOption = "com.docker.network.driver.overlay.vxlanid_list"

// NewStackResource returns the docker_stack resource.
func NewStackResource() resource.Resource { return &stackResource{} }

type stackResource struct {
	pd *providerData
}

type stackResourceModel struct {
	ID       types.String        `tfsdk:"id"`
	Name     types.String        `tfsdk:"name"`
	Services []svcSpecModel      `tfsdk:"service"`
	Networks []stackNetworkModel `tfsdk:"network"`
	Volumes  []stackVolumeModel  `tfsdk:"volume"`
	Secrets  []stackBlobModel    `tfsdk:"secret"`
	Configs  []stackBlobModel    `tfsdk:"config"`
}

type stackNetworkModel struct {
	Name       types.String `tfsdk:"name"`
	Driver     types.String `tfsdk:"driver"`
	Attachable types.Bool   `tfsdk:"attachable"`
	Internal   types.Bool   `tfsdk:"internal"`
	Labels     types.Map    `tfsdk:"labels"`
	Options    types.Map    `tfsdk:"options"`
}

type stackVolumeModel struct {
	Name       types.String `tfsdk:"name"`
	Driver     types.String `tfsdk:"driver"`
	DriverOpts types.Map    `tfsdk:"driver_opts"`
	Labels     types.Map    `tfsdk:"labels"`
}

// stackBlobModel is a stack secret or config.
type stackBlobModel struct {
	Name   types.String `tfsdk:"name"`
	Data   types.String `tfsdk:"data"`
	Labels types.Map    `tfsdk:"labels"`
}

func (r *stackResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stack"
}

func stackBlobBlock(kind string, sensitive bool) schema.ListNestedBlock {
	return schema.ListNestedBlock{
		MarkdownDescription: "A Swarm " + kind + " created as `<stack>_<name>`. Services reference it by its short `name`. " +
			kind + "s are immutable: a change removes the services using it, replaces the " + kind + " and recreates those services.",
		NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Short name inside the stack."},
			"data": schema.StringAttribute{Required: true, Sensitive: sensitive,
				MarkdownDescription: "Content, base64-encoded (use `base64encode()`). Kept from state on refresh.",
				Validators:          []validator.String{secretBase64Validator{}}},
			"labels": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "User labels."},
		}},
	}
}

func (r *stackResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	svcAttrs, svcBlocks := svcSchema(false)
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Swarm stack built from API primitives (not a compose file, no CLI): services, overlay networks, " +
			"secrets and configs, each named `<stack>_<name>` and labelled `com.docker.stack.namespace=<stack>` so `docker stack ls/ps` " +
			"show them. Requires the provider to point at a Swarm manager. Apply creates networks, secrets and configs first, then " +
			"services, whose `networks`/`secrets`/`configs` references may use the short names of the stack's own objects. Updates " +
			"diff the stack's labelled objects against the configuration: missing objects are created, services are updated in place " +
			"(recreated when `mode` switches type), changed secrets/configs/networks are replaced after removing the services that " +
			"use them, and objects no longer configured are removed. Destroy removes services first (waiting for their tasks to go), " +
			"then secrets, configs and networks.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "The stack name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true,
				MarkdownDescription: "Stack namespace. Changing this forces a new resource.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		},
		Blocks: map[string]schema.Block{
			"service": schema.ListNestedBlock{
				MarkdownDescription: "A service of the stack, created as `<stack>_<name>` with the same settings as `docker_service`. " +
					"Tasks get a network alias of the short name on every network.",
				NestedObject: schema.NestedBlockObject{Attributes: svcAttrs, Blocks: svcBlocks},
			},
			"network": schema.ListNestedBlock{
				MarkdownDescription: "A network created as `<stack>_<name>` (swarm scope). A change replaces it after removing the services attached to it.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{Required: true, MarkdownDescription: "Short name inside the stack."},
					"driver": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("overlay"),
						MarkdownDescription: "Network driver (default `overlay`)."},
					"attachable": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
						MarkdownDescription: "Let standalone containers attach."},
					"internal": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
						MarkdownDescription: "No external connectivity."},
					"labels":  schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "User labels."},
					"options": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Driver options."},
				}},
			},
			"volume": schema.ListNestedBlock{
				MarkdownDescription: "A named volume `<stack>_<name>`. Swarm cannot pre-create volumes across nodes, so nothing is " +
					"created up front: every service mount of `type = \"volume\"` whose `source` is this short name is rewritten to " +
					"`<stack>_<name>` with this driver, options and labels, and each node's daemon creates the volume when a task first " +
					"uses it. Volumes are not removed on destroy (like `docker stack rm`).",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Short name inside the stack."},
					"driver":      schema.StringAttribute{Optional: true, MarkdownDescription: "Volume driver (daemon default `local`)."},
					"driver_opts": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Driver options."},
					"labels":      schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Volume labels."},
				}},
			},
			"secret": stackBlobBlock("secret", true),
			"config": stackBlobBlock("config", false),
		},
	}
}

func (r *stackResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.pd = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// ---------------------------------------------------------------------------
// Pure planning helpers

// stackObjPlan says what to do with one kind of stack object (short names).
type stackObjPlan struct {
	Create, Replace, Remove, Keep []string
}

// stackPlanObjects diffs desired objects (short name -> fingerprint) against
// what exists on the daemon. prior holds the fingerprints recorded in state;
// an existing object whose prior fingerprint is missing or differs is
// replaced (secret data cannot be read back, so "unknown" means "replace").
func stackPlanObjects(desired, prior map[string]string, existing map[string]bool) stackObjPlan {
	var p stackObjPlan
	for name, fp := range desired {
		switch {
		case !existing[name]:
			p.Create = append(p.Create, name)
		case prior[name] == "" || prior[name] != fp:
			p.Replace = append(p.Replace, name)
		default:
			p.Keep = append(p.Keep, name)
		}
	}
	for name := range existing {
		if _, ok := desired[name]; !ok {
			p.Remove = append(p.Remove, name)
		}
	}
	sort.Strings(p.Create)
	sort.Strings(p.Replace)
	sort.Strings(p.Remove)
	sort.Strings(p.Keep)
	return p
}

// stackServiceRefs returns the network, secret and config IDs a spec uses.
func stackServiceRefs(spec swarm.ServiceSpec) map[string]bool {
	out := map[string]bool{}
	for _, n := range spec.TaskTemplate.Networks {
		out[n.Target] = true
	}
	for _, n := range spec.Networks { //nolint:staticcheck // deprecated field still read for old services
		out[n.Target] = true
	}
	if cs := spec.TaskTemplate.ContainerSpec; cs != nil {
		for _, s := range cs.Secrets {
			out[s.SecretID] = true
		}
		for _, c := range cs.Configs {
			out[c.ConfigID] = true
		}
	}
	return out
}

// stackSvcPlan says what to do with the stack's services (short names).
type stackSvcPlan struct {
	// Remove are removed first: orphans, and services recreated because
	// they use a replaced object or switch mode type.
	Remove []string
	// Create are created after objects exist (new and recreated services).
	Create []string
	// Update are updated in place.
	Update []string
}

// stackPlanServices plans services. desiredMode maps each desired short
// name to its mode attribute value; existing maps short names to their specs;
// replaced holds the IDs of objects about to be replaced.
func stackPlanServices(desiredMode map[string]string, existing map[string]swarm.ServiceSpec, replaced map[string]bool) stackSvcPlan {
	var p stackSvcPlan
	for name, spec := range existing {
		mode, want := desiredMode[name]
		if !want {
			p.Remove = append(p.Remove, name)
			continue
		}
		if mode == "" {
			mode = "replicated"
		}
		// The Engine rejects any change of mode type in ServiceUpdate.
		recreate := svcModeOf(spec) != mode
		for id := range stackServiceRefs(spec) {
			if replaced[id] {
				recreate = true
			}
		}
		if recreate {
			p.Remove = append(p.Remove, name)
			p.Create = append(p.Create, name)
		} else {
			p.Update = append(p.Update, name)
		}
	}
	for name := range desiredMode {
		if _, ok := existing[name]; !ok {
			p.Create = append(p.Create, name)
		}
	}
	sort.Strings(p.Remove)
	sort.Strings(p.Create)
	sort.Strings(p.Update)
	return p
}

// stackVolumeDef is a stack volume block as plain Go values.
type stackVolumeDef struct {
	Driver     string
	DriverOpts map[string]string
	Labels     map[string]string
}

// stackRewriteMounts points volume mounts whose source is a stack volume at
// "<stack>_<name>" with that volume's driver config and labels.
func stackRewriteMounts(spec *swarm.ServiceSpec, stack string, vols map[string]stackVolumeDef) {
	cs := spec.TaskTemplate.ContainerSpec
	if cs == nil {
		return
	}
	for i := range cs.Mounts {
		m := &cs.Mounts[i]
		v, ok := vols[m.Source]
		if m.Type != mount.TypeVolume || !ok {
			continue
		}
		m.Source = stack + "_" + m.Source
		if m.VolumeOptions == nil {
			m.VolumeOptions = &mount.VolumeOptions{}
		}
		m.VolumeOptions.Labels = mergeLabels(m.VolumeOptions.Labels, v.Labels, map[string]string{labelStackNamespace: stack})
		if v.Driver != "" || len(v.DriverOpts) > 0 {
			m.VolumeOptions.DriverConfig = &mount.Driver{Name: v.Driver, Options: v.DriverOpts}
		}
	}
}

// stackUnrewriteMounts reverses stackRewriteMounts on a spec read back from
// the daemon, so flattening compares against the user's short names.
func stackUnrewriteMounts(spec *swarm.ServiceSpec, stack string, vols map[string]stackVolumeDef) {
	cs := spec.TaskTemplate.ContainerSpec
	if cs == nil {
		return
	}
	mounts := make([]mount.Mount, len(cs.Mounts))
	copy(mounts, cs.Mounts)
	for i := range mounts {
		m := &mounts[i]
		short, ok := strings.CutPrefix(m.Source, stack+"_")
		if m.Type != mount.TypeVolume || !ok {
			continue
		}
		if _, ok := vols[short]; !ok {
			continue
		}
		m.Source = short
		m.VolumeOptions = nil
	}
	cs2 := *cs
	cs2.Mounts = mounts
	spec.TaskTemplate.ContainerSpec = &cs2
}

func stackFingerprint(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		switch v := p.(type) {
		case map[string]string:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "%q=%q,", k, v[k])
			}
		default:
			fmt.Fprintf(&b, "%v", v)
		}
		b.WriteString("|")
	}
	return b.String()
}

func stackNetworkFingerprints(ctx context.Context, nets []stackNetworkModel, diags *diag.Diagnostics) map[string]string {
	out := map[string]string{}
	for _, n := range nets {
		out[n.Name.ValueString()] = stackFingerprint(svcOr(n.Driver.ValueString(), "overlay"), n.Attachable.ValueBool(), n.Internal.ValueBool(),
			stringMapFrom(ctx, n.Labels, diags), stringMapFrom(ctx, n.Options, diags))
	}
	return out
}

func stackBlobFingerprints(ctx context.Context, blobs []stackBlobModel, diags *diag.Diagnostics) map[string]string {
	out := map[string]string{}
	for _, b := range blobs {
		if b.Data.IsNull() || b.Data.IsUnknown() {
			continue // unknown content: treated as changed
		}
		out[b.Name.ValueString()] = stackFingerprint(b.Data.ValueString(), stringMapFrom(ctx, b.Labels, diags))
	}
	return out
}

func stackVolumeDefs(ctx context.Context, vols []stackVolumeModel, diags *diag.Diagnostics) map[string]stackVolumeDef {
	out := map[string]stackVolumeDef{}
	for _, v := range vols {
		out[v.Name.ValueString()] = stackVolumeDef{
			Driver:     v.Driver.ValueString(),
			DriverOpts: stringMapFrom(ctx, v.DriverOpts, diags),
			Labels:     stringMapFrom(ctx, v.Labels, diags),
		}
	}
	return out
}

// stackDuplicates reports names used twice within one block type.
func stackDuplicates(kind string, names []string, diags *diag.Diagnostics) {
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			diags.AddError("Duplicate stack "+kind, fmt.Sprintf("%s %q is declared more than once", kind, n))
		}
		seen[n] = true
	}
}

// ---------------------------------------------------------------------------
// Daemon state

// stackExisting is everything on the daemon labelled with the namespace,
// keyed by short name.
type stackExisting struct {
	services map[string]swarm.Service
	networks map[string]network.Summary
	secrets  map[string]swarm.Secret
	configs  map[string]swarm.Config
}

func (e stackExisting) empty() bool {
	return len(e.services)+len(e.networks)+len(e.secrets)+len(e.configs) == 0
}

func stackShortName(stack, name string) string {
	if s, ok := strings.CutPrefix(name, stack+"_"); ok {
		return s
	}
	return name
}

func stackListExisting(ctx context.Context, c *client.Client, stack string) (stackExisting, error) {
	f := labelFilter(map[string]string{labelStackNamespace: stack})
	ex := stackExisting{
		services: map[string]swarm.Service{},
		networks: map[string]network.Summary{},
		secrets:  map[string]swarm.Secret{},
		configs:  map[string]swarm.Config{},
	}
	svcs, err := c.ServiceList(ctx, swarm.ServiceListOptions{Filters: f})
	if err != nil {
		return ex, fmt.Errorf("listing services: %w", err)
	}
	for _, s := range svcs {
		ex.services[stackShortName(stack, s.Spec.Name)] = s
	}
	nets, err := c.NetworkList(ctx, network.ListOptions{Filters: f})
	if err != nil {
		return ex, fmt.Errorf("listing networks: %w", err)
	}
	for _, n := range nets {
		ex.networks[stackShortName(stack, n.Name)] = n
	}
	secs, err := c.SecretList(ctx, swarm.SecretListOptions{Filters: f})
	if err != nil {
		return ex, fmt.Errorf("listing secrets: %w", err)
	}
	for _, s := range secs {
		ex.secrets[stackShortName(stack, s.Spec.Name)] = s
	}
	cfgs, err := c.ConfigList(ctx, swarm.ConfigListOptions{Filters: f})
	if err != nil {
		return ex, fmt.Errorf("listing configs: %w", err)
	}
	for _, s := range cfgs {
		ex.configs[stackShortName(stack, s.Spec.Name)] = s
	}
	return ex, nil
}

// stackRemoveRetry runs remove until it succeeds or reports not found,
// retrying while the object is still in use by tasks shutting down.
func stackRemoveRetry(ctx context.Context, what string, remove func(context.Context) error) error {
	var last error
	err := waitFor(ctx, stackRemoveTimeout, 2*time.Second, func(ctx context.Context) (bool, error) {
		err := remove(ctx)
		if err == nil || isNotFound(err) {
			return true, nil
		}
		last = err
		return false, nil
	})
	if err != nil && last != nil {
		return fmt.Errorf("removing %s: %w (last error: %v)", what, err, last)
	}
	return err
}

// stackRemoveServices removes services and waits until all their tasks are gone.
func stackRemoveServices(ctx context.Context, c *client.Client, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if err := c.ServiceRemove(ctx, id); err != nil && !isNotFound(err) {
			return fmt.Errorf("removing service %s: %w", id, err)
		}
	}
	return waitFor(ctx, stackRemoveTimeout, 2*time.Second, func(ctx context.Context) (bool, error) {
		tasks, err := c.TaskList(ctx, swarm.TaskListOptions{})
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if !svcTasksGone(tasks, id) {
				return false, nil
			}
		}
		return true, nil
	})
}

// ---------------------------------------------------------------------------
// Apply

func (r *stackResource) apply(ctx context.Context, plan, prior *stackResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	c := r.pd.Client
	stack := plan.Name.ValueString()
	prefix := stack + "_"
	if prior == nil {
		prior = &stackResourceModel{}
	}

	var names []string
	desiredMode := map[string]string{}
	for _, s := range plan.Services {
		names = append(names, s.Name.ValueString())
		desiredMode[s.Name.ValueString()] = s.Mode.ValueString()
	}
	stackDuplicates("service", names, &diags)
	for kind, list := range map[string][]string{"network": stackNames(plan.Networks), "secret": stackBlobNames(plan.Secrets), "config": stackBlobNames(plan.Configs), "volume": stackVolumeNames(plan.Volumes)} {
		stackDuplicates(kind, list, &diags)
	}
	if diags.HasError() {
		return diags
	}

	ex, err := stackListExisting(ctx, c, stack)
	if err != nil {
		diags.AddError("Cannot list stack objects", err.Error())
		return diags
	}
	existsN, existsS, existsC := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for k := range ex.networks {
		existsN[k] = true
	}
	for k := range ex.secrets {
		existsS[k] = true
	}
	for k := range ex.configs {
		existsC[k] = true
	}
	netPlan := stackPlanObjects(stackNetworkFingerprints(ctx, plan.Networks, &diags), stackNetworkFingerprints(ctx, prior.Networks, &diags), existsN)
	secPlan := stackPlanObjects(stackBlobFingerprints(ctx, plan.Secrets, &diags), stackBlobFingerprints(ctx, prior.Secrets, &diags), existsS)
	cfgPlan := stackPlanObjects(stackBlobFingerprints(ctx, plan.Configs, &diags), stackBlobFingerprints(ctx, prior.Configs, &diags), existsC)
	// Desired blobs with unknown data never get a fingerprint; plan them
	// as create/replace explicitly.
	stackAddUnfingerprinted(&secPlan, stackBlobNames(plan.Secrets), existsS)
	stackAddUnfingerprinted(&cfgPlan, stackBlobNames(plan.Configs), existsC)

	replaced := map[string]bool{}
	for _, n := range netPlan.Replace {
		replaced[ex.networks[n].ID] = true
	}
	for _, n := range secPlan.Replace {
		replaced[ex.secrets[n].ID] = true
	}
	for _, n := range cfgPlan.Replace {
		replaced[ex.configs[n].ID] = true
	}
	exSpecs := map[string]swarm.ServiceSpec{}
	for k, s := range ex.services {
		exSpecs[k] = s.Spec
	}
	svcPlan := stackPlanServices(desiredMode, exSpecs, replaced)

	// 1. Remove orphan and to-be-recreated services.
	var rmIDs []string
	for _, n := range svcPlan.Remove {
		rmIDs = append(rmIDs, ex.services[n].ID)
	}
	if err := stackRemoveServices(ctx, c, rmIDs); err != nil {
		diags.AddError("Cannot remove stack services", err.Error())
		return diags
	}

	// 2. Remove replaced and orphan configs, secrets, networks.
	for _, n := range append(append([]string{}, cfgPlan.Replace...), cfgPlan.Remove...) {
		id := ex.configs[n].ID
		if err := stackRemoveRetry(ctx, "config "+prefix+n, func(ctx context.Context) error { return c.ConfigRemove(ctx, id) }); err != nil {
			diags.AddError("Cannot remove stack config", err.Error())
			return diags
		}
	}
	for _, n := range append(append([]string{}, secPlan.Replace...), secPlan.Remove...) {
		id := ex.secrets[n].ID
		if err := stackRemoveRetry(ctx, "secret "+prefix+n, func(ctx context.Context) error { return c.SecretRemove(ctx, id) }); err != nil {
			diags.AddError("Cannot remove stack secret", err.Error())
			return diags
		}
	}
	for _, n := range append(append([]string{}, netPlan.Replace...), netPlan.Remove...) {
		id := ex.networks[n].ID
		if err := stackRemoveRetry(ctx, "network "+prefix+n, func(ctx context.Context) error { return c.NetworkRemove(ctx, id) }); err != nil {
			diags.AddError("Cannot remove stack network", err.Error())
			return diags
		}
	}

	// 3. Create networks, secrets, configs.
	nsLabels := map[string]string{labelManagedBy: labelManagedByValue, labelStackNamespace: stack}
	toCreate := func(p stackObjPlan) map[string]bool {
		m := map[string]bool{}
		for _, n := range append(append([]string{}, p.Create...), p.Replace...) {
			m[n] = true
		}
		return m
	}
	createN, createS, createC := toCreate(netPlan), toCreate(secPlan), toCreate(cfgPlan)
	for _, n := range plan.Networks {
		if !createN[n.Name.ValueString()] {
			continue
		}
		_, err := c.NetworkCreate(ctx, prefix+n.Name.ValueString(), network.CreateOptions{
			Driver:     svcOr(n.Driver.ValueString(), "overlay"),
			Scope:      "swarm",
			Attachable: n.Attachable.ValueBool(),
			Internal:   n.Internal.ValueBool(),
			Options:    stringMapFrom(ctx, n.Options, &diags),
			Labels:     mergeLabels(stringMapFrom(ctx, n.Labels, &diags), nsLabels),
		})
		if err != nil {
			diags.AddError("Cannot create stack network", fmt.Sprintf("%s%s: %v", prefix, n.Name.ValueString(), err))
			return diags
		}
	}
	for _, s := range plan.Secrets {
		if !createS[s.Name.ValueString()] {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(s.Data.ValueString())
		if err != nil {
			diags.AddError("Invalid stack secret data", fmt.Sprintf("%s: %v", s.Name.ValueString(), err))
			return diags
		}
		_, err = c.SecretCreate(ctx, swarm.SecretSpec{
			Annotations: swarm.Annotations{Name: prefix + s.Name.ValueString(), Labels: mergeLabels(stringMapFrom(ctx, s.Labels, &diags), nsLabels)},
			Data:        data,
		})
		if err != nil {
			diags.AddError("Cannot create stack secret", fmt.Sprintf("%s%s: %v", prefix, s.Name.ValueString(), err))
			return diags
		}
	}
	for _, s := range plan.Configs {
		if !createC[s.Name.ValueString()] {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(s.Data.ValueString())
		if err != nil {
			diags.AddError("Invalid stack config data", fmt.Sprintf("%s: %v", s.Name.ValueString(), err))
			return diags
		}
		_, err = c.ConfigCreate(ctx, swarm.ConfigSpec{
			Annotations: swarm.Annotations{Name: prefix + s.Name.ValueString(), Labels: mergeLabels(stringMapFrom(ctx, s.Labels, &diags), nsLabels)},
			Data:        data,
		})
		if err != nil {
			diags.AddError("Cannot create stack config", fmt.Sprintf("%s%s: %v", prefix, s.Name.ValueString(), err))
			return diags
		}
	}

	// 4. Services: create new/recreated, update changed ones in place.
	lk, err := svcLoadLookup(ctx, c, prefix)
	if err != nil {
		diags.AddError("Cannot resolve stack references", err.Error())
		return diags
	}
	vols := stackVolumeDefs(ctx, plan.Volumes, &diags)
	volsChanged := !reflect.DeepEqual(plan.Volumes, prior.Volumes)
	priorSvc := map[string]svcSpecModel{}
	for _, s := range prior.Services {
		priorSvc[s.Name.ValueString()] = s
	}
	create := map[string]bool{}
	for _, n := range svcPlan.Create {
		create[n] = true
	}
	type started struct {
		id  string
		cfg *svcConvergeModel
	}
	var touched []started
	for i := range plan.Services {
		s := &plan.Services[i]
		short := s.Name.ValueString()
		existing, exists := ex.services[short]
		if exists && !create[short] && !volsChanged && reflect.DeepEqual(*s, priorSvc[short]) && len(replaced) == 0 {
			continue // unchanged
		}
		spec, d := svcExpand(ctx, s, lk, svcExpandOpts{FullName: prefix + short, Namespace: stack})
		diags.Append(d...)
		if diags.HasError() {
			return diags
		}
		stackRewriteMounts(&spec, stack, vols)
		if exists && !create[short] {
			if err := svcUpdate(ctx, r.pd, existing.ID, spec); err != nil {
				diags.AddError("Cannot update stack service", fmt.Sprintf("%s%s: %v", prefix, short, err))
				return diags
			}
			touched = append(touched, started{existing.ID, s.ConvergeConfig})
			continue
		}
		auth, err := r.pd.registryAuthFor(spec.TaskTemplate.ContainerSpec.Image)
		if err != nil {
			diags.AddError("Cannot resolve registry auth", fmt.Sprintf("%s%s: %v", prefix, short, err))
			return diags
		}
		out, err := c.ServiceCreate(ctx, spec, swarm.ServiceCreateOptions{EncodedRegistryAuth: auth, QueryRegistry: true})
		if err != nil {
			diags.AddError("Cannot create stack service", fmt.Sprintf("%s%s: %v", prefix, short, err))
			return diags
		}
		for _, w := range out.Warnings {
			diags.AddWarning("Stack service create warning", w)
		}
		touched = append(touched, started{out.ID, s.ConvergeConfig})
	}
	for _, t := range touched {
		if err := svcWaitConverged(ctx, c, t.id, t.cfg); err != nil {
			diags.AddError("Stack service did not converge", err.Error())
			return diags
		}
	}
	return diags
}

// stackAddUnfingerprinted plans desired blobs whose data is unknown.
func stackAddUnfingerprinted(p *stackObjPlan, desired []string, existing map[string]bool) {
	planned := map[string]bool{}
	for _, l := range [][]string{p.Create, p.Replace, p.Keep} {
		for _, n := range l {
			planned[n] = true
		}
	}
	for _, n := range desired {
		if planned[n] {
			continue
		}
		if existing[n] {
			p.Replace = append(p.Replace, n)
		} else {
			p.Create = append(p.Create, n)
		}
	}
	// Remove may have listed these as orphans (no fingerprint): drop them.
	var rm []string
	for _, n := range p.Remove {
		keep := true
		for _, d := range desired {
			if d == n {
				keep = false
			}
		}
		if keep {
			rm = append(rm, n)
		}
	}
	p.Remove = rm
}

func stackNames(nets []stackNetworkModel) []string {
	out := make([]string, 0, len(nets))
	for _, n := range nets {
		out = append(out, n.Name.ValueString())
	}
	return out
}

func stackBlobNames(b []stackBlobModel) []string {
	out := make([]string, 0, len(b))
	for _, n := range b {
		out = append(out, n.Name.ValueString())
	}
	return out
}

func stackVolumeNames(v []stackVolumeModel) []string {
	out := make([]string, 0, len(v))
	for _, n := range v {
		out = append(out, n.Name.ValueString())
	}
	return out
}

// ---------------------------------------------------------------------------
// Refresh

// refresh reflects the daemon's stack objects into m (m doubles as the
// prior). It returns false when nothing of a non-empty stack is left.
func (r *stackResource) refresh(ctx context.Context, m *stackResourceModel, diags *diag.Diagnostics) bool {
	c := r.pd.Client
	stack := m.Name.ValueString()
	ex, err := stackListExisting(ctx, c, stack)
	if err != nil {
		diags.AddError("Cannot list stack objects", err.Error())
		return true
	}
	hadObjects := len(m.Services)+len(m.Networks)+len(m.Secrets)+len(m.Configs) > 0
	if ex.empty() && hadObjects {
		return false
	}
	lk, err := svcLoadLookup(ctx, c, stack+"_")
	if err != nil {
		diags.AddError("Cannot resolve stack references", err.Error())
		return true
	}
	vols := stackVolumeDefs(ctx, m.Volumes, diags)
	m.ID = types.StringValue(stack)

	// Services
	var svcs []svcSpecModel
	seen := map[string]bool{}
	flat := func(short string, prior svcSpecModel) svcSpecModel {
		spec := ex.services[short].Spec
		stackUnrewriteMounts(&spec, stack, vols)
		return svcFlatten(spec, prior, lk, svcFlattenOpts{ShortName: short, StripStack: true})
	}
	for _, p := range m.Services {
		short := p.Name.ValueString()
		if _, ok := ex.services[short]; ok && !seen[short] {
			svcs = append(svcs, flat(short, p))
			seen[short] = true
		}
	}
	for _, short := range stackSortedKeys(ex.services) {
		if !seen[short] {
			svcs = append(svcs, flat(short, svcSpecModel{}))
		}
	}
	m.Services = svcs

	// Networks
	var nets []stackNetworkModel
	seen = map[string]bool{}
	flatNet := func(short string, p stackNetworkModel) stackNetworkModel {
		n := ex.networks[short]
		opts := map[string]string{}
		for k, v := range n.Options {
			if k != stackVXLANOption {
				opts[k] = v
			}
		}
		return stackNetworkModel{
			Name:       types.StringValue(short),
			Driver:     types.StringValue(n.Driver),
			Attachable: types.BoolValue(n.Attachable),
			Internal:   types.BoolValue(n.Internal),
			Labels:     svcMapValue(p.Labels, userLabels(n.Labels, true)),
			Options:    svcMapValue(p.Options, opts),
		}
	}
	for _, p := range m.Networks {
		short := p.Name.ValueString()
		if _, ok := ex.networks[short]; ok && !seen[short] {
			nets = append(nets, flatNet(short, p))
			seen[short] = true
		}
	}
	for _, short := range stackSortedKeys(ex.networks) {
		if !seen[short] {
			nets = append(nets, flatNet(short, stackNetworkModel{}))
		}
	}
	m.Networks = nets

	// Secrets and configs: data comes from state (secrets cannot be read).
	secLabels := map[string]map[string]string{}
	for k, s := range ex.secrets {
		secLabels[k] = s.Spec.Labels
	}
	m.Secrets = stackFlattenBlobs(m.Secrets, secLabels, nil)
	cfgLabels := map[string]map[string]string{}
	cfgData := map[string]string{}
	for k, s := range ex.configs {
		cfgLabels[k] = s.Spec.Labels
		cfgData[k] = base64.StdEncoding.EncodeToString(s.Spec.Data)
	}
	m.Configs = stackFlattenBlobs(m.Configs, cfgLabels, cfgData)
	return true
}

// stackFlattenBlobs keeps prior blobs that still exist (in prior order) and
// appends new ones. data supplies content for blobs missing from prior
// (configs on import); secrets get null data.
func stackFlattenBlobs(prior []stackBlobModel, labels map[string]map[string]string, data map[string]string) []stackBlobModel {
	var out []stackBlobModel
	seen := map[string]bool{}
	for _, p := range prior {
		short := p.Name.ValueString()
		l, ok := labels[short]
		if !ok || seen[short] {
			continue
		}
		seen[short] = true
		out = append(out, stackBlobModel{Name: p.Name, Data: p.Data, Labels: svcMapValue(p.Labels, userLabels(l, true))})
	}
	for _, short := range stackSortedKeys(labels) {
		if seen[short] {
			continue
		}
		d := types.StringNull()
		if v, ok := data[short]; ok {
			d = types.StringValue(v)
		}
		out = append(out, stackBlobModel{Name: types.StringValue(short), Data: d, Labels: svcMapValue(types.MapNull(types.StringType), userLabels(labels[short], true))})
	}
	return out
}

func stackSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// CRUD

func (r *stackResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan stackResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = plan.Name
	resp.Diagnostics.Append(r.apply(ctx, &plan, nil)...)
	if resp.Diagnostics.HasError() {
		// Record the stack so a retry or destroy can clean up what was made.
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	r.refresh(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *stackResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state stackResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.refresh(ctx, &state, &resp.Diagnostics) {
		resp.State.RemoveResource(ctx)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *stackResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state stackResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = plan.Name
	resp.Diagnostics.Append(r.apply(ctx, &plan, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.refresh(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *stackResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state stackResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(stackDestroy(ctx, r.pd.Client, state.Name.ValueString())...)
}

// stackDestroy removes every object labelled with the stack namespace:
// services first (waiting for their tasks), then configs, secrets, networks.
func stackDestroy(ctx context.Context, c *client.Client, stack string) diag.Diagnostics {
	var diags diag.Diagnostics
	ex, err := stackListExisting(ctx, c, stack)
	if err != nil {
		diags.AddError("Cannot list stack objects", err.Error())
		return diags
	}
	var ids []string
	for _, s := range ex.services {
		ids = append(ids, s.ID)
	}
	if err := stackRemoveServices(ctx, c, ids); err != nil {
		diags.AddError("Cannot remove stack services", err.Error())
		return diags
	}
	for _, s := range ex.configs {
		id := s.ID
		if err := stackRemoveRetry(ctx, "config "+s.Spec.Name, func(ctx context.Context) error { return c.ConfigRemove(ctx, id) }); err != nil {
			diags.AddError("Cannot remove stack config", err.Error())
		}
	}
	for _, s := range ex.secrets {
		id := s.ID
		if err := stackRemoveRetry(ctx, "secret "+s.Spec.Name, func(ctx context.Context) error { return c.SecretRemove(ctx, id) }); err != nil {
			diags.AddError("Cannot remove stack secret", err.Error())
		}
	}
	for _, n := range ex.networks {
		id := n.ID
		if err := stackRemoveRetry(ctx, "network "+n.Name, func(ctx context.Context) error { return c.NetworkRemove(ctx, id) }); err != nil {
			diags.AddError("Cannot remove stack network", err.Error())
		}
	}
	return diags
}

func (r *stackResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ex, err := stackListExisting(ctx, r.pd.Client, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot import stack", err.Error())
		return
	}
	if ex.empty() {
		resp.Diagnostics.AddError("Cannot import stack", fmt.Sprintf("no services, networks, secrets or configs are labelled %s=%s", labelStackNamespace, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}
