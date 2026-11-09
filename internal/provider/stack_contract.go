package provider

// docker_stack outputs contract (cli-only:stack): the live stack rendered
// as a compose-format document (`stack_yaml`, what `docker stack deploy -c`
// would take to recreate it), the same as a structured `stack`, the
// `docker stack services` rows as `service_status`, and the identity maps.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	"gopkg.in/yaml.v3"
)

// stackIdentityAttributes are docker_stack's identity outputs beside id
// and name.
func stackIdentityAttributes() map[string]schema.Attribute {
	m := func(desc string) schema.MapAttribute {
		return schema.MapAttribute{ElementType: types.StringType, Computed: true, MarkdownDescription: desc}
	}
	return map[string]schema.Attribute{
		"namespace": schema.StringAttribute{Computed: true,
			MarkdownDescription: "The stack namespace, the value of `com.docker.stack.namespace` on every object (equals `name`)."},
		"labels":      m("The namespace label every object of the stack carries (`com.docker.stack.namespace=<name>`), usable as a label selector."),
		"service_ids": m("Map of short service name to Swarm service ID."),
		"network_ids": m("Map of short network name to network ID."),
		"secret_ids":  m("Map of short secret name to Swarm secret ID."),
		"config_ids":  m("Map of short config name to Swarm config ID."),
	}
}

// stackComposeService renders one service spec in compose form.
func stackComposeService(stack string, spec swarm.ServiceSpec) map[string]any {
	out := map[string]any{}
	cs := spec.TaskTemplate.ContainerSpec
	if cs != nil {
		out["image"] = cs.Image
		if len(cs.Command) > 0 {
			out["entrypoint"] = cs.Command
		}
		if len(cs.Args) > 0 {
			out["command"] = cs.Args
		}
		if len(cs.Env) > 0 {
			env := map[string]any{}
			for _, e := range cs.Env {
				k, v, _ := strings.Cut(e, "=")
				env[k] = v
			}
			out["environment"] = env
		}
		if len(cs.Labels) > 0 {
			out["labels"] = cs.Labels
		}
		for k, v := range map[string]string{"hostname": cs.Hostname, "user": cs.User, "working_dir": cs.Dir, "stop_signal": cs.StopSignal} {
			if v != "" {
				out[k] = v
			}
		}
		if cs.TTY {
			out["tty"] = true
		}
		if cs.ReadOnly {
			out["read_only"] = true
		}
		if cs.StopGracePeriod != nil {
			out["stop_grace_period"] = cs.StopGracePeriod.String()
		}
		if len(cs.Hosts) > 0 {
			out["extra_hosts"] = cs.Hosts
		}
		var vols []any
		for _, m := range cs.Mounts {
			v := map[string]any{"type": string(m.Type), "source": stackShortName(stack, m.Source), "target": m.Target}
			if m.ReadOnly {
				v["read_only"] = true
			}
			if m.Type == mount.TypeVolume && m.Source == "" {
				delete(v, "source")
			}
			vols = append(vols, v)
		}
		if len(vols) > 0 {
			out["volumes"] = vols
		}
		var secs, cfgs []any
		for _, s := range cs.Secrets {
			e := map[string]any{"source": stackShortName(stack, s.SecretName)}
			if s.File != nil {
				e["target"] = s.File.Name
			}
			secs = append(secs, e)
		}
		for _, c := range cs.Configs {
			e := map[string]any{"source": stackShortName(stack, c.ConfigName)}
			if c.File != nil {
				e["target"] = c.File.Name
			}
			cfgs = append(cfgs, e)
		}
		if len(secs) > 0 {
			out["secrets"] = secs
		}
		if len(cfgs) > 0 {
			out["configs"] = cfgs
		}
		if hc := cs.Healthcheck; hc != nil && len(hc.Test) > 0 {
			h := map[string]any{"test": hc.Test}
			if hc.Interval > 0 {
				h["interval"] = hc.Interval.String()
			}
			if hc.Timeout > 0 {
				h["timeout"] = hc.Timeout.String()
			}
			if hc.Retries > 0 {
				h["retries"] = hc.Retries
			}
			out["healthcheck"] = h
		}
	}
	deploy := map[string]any{"mode": strings.ReplaceAll(stackServiceMode(spec.Mode), " ", "-")}
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		deploy["replicas"] = *spec.Mode.Replicated.Replicas
	}
	if len(spec.Labels) > 0 {
		l := map[string]string{}
		for k, v := range spec.Labels {
			if k != labelStackNamespace && k != labelManagedBy {
				l[k] = v
			}
		}
		if len(l) > 0 {
			deploy["labels"] = l
		}
	}
	if p := spec.TaskTemplate.Placement; p != nil && len(p.Constraints) > 0 {
		deploy["placement"] = map[string]any{"constraints": p.Constraints}
	}
	if r := spec.TaskTemplate.Resources; r != nil && r.Limits != nil {
		lim := map[string]any{}
		if r.Limits.NanoCPUs > 0 {
			lim["cpus"] = fmt.Sprintf("%g", float64(r.Limits.NanoCPUs)/1e9)
		}
		if r.Limits.MemoryBytes > 0 {
			lim["memory"] = r.Limits.MemoryBytes
		}
		if len(lim) > 0 {
			deploy["resources"] = map[string]any{"limits": lim}
		}
	}
	if rp := spec.TaskTemplate.RestartPolicy; rp != nil && rp.Condition != "" {
		deploy["restart_policy"] = map[string]any{"condition": string(rp.Condition)}
	}
	out["deploy"] = deploy
	if es := spec.EndpointSpec; es != nil && len(es.Ports) > 0 {
		var ports []any
		for _, p := range es.Ports {
			e := map[string]any{"target": p.TargetPort, "protocol": string(p.Protocol)}
			if p.PublishedPort > 0 {
				e["published"] = fmt.Sprint(p.PublishedPort)
			}
			if p.PublishMode != "" {
				e["mode"] = string(p.PublishMode)
			}
			ports = append(ports, e)
		}
		out["ports"] = ports
	}
	if len(spec.TaskTemplate.Networks) > 0 {
		nets := map[string]any{}
		for _, n := range spec.TaskTemplate.Networks {
			nets[n.Target] = map[string]any{}
		}
		out["networks"] = nets
	}
	return out
}

