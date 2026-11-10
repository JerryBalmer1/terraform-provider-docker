package provider

// docker_compose, mode = "local": service lifecycle hooks.
//
//   - post_start: commands exec'd in the service container once it has
//     started (docker compose v5.1.4 pkg/compose/convergence.go:926-931,
//     hook.go:34-85 runHook: ExecCreate with the hook's user, privileged,
//     environment, working_dir and command, then wait for exit 0).
//   - pre_stop: the same, exec'd before a container of the service is
//     stopped and removed (down.go:298-311 stopContainer), errors
//     "not found" / "conflict" ignored as compose does.
//   - pre_start: init containers that run to completion before the service
//     container starts (compose-spec 05-services.md "pre_start", Compose
//     v5.3.0; compose v5.1.4 predates it). compose-go v2.16.2 resolves each
//     step's inheritance from the service at load time
//     (loader/normalize.go:156-197 resolvePreStartHooks), except volumes,
//     which "inherit at runtime through volumes_from": each step container
//     is created from its own container specification, with VolumesFrom set
//     to the service container it runs for, after that container is
//     created and before it is started. per_replica steps run for every
//     replica created; the others run once, before the first replica the
//     apply creates for the service, when no replica of the service is kept
//     (a kept replica means the steps already succeeded for the current
//     definition: a change to a step changes the service's config-hash,
//     which recreates every replica). A step container is removed once it
//     exits; a non-zero exit fails the service.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	ctypes "github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// composeLocalHook is one planned pre_start step.
type composeLocalHook struct {
	Index      int
	PerReplica bool
	// Container is the step's container, expanded like a service
	// container (name, labels and restart policy rewritten).
	Container composeLocalContainer
}

// composeHookTimeout bounds one hook (exec or init container).
const composeHookTimeout = 10 * time.Minute

// composeLocalPreStart expands a service's pre_start steps.
func composeLocalPreStart(p *ctypes.Project, plan *composeLocalPlan, svc ctypes.ServiceConfig) ([]composeLocalHook, error) {
	var out []composeLocalHook
	for i, h := range svc.PreStart {
		hs := ctypes.ServiceConfig{Name: svc.Name, ContainerSpec: h.ContainerSpec}
		if hs.Image == "" {
			hs.Image = composeImageName(p.Name, svc)
		}
		// A step only reaches the volumes of the container it runs for
		// (VolumesFrom at run time) and its own.
		cs, err := composeLocalService(p, plan, hs)
		if err != nil {
			return nil, fmt.Errorf("pre_start[%d]: %w", i, err)
		}
		if len(cs) != 1 {
			return nil, fmt.Errorf("pre_start[%d]: expanded to %d containers", i, len(cs))
		}
		c := cs[0]
		cfg := *c.Config
		cfg.Labels = mergeLabels(cfg.Labels, map[string]string{composeLabelOneoff: "True"})
		for _, k := range []string{composeLabelNumber, composeLabelConfigHash, composeLabelSpecHash, composeLabelConfigFiles} {
			delete(cfg.Labels, k)
		}
		host := *c.Host
		host.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyDisabled}
		c.Config, c.Host = &cfg, &host
		c.PreStart, c.PostStart, c.PreStop = nil, nil, nil
		c.DependsOn = nil
		out = append(out, composeLocalHook{Index: i, PerReplica: h.PerReplica, Container: c})
	}
	return out, nil
}

// composeHookName names a step container: per replica after the replica,
// otherwise after the service.
func composeHookName(project string, dc composeLocalContainer, h composeLocalHook) string {
	if h.PerReplica {
		return fmt.Sprintf("%s-pre_start-%d", dc.Name, h.Index+1)
	}
	return fmt.Sprintf("%s-%s-pre_start-%d", project, dc.Service, h.Index+1)
}

// composeRunPreStart runs dc's pre_start steps for the created (not yet
// started) container id: the per_replica ones always, the others when
// dc.SharedPreStart is set.
func composeRunPreStart(ctx context.Context, pd *providerData, dc composeLocalContainer, id string, netIDs map[string]string) error {
	project := dc.Config.Labels[composeLabelProject]
	for _, h := range dc.PreStart {
		if !h.PerReplica && !dc.SharedPreStart {
			continue
		}
		step := h.Container
		host := *step.Host
		host.VolumesFrom = append([]string{id}, step.Host.VolumesFrom...)
		step.Host = &host
		step.Name = composeHookName(project, dc, h)
		step.Pull = dc.Pull
		if err := composeRunStep(ctx, pd, step, netIDs); err != nil {
			return fmt.Errorf("container %s: pre_start[%d]: %w", dc.Name, h.Index, err)
		}
	}
	return nil
}

