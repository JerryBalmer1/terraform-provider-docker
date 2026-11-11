package provider

// docker_compose_scale (compose:cli-scale, Q16), docker_compose_run
// (compose:cli-run, Q18/Q43) and docker_compose_exec (compose:cli-exec,
// Q43). Scale and run load the compose model (the project source
// attributes, like docker_compose); exec only needs the project name.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	aschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// composeActionDefaultTimeout bounds run / exec / wait when timeout is unset.
const composeActionDefaultTimeout = 10 * time.Minute

func composeTimeoutAttr(what string) aschema.StringAttribute {
	return aschema.StringAttribute{Optional: true,
		MarkdownDescription: "How long " + what + " may take, as a Go duration (`30s`, `10m`). Default `10m`."}
}

// composeActionSource adds the project source attributes to attrs.
func composeActionSource(attrs map[string]aschema.Attribute) map[string]aschema.Attribute {
	for k, v := range composeSourceActionAttributes() {
		attrs[k] = v
	}
	return attrs
}

// composeValidateSource checks exactly one of content / files.
func composeValidateSource(m composeSourceModel, diags *diag.Diagnostics) {
	if m.Content.IsUnknown() || m.Files.IsUnknown() {
		return
	}
	if m.Content.IsNull() == m.Files.IsNull() {
		diags.AddError("Exactly one of content and files", "Set content (compose YAML) or files (compose file paths), not both and not neither.")
	}
}

