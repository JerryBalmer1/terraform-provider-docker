package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// ---------------------------------------------------------------------------
// Fake Engine API used by the Lane 3 unit tests.

var swarmFakeVersionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// swarmFakeEngine is a tiny programmable Engine API. Handlers are keyed by
// "METHOD /path" with the /vX.Y prefix stripped; every call is recorded.
type swarmFakeEngine struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []string
	handlers map[string]func(w http.ResponseWriter, r *http.Request)
}

func swarmNewFakeEngine(t *testing.T) *swarmFakeEngine {
	t.Helper()
	f := &swarmFakeEngine{t: t, handlers: map[string]func(http.ResponseWriter, *http.Request){}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *swarmFakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	p := swarmFakeVersionPrefix.ReplaceAllString(r.URL.Path, "")
	if p == "/_ping" {
		w.Header().Set("Api-Version", "1.47")
		_, _ = w.Write([]byte("OK"))
		return
	}
	key := r.Method + " " + p
	f.mu.Lock()
	f.calls = append(f.calls, key)
	h := f.handlers[key]
	f.mu.Unlock()
	if h == nil {
		swarmFakeError(w, http.StatusNotImplemented, "fake: no handler for "+key)
		return
	}
	h(w, r)
}

func (f *swarmFakeEngine) handle(key string, h func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[key] = h
}

func (f *swarmFakeEngine) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *swarmFakeEngine) client(t *testing.T) *client.Client {
	t.Helper()
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + f.srv.Listener.Addr().String(), APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func swarmFakeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func swarmFakeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func swarmFastPoll(t *testing.T) {
	t.Helper()
	old := swarmPollInterval
	swarmPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { swarmPollInterval = old })
}

// swarmFakeNodeStore keeps one mutable node for the manager fake.
type swarmFakeNodeStore struct {
	mu   sync.Mutex
	node swarm.Node
}

func (s *swarmFakeNodeStore) get() swarm.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node
}

func (s *swarmFakeNodeStore) set(f func(n *swarm.Node)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.node)
}