// composeRunStep creates, starts and waits for one init container, then
// removes it. A leftover container of the same name (an earlier failed
// run) is removed first.
func composeRunStep(ctx context.Context, pd *providerData, step composeLocalContainer, netIDs map[string]string) error {
	c := pd.Client
	if _, err := c.ContainerRemove(ctx, step.Name, client.ContainerRemoveOptions{Force: true}); err != nil && !isNotFound(err) {
		return fmt.Errorf("removing leftover %s: %w", step.Name, err)
	}
	id, err := composeLocalCreateOnly(ctx, pd, step, netIDs)
	defer func() {
		if id != "" {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			_, _ = c.ContainerRemove(rctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	}()
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, composeHookTimeout)
	defer cancel()
	wait := c.ContainerWait(wctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := c.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("starting %s: %w", step.Name, err)
	}
	var code int64
	select {
	case res := <-wait.Result:
		if res.Error != nil && res.Error.Message != "" {
			return fmt.Errorf("waiting for %s: %s", step.Name, res.Error.Message)
		}
		code = res.StatusCode
	case err := <-wait.Error:
		return fmt.Errorf("waiting for %s: %w", step.Name, err)
	}
	if code != 0 {
		return fmt.Errorf("%s exited with code %d%s", step.Name, code, composeLogTail(ctx, pd, id))
	}
	return nil
}

// composeLogTail returns the last lines of a container's output, for an
// error message ("" when they cannot be read).
func composeLogTail(ctx context.Context, pd *providerData, id string) string {
	rc, err := pd.Client.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "20"})
	if err != nil {
		return ""
	}
	defer rc.Close()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, io.LimitReader(rc, 64<<10)); err != nil && out.Len() == 0 {
		return ""
	}
	s := strings.TrimSpace(out.String())
	if s == "" {
		return ""
	}
	return ":\n" + s
}

// composeRunExecHooks runs post_start or pre_stop hooks in container id
// (hook.go:34-85 runHook). which names the hook kind in errors.
func composeRunExecHooks(ctx context.Context, pd *providerData, id, name, which string, hooks []ctypes.ServiceHook) error {
	for i, h := range hooks {
		env := composeEnvWithUnset(composeEnvList(h.Environment), composeEnvUnset(h.Environment))
		out, err := copExecRun(ctx, pd.Client, copExecSpec{
			Container: id,
			Opts: client.ExecCreateOptions{
				User: h.User, Privileged: h.Privileged, Env: env, WorkingDir: h.WorkingDir, Cmd: []string(h.Command),
			},
			Timeout:  composeHookTimeout,
			MaxBytes: 16 << 10,
		})
		if err != nil {
			return fmt.Errorf("container %s: %s[%d]: %w", name, which, i, err)
		}
		if out.ExitCode != 0 {
			detail := strings.TrimSpace(out.Stderr)
			if detail == "" {
				detail = strings.TrimSpace(out.Stdout)
			}
			if detail != "" {
				detail = ": " + detail
			}
			return fmt.Errorf("container %s: %s[%d] hook exited with status %d%s", name, which, i, out.ExitCode, detail)
		}
	}
	return nil
}

// composeRunPreStop runs pre_stop hooks before a running container is
// removed. Like compose (down.go:303-311) a container that is gone or not
// running is not an error.
func composeRunPreStop(ctx context.Context, pd *providerData, s container.Summary, hooks []ctypes.ServiceHook) error {
	if len(hooks) == 0 || s.State != container.StateRunning {
		return nil
	}
	name := s.ID
	if len(s.Names) > 0 {
		name = strings.TrimPrefix(s.Names[0], "/")
	}
	err := composeRunExecHooks(ctx, pd, s.ID, name, "pre_stop", hooks)
	if err != nil && (isNotFound(err) || cerrdefs.IsConflict(err)) {
		return nil
	}
	return err
}

// composeHooksByService maps service name -> pre_stop hooks of a plan.
func composeHooksByService(plan *composeLocalPlan) map[string][]ctypes.ServiceHook {
	out := map[string][]ctypes.ServiceHook{}
	if plan == nil {
		return out
	}
	for _, c := range plan.Containers {
		if len(c.PreStop) > 0 {
			out[c.Service] = c.PreStop
		}
	}
	return out
}
