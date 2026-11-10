package provider

// docker_swarm_init: the swarm spec settings `docker swarm update` changes
// (SwarmSpec Orchestration / Raft / Dispatcher / CAConfig, plus Labels),
// set at init and updated in place through SwarmUpdate. Each block mirrors
// its SwarmSpec object; an unset block, or an unset attribute inside one,
// leaves the daemon's value alone and is not read back.

import (
	"context"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/moby/moby/api/types/swarm"
)

type swarmOrchestrationModel struct {
	TaskHistoryRetentionLimit types.Int64 `tfsdk:"task_history_retention_limit"`
}

type swarmRaftModel struct {
	SnapshotInterval           types.Int64 `tfsdk:"snapshot_interval"`
	KeepOldSnapshots           types.Int64 `tfsdk:"keep_old_snapshots"`
	LogEntriesForSlowFollowers types.Int64 `tfsdk:"log_entries_for_slow_followers"`
}

type swarmDispatcherModel struct {
	HeartbeatPeriod types.String `tfsdk:"heartbeat_period"`
}

type swarmCAConfigModel struct {
	NodeCertExpiry types.String           `tfsdk:"node_cert_expiry"`
	ExternalCAs    []swarmExternalCAModel `tfsdk:"external_cas"`
}

// swarmExternalCAModel is one CAConfig.ExternalCAs entry.
type swarmExternalCAModel struct {
	Protocol types.String `tfsdk:"protocol"`
	URL      types.String `tfsdk:"url"`
	Options  types.Map    `tfsdk:"options"`
	CACert   types.String `tfsdk:"ca_cert"`
}

// swarmInitSpecAttributes are the spec attributes of docker_swarm_init.
func swarmInitSpecAttributes() map[string]schema.Attribute {
	nonNeg := []validator.Int64{int64validator.AtLeast(0)}
	dur := []validator.String{svcDurationValidator()}
	return map[string]schema.Attribute{
		"orchestration": schema.SingleNestedAttribute{Optional: true,
			MarkdownDescription: "Orchestration settings (`Spec.Orchestration`). Updated in place (SwarmUpdate).",
			Attributes: map[string]schema.Attribute{
				"task_history_retention_limit": schema.Int64Attribute{Optional: true, Validators: nonNeg,
					MarkdownDescription: "Old tasks kept per service slot (`TaskHistoryRetentionLimit`, `docker swarm update --task-history-limit`)."},
			}},
		"raft": schema.SingleNestedAttribute{Optional: true,
			MarkdownDescription: "Raft settings (`Spec.Raft`). Updated in place (SwarmUpdate).",
			Attributes: map[string]schema.Attribute{
				"snapshot_interval": schema.Int64Attribute{Optional: true, Validators: nonNeg,
					MarkdownDescription: "Log entries between raft snapshots (`SnapshotInterval`, `--snapshot-interval`)."},
				"keep_old_snapshots": schema.Int64Attribute{Optional: true, Validators: nonNeg,
					MarkdownDescription: "Snapshots kept beyond the current one (`KeepOldSnapshots`)."},
				"log_entries_for_slow_followers": schema.Int64Attribute{Optional: true, Validators: nonNeg,
					MarkdownDescription: "Log entries kept after a snapshot to sync slow followers (`LogEntriesForSlowFollowers`)."},
			}},
		"dispatcher": schema.SingleNestedAttribute{Optional: true,
			MarkdownDescription: "Dispatcher settings (`Spec.Dispatcher`). Updated in place (SwarmUpdate).",
			Attributes: map[string]schema.Attribute{
				"heartbeat_period": schema.StringAttribute{Optional: true, Validators: dur,
					MarkdownDescription: "How often agents send heartbeats, as a Go duration such as `5s` (`HeartbeatPeriod`, `--dispatcher-heartbeat`)."},
			}},
		"ca_config": schema.SingleNestedAttribute{Optional: true,
			MarkdownDescription: "CA settings (`Spec.CAConfig`). Updated in place (SwarmUpdate); rotate the root CA with `docker_swarm_rotate_ca`.",
			Attributes: map[string]schema.Attribute{
				"node_cert_expiry": schema.StringAttribute{Optional: true, Validators: dur,
					MarkdownDescription: "Validity of node certificates, as a Go duration such as `2160h` (`NodeCertExpiry`, `--cert-expiry`)."},
				"external_cas": schema.ListNestedAttribute{Optional: true,
					MarkdownDescription: "External certificate authorities that sign node certificates (`ExternalCAs`, `docker swarm init " +
						"--external-ca`). When set, the swarm gets exactly this list; unset leaves it as it is.",
					NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
						"protocol": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("cfssl"),
							Validators:          []validator.String{stringvalidator.OneOf("cfssl")},
							MarkdownDescription: "Protocol of the CA, only `cfssl` (the default) (`Protocol`)."},
						"url": schema.StringAttribute{Required: true, MarkdownDescription: "URL of the CA's signing endpoint (`URL`)."},
						"options": schema.MapAttribute{ElementType: types.StringType, Optional: true,
							MarkdownDescription: "Protocol-specific options for the CA (`Options`)."},
						"ca_cert": schema.StringAttribute{Optional: true,
							MarkdownDescription: "PEM root CA certificate the external CA signs with; unset means the swarm's current root CA (`CACert`)."},
					}},
				},
			}},
	}
}