// swarmCallIndex returns the index of the first call equal to want, or -1.
func swarmCallIndex(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Pure helpers

func TestSwarmValidation(t *testing.T) {
	for _, v := range []string{"active", "pause", "drain"} {
		if !swarmIsValidAvailability(v) {
			t.Errorf("%q should be a valid availability", v)
		}
	}
	for _, v := range []string{"", "Active", "paused", "worker"} {
		if swarmIsValidAvailability(v) {
			t.Errorf("%q should be an invalid availability", v)
		}
	}
	for _, v := range []string{"worker", "manager"} {
		if !swarmIsValidRole(v) {
			t.Errorf("%q should be a valid role", v)
		}
	}
	for _, v := range []string{"", "leader", "drain"} {
		if swarmIsValidRole(v) {
			t.Errorf("%q should be an invalid role", v)
		}
	}
}

func TestSwarmIsVersionConflict(t *testing.T) {
	if !swarmIsVersionConflict(fmt.Errorf("rpc error: code = Unknown desc = update out of sequence")) {
		t.Error("update out of sequence should be a conflict")
	}
	if swarmIsVersionConflict(fmt.Errorf("node not found")) || swarmIsVersionConflict(nil) {
		t.Error("unrelated errors are not conflicts")
	}
	if !swarmIsNotInSwarm(fmt.Errorf("This node is not part of a swarm")) {
		t.Error("not part of a swarm not detected")
	}
}

func TestSwarmBuildJoinRequest(t *testing.T) {
	r := swarmBuildJoinRequest("10.0.0.5:2377", "SWMTKN-1-x", "10.0.0.6", "")
	if r.ListenAddr != swarmDefaultListenAddr || r.AdvertiseAddr != "10.0.0.6" || r.JoinToken != "SWMTKN-1-x" ||
		len(r.RemoteAddrs) != 1 || r.RemoteAddrs[0] != "10.0.0.5:2377" {
		t.Errorf("unexpected join request: %+v", r)
	}
	r = swarmBuildJoinRequest("m:2377", "t", "", "0.0.0.0:4000")
	if r.ListenAddr != "0.0.0.0:4000" || r.AdvertiseAddr != "" {
		t.Errorf("listen addr not kept: %+v", r)
	}
}

func TestSwarmApplyNodeSpec(t *testing.T) {
	spec := swarm.NodeSpec{Role: swarm.NodeRoleWorker, Availability: swarm.NodeAvailabilityActive,
		Annotations: swarm.Annotations{Name: "keep", Labels: map[string]string{"a": "1"}}}
	if _, changed := swarmApplyNodeSpec(spec, swarmNodeSpecChange{}); changed {
		t.Error("empty change should not change")
	}
	same := map[string]string{"a": "1"}
	act := swarm.NodeAvailabilityActive
	if _, changed := swarmApplyNodeSpec(spec, swarmNodeSpecChange{Availability: &act, Labels: &same}); changed {
		t.Error("identical change should not change")
	}
	drain := swarm.NodeAvailabilityDrain
	mgr := swarm.NodeRoleManager
	labels := map[string]string{"zone": "a"}
	out, changed := swarmApplyNodeSpec(spec, swarmNodeSpecChange{Availability: &drain, Role: &mgr, Labels: &labels})
	if !changed || out.Availability != drain || out.Role != mgr || out.Labels["zone"] != "a" || len(out.Labels) != 1 || out.Name != "keep" {
		t.Errorf("unexpected spec: %+v", out)
	}
	if spec.Labels["a"] != "1" {
		t.Error("input spec was mutated")
	}
	var empty map[string]string
	out, changed = swarmApplyNodeSpec(spec, swarmNodeSpecChange{Labels: &empty})
	if !changed || len(out.Labels) != 0 {
		t.Errorf("nil labels should clear: %+v", out)
	}
}

func TestSwarmNodeChange(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	m := swarmNodeModel{
		Availability: types.StringValue("drain"),
		Role:         types.StringValue("manager"),
		Labels:       types.MapNull(types.StringType),
	}
	ch := swarmNodeChange(ctx, m, &diags)
	if diags.HasError() || ch.Availability == nil || *ch.Availability != swarm.NodeAvailabilityDrain ||
		ch.Role == nil || *ch.Role != swarm.NodeRoleManager || ch.Labels == nil || len(*ch.Labels) != 0 {
		t.Errorf("unexpected change: %+v %v", ch, diags)
	}
	m.Labels = types.MapUnknown(types.StringType)
	m.Role = types.StringUnknown()
	ch = swarmNodeChange(ctx, m, &diags)
	if ch.Labels != nil || ch.Role != nil {
		t.Errorf("unknown values must not be applied: %+v", ch)
	}
}

func TestSwarmNodeEndpoint(t *testing.T) {
	m := swarmNodeModel{Host: types.StringValue("tcp://w1:2376"),
		TLS: &tlsModel{CA: types.StringValue("ca.pem"), Cert: types.StringValue("cert.pem"), Key: types.StringValue("key.pem")}}
	ep := swarmNodeEndpoint(m, "ua")
	if ep.Host != "tcp://w1:2376" || ep.CA != "ca.pem" || ep.Cert != "cert.pem" || ep.Key != "key.pem" || ep.UserAgent != "ua" {
		t.Errorf("unexpected endpoint %+v", ep)
	}
	ep = swarmNodeEndpoint(swarmNodeModel{Host: types.StringValue("tcp://w1:2375")}, "")
	if ep.CA != "" || ep.Cert != "" || ep.Key != "" {
		t.Errorf("no tls block should give no tls: %+v", ep)
	}
}

func TestSwarmNodeFlatten(t *testing.T) {
	n := swarm.Node{ID: "n1",
		Spec:        swarm.NodeSpec{Role: swarm.NodeRoleWorker, Availability: swarm.NodeAvailabilityPause},
		Description: swarm.NodeDescription{Hostname: "w1"}}
	m := swarmNodeModel{Labels: types.MapNull(types.StringType), ListenAddr: types.StringNull()}
	swarmNodeFlatten(&m, n)
	if m.ID.ValueString() != "n1" || m.NodeID.ValueString() != "n1" || m.Hostname.ValueString() != "w1" ||
		m.Role.ValueString() != "worker" || m.Availability.ValueString() != "pause" || !m.Labels.IsNull() ||
		m.ListenAddr.ValueString() != swarmDefaultListenAddr {
		t.Errorf("unexpected model %+v", m)
	}
	m.Labels = stringMapValue(nil, false) // labels = {} in config
	swarmNodeFlatten(&m, n)
	if m.Labels.IsNull() || len(m.Labels.Elements()) != 0 {
		t.Errorf("empty labels should stay empty, got %v", m.Labels)
	}
	n.Spec.Labels = map[string]string{"zone": "a"}
	swarmNodeFlatten(&m, n)
	if len(m.Labels.Elements()) != 1 {
		t.Errorf("labels not read: %v", m.Labels)
	}
}

// ---------------------------------------------------------------------------
// Sequencing against the fake Engine API

func TestSwarmUpdateNodeRetriesVersionConflict(t *testing.T) {
	mgr := swarmNewFakeEngine(t)
	store := &swarmFakeNodeStore{node: swarm.Node{ID: "n1", Meta: swarm.Meta{Version: swarm.Version{Index: 5}},
		Spec: swarm.NodeSpec{Role: swarm.NodeRoleWorker, Availability: swarm.NodeAvailabilityActive}}}
	var versions []string
	updates := 0
	mgr.handle("GET /nodes/n1", func(w http.ResponseWriter, r *http.Request) { swarmFakeJSON(w, store.get()) })
	mgr.handle("POST /nodes/n1/update", func(w http.ResponseWriter, r *http.Request) {
		updates++
		versions = append(versions, r.URL.Query().Get("version"))
		if updates == 1 {
			// Someone else bumped the version meanwhile.
			store.set(func(n *swarm.Node) { n.Version.Index = 6 })
			swarmFakeError(w, http.StatusInternalServerError, "rpc error: code = Unknown desc = update out of sequence")
			return
		}
		var spec swarm.NodeSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		store.set(func(n *swarm.Node) { n.Spec = spec; n.Version.Index++ })
		w.WriteHeader(http.StatusOK)
	})
	drain := swarm.NodeAvailabilityDrain
	labels := map[string]string{"zone": "a"}
	err := swarmUpdateNode(context.Background(), mgr.client(t), "n1", swarmNodeSpecChange{Availability: &drain, Labels: &labels})
	if err != nil {
		t.Fatal(err)
	}
	if updates != 2 || versions[0] != "5" || versions[1] != "6" {
		t.Errorf("updates=%d versions=%v", updates, versions)
	}
	got := store.get().Spec
	if got.Availability != drain || got.Labels["zone"] != "a" {
		t.Errorf("spec not applied: %+v", got)
	}
}

func TestSwarmUpdateNodeOtherErrorFails(t *testing.T) {
	mgr := swarmNewFakeEngine(t)
	mgr.handle("GET /nodes/n1", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Node{ID: "n1", Spec: swarm.NodeSpec{Role: swarm.NodeRoleWorker}})
	})
	mgr.handle("POST /nodes/n1/update", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusBadRequest, "invalid role")
	})
	mrole := swarm.NodeRoleManager
	err := swarmUpdateNode(context.Background(), mgr.client(t), "n1", swarmNodeSpecChange{Role: &mrole})
	if err == nil || !strings.Contains(err.Error(), "invalid role") {
		t.Errorf("want invalid role error, got %v", err)
	}
	if n := strings.Count(strings.Join(mgr.callLog(), ","), "POST /nodes/n1/update"); n != 1 {
		t.Errorf("non-conflict errors must not retry, got %d updates", n)
	}
}