// composeSvcContainers returns the plan containers of one service.
func composeSvcContainers(plan *composeLocalPlan, svc string) []composeLocalContainer {
	var out []composeLocalContainer
	for _, c := range plan.Containers {
		if c.Service == svc {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// docker_compose_scale

// NewComposeScaleAction returns docker_compose_scale.
func NewComposeScaleAction() action.Action { return &composeScaleAction{} }

type composeScaleAction struct{ actBase }

type composeScaleModel struct {
	composeSourceModel
	Replicas   types.Map    `tfsdk:"replicas"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *composeScaleAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_scale"
}

func (a *composeScaleAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Sets the number of containers of compose services on the provider's daemon, like `docker compose scale " +
			"web=3`: missing `<project>-<service>-<n>` containers are created from the compose model (pulled or built as needed) and " +
			"the highest-numbered extra ones are removed; kept containers that are stopped are started. Local mode only; Swarm services scale with `docker_service_scale`. A later " +
			"`docker_compose` apply brings the count back to the configuration.",
		Attributes: composeActionSource(map[string]aschema.Attribute{
			"replicas": aschema.MapAttribute{ElementType: types.Int64Type, Required: true,
				MarkdownDescription: "Map of service name to the number of containers it should have (`0` removes them all)."},
			"result_path": actResultPathAttribute(),
		}),
	}
}

func (a *composeScaleAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m composeScaleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() {
		composeValidateSource(m.composeSourceModel, &resp.Diagnostics)
	}
}

func (a *composeScaleAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composeScaleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	want := map[string]int64{}
	resp.Diagnostics.Append(m.Replicas.ElementsAs(ctx, &want, false)...)
	p := composeLoadSource(ctx, m.composeSourceModel, &resp.Diagnostics)
	if p == nil || resp.Diagnostics.HasError() {
		return
	}
	for _, svcName := range composeSortedKeys(want) {
		svc, ok := p.Services[svcName]
		if !ok {
			resp.Diagnostics.AddError("docker_compose_scale: unknown service", fmt.Sprintf("service %q is not in project %s", svcName, p.Name))
			return
		}
		if want[svcName] < 0 {
			resp.Diagnostics.AddError("docker_compose_scale: invalid replicas", fmt.Sprintf("service %s: %d", svcName, want[svcName]))
			return
		}
		n := int(want[svcName])
		svc.Scale = &n
		if svc.Deploy != nil {
			d := *svc.Deploy
			d.Replicas = &n
			svc.Deploy = &d
		}
		p.Services[svcName] = svc
	}
	plan, err := composeLocalExpand(p)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_scale: invalid project", err.Error())
		return
	}
	for _, svcName := range composeSortedKeys(want) {
		existing, err := composeContainers(ctx, a.pd, composeSelector{Project: p.Name, Services: []string{svcName}, All: true})
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_scale: cannot list containers", err.Error())
			return
		}
		byName := map[string]container.Summary{}
		for _, s := range existing {
			byName[composeSummaryName(s)] = s
		}
		desired := composeSvcContainers(plan, svcName)
		keep := map[string]bool{}
		for _, dc := range desired {
			keep[dc.Name] = true
			if got, ok := byName[dc.Name]; ok {
				// Like `docker compose scale` (create, then start): kept
				// containers that are created or exited are started.
				if got.State == container.StateCreated || got.State == container.StateExited {
					if _, err := c.ContainerStart(ctx, got.ID, client.ContainerStartOptions{}); err != nil {
						resp.Diagnostics.AddError("docker_compose_scale: cannot start container", fmt.Sprintf("%s: %v", dc.Name, err))
						return
					}
					actProgress(ctx, resp, "started %s", dc.Name)
				}
				continue
			}
			// Owner label inject-only: an action owns nothing in state.
			if _, err := composeLocalCreate(composeOwnerNoCheck(ctx), a.pd, dc, nil); err != nil {
				resp.Diagnostics.AddError("docker_compose_scale: cannot create container", err.Error())
				return
			}
			actProgress(ctx, resp, "created %s", dc.Name)
		}
		// Remove extras, highest container number first.
		sort.SliceStable(existing, func(i, j int) bool {
			ni, _ := strconv.Atoi(existing[i].Labels[composeLabelNumber])
			nj, _ := strconv.Atoi(existing[j].Labels[composeLabelNumber])
			return ni > nj
		})
		for _, s := range existing {
			if keep[composeSummaryName(s)] {
				continue
			}
			if _, err := c.ContainerRemove(ctx, s.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
				resp.Diagnostics.AddError("docker_compose_scale: cannot remove container", fmt.Sprintf("%s: %v", composeSummaryName(s), err))
				return
			}
			actProgress(ctx, resp, "removed %s", composeSummaryName(s))
		}
	}
	after, err := composeContainers(ctx, a.pd, composeSelector{Project: p.Name, Services: composeSortedKeys(want), All: true})
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_scale: cannot list containers", err.Error())
		return
	}
	states := make([]any, 0, len(after))
	for _, s := range after {
		states = append(states, composeContainerState(s))
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-scale", map[string]any{
		"name":          p.Name,
		"container_ids": composeContainerIDs(after),
		"replicas":      want,
		"result":        map[string]any{"container_states": states},
	}, m.ResultPath)
}

// ---------------------------------------------------------------------------
// docker_compose_run

// NewComposeRunAction returns docker_compose_run.
func NewComposeRunAction() action.Action { return &composeRunAction{} }

type composeRunAction struct{ actBase }

type composeRunModel struct {
	composeSourceModel
	Service    types.String `tfsdk:"service"`
	Command    types.List   `tfsdk:"command"`
	Entrypoint types.List   `tfsdk:"entrypoint"`
	RunEnv     types.Map    `tfsdk:"environment"`
	User       types.String `tfsdk:"user"`
	Workdir    types.String `tfsdk:"workdir"`
	Remove     types.Bool   `tfsdk:"rm"`
	StartDeps  types.Bool   `tfsdk:"start_dependencies"`
	NoRecreate types.Bool   `tfsdk:"no_recreate"`
	Timeout    types.String `tfsdk:"timeout"`
	ResultPath types.String `tfsdk:"result_path"`
}

func (a *composeRunAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_run"
}

