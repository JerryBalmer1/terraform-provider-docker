package provider

import (
	"context"
	"sync"
	"testing"
)

// TestContractNodeTypesWarmP6bZ: every node a cached output node reaches has its attr.Type computed before the node
// is shared (contractOutputNode -> contractWarmTypes), so concurrent reads never write n.attrType. Pass 6b found the
// race through data.docker_tasks: two data sources read in parallel, one saw a half-written MapType and panicked in
// basetypes.MapType.Equal.
func TestContractNodeTypesWarmP6bZ(t *testing.T) {
	for _, id := range []string{"swarm:tasks"} {
		it := contractMustItem(id)
		for _, o := range contractModelOutputs(it) {
			if n := contractOutputNode(it, o); !contractNodeTypesWarm(n) {
				t.Errorf("%s.%s: node tree published with uncached attr types", id, o.Name)
			}
		}
	}
}

// TestContractValuesConcurrentP6bZ converts the same task list from many goroutines at once, as Terraform does when
// it reads several docker_tasks data sources in parallel. Without the warm-up this panicked intermittently (and fails
// under -race).
func TestContractValuesConcurrentP6bZ(t *testing.T) {
	contractNodeMu.Lock()
	for k := range contractNodeCache {
		delete(contractNodeCache, k)
	}
	contractNodeMu.Unlock()
	items := []any{map[string]any{
		"ID": "t1", "ServiceID": "s1", "NodeID": "n1", "Slot": 1, "DesiredState": "running",
		"Spec":   map[string]any{"ContainerSpec": map[string]any{"Image": "nginx", "Labels": map[string]any{"a": "b"}}},
		"Status": map[string]any{"State": "running", "ContainerStatus": map[string]any{"ContainerID": "c1"}},
	}}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, diags := contractValues(context.Background(), nil, "swarm:tasks", swarmListInput("tasks", items))
			if diags.HasError() {
				t.Errorf("contractValues: %v", diags)
			}
		}()
	}
	wg.Wait()
}