func TestSwarmJoinNodeSequence(t *testing.T) {
	swarmFastPoll(t)
	node := swarmNewFakeEngine(t)
	mgr := swarmNewFakeEngine(t)
	var mu sync.Mutex
	joined := false
	var got swarm.JoinRequest
	node.handle("GET /info", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !joined {
			swarmFakeJSON(w, map[string]any{"Swarm": map[string]any{"LocalNodeState": "inactive"}})
			return
		}
		swarmFakeJSON(w, map[string]any{"Swarm": map[string]any{"LocalNodeState": "active", "NodeID": "n1"}})
	})
	node.handle("POST /swarm/join", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		joined = true
		w.WriteHeader(http.StatusOK)
	})
	mgr.handle("GET /nodes/n1", func(w http.ResponseWriter, r *http.Request) { swarmFakeJSON(w, swarm.Node{ID: "n1"}) })

	req := swarmBuildJoinRequest("10.0.0.5:2377", "SWMTKN-1-abc", "10.0.0.6", "")
	id, err := swarmJoinNode(context.Background(), node.client(t), mgr.client(t), req, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if id != "n1" {
		t.Errorf("node id %q", id)
	}
	if got.JoinToken != "SWMTKN-1-abc" || len(got.RemoteAddrs) != 1 || got.RemoteAddrs[0] != "10.0.0.5:2377" ||
		got.AdvertiseAddr != "10.0.0.6" || got.ListenAddr != swarmDefaultListenAddr {
		t.Errorf("join body %+v", got)
	}
	if swarmCallIndex(mgr.callLog(), "GET /nodes/n1") < 0 {
		t.Error("manager was not asked for the joined node")
	}
}

