package provider

// Pass 5 Lane B: unit tests of the new swarm/node actions (promote, demote,
// unlock, rotate unlock key, rotate CA) and of the result object every
// swarm action reports (result_path, progress message).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/swarm"
)

// actReadResult decodes a result_path file.
func actReadResult(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return m
}

func TestNodePromoteDemoteActions(t *testing.T) {
	f := swarmNewFakeEngine(t)
	n := actNewFakeNode(t, f, swarm.NodeAvailabilityActive)
	out := filepath.Join(t.TempDir(), "promote.json")
	res := actInvoke(t, f, NewNodePromoteAction(), map[string]tftypes.Value{"node_id": actTfStr("worker-1"), "result_path": actTfStr(out)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(n.specs) != 1 || n.specs[0].Role != swarm.NodeRoleManager {
		t.Fatalf("specs %+v", n.specs)
	}
	got := actReadResult(t, out)
	result, _ := got["result"].(map[string]any)
	spec, _ := result["spec"].(map[string]any)
	if got["node_id"] != "n1abcdef" || got["hostname"] != "worker-1" || spec["role"] != "manager" || got["version_index"] != float64(2) {
		t.Errorf("result object = %v", got)
	}
	if got["docker_server_version"] != "29.0.0-fake" {
		t.Errorf("version outputs = %v", got)
	}
	if !actHasProgress(res, "docker_node_promote result:") {
		t.Errorf("progress %q", res.Progress)
	}

	// Demote back; already-worker demote sends nothing.
	res = actInvoke(t, f, NewNodeDemoteAction(), map[string]tftypes.Value{"node_id": actTfStr("n1abcdef")})
	if res.Diags.HasError() || len(n.specs) != 2 || n.specs[1].Role != swarm.NodeRoleWorker {
		t.Fatalf("demote: %s %+v", res.errText(), n.specs)
	}
	res = actInvoke(t, f, NewNodeDemoteAction(), map[string]tftypes.Value{"node_id": actTfStr("n1abcdef")})
	if res.Diags.HasError() || len(n.specs) != 2 {
		t.Fatalf("idempotent demote: %s %+v", res.errText(), n.specs)
	}

	// Unknown node.
	res = actInvoke(t, f, NewNodeDemoteAction(), map[string]tftypes.Value{"node_id": actTfStr("nope")})
	if !res.Diags.HasError() {
		t.Error("unknown node should fail")
	}
}

func TestNodeDemoteLastManagerError(t *testing.T) {
	f := swarmNewFakeEngine(t)
	n := actNewFakeNode(t, f, swarm.NodeAvailabilityActive)
	n.node.Spec.Role = swarm.NodeRoleManager
	f.handle("POST /nodes/n1abcdef/update", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeError(w, http.StatusBadRequest, "attempting to demote the last manager of the swarm")
	})
	res := actInvoke(t, f, NewNodeDemoteAction(), map[string]tftypes.Value{"node_id": actTfStr("worker-1")})
	if !strings.Contains(res.errText(), "last manager") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestSwarmRotateTokensResult(t *testing.T) {
	f := swarmNewFakeEngine(t)
	f.handle("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "sw", Meta: swarm.Meta{Version: swarm.Version{Index: 9}}},
			JoinTokens: swarm.JoinTokens{Worker: "SWMTKN-new-w", Manager: "SWMTKN-new-m"}})
	})
	f.handle("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	out := filepath.Join(t.TempDir(), "sub", "tokens.json")
	res := actInvoke(t, f, NewSwarmRotateTokensAction(), map[string]tftypes.Value{"result_path": actTfStr(out)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	got := actReadResult(t, out)
	if got["worker_join_token"] != "SWMTKN-new-w" || got["id"] != "sw" {
		t.Errorf("result file = %v", got)
	}
	for _, p := range res.Progress {
		if strings.Contains(p, "SWMTKN-new") {
			t.Errorf("token leaked into progress: %q", p)
		}
	}
}

func TestSwarmRotateUnlockKeyAction(t *testing.T) {
	f := swarmNewFakeEngine(t)
	var mu sync.Mutex
	autolock := false
	var rotated string
	f.handle("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "sw", Meta: swarm.Meta{Version: swarm.Version{Index: 3}}}}
		sw.Spec.EncryptionConfig.AutoLockManagers = autolock
		swarmFakeJSON(w, sw)
	})
	f.handle("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) {
		rotated = r.URL.Query().Get("rotateManagerUnlockKey")
		w.WriteHeader(http.StatusOK)
	})
	f.handle("GET /swarm/unlockkey", func(w http.ResponseWriter, r *http.Request) {
		swarmFakeJSON(w, map[string]string{"UnlockKey": "SWMKEY-1-new"})
	})
	res := actInvoke(t, f, NewSwarmRotateUnlockKeyAction(), nil)
	if !strings.Contains(res.errText(), "autolock is off") || rotated != "" {
		t.Fatalf("autolock off: %q rotated=%q", res.errText(), rotated)
	}
	mu.Lock()
	autolock = true
	mu.Unlock()
	out := filepath.Join(t.TempDir(), "key.json")
	res = actInvoke(t, f, NewSwarmRotateUnlockKeyAction(), map[string]tftypes.Value{"result_path": actTfStr(out)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	got := actReadResult(t, out)
	if rotated != "true" || got["unlock_key"] != "SWMKEY-1-new" {
		t.Errorf("rotated=%q result=%v", rotated, got)
	}
	for _, p := range res.Progress {
		if strings.Contains(p, "SWMKEY-1-new") {
			t.Errorf("unlock key leaked into progress: %q", p)
		}
	}
}

func TestSwarmRotateCAAction(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	var mu sync.Mutex
	polls := 0
	var sent swarm.Spec
	f.handle("GET /swarm", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		sw := swarm.Swarm{ClusterInfo: swarm.ClusterInfo{ID: "sw", Meta: swarm.Meta{Version: swarm.Version{Index: uint64(polls)}},
			TLSInfo:                swarm.TLSInfo{TrustRoot: "-----BEGIN CERTIFICATE-----", CertIssuerSubject: []byte("subject")},
			RootRotationInProgress: polls < 4}}
		sw.Spec.CAConfig.ForceRotate = 2
		swarmFakeJSON(w, sw)
	})
	f.handle("POST /swarm/update", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.WriteHeader(http.StatusOK)
	})
	out := filepath.Join(t.TempDir(), "ca.json")
	res := actInvoke(t, f, NewSwarmRotateCaAction(), map[string]tftypes.Value{"result_path": actTfStr(out)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if sent.CAConfig.ForceRotate != 3 || sent.CAConfig.SigningCACert != "" {
		t.Errorf("sent CA config %+v", sent.CAConfig)
	}
	got := actReadResult(t, out)
	result, _ := got["result"].(map[string]any)
	tls, _ := result["tls_info"].(map[string]any)
	if result["root_rotation_in_progress"] != false || tls["trust_root"] != "-----BEGIN CERTIFICATE-----" {
		t.Errorf("result = %v", got)
	}
	if !actHasProgress(res, "rotation finished") {
		t.Errorf("progress %q", res.Progress)
	}

	// External CA: both halves required.
	d := actValidate(t, NewSwarmRotateCaAction(), map[string]tftypes.Value{"signing_ca_cert": actTfStr("PEM")})
	if d.ErrorsCount() != 1 {
		t.Errorf("cert without key: %v", d)
	}
	res = actInvoke(t, f, NewSwarmRotateCaAction(), map[string]tftypes.Value{
		"signing_ca_cert": actTfStr("CERT"), "signing_ca_key": actTfStr("KEY"), "wait": actTfBool(false)})
	if res.Diags.HasError() || sent.CAConfig.SigningCACert != "CERT" || sent.CAConfig.SigningCAKey != "KEY" {
		t.Errorf("external CA: %s %+v", res.errText(), sent.CAConfig)
	}
}

func TestSwarmUnlockAction(t *testing.T) {
	f := swarmNewFakeEngine(t)
	var mu sync.Mutex
	state := "locked"
	var gotKey string
	f.handle("GET /info", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		swarmFakeJSON(w, map[string]any{"Swarm": map[string]any{"LocalNodeState": state, "NodeID": "n1", "NodeAddr": "10.0.0.5", "ControlAvailable": state == "active"}})
	})
	f.handle("POST /swarm/unlock", func(w http.ResponseWriter, r *http.Request) {
		var req swarm.UnlockRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		gotKey = req.UnlockKey
		if req.UnlockKey != "SWMKEY-1-ok" {
			swarmFakeError(w, http.StatusBadRequest, "invalid key")
			return
		}
		state = "active"
		w.WriteHeader(http.StatusOK)
	})
	res := actInvoke(t, f, NewSwarmUnlockAction(), map[string]tftypes.Value{"unlock_key": actTfStr("SWMKEY-1-bad")})
	if !strings.Contains(res.errText(), "invalid key") {
		t.Fatalf("bad key: %q", res.errText())
	}
	out := filepath.Join(t.TempDir(), "unlock.json")
	res = actInvoke(t, f, NewSwarmUnlockAction(), map[string]tftypes.Value{"unlock_key": actTfStr("SWMKEY-1-ok"), "result_path": actTfStr(out)})
	if res.Diags.HasError() || gotKey != "SWMKEY-1-ok" {
		t.Fatalf("%s key=%q", res.errText(), gotKey)
	}
	got := actReadResult(t, out)
	result, _ := got["result"].(map[string]any)
	si, _ := result["swarm_info"].(map[string]any)
	if got["node_id"] != "n1" || si["local_node_state"] != "active" || si["control_available"] != true {
		t.Errorf("result = %v", got)
	}
	if strings.Contains(strings.Join(res.Progress, "\n"), "SWMKEY") {
		t.Errorf("key leaked: %q", res.Progress)
	}
	// Already unlocked: no call, success.
	gotKey = ""
	res = actInvoke(t, f, NewSwarmUnlockAction(), map[string]tftypes.Value{"unlock_key": actTfStr("x")})
	if res.Diags.HasError() || gotKey != "" || !actHasProgress(res, "not locked") {
		t.Errorf("unlocked: %s key=%q %q", res.errText(), gotKey, res.Progress)
	}
	// Not in a swarm.
	mu.Lock()
	state = "inactive"
	mu.Unlock()
	if res = actInvoke(t, f, NewSwarmUnlockAction(), map[string]tftypes.Value{"unlock_key": actTfStr("x")}); !res.Diags.HasError() {
		t.Error("inactive node should fail")
	}
}

func TestServiceActionsResult(t *testing.T) {
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 1)
	s.svc.Spec.TaskTemplate.ContainerSpec.Image = "ghcr.io/acme/web:2@sha256:" + repeatA(64)
	out := filepath.Join(t.TempDir(), "scale.json")
	res := actInvoke(t, f, NewServiceScaleAction(), map[string]tftypes.Value{
		"service_id": actTfStr("web"), "replicas": actTfNum(4), "wait": actTfBool(false), "result_path": actTfStr(out)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	got := actReadResult(t, out)
	result, _ := got["result"].(map[string]any)
	mode, _ := result["mode"].(map[string]any)
	repl, _ := mode["replicated"].(map[string]any)
	if got["service_id"] != "s1abcdef" || got["name"] != "web" || repl["replicas"] != float64(4) {
		t.Errorf("scale result = %v", got)
	}
	if ur, _ := result["update_response"].(map[string]any); ur == nil {
		t.Errorf("update_response missing: %v", result)
	}

	out = filepath.Join(t.TempDir(), "force.json")
	res = actInvoke(t, f, NewServiceForceUpdateAction(), map[string]tftypes.Value{"service_id": actTfStr("web"), "wait": actTfBool(false), "result_path": actTfStr(out)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	got = actReadResult(t, out)
	result, _ = got["result"].(map[string]any)
	if result["force_update"] != float64(1) {
		t.Errorf("force result = %v", got)
	}
}
