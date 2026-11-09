package provider

// docker_swarm_init: the swarm spec settings `docker swarm update` changes
// (SwarmSpec Orchestration / Raft / Dispatcher / CAConfig, plus Labels),
// set at init and updated in place through SwarmUpdate. Each block mirrors
// its SwarmSpec object; an unset block, or an unset attribute inside one,
// leaves the daemon's value alone and is not read back.

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
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
	NodeCertExpiry types.String `tfsdk:"node_cert_expiry"`
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
	}
	if !m.Labels.IsNull() && !m.Labels.IsUnknown() {
		want := stringMapFrom(ctx, m.Labels, diags)
		if !swarmStringMapsEqual(spec.Labels, want) {
			spec.Labels = want
			changed = true
		}
	}
	return changed
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
	}
	m.Labels = stringMapValue(spec.Labels, false)
}