func TestSwarmJoinNodeAdoptsExistingMember(t *testing.T) {
	node := swarmNewFakeEngine(t)
	mgr := swarmNewFakeEngine(t)
	node.handle("GET /info", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, map[string]any{"Swarm": map[string]any{"LocalNodeState": "active", "NodeID": "n9"}})
	})
	mgr.handle("GET /nodes/n9", func(w http.ResponseWriter, r *http.Request) { swarmFakeJSON(w, swarm.Node{ID: "n9"}) })
	id, err := swarmJoinNode(context.Background(), node.client(t), mgr.client(t), swarmBuildJoinRequest("m:2377", "t", "", ""), time.Second)
	if err != nil || id != "n9" {
		t.Fatalf("got %q, %v", id, err)
	}
	if swarmCallIndex(node.callLog(), "POST /swarm/join") >= 0 {
		t.Error("an existing member must not be re-joined")
	}
	// Member of another swarm: the manager does not know it.
	mgr.handle("GET /nodes/n9", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusNotFound, "node n9 not found")
	})
	if _, err := swarmJoinNode(context.Background(), node.client(t), mgr.client(t), swarmBuildJoinRequest("m:2377", "t", "", ""), time.Second); err == nil {
		t.Error("member of another swarm should fail")
	}
}

func TestSwarmLeaveThenRemoveSequence(t *testing.T) {
	swarmFastPoll(t)
	node := swarmNewFakeEngine(t)
	mgr := swarmNewFakeEngine(t)
	store := &swarmFakeNodeStore{node: swarm.Node{ID: "n1", Meta: swarm.Meta{Version: swarm.Version{Index: 3}},
		Spec:   swarm.NodeSpec{Role: swarm.NodeRoleManager, Availability: swarm.NodeAvailabilityActive},
		Status: swarm.NodeStatus{State: swarm.NodeStateReady}}}
	var mu sync.Mutex
	left := false
	inspectsAfterLeave := 0
	mgr.handle("GET /nodes/n1", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if left {
			inspectsAfterLeave++
			// The manager notices the departure after a couple of polls.
			if inspectsAfterLeave >= 3 {
				store.set(func(n *swarm.Node) { n.Status.State = swarm.NodeStateDown })
			}
		}
		mu.Unlock()
		swarmFakeJSON(w, store.get())
	})
	mgr.handle("POST /nodes/n1/update", func(w http.ResponseWriter, r *http.Request) {
		var spec swarm.NodeSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		store.set(func(n *swarm.Node) { n.Spec = spec; n.Version.Index++ })
		w.WriteHeader(http.StatusOK)
	})
	removeForce := ""
	mgr.handle("DELETE /nodes/n1", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !left {
			swarmFakeError(w, http.StatusBadRequest, "node n1 is not down and can't be removed")
			return
		}
		removeForce = r.URL.Query().Get("force")
		w.WriteHeader(http.StatusOK)
	})
	node.handle("POST /swarm/leave", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("force") != "1" {
			t.Error("leave must be forced")
		}
		left = true
		w.WriteHeader(http.StatusOK)
	})

	if err := swarmLeaveAndRemove(context.Background(), node.client(t), mgr.client(t), "n1", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	mc := mgr.callLog()
	updateAt := swarmCallIndex(mc, "POST /nodes/n1/update")
	deleteAt := swarmCallIndex(mc, "DELETE /nodes/n1")
	if updateAt < 0 || deleteAt < 0 || updateAt > deleteAt {
		t.Errorf("want demote before remove, got %v", mc)
	}
	if store.get().Spec.Role != swarm.NodeRoleWorker {
		t.Error("manager node was not demoted")
	}
	if swarmCallIndex(node.callLog(), "POST /swarm/leave") < 0 {
		t.Error("node never left")
	}
	if inspectsAfterLeave < 3 {
		t.Errorf("remove happened before the manager saw the node down (%d polls)", inspectsAfterLeave)
	}
	if removeForce != "1" {
		t.Errorf("NodeRemove must be forced, force=%q", removeForce)
	}
}

