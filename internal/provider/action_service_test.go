package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// actFakeService serves one service "s1" named "web".
type actFakeService struct {
	mu      sync.Mutex
	svc     swarm.Service
	updates []actFakeUpdate
	// onUpdate runs after each accepted update, under mu.
	onUpdate func(s *actFakeService, rollback bool)
	// tasks returns the task list for the current state, under mu.
	tasks    func(s *actFakeService) []swarm.Task
	conflict atomic.Int32
}

type actFakeUpdate struct {
	Query map[string][]string
	Auth  string
	Spec  swarm.ServiceSpec
}

func actNewFakeService(t *testing.T, f *swarmFakeEngine, replicas uint64) *actFakeService {
	t.Helper()
	s := &actFakeService{}
	s.svc = swarm.Service{ID: "s1abcdef", Meta: swarm.Meta{Version: swarm.Version{Index: 10}, UpdatedAt: time.Now().Add(-time.Hour)},
		Spec: swarm.ServiceSpec{
			Annotations:  swarm.Annotations{Name: "web"},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "ghcr.io/acme/web:2"}},
			Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
		}}
	s.tasks = func(s *actFakeService) []swarm.Task {
		n := int(*s.svc.Spec.Mode.Replicated.Replicas)
		out := make([]swarm.Task, n)
		for i := range out {
			out[i] = swarm.Task{ID: "t", ServiceID: s.svc.ID, DesiredState: swarm.TaskStateRunning, Status: swarm.TaskStatus{State: swarm.TaskStateRunning}}
		}
		return out
	}
	f.handle("GET /services", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		swarmFakeJSON(w, []swarm.Service{s.svc})
	})
	f.handle("GET /services/s1abcdef", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		swarmFakeJSON(w, s.svc)
	})
	f.handle("POST /services/s1abcdef/update", func(w http.ResponseWriter, r *http.Request) {
		if s.conflict.Add(-1) >= 0 {
			swarmFakeError(w, http.StatusInternalServerError, "rpc error: code = Unknown desc = update out of sequence")
			return
		}
		var spec swarm.ServiceSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Query().Get("version") != "10" && r.URL.Query().Get("version") != "11" {
			t.Errorf("stale version %s", r.URL.Query().Get("version"))
		}
		s.updates = append(s.updates, actFakeUpdate{Query: r.URL.Query(), Auth: r.Header.Get("X-Registry-Auth"), Spec: spec})
		rollback := r.URL.Query().Get("rollback") == "previous"
		if rollback {
			prev := s.svc.Spec
			s.svc.Spec = *s.svc.PreviousSpec
			s.svc.PreviousSpec = &prev
		} else {
			prev := s.svc.Spec
			s.svc.PreviousSpec = &prev
			s.svc.Spec = spec
		}
		s.svc.Version.Index++
		s.svc.UpdatedAt = time.Now()
		if s.onUpdate != nil {
			s.onUpdate(s, rollback)
		}
		swarmFakeJSON(w, swarm.ServiceUpdateResponse{Warnings: []string{"image could not be accessed on a registry"}})
	})
	f.handle("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		swarmFakeJSON(w, s.tasks(s))
	})
	return s
}

func (s *actFakeService) setStatus(state swarm.UpdateState) {
	now := time.Now()
	s.svc.UpdateStatus = &swarm.UpdateStatus{State: state, StartedAt: &now}
}

func actServicePD(t *testing.T, f *swarmFakeEngine) *providerData {
	return &providerData{Client: f.client(t), RegistryAuth: []registryCredential{{Address: "ghcr.io", Username: "u", Password: "p"}}}
}