func swarmParseDur(v types.String, attr string, diags *diag.Diagnostics) (time.Duration, bool) {
	s, ok := knownString(v)
	if !ok || s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		diags.AddAttributeError(path.Root(attr), "Invalid duration", err.Error())
		return 0, false
	}
	return d, true
}

// swarmApplySpec applies the configured spec settings of m to spec and
// reports whether anything changed.
func swarmApplySpec(ctx context.Context, m swarmInitModel, spec *swarm.Spec, diags *diag.Diagnostics) bool {
	return swarmApplySpecOwner(ctx, m, "", spec, diags)
}

// swarmApplySpecOwner is swarmApplySpec that also keeps the owner label
// (io.terraform.provider.owner=owner) on the swarm when owner is set.
func swarmApplySpecOwner(ctx context.Context, m swarmInitModel, owner string, spec *swarm.Spec, diags *diag.Diagnostics) bool {
	changed := false
	setI := func(v types.Int64, cur *uint64) {
		if n, ok := knownInt64(v); ok && uint64(n) != *cur {
			*cur = uint64(n)
			changed = true
		}
	}
	if o := m.Orchestration; o != nil {
		if n, ok := knownInt64(o.TaskHistoryRetentionLimit); ok {
			if spec.Orchestration.TaskHistoryRetentionLimit == nil || *spec.Orchestration.TaskHistoryRetentionLimit != n {
				spec.Orchestration.TaskHistoryRetentionLimit = &n
				changed = true
			}
		}
	}
	if r := m.Raft; r != nil {
		setI(r.SnapshotInterval, &spec.Raft.SnapshotInterval)
		setI(r.LogEntriesForSlowFollowers, &spec.Raft.LogEntriesForSlowFollowers)
		if n, ok := knownInt64(r.KeepOldSnapshots); ok {
			if spec.Raft.KeepOldSnapshots == nil || int64(*spec.Raft.KeepOldSnapshots) != n {
				u := uint64(n)
				spec.Raft.KeepOldSnapshots = &u
				changed = true
			}
		}
	}
	if d := m.Dispatcher; d != nil {
		if v, ok := swarmParseDur(d.HeartbeatPeriod, "dispatcher", diags); ok && spec.Dispatcher.HeartbeatPeriod != v {
			spec.Dispatcher.HeartbeatPeriod = v
			changed = true
		}
	}
	if c := m.CAConfig; c != nil {
		if v, ok := swarmParseDur(c.NodeCertExpiry, "ca_config", diags); ok && spec.CAConfig.NodeCertExpiry != v {
			spec.CAConfig.NodeCertExpiry = v
			changed = true
		}
		if c.ExternalCAs != nil {
			var want []*swarm.ExternalCA
			for _, e := range c.ExternalCAs {
				want = append(want, &swarm.ExternalCA{
					Protocol: swarm.ExternalCAProtocol(svcStrOr(e.Protocol, string(swarm.ExternalCAProtocolCFSSL))),
					URL:      e.URL.ValueString(),
					Options:  stringMapFrom(ctx, e.Options, diags),
					CACert:   e.CACert.ValueString(),
				})
			}
			if !swarmExternalCAsEqual(spec.CAConfig.ExternalCAs, want) {
				spec.CAConfig.ExternalCAs = want
				changed = true
			}
		}
	}
	if !m.Labels.IsNull() && !m.Labels.IsUnknown() {
		want := stringMapFrom(ctx, m.Labels, diags)
		if owner != "" {
			want = mergeLabels(want, map[string]string{ownerLabelKey: owner})
		}
		if !swarmStringMapsEqual(spec.Labels, want) {
			spec.Labels = want
			changed = true
		}
	} else if owner != "" && ownerOf(spec.Labels) != owner {
		spec.Labels = mergeLabels(spec.Labels, map[string]string{ownerLabelKey: owner})
		changed = true
	}
	return changed
}