func TestSwarmLeaveThenRemoveAlreadyGone(t *testing.T) {
	node := swarmNewFakeEngine(t)
	mgr := swarmNewFakeEngine(t)
	mgr.handle("GET /nodes/n1", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusNotFound, "node n1 not found")
	})
	node.handle("POST /swarm/leave", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusServiceUnavailable, "This node is not part of a swarm")
	})
	if err := swarmLeaveAndRemove(context.Background(), node.client(t), mgr.client(t), "n1", time.Second); err != nil {
		t.Fatal(err)
	}
	if swarmCallIndex(mgr.callLog(), "DELETE /nodes/n1") >= 0 {
		t.Error("no remove expected for a node the manager no longer has")
	}
}

func TestSwarmLeaveThenRemoveUnreachableNode(t *testing.T) {
	swarmFastPoll(t)
	mgr := swarmNewFakeEngine(t)
	mgr.handle("GET /nodes/n1", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Node{ID: "n1", Spec: swarm.NodeSpec{Role: swarm.NodeRoleWorker}, Status: swarm.NodeStatus{State: swarm.NodeStateDown}})
	})
	mgr.handle("DELETE /nodes/n1", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := dead.Listener.Addr().String()
	dead.Close()
	nc, err := newDockerClient(endpointConfig{Host: "tcp://" + addr, APIVersion: "1.47"})
	if err != nil {
		t.Fatal(err)
	}
	if err := swarmLeaveAndRemove(context.Background(), nc, mgr.client(t), "n1", 5*time.Second); err != nil {
		t.Fatalf("an unreachable node should still be removed from the manager: %v", err)
	}
	if swarmCallIndex(mgr.callLog(), "DELETE /nodes/n1") < 0 {
		t.Error("node not removed")
	}
}

// ---------------------------------------------------------------------------
// Acceptance

// swarmAccManagerAddr returns "<manager ip>:2377" for the provider's host,
// skipping the test unless that host is an active swarm manager.
func swarmAccManagerAddr(t *testing.T) string {
	t.Helper()
	infoRes, err := testAccDockerClient(t).Info(context.Background(), client.InfoOptions{})
	if err != nil {
		t.Skipf("cannot reach DOCKER_HOST: %v", err)
	}
	info := infoRes.Info
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateActive || !info.Swarm.ControlAvailable {
		t.Skip("DOCKER_HOST is not a swarm manager")
	}
	if v := os.Getenv("DOCKER_SWARM_MANAGER_ADDR"); v != "" {
		return v
	}
	return info.Swarm.NodeAddr + ":2377"
}