func TestServiceScaleAction(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 1)
	s.conflict.Store(1)
	var polls atomic.Int32
	s.tasks = func(s *actFakeService) []swarm.Task {
		// Report one running task for the first polls, then all three.
		n := 1
		if polls.Add(1) > 2 {
			n = int(*s.svc.Spec.Mode.Replicated.Replicas)
		}
		out := []swarm.Task{}
		for i := 0; i < n; i++ {
			out = append(out, swarm.Task{DesiredState: swarm.TaskStateRunning, Status: swarm.TaskStatus{State: swarm.TaskStateRunning}})
		}
		return out
	}
	res := actInvokePD(t, actServicePD(t, f), NewServiceScaleAction(), map[string]tftypes.Value{"service": actTfStr("web"), "replicas": actTfNum(3)})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(s.updates) != 1 || *s.updates[0].Spec.Mode.Replicated.Replicas != 3 || s.updates[0].Spec.TaskTemplate.ContainerSpec.Image != "ghcr.io/acme/web:2" {
		t.Fatalf("updates %+v", s.updates)
	}
	if a, err := registry.DecodeAuthConfig(s.updates[0].Auth); err != nil || a.Username != "u" {
		t.Errorf("registry auth not sent: %v %v", a, err)
	}
	if polls.Load() < 3 {
		t.Errorf("did not wait for convergence: %d polls", polls.Load())
	}
	if len(res.Diags.Warnings()) != 1 {
		t.Errorf("daemon warning not surfaced: %v", res.Diags)
	}
	if !actHasProgress(res, "converged with 3 replicas") {
		t.Errorf("progress %q", res.Progress)
	}
}

func TestServiceScaleActionGlobal(t *testing.T) {
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 1)
	s.svc.Spec.Mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	res := actInvoke(t, f, NewServiceScaleAction(), map[string]tftypes.Value{"service": actTfStr("web"), "replicas": actTfNum(2)})
	if !strings.Contains(res.errText(), "not in replicated mode") || len(s.updates) != 0 {
		t.Fatalf("got %q, %d updates", res.errText(), len(s.updates))
	}
	d := actValidate(t, NewServiceScaleAction(), map[string]tftypes.Value{"service": actTfStr("web"), "replicas": actTfNum(-1)})
	if d.ErrorsCount() != 1 {
		t.Errorf("negative replicas: %v", d)
	}
}