func (a *composeRunAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Runs a one-off container for a compose service, like `docker compose run`: the container " +
			"`<project>-<service>-run-<id>` is created from the service's compose model (image pulled or built, networks, volumes, " +
			"secrets and configs as for `docker_compose`), labelled `com.docker.compose.oneoff=True`, waited for and removed. The action " +
			"fails when the command exits non-zero. The result carries `exit_code` and the captured `stdout` / `stderr`. Use it for " +
			"migrations and other jobs: compose `jobs` are not run by `docker_compose` yet. The project's networks must exist " +
			"(apply the `docker_compose` resource first). By default the service's `depends_on` services are expected to run " +
			"already (CLI `--no-deps`); set `start_dependencies` to bring them up first.",
		Attributes: composeActionSource(map[string]aschema.Attribute{
			"service": aschema.StringAttribute{Required: true, MarkdownDescription: "The service to run."},
			"command": aschema.ListAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Command and arguments, overriding the service's `command`."},
			"entrypoint": aschema.ListAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Entrypoint, overriding the service's `entrypoint` (CLI `--entrypoint`)."},
			"environment": aschema.MapAttribute{ElementType: types.StringType, Optional: true,
				MarkdownDescription: "Extra environment variables for the container (CLI `-e`), on top of the service's."},
			"user":    aschema.StringAttribute{Optional: true, MarkdownDescription: "User to run as (CLI `--user`)."},
			"workdir": aschema.StringAttribute{Optional: true, MarkdownDescription: "Working directory (CLI `--workdir`)."},
			"rm": aschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Remove the container after it exits (CLI `--rm`). Default `true`."},
			"start_dependencies": aschema.BoolAttribute{Optional: true,
				MarkdownDescription: "Bring up the service's `depends_on` services (transitively) before the run, like `docker compose run` " +
					"without `--no-deps`: a missing dependency container is created and started, a stopped one is started, a dependency " +
					"container whose configuration diverged from the compose model is recreated (unless `no_recreate`), and the run waits for " +
					"each `depends_on` condition. Default `false` (CLI `--no-deps`): the dependencies are expected to run already, usually " +
					"managed by `docker_compose`.",
			},
			"no_recreate": aschema.BoolAttribute{Optional: true,
				MarkdownDescription: "With `start_dependencies`, an existing dependency container is never recreated, even if its " +
					"configuration diverged; it is only started when stopped (CLI `--no-recreate`). Default `false`.",
			},
			"timeout":     composeTimeoutAttr("the run"),
			"result_path": actResultPathAttribute(),
		}),
	}
}

func (a *composeRunAction) ValidateConfig(ctx context.Context, req action.ValidateConfigRequest, resp *action.ValidateConfigResponse) {
	var m composeRunModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() {
		composeValidateSource(m.composeSourceModel, &resp.Diagnostics)
		actDuration(m.Timeout, "timeout", &resp.Diagnostics)
		if nr, ok := knownBool(m.NoRecreate); ok && nr && !m.StartDeps.IsUnknown() && !actBool(m.StartDeps, false) {
			resp.Diagnostics.AddAttributeError(path.Root("no_recreate"), "no_recreate needs start_dependencies",
				"no_recreate only applies to the dependency containers that start_dependencies brings up; set start_dependencies = true.")
		}
	}
}

// composeRunSlug is the 12-hex-digit suffix of a run container name.
func composeRunSlug() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// composeCapture reads a container's stdout and stderr (not a TTY:
// demultiplexed; TTY: all on stdout).
func composeCapture(ctx context.Context, c *client.Client, id string, tty bool) (string, string, error) {
	rc, err := c.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", "", err
	}
	defer rc.Close()
	var out, errb bytes.Buffer
	if tty {
		_, err = out.ReadFrom(rc)
	} else {
		_, err = stdcopy.StdCopy(&out, &errb, rc)
	}
	return out.String(), errb.String(), err
}