// stackComposeValue renders the live stack as a compose document.
func stackComposeValue(stack string, ex stackExisting, netNames map[string]string) map[string]any {
	svcs := map[string]any{}
	for short, s := range ex.services {
		svc := stackComposeService(stack, s.Spec)
		if nets, ok := svc["networks"].(map[string]any); ok {
			renamed := map[string]any{}
			for target, v := range nets {
				name := target
				if n, ok := netNames[target]; ok {
					name = n
				}
				renamed[stackShortName(stack, name)] = v
			}
			svc["networks"] = renamed
		}
		svcs[short] = svc
	}
	doc := map[string]any{"name": stack, "services": svcs}
	if len(ex.networks) > 0 {
		nets := map[string]any{}
		for short, n := range ex.networks {
			e := map[string]any{"name": n.Name, "driver": n.Driver}
			if n.Attachable {
				e["attachable"] = true
			}
			if n.Internal {
				e["internal"] = true
			}
			nets[short] = e
		}
		doc["networks"] = nets
	}
	if len(ex.secrets) > 0 {
		secs := map[string]any{}
		for short, s := range ex.secrets {
			secs[short] = map[string]any{"name": s.Spec.Name, "external": true}
		}
		doc["secrets"] = secs
	}
	if len(ex.configs) > 0 {
		cfgs := map[string]any{}
		for short, c := range ex.configs {
			cfgs[short] = map[string]any{"name": c.Spec.Name, "external": true}
		}
		doc["configs"] = cfgs
	}
	return doc
}

// stackFillOutputs fills the contract and identity outputs of m from the
// live objects.
func (r *stackResource) stackFillOutputs(ctx context.Context, m *stackResourceModel, ex stackExisting) diag.Diagnostics {
	var diags diag.Diagnostics
	stack := m.Name.ValueString()
	m.Namespace = types.StringValue(stack)
	m.Labels = stringMapValue(map[string]string{labelStackNamespace: stack}, false)
	ids := func(n int, each func(func(string, string))) types.Map {
		out := make(map[string]string, n)
		each(func(k, v string) { out[k] = v })
		return stringMapValue(out, false)
	}
	netNames := map[string]string{}
	m.ServiceIDs = ids(len(ex.services), func(put func(string, string)) {
		for k, s := range ex.services {
			put(k, s.ID)
		}
	})
	m.NetworkIDs = ids(len(ex.networks), func(put func(string, string)) {
		for k, n := range ex.networks {
			put(k, n.ID)
			netNames[n.ID] = n.Name
		}
	})
	m.SecretIDs = ids(len(ex.secrets), func(put func(string, string)) {
		for k, s := range ex.secrets {
			put(k, s.ID)
		}
	})
	m.ConfigIDs = ids(len(ex.configs), func(put func(string, string)) {
		for k, c := range ex.configs {
			put(k, c.ID)
		}
	})
	doc := stackComposeValue(stack, ex, netNames)
	y, err := yaml.Marshal(doc)
	if err != nil {
		diags.AddWarning("Cannot render stack_yaml", err.Error())
	}
	var status []map[string]any
	if r.pd != nil && r.pd.Client != nil {
		rows, err := stackServiceRows(ctx, r.pd.Client, stack)
		if err != nil {
			diags.AddWarning("Cannot read stack service status", err.Error())
		}
		for _, row := range rows {
			status = append(status, stackServiceStatus(row))
		}
	}
	sort.Slice(status, func(i, j int) bool { return fmt.Sprint(status[i]["Name"]) < fmt.Sprint(status[j]["Name"]) })
	if status == nil {
		status = []map[string]any{}
	}
	in := contractFillInput{Structured: map[string]any{"stack": doc}, Result: map[string]any{"service_status": status}}
	if err == nil {
		in.Raw = map[string][]byte{"stack_yaml": y}
	}
	diags.Append(m.contractFill(ctx, r.pd, in)...)
	return diags
}

// stackNullOutputs replaces unknown computed outputs with nulls (a failed
// create still records state).
func stackNullOutputs(m *stackResourceModel) {
	if m.Namespace.IsUnknown() {
		m.Namespace = types.StringNull()
	}
	for _, p := range []*types.Map{&m.Labels, &m.ServiceIDs, &m.NetworkIDs, &m.SecretIDs, &m.ConfigIDs} {
		if p.IsUnknown() {
			*p = types.MapNull(types.StringType)
		}
	}
	m.contractSetNull()
}