// swarmExternalCAsEqual compares two ExternalCAs lists in order. A wanted
// entry without a CA certificate matches any daemon certificate (the daemon
// may fill in the current root).
func swarmExternalCAsEqual(have, want []*swarm.ExternalCA) bool {
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		h, w := have[i], want[i]
		if !strings.EqualFold(string(h.Protocol), string(w.Protocol)) || h.URL != w.URL || !swarmStringMapsEqual(h.Options, w.Options) ||
			(w.CACert != "" && h.CACert != w.CACert) {
			return false
		}
	}
	return true
}

// swarmFlattenSpec reads the daemon's spec into the configured blocks of
// m, keeping unset attributes null.
func swarmFlattenSpec(m *swarmInitModel, spec swarm.Spec) {
	intVal := func(prior types.Int64, v int64) types.Int64 {
		if prior.IsNull() {
			return prior
		}
		return types.Int64Value(v)
	}
	durVal := func(prior types.String, d time.Duration) types.String {
		if prior.IsNull() {
			return prior
		}
		return svcDurValue(prior, d)
	}
	if o := m.Orchestration; o != nil {
		var n int64
		if spec.Orchestration.TaskHistoryRetentionLimit != nil {
			n = *spec.Orchestration.TaskHistoryRetentionLimit
		}
		o.TaskHistoryRetentionLimit = intVal(o.TaskHistoryRetentionLimit, n)
	}
	if r := m.Raft; r != nil {
		var keep int64
		if spec.Raft.KeepOldSnapshots != nil {
			keep = int64(*spec.Raft.KeepOldSnapshots)
		}
		r.SnapshotInterval = intVal(r.SnapshotInterval, int64(spec.Raft.SnapshotInterval))
		r.KeepOldSnapshots = intVal(r.KeepOldSnapshots, keep)
		r.LogEntriesForSlowFollowers = intVal(r.LogEntriesForSlowFollowers, int64(spec.Raft.LogEntriesForSlowFollowers))
	}
	if d := m.Dispatcher; d != nil {
		d.HeartbeatPeriod = durVal(d.HeartbeatPeriod, spec.Dispatcher.HeartbeatPeriod)
	}
	if c := m.CAConfig; c != nil {
		c.NodeCertExpiry = durVal(c.NodeCertExpiry, spec.CAConfig.NodeCertExpiry)
		if c.ExternalCAs != nil {
			prior := c.ExternalCAs
			out := []swarmExternalCAModel{}
			for i, e := range spec.CAConfig.ExternalCAs {
				var p swarmExternalCAModel
				if i < len(prior) {
					p = prior[i]
				}
				// The daemon may report the root it signs with; it is only
				// shown when the configuration set one.
				cert := types.StringNull()
				if svcPriorSet(p.CACert) {
					cert = types.StringValue(e.CACert)
				}
				out = append(out, swarmExternalCAModel{
					Protocol: types.StringValue(strings.ToLower(string(e.Protocol))),
					URL:      types.StringValue(e.URL),
					Options:  svcMapValue(p.Options, e.Options),
					CACert:   cert,
				})
			}
			c.ExternalCAs = out
		}
	}
	m.Labels = stringMapValue(ownerStrip(spec.Labels), false)
}
