package provider

import (
	"testing"

	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestNodesDSListOptions(t *testing.T) {
	if got := nodesDSListOptions("").Filters.Len(); got != 0 {
		t.Errorf("no role should give no filter, got %d", got)
	}
	if got := nodesDSListOptions("manager").Filters.Get("role"); len(got) != 1 || got[0] != "manager" {
		t.Errorf("role filter: %v", got)
	}
}

func TestNodesDSListValueSorted(t *testing.T) {
	nodes := []swarm.Node{
		{ID: "z", Description: swarm.NodeDescription{Hostname: "worker2"}},
		{ID: "b", Description: swarm.NodeDescription{Hostname: "manager1"}, ManagerStatus: &swarm.ManagerStatus{Leader: true}},
		{ID: "a", Description: swarm.NodeDescription{Hostname: "worker2"}},
	}
	l := nodesDSListValue(nodes)
	if len(l.Elements()) != 3 {
		t.Fatalf("got %d elements", len(l.Elements()))
	}
	var ids []string
	for _, e := range l.Elements() {
		ids = append(ids, e.(types.Object).Attributes()["id"].(types.String).ValueString())
	}
	if ids[0] != "b" || ids[1] != "a" || ids[2] != "z" {
		t.Errorf("unexpected order %v", ids)
	}
	if !l.Elements()[0].(types.Object).Attributes()["leader"].(types.Bool).ValueBool() {
		t.Error("leader not flattened")
	}
	if empty := nodesDSListValue(nil); empty.IsNull() || len(empty.Elements()) != 0 {
		t.Error("no nodes should be an empty list")
	}
}

func TestAccNodes_all(t *testing.T) {
	testAccPreCheck(t)
	swarmAccManagerAddr(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `data "docker_nodes" "all" {}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.docker_nodes.all", "nodes.0.id"),
				resource.TestCheckResourceAttrSet("data.docker_nodes.all", "nodes.0.state"),
			),
		}},
	})
}