func (a *composeRunAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composeRunModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	p := composeLoadSource(ctx, m.composeSourceModel, &resp.Diagnostics)
	if p == nil {
		return
	}
	svcName := m.Service.ValueString()
	svc, ok := p.Services[svcName]
	if !ok {
		if _, job := p.Jobs[svcName]; job {
			resp.Diagnostics.AddError("docker_compose_run: jobs are not supported yet", fmt.Sprintf("%q is a compose job; run a service instead (Q18).", svcName))
			return
		}
		resp.Diagnostics.AddError("docker_compose_run: unknown service", fmt.Sprintf("service %q is not in project %s (or not enabled by profiles)", svcName, p.Name))
		return
	}
	one := 1
	svc.Scale = &one
	svc.ContainerName = ""
	svc.Restart = ""
	if svc.Deploy != nil {
		d := *svc.Deploy
		d.Replicas = &one
		d.RestartPolicy = nil
		svc.Deploy = &d
	}
	svc.Ports = nil // like `compose run` without --service-ports
	runDeps := svc.DependsOn
	svc.DependsOn = nil // the run waits for them below (start_dependencies), not through the plan
	sub := *p
	sub.Services = ctypes.Services{svcName: svc}
	plan, err := composeLocalExpand(&sub)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_run: invalid project", err.Error())
		return
	}
	dcs := composeSvcContainers(plan, svcName)
	if len(dcs) != 1 {
		resp.Diagnostics.AddError("docker_compose_run: provider bug", fmt.Sprintf("expected one container for %s, got %d", svcName, len(dcs)))
		return
	}
	dc := dcs[0]
	cfg := *dc.Config
	host := *dc.Host
	dc.Config, dc.Host = &cfg, &host
	dc.Name = fmt.Sprintf("%s-%s-run-%s", p.Name, svcName, composeRunSlug())
	cfg.Labels = mergeLabels(cfg.Labels, map[string]string{composeLabelOneoff: "True"})
	delete(cfg.Labels, composeLabelNumber)
	host.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyDisabled}
	if cmd := stringListFrom(ctx, m.Command, &resp.Diagnostics); len(cmd) > 0 {
		cfg.Cmd = cmd
	}
	if ep := stringListFrom(ctx, m.Entrypoint, &resp.Diagnostics); len(ep) > 0 {
		cfg.Entrypoint = ep
	}
	extra := stringMapFrom(ctx, m.RunEnv, &resp.Diagnostics)
	for _, k := range composeSortedKeys(extra) {
		cfg.Env = append(cfg.Env, k+"="+extra[k])
	}
	if u := m.User.ValueString(); u != "" {
		cfg.User = u
	}
	if w := m.Workdir.ValueString(); w != "" {
		cfg.WorkingDir = w
	}
	if resp.Diagnostics.HasError() {
		return
	}
	timeout := actTimeout(m.Timeout, composeActionDefaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if actBool(m.StartDeps, false) {
		warn := func(summary, detail string) { resp.Diagnostics.AddWarning(summary, detail) }
		ids, err := composeRunStartDeps(ctx, a.pd, p, svcName, actBool(m.NoRecreate, false), timeout,
			func(msg string) { actProgress(ctx, resp, "%s", msg) }, warn)
		if err != nil {
			resp.Diagnostics.AddError("docker_compose_run: cannot start dependencies", err.Error())
			return
		}
		if err := composeRunWaitDeps(ctx, a.pd, runDeps, ids, timeout, warn); err != nil {
			resp.Diagnostics.AddError("docker_compose_run: dependency not ready", fmt.Sprintf("service %s: %v", svcName, err))
			return
		}
	}

	id, err := composeLocalCreate(ctx, a.pd, dc, nil)
	remove := func() {
		if id != "" && actBool(m.Remove, true) {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer rcancel()
			_, _ = c.ContainerRemove(rctx, id, client.ContainerRemoveOptions{Force: true})
		}
	}
	defer remove()
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_run: cannot start container", err.Error())
		return
	}
	actProgress(ctx, resp, "running %s", dc.Name)
	wr := c.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var code int64
	select {
	case res := <-wr.Result:
		code = res.StatusCode
		if res.Error != nil && res.Error.Message != "" {
			resp.Diagnostics.AddError("docker_compose_run: wait failed", res.Error.Message)
			return
		}
	case err := <-wr.Error:
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			err = fmt.Errorf("did not finish within %s", timeout)
		}
		resp.Diagnostics.AddError("docker_compose_run: wait failed", fmt.Sprintf("%s: %v", dc.Name, err))
		return
	}
	stdout, stderr, err := composeCapture(ctx, c, id, cfg.Tty)
	if err != nil {
		resp.Diagnostics.AddWarning("docker_compose_run: output not captured", err.Error())
	}
	if code != 0 {
		resp.Diagnostics.AddError("docker_compose_run: command failed",
			fmt.Sprintf("%s exited with code %d.%s", dc.Name, code, actExecTail(composeTailLines(stdout+stderr))))
		return
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-run", map[string]any{
		"name":           p.Name,
		"container_id":   id,
		"container_name": dc.Name,
		"result":         map[string]any{"exit_code": code, "stdout": stdout, "stderr": stderr},
	}, m.ResultPath)
}

