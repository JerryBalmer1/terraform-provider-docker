package provider

import (
	"testing"

	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestNodeDSFlatten(t *testing.T) {
	n := swarm.Node{
		ID:            "n1",
		Spec:          swarm.NodeSpec{Role: swarm.NodeRoleManager, Availability: swarm.NodeAvailabilityDrain, Annotations: swarm.Annotations{Labels: map[string]string{"zone": "a"}}},
		Description:   swarm.NodeDescription{Hostname: "mgr1", Engine: swarm.EngineDescription{EngineVersion: "28.5.2"}},
		Status:        swarm.NodeStatus{State: swarm.NodeStateReady, Addr: "10.0.0.5"},
		ManagerStatus: &swarm.ManagerStatus{Leader: true},
	}
	m := nodeDSFromInfo(swarmFlattenNode(n))
	if m.ID.ValueString() != "n1" || m.Hostname.ValueString() != "mgr1" || m.Role.ValueString() != "manager" ||
		m.Availability.ValueString() != "drain" || m.State.ValueString() != "ready" || m.Addr.ValueString() != "10.0.0.5" ||
		m.EngineVersion.ValueString() != "28.5.2" || !m.Leader.ValueBool() || len(m.Labels.Elements()) != 1 {
		t.Errorf("unexpected model %+v", m)
	}
	n.ManagerStatus = nil
	n.Spec.Labels = nil
	m = nodeDSFromInfo(swarmFlattenNode(n))
	if m.Leader.ValueBool() || m.Labels.IsNull() {
		t.Errorf("worker: leader=%v labels=%v (labels should be an empty map, not null)", m.Leader, m.Labels)
	}
}

func TestNodeDSRef(t *testing.T) {
	ref, a := nodeDSRef(nodeDSModel{ID: types.StringValue("abc"), Hostname: types.StringNull()})
	if ref != "abc" || a != "id" {
		t.Errorf("got %q %q", ref, a)
	}
	ref, a = nodeDSRef(nodeDSModel{ID: types.StringNull(), Hostname: types.StringValue("w1")})
	if ref != "w1" || a != "hostname" {
		t.Errorf("got %q %q", ref, a)
	}
}

func TestAccNode_byHostname(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "docker_nodes" "managers" {
  role = "manager"
}

data "docker_node" "first" {
  hostname = data.docker_nodes.managers.nodes[0].hostname
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.docker_node.first", "id", "data.docker_nodes.managers", "nodes.0.id"),
				resource.TestCheckResourceAttr("data.docker_node.first", "role", "manager"),
				resource.TestCheckResourceAttrSet("data.docker_node.first", "engine_version"),
			),
		}},
	})
}
