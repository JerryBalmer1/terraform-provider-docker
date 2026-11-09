package provider

// Shared helpers for the compose data sources and actions: selecting a
// project's containers by the com.docker.compose.* labels (what `docker
// compose ps` does), loading a project from the common source attributes,
// and turning Engine structs into plain values for the outputs contract.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// composeSelector picks containers of one project.
type composeSelector struct {
	Project  string
	Services []string // empty: every service
	All      bool     // include stopped containers
	OneOff   bool     // include `run` containers
}

// composeContainers lists the selected containers, ordered by service then
// container number.
func composeContainers(ctx context.Context, pd *providerData, sel composeSelector) ([]container.Summary, error) {
	if pd == nil || pd.Client == nil {
		return nil, fmt.Errorf("provider not configured")
	}
	f := client.Filters{}.Add("label", composeLabelProject+"="+sel.Project)
	res, err := pd.Client.ContainerList(ctx, client.ContainerListOptions{All: sel.All, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("listing containers of project %s: %w", sel.Project, err)
	}
	want := map[string]bool{}
	for _, s := range sel.Services {
		want[s] = true
	}
	var out []container.Summary
	for _, s := range res.Items {
		if !sel.OneOff && strings.EqualFold(s.Labels[composeLabelOneoff], "true") {
			continue
		}
		if len(want) > 0 && !want[s.Labels[composeLabelService]] {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Labels, out[j].Labels
		if a[composeLabelService] != b[composeLabelService] {
			return a[composeLabelService] < b[composeLabelService]
		}
		na, _ := strconv.Atoi(a[composeLabelNumber])
		nb, _ := strconv.Atoi(b[composeLabelNumber])
		if na != nb {
			return na < nb
		}
		return composeSummaryName(out[i]) < composeSummaryName(out[j])
	})
	return out, nil
}

// composeSummaryName is the container name without the leading slash.
func composeSummaryName(s container.Summary) string {
	if len(s.Names) > 0 {
		return strings.TrimPrefix(s.Names[0], "/")
	}
	return s.ID
}

// composeContainerIDs groups container IDs by service (identity output
// container_ids).
func composeContainerIDs(cs []container.Summary) map[string][]string {
	out := map[string][]string{}
	for _, c := range cs {
		svc := c.Labels[composeLabelService]
		out[svc] = append(out[svc], c.ID)
	}
	return out
}

// composeServiceNames returns the sorted distinct services of cs.
func composeServiceNames(cs []container.Summary) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cs {
		s := c.Labels[composeLabelService]
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// composePlain marshals v to a plain JSON object and adds extra keys.
func composePlain(v any, extra map[string]any) map[string]any {
	out := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.UseNumber()
		_ = d.Decode(&out)
	}
	for k, x := range extra {
		out[k] = x
	}
	return out
}

// composeContainerState is one element of an action's result.container_states.
func composeContainerState(s container.Summary) map[string]any {
	return map[string]any{
		"id": s.ID, "name": composeSummaryName(s), "service": s.Labels[composeLabelService],
		"state": string(s.State), "status": s.Status,
	}
}

// composeSourceModel holds the attributes that load a project for the
// types that need the compose model (config, build, pull, push, publish,
// run, scale). They match docker_compose's.
type composeSourceModel struct {
	Content          types.String `tfsdk:"content"`
	Files            types.List   `tfsdk:"files"`
	Name             types.String `tfsdk:"name"`
	Profiles         types.List   `tfsdk:"profiles"`
	Env              types.Map    `tfsdk:"env"`
	EnvFile          types.List   `tfsdk:"env_file"`
	ProjectDirectory types.String `tfsdk:"project_directory"`
}

// composeLoadSource loads the project the source attributes describe.
func composeLoadSource(ctx context.Context, m composeSourceModel, diags *diag.Diagnostics) *ctypes.Project {
	req := seamComposeLoadRequest{
		Content:     m.Content.ValueString(),
		Paths:       stringListFrom(ctx, m.Files, diags),
		WorkingDir:  m.ProjectDirectory.ValueString(),
		ProjectName: m.Name.ValueString(),
		Env:         stringMapFrom(ctx, m.Env, diags),
		EnvFiles:    stringListFrom(ctx, m.EnvFile, diags),
		Profiles:    stringListFrom(ctx, m.Profiles, diags),
	}
	if diags.HasError() {
		return nil
	}
	p, err := seamComposeImpl.Load(ctx, req)
	if err != nil {
		diags.AddError("Invalid compose project", err.Error())
		return nil
	}
	return p
}

// composeSelectServices returns the enabled services named in want (all
// when empty), sorted, or an error naming unknown ones.
func composeSelectServices(p *ctypes.Project, want []string) ([]ctypes.ServiceConfig, error) {
	if len(want) == 0 {
		var out []ctypes.ServiceConfig
		for _, n := range composeSortedKeys(p.Services) {
			out = append(out, p.Services[n])
		}
		return out, nil
	}
	var out []ctypes.ServiceConfig
	for _, n := range want {
		s, ok := p.Services[n]
		if !ok {
			return nil, fmt.Errorf("service %q is not in project %s (or not enabled by profiles)", n, p.Name)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// composeMarshal is json.Marshal with a nil slice or map written as [].
func composeMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err == nil && string(b) == "null" {
		return []byte("[]"), nil
	}
	return b, err
}

// newBigFloatInt is n as a *big.Float (types.NumberValue).
func newBigFloatInt(n int64) *big.Float { return new(big.Float).SetInt64(n) }