// composeRunStartDeps brings up the depends_on closure of svcName before a
// one-off run, like compose v5.7.1 RunOneOffContainer -> startDependencies
// (pkg/compose/run.go:296-326): the project is narrowed to the service and
// its dependencies (cmd/compose/run.go:279-280, ToProject with the service
// selected), the service itself is left out, and the rest is created with
// the "diverged" recreate policy, or "never" with --no-recreate
// (cmd/compose/create.go:165-173 dependenciesRecreateStrategy;
// pkg/compose/reconcile.go:1029-1032 mustRecreate returns false for
// RecreateNever), then started. Containers are handled as docker_compose
// handles them (composeLocalApply): missing ones created, diverged ones
// recreated (pre_stop hooks first), created or failed ones started, each
// after its own depends_on conditions. Nothing else of the project is
// touched (no orphan removal, no network or volume changes). It returns the
// container IDs per dependency service.
func composeRunStartDeps(ctx context.Context, pd *providerData, p *ctypes.Project, svcName string, noRecreate bool, timeout time.Duration, progress func(string), warn func(string, string)) (map[string][]string, error) {
	sel, err := p.WithSelectedServices([]string{svcName})
	if err != nil {
		return nil, err
	}
	deps := ctypes.Services{}
	for name, s := range sel.Services {
		if name != svcName {
			deps[name] = s
		}
	}
	if len(deps) == 0 {
		return nil, nil
	}
	sub := *sel
	sub.Services = deps
	plan, err := composeLocalExpand(&sub)
	if err != nil {
		return nil, fmt.Errorf("invalid project: %w", err)
	}
	ex, err := composeLocalList(ctx, pd, p.Name)
	if err != nil {
		return nil, err
	}
	recreate := map[string]bool{}
	replaced := map[string]bool{}
	recreatedSvc := map[string]bool{}
	kept := map[string]bool{}
	for _, dc := range plan.Containers {
		got, ok := ex.containers[dc.Name]
		if !ok {
			replaced[dc.Name] = true
			recreatedSvc[dc.Service] = true
			continue
		}
		diverged := false
		if !noRecreate {
			diverged = !composeHashMatches(got.Labels, dc.Hash) || !composeLocalConfigHashCurrent(dc, got.Labels, ex, replaced)
			for dep, d := range dc.DependsOn {
				if d.Restart && recreatedSvc[dep] {
					diverged = true
				}
			}
		}
		if diverged {
			recreate[dc.Name] = true
			replaced[dc.Name] = true
			recreatedSvc[dc.Service] = true
			continue
		}
		kept[dc.Service] = true
	}
	c := pd.Client
	ids := map[string][]string{}
	sharedDone := map[string]bool{}
	for _, dc := range plan.Containers {
		if err := composeRunWaitDeps(ctx, pd, dc.DependsOn, ids, timeout, warn); err != nil {
			return ids, fmt.Errorf("service %s: %w", dc.Service, err)
		}
		got, exists := ex.containers[dc.Name]
		if exists && !recreate[dc.Name] {
			ids[dc.Service] = append(ids[dc.Service], got.ID)
			if got.State == container.StateCreated || (got.State == container.StateExited && composeLocalStateDrift(got, dc.Host.RestartPolicy.Name) != "") {
				progress("starting dependency " + dc.Name)
				if _, err := c.ContainerStart(ctx, got.ID, client.ContainerStartOptions{}); err != nil {
					return ids, fmt.Errorf("starting container %s: %w", dc.Name, err)
				}
				if err := composeRunExecHooks(ctx, pd, got.ID, dc.Name, "post_start", dc.PostStart); err != nil {
					return ids, err
				}
			}
			continue
		}
		if exists {
			progress("recreating dependency " + dc.Name + " (configuration diverged)")
			if err := composeRunPreStop(ctx, pd, got, dc.PreStop); err != nil {
				return ids, err
			}
			if _, err := c.ContainerRemove(ctx, got.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isNotFound(err) {
				return ids, fmt.Errorf("removing container %s: %w", dc.Name, err)
			}
		} else {
			progress("creating dependency " + dc.Name)
		}
		if !kept[dc.Service] && !sharedDone[dc.Service] {
			dc.SharedPreStart = true
			sharedDone[dc.Service] = true
		}
		id, err := composeLocalCreate(ctx, pd, dc, nil)
		if err != nil {
			return ids, err
		}
		ids[dc.Service] = append(ids[dc.Service], id)
	}
	return ids, nil
}

// composeRunWaitDeps waits for the depends_on conditions of one service;
// an optional dependency (required: false) that is not ready is a warning.
func composeRunWaitDeps(ctx context.Context, pd *providerData, deps ctypes.DependsOnConfig, ids map[string][]string, timeout time.Duration, warn func(string, string)) error {
	for _, dep := range composeSortedKeys(deps) {
		d := deps[dep]
		if err := composeWaitDependency(ctx, pd, dep, ids[dep], d.Condition, timeout); err != nil {
			if !d.Required {
				warn("Optional dependency not ready", err.Error())
				continue
			}
			return err
		}
	}
	return nil
}

// composeTailLines returns the last actTailLines lines of s.
func composeTailLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > actTailLines {
		lines = lines[len(lines)-actTailLines:]
	}
	return lines
}

