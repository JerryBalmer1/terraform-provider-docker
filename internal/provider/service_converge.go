package provider

// Waiting for a service to converge after create/update, and for its tasks
// to disappear after removal.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

// svcConvergeState is the outcome of one convergence check.
type svcConvergeState struct {
	Done    bool
	Running int
	Want    int
	// Fatal is set when waiting longer cannot help (update paused or
	// rolled back).
	Fatal error
	// TaskErrors are the distinct Status.Err of failed/rejected tasks.
	TaskErrors []string
}

// svcConvergeCheck decides from the service's tasks whether it converged.
// replicas is the wanted count for replicated services and -1 for global
// services, where every task the orchestrator wants running must run.
// specUpdated is the service's UpdatedAt: an UpdateStatus that started
// before it belongs to an earlier rollout and is ignored.
func svcConvergeCheck(tasks []swarm.Task, replicas int, us *swarm.UpdateStatus, specUpdated time.Time) svcConvergeState {
	st := svcConvergeState{Want: replicas}
	desired := 0
	errs := map[string]bool{}
	for _, t := range tasks {
		if t.DesiredState == swarm.TaskStateRunning {
			desired++
			if t.Status.State == swarm.TaskStateRunning {
				st.Running++
			}
		}
		switch t.Status.State {
		case swarm.TaskStateFailed, swarm.TaskStateRejected:
			msg := t.Status.Err
			if msg == "" {
				msg = t.Status.Message
			}
			if msg != "" {
				errs[fmt.Sprintf("task %s (%s): %s", svcShortID(t.ID), t.Status.State, msg)] = true
			}
		}
	}
	for e := range errs {
		st.TaskErrors = append(st.TaskErrors, e)
	}
	sort.Strings(st.TaskErrors)
	if replicas < 0 {
		st.Want = desired
	}
	if us != nil && us.StartedAt != nil && !specUpdated.IsZero() && us.StartedAt.Before(specUpdated.Add(-time.Second)) {
		us = nil
	}
	if us != nil {
		switch us.State {
		case swarm.UpdateStatePaused:
			st.Fatal = fmt.Errorf("service update paused: %s", us.Message)
			return st
		case swarm.UpdateStateRollbackStarted, swarm.UpdateStateRollbackPaused, swarm.UpdateStateRollbackCompleted:
			st.Fatal = fmt.Errorf("service update rolled back (%s): %s", us.State, us.Message)
			return st
		case swarm.UpdateStateUpdating:
			return st
		}
	}
	st.Done = st.Running == st.Want
	return st
}

func svcShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// svcWaitConverged polls until the service converges (cfg nil: no wait).
func svcWaitConverged(ctx context.Context, c *client.Client, id string, cfg *svcConvergeModel) error {
	if cfg == nil {
		return nil
	}
	timeout, err := parseDurationOr(cfg.Timeout.ValueString(), 3*time.Minute)
	if err != nil {
		return fmt.Errorf("converge_config.timeout: %w", err)
	}
	delay, err := parseDurationOr(cfg.Delay.ValueString(), 5*time.Second)
	if err != nil {
		return fmt.Errorf("converge_config.delay: %w", err)
	}
	var last svcConvergeState
	werr := waitFor(ctx, timeout, delay, func(ctx context.Context) (bool, error) {
		svc, _, err := c.ServiceInspectWithRaw(ctx, id, swarm.ServiceInspectOptions{})
		if err != nil {
			return false, err
		}
		replicas := -1
		if svc.Spec.Mode.Replicated != nil {
			replicas = 1
			if svc.Spec.Mode.Replicated.Replicas != nil {
				replicas = int(*svc.Spec.Mode.Replicated.Replicas)
			}
		}
		tasks, err := c.TaskList(ctx, swarm.TaskListOptions{Filters: filters.NewArgs(filters.Arg("service", id))})
		if err != nil {
			return false, err
		}
		last = svcConvergeCheck(tasks, replicas, svc.UpdateStatus, svc.UpdatedAt)
		if last.Fatal != nil {
			return false, svcConvergeError(last.Fatal, last)
		}
		return last.Done, nil
	})
	if werr == nil {
		return nil
	}
	if errors.Is(werr, context.DeadlineExceeded) {
		return svcConvergeError(fmt.Errorf("service %s did not converge: %d/%d tasks running: %w", id, last.Running, last.Want, werr), last)
	}
	return werr
}

func svcConvergeError(base error, st svcConvergeState) error {
	if len(st.TaskErrors) == 0 {
		return base
	}
	return fmt.Errorf("%w\ntask errors:\n  %s", base, strings.Join(st.TaskErrors, "\n  "))
}

// svcRemoveAndWait removes a service and waits until none of its tasks is
// left, so the networks, secrets and configs it used can be removed. A
// service that is already gone is not an error.
func svcRemoveAndWait(ctx context.Context, c *client.Client, id string, timeout time.Duration) error {
	if err := c.ServiceRemove(ctx, id); err != nil && !isNotFound(err) {
		return err
	}
	return waitFor(ctx, timeout, 2*time.Second, func(ctx context.Context) (bool, error) {
		// Filtering by service fails once the service is gone, so list all
		// tasks and filter here.
		tasks, err := c.TaskList(ctx, swarm.TaskListOptions{})
		if err != nil {
			return false, err
		}
		return svcTasksGone(tasks, id), nil
	})
}

// svcTasksGone reports whether no task of service id remains.
func svcTasksGone(tasks []swarm.Task, id string) bool {
	for _, t := range tasks {
		if t.ServiceID == id {
			return false
		}
	}
	return true
}