func TestAccSwarmNode_basic(t *testing.T) {
	testAccPreCheck(t)
	worker := os.Getenv("DOCKER_WORKER1_HOST")
	if worker == "" {
		t.Skip("DOCKER_WORKER1_HOST not set; skipping multi-node swarm test")
	}
	mgrAddr := swarmAccManagerAddr(t)
	cfg := func(avail string, labels string) string {
		return fmt.Sprintf(`
data "docker_swarm_tokens" "t" {}

resource "docker_swarm_node" "w1" {
  host            = %q
  join_token      = data.docker_swarm_tokens.t.worker
  manager_address = %q
  availability    = %q
  labels          = { %s }
}
`, worker, mgrAddr, avail, labels)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             swarmAccCheckNodesGone,
		Steps: []resource.TestStep{
			{
				Config: cfg("active", `zone = "a"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_swarm_node.w1", "node_id"),
					resource.TestCheckResourceAttrSet("docker_swarm_node.w1", "hostname"),
					resource.TestCheckResourceAttr("docker_swarm_node.w1", "role", "worker"),
					resource.TestCheckResourceAttr("docker_swarm_node.w1", "labels.zone", "a"),
				),
			},
			{
				Config: cfg("drain", `zone = "b"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_swarm_node.w1", "availability", "drain"),
					resource.TestCheckResourceAttr("docker_swarm_node.w1", "labels.zone", "b"),
				),
			},
			{
				ResourceName:            "docker_swarm_node.w1",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"host", "join_token", "manager_address", "advertise_addr", "listen_addr", "tls"},
			},
		},
	})
}

func TestAccSwarmNode_twoWorkersPromote(t *testing.T) {
	testAccPreCheck(t)
	w1, w2 := os.Getenv("DOCKER_WORKER1_HOST"), os.Getenv("DOCKER_WORKER2_HOST")
	if w1 == "" || w2 == "" {
		t.Skip("DOCKER_WORKER1_HOST and DOCKER_WORKER2_HOST not set; skipping multi-node swarm test")
	}
	mgrAddr := swarmAccManagerAddr(t)
	cfg := func(role2 string) string {
		return fmt.Sprintf(`
data "docker_swarm_tokens" "t" {}

resource "docker_swarm_node" "w1" {
  host            = %q
  join_token      = data.docker_swarm_tokens.t.worker
  manager_address = %q
}

resource "docker_swarm_node" "w2" {
  host            = %q
  join_token      = data.docker_swarm_tokens.t.worker
  manager_address = %q
  role            = %q
}

data "docker_nodes" "all" {
  depends_on = [docker_swarm_node.w1, docker_swarm_node.w2]
}
`, w1, mgrAddr, w2, mgrAddr, role2)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             swarmAccCheckNodesGone,
		Steps: []resource.TestStep{
			{Config: cfg("worker"), Check: resource.TestCheckResourceAttr("docker_swarm_node.w2", "role", "worker")},
			{Config: cfg("manager"), Check: resource.TestCheckResourceAttr("docker_swarm_node.w2", "role", "manager")},
		},
	})
}

func swarmAccCheckNodesGone(s *terraform.State) error {
	c, err := newDockerClient(endpointConfig{Host: os.Getenv("DOCKER_HOST")})
	if err != nil {
		return err
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "docker_swarm_node" {
			continue
		}
		if _, err := c.NodeInspect(context.Background(), rs.Primary.ID, client.NodeInspectOptions{}); !isNotFound(err) {
			return fmt.Errorf("swarm node %s still present (err=%v)", rs.Primary.ID, err)
		}
	}
	return nil
}