// ---------------------------------------------------------------------------
// docker_compose_exec

// NewComposeExecAction returns docker_compose_exec.
func NewComposeExecAction() action.Action { return &composeExecAction{} }

type composeExecAction struct{ actBase }

type composeExecModel struct {
	Name       types.String `tfsdk:"name"`
	Service    types.String `tfsdk:"service"`
	Index      types.Int64  `tfsdk:"index"`
	Command    types.List   `tfsdk:"command"`
	Env        types.Map    `tfsdk:"environment"`
	User       types.String `tfsdk:"user"`
	Workdir    types.String `tfsdk:"workdir"`
	Privileged types.Bool   `tfsdk:"privileged"`
	Timeout    types.String `tfsdk:"timeout"`
	ResultPath types.String `tfsdk:"result_path"`
}

func composeIndexAttr() aschema.Int64Attribute {
	return aschema.Int64Attribute{Optional: true,
		MarkdownDescription: "Which container of the service, by container number (CLI `--index`). Default `1`.",
		Validators:          []validator.Int64{int64validator.AtLeast(1)}}
}

func (a *composeExecAction) Metadata(_ context.Context, req action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compose_exec"
}

func (a *composeExecAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = aschema.Schema{
		MarkdownDescription: "Runs a command in a running container of a compose service, like `docker compose exec`, and fails when " +
			"it exits non-zero. The container is found by the `com.docker.compose.*` labels. The result carries `exit_code` and the " +
			"captured `stdout` / `stderr`.",
		Attributes: map[string]aschema.Attribute{
			"name":    composeActionProjectAttr(),
			"service": aschema.StringAttribute{Required: true, MarkdownDescription: "The service whose container runs the command."},
			"index":   composeIndexAttr(),
			"command": aschema.ListAttribute{ElementType: types.StringType, Required: true,
				MarkdownDescription: "Command and arguments, exec form. No shell is added.",
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)}},
			"environment": aschema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Extra environment variables (CLI `-e`)."},
			"user":        aschema.StringAttribute{Optional: true, MarkdownDescription: "User to run as (CLI `--user`)."},
			"workdir":     aschema.StringAttribute{Optional: true, MarkdownDescription: "Working directory (CLI `--workdir`)."},
			"privileged":  aschema.BoolAttribute{Optional: true, MarkdownDescription: "Extended privileges (CLI `--privileged`). Default `false`."},
			"timeout":     composeTimeoutAttr("the command"),
			"result_path": actResultPathAttribute(),
		},
	}
}

