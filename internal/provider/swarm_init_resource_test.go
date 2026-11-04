package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestSwarmInitBuildRequest(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	m := swarmInitModel{
		AdvertiseAddr:   types.StringValue("10.0.0.5"),
		ListenAddr:      types.StringNull(),
		DefaultAddrPool: stringListValue([]string{"10.20.0.0/16"}, false),
		SubnetSize:      types.Int64Value(26),
		DataPathPort:    types.Int64Unknown(),
		ForceNewCluster: types.BoolValue(true),
		Autolock:        types.BoolValue(false),
	}
	r := swarmInitBuildRequest(ctx, m, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if r.ListenAddr != swarmDefaultListenAddr || r.AdvertiseAddr != "10.0.0.5" || len(r.DefaultAddrPool) != 1 ||
		r.DefaultAddrPool[0] != "10.20.0.0/16" || r.SubnetSize != 26 || r.DataPathPort != 0 || !r.ForceNewCluster || r.AutoLockManagers {
		t.Errorf("unexpected request %+v", r)
	}
	m.DefaultAddrPool = types.ListUnknown(types.StringType)
	m.SubnetSize = types.Int64Unknown()
	m.ListenAddr = types.StringValue("0.0.0.0:4000")
	r = swarmInitBuildRequest(ctx, m, &diags)
	if r.DefaultAddrPool != nil || r.SubnetSize != 0 || r.ListenAddr != "0.0.0.0:4000" {
		t.Errorf("unknowns must be left to the daemon: %+v", r)
	}
}

func TestSwarmInitFlatten(t *testing.T) {
	sw := swarm.Swarm{
		ClusterInfo: swarm.ClusterInfo{ID: "c1", DefaultAddrPool: []string{"10.0.0.0/8"}, SubnetSize: 24, DataPathPort: 4789,
			Spec: swarm.Spec{EncryptionConfig: swarm.EncryptionConfig{AutoLockManagers: true}}},
		JoinTokens: swarm.JoinTokens{Worker: "w", Manager: "m"},
	}
	m := swarmInitModel{ListenAddr: types.StringNull(), ForceNewCluster: types.BoolNull()}
	swarmInitFlatten(&m, "n1", sw, "SWMKEY-1-x")
	if m.ID.ValueString() != "c1" || m.ClusterID.ValueString() != "c1" || m.NodeID.ValueString() != "n1" ||
		m.WorkerJoinToken.ValueString() != "w" || m.ManagerJoinToken.ValueString() != "m" ||
		m.SubnetSize.ValueInt64() != 24 || m.DataPathPort.ValueInt64() != 4789 || !m.Autolock.ValueBool() ||
		m.UnlockKey.ValueString() != "SWMKEY-1-x" || m.ListenAddr.ValueString() != swarmDefaultListenAddr || m.ForceNewCluster.ValueBool() {
		t.Errorf("unexpected model %+v", m)
	}
	sw.Spec.EncryptionConfig.AutoLockManagers = false
	swarmInitFlatten(&m, "n1", sw, "")
	if !m.UnlockKey.IsNull() || m.Autolock.ValueBool() {
		t.Errorf("unlock key should be null without autolock: %+v", m)
	}
}

func swarmFakeManager(t *testing.T, state string, clusterID string) *swarmFakeEngine {
	f := swarmNewFakeEngine(t)
	f.handle("GET /info", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, map[string]any{"Swarm": map[string]any{"LocalNodeState": state, "NodeID": "n1", "ControlAvailable": true}})
	})
	f.handle("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: clusterID, SubnetSize: 24, DataPathPort: 4789,
			DefaultAddrPool: []string{"10.0.0.0/8"}}, JoinTokens: swarm.JoinTokens{Worker: "SWMTKN-w", Manager: "SWMTKN-m"}})
	})
	return f
}

