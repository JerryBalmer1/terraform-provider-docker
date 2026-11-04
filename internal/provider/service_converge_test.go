package provider

import (
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/swarm"
)

func svcTask(id string, desired, state swarm.TaskState, errMsg string) swarm.Task {
	return swarm.Task{ID: id, ServiceID: "svc1", DesiredState: desired, Status: swarm.TaskStatus{State: state, Err: errMsg}}
}

func TestServiceConvergeCheck(t *testing.T) {
	run, shut := swarm.TaskStateRunning, swarm.TaskStateShutdown
	now := time.Now()
	earlier := now.Add(-time.Hour)
	cases := []struct {
		name     string
		tasks    []swarm.Task
		replicas int
		us       *swarm.UpdateStatus
		done     bool
		fatal    bool
		errs     int
	}{
		{"all running", []swarm.Task{svcTask("a", run, run, ""), svcTask("b", run, run, "")}, 2, nil, true, false, 0},
		{"one pending", []swarm.Task{svcTask("a", run, run, ""), svcTask("b", run, swarm.TaskStatePending, "")}, 2, nil, false, false, 0},
		{"old tasks shutting down ignored", []swarm.Task{svcTask("a", run, run, ""), svcTask("old", shut, run, "")}, 1, nil, true, false, 0},
		{"failed task reported", []swarm.Task{svcTask("a", run, swarm.TaskStateStarting, ""), svcTask("f", shut, swarm.TaskStateFailed, "exit 1")}, 1, nil, false, false, 1},
		{"rejected task reported", []swarm.Task{svcTask("r", shut, swarm.TaskStateRejected, "no such image")}, 1, nil, false, false, 1},
		{"zero replicas", nil, 0, nil, true, false, 0},
		{"global all desired running", []swarm.Task{svcTask("a", run, run, ""), svcTask("b", run, run, "")}, -1, nil, true, false, 0},
		{"global one starting", []swarm.Task{svcTask("a", run, run, ""), svcTask("b", run, swarm.TaskStateStarting, "")}, -1, nil, false, false, 0},
		{"updating not done", []swarm.Task{svcTask("a", run, run, "")}, 1, &swarm.UpdateStatus{State: swarm.UpdateStateUpdating, StartedAt: &now}, false, false, 0},
		{"update completed", []swarm.Task{svcTask("a", run, run, "")}, 1, &swarm.UpdateStatus{State: swarm.UpdateStateCompleted, StartedAt: &now}, true, false, 0},
		{"paused is fatal", []swarm.Task{svcTask("a", run, run, "")}, 1, &swarm.UpdateStatus{State: swarm.UpdateStatePaused, StartedAt: &now, Message: "update paused due to failure"}, false, true, 0},
		{"rollback is fatal", []swarm.Task{svcTask("a", run, run, "")}, 1, &swarm.UpdateStatus{State: swarm.UpdateStateRollbackCompleted, StartedAt: &now}, false, true, 0},
		{"stale rollback ignored", []swarm.Task{svcTask("a", run, run, "")}, 1, &swarm.UpdateStatus{State: swarm.UpdateStateRollbackCompleted, StartedAt: &earlier}, true, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := svcConvergeCheck(c.tasks, c.replicas, c.us, now)
			if st.Done != c.done || (st.Fatal != nil) != c.fatal || len(st.TaskErrors) != c.errs {
				t.Errorf("got %+v", st)
			}
		})
	}
}

func TestServiceConvergeErrorNamesTasks(t *testing.T) {
	st := svcConvergeCheck([]swarm.Task{
		svcTask("0123456789abcdef", swarm.TaskStateShutdown, swarm.TaskStateFailed, "task: non-zero exit (1)"),
		svcTask("fedcba9876543210", swarm.TaskStateShutdown, swarm.TaskStateFailed, "task: non-zero exit (1)"),
	}, 1, nil, time.Time{})
	err := svcConvergeError(errTest("did not converge"), st)
	if !strings.Contains(err.Error(), "0123456789ab (failed): task: non-zero exit (1)") || !strings.Contains(err.Error(), "fedcba987654") {
		t.Errorf("error = %v", err)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestServiceTasksGone(t *testing.T) {
	tasks := []swarm.Task{{ServiceID: "a"}, {ServiceID: "b"}}
	if svcTasksGone(tasks, "a") || !svcTasksGone(tasks, "c") || !svcTasksGone(nil, "a") {
		t.Error("svcTasksGone")
	}
}
