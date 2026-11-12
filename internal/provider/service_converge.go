package provider

// Waiting for a service to converge after create/update, and for its tasks
// to disappear after removal.

import (
	"context"
	"errors"
	"fmt"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"sort"
	"strings"
	"time"
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
	// Job is set for job modes: Running then counts completed tasks.
	Job bool
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
	// A global service reports 0/0 before the orchestrator creates its first
	// task; don't call that converged.
	st.Done = st.Running == st.Want && (replicas >= 0 || st.Want > 0)
	return st
}

// svcJobConvergeCheck decides whether a job-mode service finished its
// current run (tasks of svc.JobStatus.JobIteration; all tasks when the
// daemon reports no JobStatus). A replicated job is done when
// TotalCompletions tasks completed (default MaxConcurrent, default 1); a
// global job when every task is in a terminal state and at least one
// completed. Failed tasks are not fatal (the restart policy may retry them)
// but their errors are reported on timeout.
func svcJobConvergeCheck(tasks []swarm.Task, svc swarm.Service) svcConvergeState {
	st := svcConvergeState{Job: true}
	var iter *uint64
	if svc.JobStatus != nil {
		i := svc.JobStatus.JobIteration.Index
		iter = &i
	}
	errs := map[string]bool{}
	current, pending := 0, 0
	for _, t := range tasks {
		if iter != nil && (t.JobIteration == nil || t.JobIteration.Index != *iter) {
			continue
		}
		current++
		switch t.Status.State {
		case swarm.TaskStateComplete:
			st.Running++
		case swarm.TaskStateFailed, swarm.TaskStateRejected:
			msg := t.Status.Err
			if msg == "" {
				msg = t.Status.Message
			}
			if msg != "" {
				errs[fmt.Sprintf("task %s (%s): %s", svcShortID(t.ID), t.Status.State, msg)] = true
			}
		case swarm.TaskStateShutdown, swarm.TaskStateOrphaned, swarm.TaskStateRemove:
		default:
			pending++
		}
	}
	for e := range errs {
		st.TaskErrors = append(st.TaskErrors, e)
	}
	sort.Strings(st.TaskErrors)
	if j := svc.Spec.Mode.ReplicatedJob; j != nil {
		want := uint64(1)
		if j.MaxConcurrent != nil {
			want = *j.MaxConcurrent
		}
		if j.TotalCompletions != nil {
			want = *j.TotalCompletions
		}
		st.Want = int(want)
		st.Done = st.Running >= st.Want
		return st
	}
	st.Want = current
	st.Done = current > 0 && pending == 0 && st.Running > 0
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
	// One deadline for both waits: first let swarm finish applying the spec
	// (so the task check below does not pass on the old tasks before the
	// rollout starts), then wait for the tasks.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	settleEvery := svcSettleMinInterval
	if delay > 0 && delay < settleEvery {
		settleEvery = delay
	}
	if err := svcWaitSettled(ctx, c, id, timeout, settleEvery); err != nil {
		return err
	}
	var last svcConvergeState
	werr := waitFor(ctx, timeout, delay, func(ctx context.Context) (bool, error) {
		svcRes, err := c.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
		if err != nil {
			return false, err
		}
		svc := svcRes.Service
		if svc.Spec.Mode.ReplicatedJob != nil || svc.Spec.Mode.GlobalJob != nil {
			tasksRes, err := c.TaskList(ctx, client.TaskListOptions{Filters: client.Filters{}.Add("service", id)})
			if err != nil {
				return false, err
			}
			tasks := tasksRes.Items
			last = svcJobConvergeCheck(tasks, svc)
			return last.Done, nil
		}
		replicas := -1
		if svc.Spec.Mode.Replicated != nil {
			replicas = 1
			if svc.Spec.Mode.Replicated.Replicas != nil {
				replicas = int(*svc.Spec.Mode.Replicated.Replicas)
			}
		}
		tasksRes, err := c.TaskList(ctx, client.TaskListOptions{Filters: client.Filters{}.Add("service", id)})
		if err != nil {
			return false, err
		}
		tasks := tasksRes.Items
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
		what := "running"
		if last.Job {
			what = "completed"
		}
		return svcConvergeError(fmt.Errorf("service %s did not converge: %d/%d tasks %s: %w", id, last.Running, last.Want, what, werr), last)
	}
	return werr
}

const (
	// svcSettleTimeout bounds svcWaitSettled for a service without
	// converge_config (with one, its timeout applies).
	svcSettleTimeout = 3 * time.Minute
	// svcSettleMinInterval is the first gap between two inspects; it doubles
	// up to svcSettleMaxInterval. Swarm starts a rollout a few milliseconds
	// after ServiceUpdate returns, well inside the first gap.
	svcSettleMinInterval = 500 * time.Millisecond
	svcSettleMaxInterval = 4 * time.Second
)

// svcSettleTimeoutFor is converge_config.timeout when set, else
// svcSettleTimeout.
func svcSettleTimeoutFor(cfg *svcConvergeModel) (time.Duration, error) {
	if cfg == nil {
		return svcSettleTimeout, nil
	}
	d, err := parseDurationOr(cfg.Timeout.ValueString(), 3*time.Minute)
	if err != nil {
		return 0, fmt.Errorf("converge_config.timeout: %w", err)
	}
	return d, nil
}

// svcWaitSettled polls the service after a create or update until swarm has
// finished applying it: no rollout of the current spec in progress (its
// UpdateStatus absent, belonging to an earlier spec, or completed) and the
// same Version.Index on two consecutive inspects. A paused or rollback_paused
// rollout is an error naming the state, and so is rollback_completed (the
// update failed and swarm rolled it back). interval is the first gap between
// inspects; it doubles up to svcSettleMaxInterval.
func svcWaitSettled(ctx context.Context, c *client.Client, id string, timeout, interval time.Duration) error {
	if interval <= 0 {
		interval = svcSettleMinInterval
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stable *uint64 // index of the last inspect with no rollout running
	status, index := "none", uint64(0)
	timedOut := func() error {
		return fmt.Errorf("service %s did not settle within %s: update status %s at version %d: %w",
			id, timeout, status, index, context.DeadlineExceeded)
	}
	for {
		res, err := c.ServiceInspect(ctx, id, client.ServiceInspectOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return timedOut()
			}
			return err
		}
		svc := res.Service
		index = svc.Version.Index
		us := actRolloutStatus(svc)
		status = "none"
		if us != nil {
			status = string(us.State)
			if us.Message != "" {
				status += " (" + us.Message + ")"
			}
		}
		switch {
		case us == nil || us.State == swarm.UpdateStateCompleted:
			if stable != nil && *stable == index {
				return nil
			}
			i := index
			stable = &i
		case us.State == swarm.UpdateStatePaused || us.State == swarm.UpdateStateRollbackPaused:
			return fmt.Errorf("service update %s: %s", us.State, us.Message)
		case us.State == swarm.UpdateStateRollbackCompleted:
			return fmt.Errorf("service update rolled back (%s): %s", us.State, us.Message)
		default: // updating, rollback_started
			stable = nil
		}
		select {
		case <-ctx.Done():
			return timedOut()
		case <-time.After(interval):
		}
		if interval *= 2; interval > svcSettleMaxInterval {
			interval = svcSettleMaxInterval
		}
	}
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
	if _, err := c.ServiceRemove(ctx, id, client.ServiceRemoveOptions{}); err != nil && !isNotFound(err) {
		return err
	}
	return waitFor(ctx, timeout, 2*time.Second, func(ctx context.Context) (bool, error) {
		// Filtering by service fails once the service is gone, so list all
		// tasks and filter here.
		tasksRes, err := c.TaskList(ctx, client.TaskListOptions{})
		if err != nil {
			return false, err
		}
		tasks := tasksRes.Items
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