// composeServiceContainer finds container number index of a service.
func composeServiceContainer(ctx context.Context, pd *providerData, project, service string, index types.Int64, running bool) (container.Summary, error) {
	n := int64(1)
	if !index.IsNull() && !index.IsUnknown() {
		n = index.ValueInt64()
	}
	cs, err := composeContainers(ctx, pd, composeSelector{Project: project, Services: []string{service}, All: !running})
	if err != nil {
		return container.Summary{}, err
	}
	for _, c := range cs {
		if c.Labels[composeLabelNumber] == strconv.FormatInt(n, 10) {
			return c, nil
		}
	}
	state := ""
	if running {
		state = "running "
	}
	return container.Summary{}, fmt.Errorf("project %s has no %scontainer %d of service %s", project, state, n, service)
}

func (a *composeExecAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var m composeExecModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	c := a.client(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctr, err := composeServiceContainer(ctx, a.pd, m.Name.ValueString(), m.Service.ValueString(), m.Index, true)
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_exec: no container", err.Error())
		return
	}
	cmd := stringListFrom(ctx, m.Command, &resp.Diagnostics)
	envMap := stringMapFrom(ctx, m.Env, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var env []string
	for _, k := range composeSortedKeys(envMap) {
		env = append(env, k+"="+envMap[k])
	}
	timeout := actTimeout(m.Timeout, composeActionDefaultTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	created, err := c.ExecCreate(ctx, ctr.ID, client.ExecCreateOptions{
		User: m.User.ValueString(), Privileged: actBool(m.Privileged, false), AttachStdout: true, AttachStderr: true,
		Env: env, WorkingDir: m.Workdir.ValueString(), Cmd: cmd,
	})
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_exec: cannot create exec", fmt.Sprintf("%s: %v", composeSummaryName(ctr), err))
		return
	}
	hj, err := c.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		resp.Diagnostics.AddError("docker_compose_exec: cannot start exec", err.Error())
		return
	}
	stop := context.AfterFunc(ctx, hj.Close)
	defer stop()
	defer hj.Close()
	var out, errb bytes.Buffer
	_, cerr := stdcopy.StdCopy(&out, &errb, hj.Reader)
	if ctx.Err() != nil {
		resp.Diagnostics.AddError("docker_compose_exec: timed out", fmt.Sprintf("command did not finish within %s", timeout))
		return
	}
	if cerr != nil {
		resp.Diagnostics.AddError("docker_compose_exec: reading output failed", cerr.Error())
		return
	}
	var insp client.ExecInspectResult
	werr := waitFor(ctx, timeout, 100*time.Millisecond, func(ctx context.Context) (bool, error) {
		var err error
		insp, err = c.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return false, err
		}
		return !insp.Running, nil
	})
	if werr != nil {
		resp.Diagnostics.AddError("docker_compose_exec: cannot read exit code", werr.Error())
		return
	}
	if insp.ExitCode != 0 {
		resp.Diagnostics.AddError("docker_compose_exec: command failed",
			fmt.Sprintf("command %q in %s exited with code %d.%s", strings.Join(cmd, " "), composeSummaryName(ctr), insp.ExitCode,
				actExecTail(composeTailLines(out.String()+errb.String()))))
		return
	}
	actFinishResult(ctx, resp, a.pd, "compose:cli-exec", map[string]any{
		"name":         m.Name.ValueString(),
		"container_id": ctr.ID,
		"exec_id":      created.ID,
		"result":       map[string]any{"exit_code": insp.ExitCode, "stdout": out.String(), "stderr": errb.String()},
	}, m.ResultPath)
}