func TestSwarmInitRefresh(t *testing.T) {
	ctx := context.Background()
	c := swarmFakeManager(t, "active", "c1").client(t)
	m := swarmInitModel{ID: types.StringValue("c1"), ListenAddr: types.StringValue("0.0.0.0:2377")}
	ok, err := swarmInitRefresh(ctx, c, &m)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if m.WorkerJoinToken.ValueString() != "SWMTKN-w" || m.NodeID.ValueString() != "n1" || len(m.DefaultAddrPool.Elements()) != 1 {
		t.Errorf("unexpected model %+v", m)
	}
	// A different swarm on the host means ours is gone.
	m = swarmInitModel{ID: types.StringValue("other")}
	if ok, err := swarmInitRefresh(ctx, c, &m); ok || err != nil {
		t.Errorf("mismatched cluster: ok=%v err=%v", ok, err)
	}
	// Not in a swarm any more.
	c = swarmFakeManager(t, "inactive", "").client(t)
	if ok, err := swarmInitRefresh(ctx, c, &swarmInitModel{ID: types.StringValue("c1")}); ok || err != nil {
		t.Errorf("inactive: ok=%v err=%v", ok, err)
	}
	// Locked managers are kept.
	c = swarmFakeManager(t, "locked", "c1").client(t)
	if ok, err := swarmInitRefresh(ctx, c, &swarmInitModel{ID: types.StringValue("c1")}); !ok || !errors.Is(err, swarmInitErrLocked) {
		t.Errorf("locked: ok=%v err=%v", ok, err)
	}
}

func TestSwarmLeaveToleratesNotInSwarm(t *testing.T) {
	f := swarmNewFakeEngine(t)
	f.handle("POST /swarm/leave", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusServiceUnavailable, "This node is not part of a swarm")
	})
	if err := swarmLeave(context.Background(), f.client(t)); err != nil {
		t.Errorf("leave of a non-member should succeed: %v", err)
	}
	f.handle("POST /swarm/leave", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusInternalServerError, "boom")
	})
	if err := swarmLeave(context.Background(), f.client(t)); err == nil {
		t.Error("other errors must surface")
	}
}

func TestAccSwarmInit_basic(t *testing.T) {
	testAccPreCheck(t)
	info, err := testAccDockerClient(t).Info(context.Background())
	if err != nil {
		t.Skipf("cannot reach DOCKER_HOST: %v", err)
	}
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skip("DOCKER_HOST is already part of a swarm; docker_swarm_init needs a daemon outside any swarm")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			info, err := testAccDockerClient(t).Info(context.Background())
			if err != nil {
				return err
			}
			if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
				return fmt.Errorf("daemon still in swarm: %s", info.Swarm.LocalNodeState)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: `
resource "docker_swarm_init" "this" {
  advertise_addr    = "127.0.0.1"
  default_addr_pool = ["10.20.0.0/16"]
  subnet_size       = 24
}

data "docker_swarm_tokens" "t" {
  depends_on = [docker_swarm_init.this]
}

data "docker_nodes" "managers" {
  role       = "manager"
  depends_on = [docker_swarm_init.this]
}

data "docker_node" "self" {
  id = docker_swarm_init.this.node_id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_swarm_init.this", "cluster_id"),
					resource.TestCheckResourceAttrPair("docker_swarm_init.this", "id", "docker_swarm_init.this", "cluster_id"),
					resource.TestCheckResourceAttrSet("docker_swarm_init.this", "worker_join_token"),
					resource.TestCheckResourceAttr("docker_swarm_init.this", "listen_addr", swarmDefaultListenAddr),
					resource.TestCheckResourceAttrPair("data.docker_swarm_tokens.t", "worker", "docker_swarm_init.this", "worker_join_token"),
					resource.TestCheckResourceAttrPair("data.docker_swarm_tokens.t", "manager", "docker_swarm_init.this", "manager_join_token"),
					resource.TestCheckResourceAttr("data.docker_nodes.managers", "nodes.#", "1"),
					resource.TestCheckResourceAttr("data.docker_node.self", "role", "manager"),
					resource.TestCheckResourceAttr("data.docker_node.self", "leader", "true"),
				),
			},
			{
				ResourceName:            "docker_swarm_init.this",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"advertise_addr"},
			},
		},
	})
}
