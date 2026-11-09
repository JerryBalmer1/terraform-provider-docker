package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/swarm"
)

func TestSwarmRotateTokensAction(t *testing.T) {
	f := swarmNewFakeEngine(t)
	var updates atomic.Int32
	var q map[string][]string
	f.handle("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "sw", Meta: swarm.Meta{Version: swarm.Version{Index: 7 + uint64(updates.Load())}}}})
	})
	f.handle("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) {
		// First attempt races another writer.
		if updates.Add(1) == 1 {
			swarmFakeError(w, http.StatusInternalServerError, "update out of sequence")
			return
		}
		q = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	})
	res := actInvoke(t, f, NewSwarmRotateTokensAction(), map[string]tftypes.Value{"manager": actTfBool(true)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if q["version"][0] != "8" || q["rotateWorkerToken"][0] != "true" || q["rotateManagerToken"][0] != "true" {
		t.Errorf("query %v", q)
	}
	if !actHasProgress(res, "rotated swarm worker and manager join token(s)") {
		t.Errorf("progress %q", res.Progress)
	}
	d := actValidate(t, NewSwarmRotateTokensAction(), map[string]tftypes.Value{"worker": actTfBool(false)})
	if d.ErrorsCount() != 1 {
		t.Errorf("nothing to rotate: %v", d)
	}
}

func TestSwarmRotateTokensActionNotManager(t *testing.T) {
	f := swarmNewFakeEngine(t)
	f.handle("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusServiceUnavailable, "This node is not a swarm manager.")
	})
	res := actInvoke(t, f, NewSwarmRotateTokensAction(), nil)
	if !strings.Contains(res.errText(), "not a swarm manager") {
		t.Fatalf("got %q", res.errText())
	}
}

// actFakeNode serves one node "n1" (hostname worker-1) and a task list.
type actFakeNode struct {
	mu    sync.Mutex
	node  swarm.Node
	tasks func() []swarm.Task
	specs []swarm.NodeSpec
}

func actNewFakeNode(t *testing.T, f *swarmFakeEngine, avail swarm.NodeAvailability) *actFakeNode {
	t.Helper()
	n := &actFakeNode{node: swarm.Node{ID: "n1abcdef", Meta: swarm.Meta{Version: swarm.Version{Index: 1}},
		Spec:        swarm.NodeSpec{Role: swarm.NodeRoleWorker, Availability: avail},
		Description: swarm.NodeDescription{Hostname: "worker-1"}}}
	n.tasks = func() []swarm.Task { return nil }
	f.handle("GET /nodes", func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		swarmFakeJSON(w, []swarm.Node{n.node})
	})
	f.handle("GET /nodes/n1abcdef", func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		swarmFakeJSON(w, n.node)
	})
	f.handle("POST /nodes/n1abcdef/update", func(w http.ResponseWriter, r *http.Request) {
		var spec swarm.NodeSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			t.Error(err)
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		n.specs = append(n.specs, spec)
		n.node.Spec = spec
		n.node.Version.Index++
		w.WriteHeader(http.StatusOK)
	})
	f.handle("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, n.tasks())
	})
	return n
}

func actTask(id, node string, state swarm.TaskState) swarm.Task {
	return swarm.Task{ID: id, NodeID: node, DesiredState: swarm.TaskStateShutdown, Status: swarm.TaskStatus{State: state}}
}

func TestNodeDrainAction(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	n := actNewFakeNode(t, f, swarm.NodeAvailabilityActive)
	var polls atomic.Int32
	n.tasks = func() []swarm.Task {
		switch polls.Add(1) {
		case 1:
			return []swarm.Task{actTask("t1", "n1abcdef", swarm.TaskStateRunning), actTask("t2", "n1abcdef", swarm.TaskStateRunning)}
		case 2:
			return []swarm.Task{actTask("t1", "n1abcdef", swarm.TaskStateShutdown), actTask("t2", "n1abcdef", swarm.TaskStateRunning)}
		}
		return []swarm.Task{actTask("t1", "n1abcdef", swarm.TaskStateShutdown), actTask("t2", "n1abcdef", swarm.TaskStateComplete)}
	}
	res := actInvoke(t, f, NewNodeDrainAction(), map[string]tftypes.Value{"node_id": actTfStr("worker-1")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(n.specs) != 1 || n.specs[0].Availability != swarm.NodeAvailabilityDrain || n.specs[0].Role != swarm.NodeRoleWorker {
		t.Errorf("node updates %+v", n.specs)
	}
	for _, want := range []string{"2 task(s) still running", "1 task(s) still running", "0 task(s) still running", "drained"} {
		if !actHasProgress(res, want) {
			t.Errorf("progress lacks %q: %q", want, res.Progress)
		}
	}
}

func TestNodeDrainActionTimeout(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	n := actNewFakeNode(t, f, swarm.NodeAvailabilityActive)
	n.tasks = func() []swarm.Task {
		return []swarm.Task{actTask("stuck1234567890", "n1abcdef", swarm.TaskStateRunning)}
	}
	res := actInvoke(t, f, NewNodeDrainAction(), map[string]tftypes.Value{"node_id": actTfStr("n1abcdef"), "timeout": actTfStr("100ms")})
	if !strings.Contains(res.errText(), "1 task(s) were still running after 100ms: stuck1234567") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestNodeDrainActionNoWait(t *testing.T) {
	f := swarmNewFakeEngine(t)
	n := actNewFakeNode(t, f, swarm.NodeAvailabilityActive)
	n.tasks = func() []swarm.Task {
		t.Error("tasks listed with wait = false")
		return nil
	}
	res := actInvoke(t, f, NewNodeDrainAction(), map[string]tftypes.Value{"node_id": actTfStr("worker-1"), "wait": actTfBool(false)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
}

func TestNodeActivateAction(t *testing.T) {
	f := swarmNewFakeEngine(t)
	n := actNewFakeNode(t, f, swarm.NodeAvailabilityDrain)
	res := actInvoke(t, f, NewNodeActivateAction(), map[string]tftypes.Value{"node_id": actTfStr("worker-1")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(n.specs) != 1 || n.specs[0].Availability != swarm.NodeAvailabilityActive {
		t.Errorf("node updates %+v", n.specs)
	}
	// Already active: no update is sent.
	res = actInvoke(t, f, NewNodeActivateAction(), map[string]tftypes.Value{"node_id": actTfStr("worker-1")})
	if res.Diags.HasError() || len(n.specs) != 1 {
		t.Errorf("second activate: %s, %d updates", res.errText(), len(n.specs))
	}
	res = actInvoke(t, f, NewNodeActivateAction(), map[string]tftypes.Value{"node_id": actTfStr("nope")})
	if !strings.Contains(res.errText(), "no node named") {
		t.Errorf("unknown node: %q", res.errText())
	}
}

func TestActTaskActive(t *testing.T) {
	for st, want := range map[swarm.TaskState]bool{
		swarm.TaskStateRunning: true, swarm.TaskStateStarting: true, swarm.TaskStatePending: true,
		swarm.TaskStateShutdown: false, swarm.TaskStateComplete: false, swarm.TaskStateFailed: false, swarm.TaskStateRejected: false,
	} {
		if got := actTaskActive(swarm.Task{Status: swarm.TaskStatus{State: st}}); got != want {
			t.Errorf("%s: got %v", st, got)
		}
	}
}
