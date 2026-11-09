package provider

// Pass 5 Lane B: docker_node (Q4) against the fake Engine, and the
// docker_swarm_init spec settings (swarm update).

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
)

func TestNodeResourceFakeLifecycle(t *testing.T) {
	f := swarmNewFakeEngine(t)
	n := actNewFakeNode(t, f, swarm.NodeAvailabilityActive)
	host := "tcp://" + f.srv.Listener.Addr().String()
	cfg := func(zone, avail string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_node" "w1" {
  node_id      = "worker-1"
  availability = %q
  labels       = { zone = %q }
}
`, avail, zone))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("a", "drain"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_node.w1", "id", "n1abcdef"),
					resource.TestCheckResourceAttr("docker_node.w1", "hostname", "worker-1"),
					resource.TestCheckResourceAttr("docker_node.w1", "role", "worker"),
					resource.TestCheckResourceAttr("docker_node.w1", "inspect.spec.labels.zone", "a"),
					resource.TestCheckResourceAttr("docker_node.w1", "inspect.description.hostname", "worker-1"),
					resource.TestCheckResourceAttrSet("docker_node.w1", "inspect_json"),
					resource.TestCheckResourceAttr("docker_node.w1", "docker_server_version", "29.0.0-fake"),
					func(*terraform.State) error {
						n.mu.Lock()
						defer n.mu.Unlock()
						if n.node.Spec.Availability != swarm.NodeAvailabilityDrain || n.node.Spec.Labels["zone"] != "a" {
							return fmt.Errorf("spec = %+v", n.node.Spec)
						}
						return nil
					},
				),
			},
			{
				Config: cfg("b", "active"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_node.w1", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_node.w1", "labels.zone", "b"),
					resource.TestCheckResourceAttr("docker_node.w1", "availability", "active"),
				),
			},
			{
				ResourceName:      "docker_node.w1",
				ImportState:       true,
				ImportStateId:     "worker-1",
				ImportStateVerify: true,
			},
		},
		CheckDestroy: func(*terraform.State) error {
			for _, c := range f.callLog() {
				if strings.HasPrefix(c, "DELETE ") || strings.Contains(c, "/swarm/leave") {
					return fmt.Errorf("destroy must only forget the node, but called %s", c)
				}
			}
			return nil
		},
	})
}

func TestNodeResourceUnknownNode(t *testing.T) {
	f := swarmNewFakeEngine(t)
	actNewFakeNode(t, f, swarm.NodeAvailabilityActive)
	host := "tcp://" + f.srv.Listener.Addr().String()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      svcFakeConfig(host, `resource "docker_node" "x" { node_id = "ghost" }`),
			ExpectError: regexp.MustCompile(`Cannot find swarm node`),
		}},
	})
}

func TestNodeResourceChange(t *testing.T) {
	var d diag.Diagnostics
	ch := nodeResourceChange(context.Background(), nodeResourceModel{
		Availability: types.StringUnknown(), Role: types.StringValue("manager"), Labels: types.MapNull(types.StringType),
	}, &d)
	if ch.Availability != nil || ch.Role == nil || *ch.Role != swarm.NodeRoleManager || ch.Labels != nil {
		t.Errorf("change = %+v", ch)
	}
}

func TestSwarmInitSpecApplyFlatten(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	m := swarmInitModel{
		Labels:        stringMapValue(map[string]string{"env": "prod"}, false),
		Orchestration: &swarmOrchestrationModel{TaskHistoryRetentionLimit: types.Int64Value(3)},
		Raft:          &swarmRaftModel{SnapshotInterval: types.Int64Value(5000), KeepOldSnapshots: types.Int64Null(), LogEntriesForSlowFollowers: types.Int64Null()},
		Dispatcher:    &swarmDispatcherModel{HeartbeatPeriod: types.StringValue("10s")},
		CAConfig:      &swarmCAConfigModel{NodeCertExpiry: types.StringValue("720h")},
	}
	five := int64(5)
	keep := uint64(0)
	spec := swarm.Spec{Orchestration: swarm.OrchestrationConfig{TaskHistoryRetentionLimit: &five},
		Raft: swarm.RaftConfig{SnapshotInterval: 10000, KeepOldSnapshots: &keep, LogEntriesForSlowFollowers: 500}}
	spec.Dispatcher.HeartbeatPeriod = 5 * time.Second
	if !swarmApplySpec(ctx, m, &spec, &d) || d.HasError() {
		t.Fatalf("no change detected: %v", d)
	}
	if *spec.Orchestration.TaskHistoryRetentionLimit != 3 || spec.Raft.SnapshotInterval != 5000 || spec.Raft.LogEntriesForSlowFollowers != 500 ||
		spec.Dispatcher.HeartbeatPeriod != 10*time.Second || spec.CAConfig.NodeCertExpiry != 720*time.Hour || spec.Labels["env"] != "prod" {
		t.Errorf("spec = %+v", spec)
	}
	if swarmApplySpec(ctx, m, &spec, &d) {
		t.Error("second apply should be a no-op")
	}
	// Flatten keeps unset attributes null and the configured spelling.
	swarmFlattenSpec(&m, spec)
	if !m.Raft.KeepOldSnapshots.IsNull() || m.Raft.SnapshotInterval.ValueInt64() != 5000 || m.CAConfig.NodeCertExpiry.ValueString() != "720h" {
		t.Errorf("flatten = %+v %+v", m.Raft, m.CAConfig)
	}
	// Drift is reported.
	spec.Raft.SnapshotInterval = 42
	swarmFlattenSpec(&m, spec)
	if m.Raft.SnapshotInterval.ValueInt64() != 42 {
		t.Errorf("drift not read: %v", m.Raft.SnapshotInterval)
	}
	bad := swarmInitModel{Labels: types.MapNull(types.StringType), Dispatcher: &swarmDispatcherModel{HeartbeatPeriod: types.StringValue("soon")}}
	swarmApplySpec(ctx, bad, &spec, &d)
	if !d.HasError() {
		t.Error("bad duration accepted")
	}
}