func TestServiceScaleActionTimeout(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 1)
	s.tasks = func(s *actFakeService) []swarm.Task { return nil }
	res := actInvoke(t, f, NewServiceScaleAction(), map[string]tftypes.Value{"service": actTfStr("web"), "replicas": actTfNum(2), "timeout": actTfStr("100ms")})
	if !strings.Contains(res.errText(), "did not converge: 0/2 tasks running") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestServiceForceUpdateAction(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 2)
	s.svc.Spec.TaskTemplate.ForceUpdate = 4
	// A completed rollout from long ago must not satisfy the wait.
	old := time.Now().Add(-2 * time.Hour)
	s.svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateCompleted, StartedAt: &old}
	var polls atomic.Int32
	s.onUpdate = func(s *actFakeService, _ bool) { polls.Store(0) }
	f.handle("GET /services/s1abcdef", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.updates) > 0 {
			switch p := polls.Add(1); {
			case p == 2:
				s.setStatus(swarm.UpdateStateUpdating)
			case p == 5:
				s.setStatus(swarm.UpdateStateCompleted)
			}
		}
		swarmFakeJSON(w, s.svc)
	})
	res := actInvoke(t, f, NewServiceForceUpdateAction(), map[string]tftypes.Value{"service": actTfStr("s1abcdef")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(s.updates) != 1 || s.updates[0].Spec.TaskTemplate.ForceUpdate != 5 {
		t.Fatalf("updates %+v", s.updates)
	}
	if polls.Load() < 5 {
		t.Errorf("returned before the rollout completed: %d polls", polls.Load())
	}
	if !actHasProgress(res, "force update 5 started") || !actHasProgress(res, "redeployed and converged") {
		t.Errorf("progress %q", res.Progress)
	}
}

func TestServiceForceUpdateActionPaused(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 1)
	s.onUpdate = func(s *actFakeService, _ bool) {
		now := time.Now()
		s.svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStatePaused, StartedAt: &now, Message: "update paused due to failure"}
	}
	res := actInvoke(t, f, NewServiceForceUpdateAction(), map[string]tftypes.Value{"service": actTfStr("web")})
	if !strings.Contains(res.errText(), "update paused due to failure") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestServiceRollbackAction(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 2)
	prev := s.svc.Spec
	prev.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{Image: "ghcr.io/acme/web:1"}
	s.svc.PreviousSpec = &prev
	var polls atomic.Int32
	s.onUpdate = func(s *actFakeService, rollback bool) {
		if !rollback {
			t.Error("rollback flag not sent")
		}
		s.setStatus(swarm.UpdateStateRollbackStarted)
	}
	f.handle("GET /services/s1abcdef", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.updates) > 0 && polls.Add(1) == 4 {
			s.setStatus(swarm.UpdateStateRollbackCompleted)
		}
		swarmFakeJSON(w, s.svc)
	})
	res := actInvokePD(t, actServicePD(t, f), NewServiceRollbackAction(), map[string]tftypes.Value{"service": actTfStr("web")})
	if res.Diags.HasError() {
		t.Fatal(res.errText())
	}
	if len(s.updates) != 1 || s.updates[0].Query["rollback"][0] != "previous" {
		t.Fatalf("updates %+v", s.updates)
	}
	if s.svc.Spec.TaskTemplate.ContainerSpec.Image != "ghcr.io/acme/web:1" {
		t.Errorf("fake did not roll back: %s", s.svc.Spec.TaskTemplate.ContainerSpec.Image)
	}
	if polls.Load() < 4 {
		t.Errorf("returned before rollback_completed: %d polls", polls.Load())
	}
	if !actHasProgress(res, "rolled back and converged") {
		t.Errorf("progress %q", res.Progress)
	}
}

func TestServiceRollbackActionNoPrevious(t *testing.T) {
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 1)
	res := actInvoke(t, f, NewServiceRollbackAction(), map[string]tftypes.Value{"service": actTfStr("web")})
	if !strings.Contains(res.errText(), "no previous spec") || len(s.updates) != 0 {
		t.Fatalf("got %q", res.errText())
	}
}

func TestServiceRollbackActionPaused(t *testing.T) {
	actFastPoll(t)
	f := swarmNewFakeEngine(t)
	s := actNewFakeService(t, f, 1)
	prev := s.svc.Spec
	s.svc.PreviousSpec = &prev
	s.onUpdate = func(s *actFakeService, _ bool) {
		now := time.Now()
		s.svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateRollbackPaused, StartedAt: &now, Message: "rollback paused"}
	}
	res := actInvoke(t, f, NewServiceRollbackAction(), map[string]tftypes.Value{"service": actTfStr("web")})
	if !strings.Contains(res.errText(), "rollback paused") {
		t.Fatalf("got %q", res.errText())
	}
}

func TestActRollbackCheck(t *testing.T) {
	r := uint64(1)
	now := time.Now()
	svc := swarm.Service{Meta: swarm.Meta{UpdatedAt: now}, Spec: swarm.ServiceSpec{Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &r}}}}
	running := []swarm.Task{{DesiredState: swarm.TaskStateRunning, Status: swarm.TaskStatus{State: swarm.TaskStateRunning}}}
	if done, _ := actRollbackCheck(svc, running, false); done {
		t.Error("no status, grace not over: must wait")
	}
	if done, _ := actRollbackCheck(svc, running, true); !done {
		t.Error("no status after grace with tasks running: done")
	}
	svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateRollbackCompleted, StartedAt: &now}
	if done, _ := actRollbackCheck(svc, nil, false); done {
		t.Error("completed but no tasks running: not done")
	}
	if done, _ := actRollbackCheck(svc, running, false); !done {
		t.Error("completed and running: done")
	}
}
