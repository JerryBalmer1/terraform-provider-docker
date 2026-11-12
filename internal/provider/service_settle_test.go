package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

// svcSettleScript plays swarm's orchestrator after the first ServiceUpdate:
// on the first inspect it starts the rollout (Version.Index+1, UpdatedAt
// moves, UpdateStatus updating), the second still reports updating, the
// third completes it (Version.Index+1 again). Later inspects are unchanged.
func svcSettleScript() func(s *swarm.Service, updates int) {
	n := 0
	return func(s *swarm.Service, updates int) {
		if updates == 0 {
			return
		}
		n++
		now := time.Now()
		switch n {
		case 1:
			s.Version.Index++
			s.UpdatedAt = now
			s.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateUpdating, StartedAt: &now, Message: "update in progress"}
		case 3:
			s.Version.Index++
			s.UpdateStatus.State = swarm.UpdateStateCompleted
			s.UpdateStatus.CompletedAt = &now
			s.UpdateStatus.Message = "update completed"
		}
	}
}

// TestServiceSettleBeforeStateFakeDaemon: an update without converge_config
// whose rollout reports updating on the first two inspects. The state written
// must come from the settled read (completed, index 4: create 1, update 2,
// rollout start 3, rollout end 4), and the next plan must be empty.
func TestServiceSettleBeforeStateFakeDaemon(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	f.onInspect = svcSettleScript()
	cfg := func(replicas string) string {
		return svcFakeConfig(host, `
resource "docker_service" "s" {
  name     = "settle"
  image    = "busybox"
  args     = ["sleep", "infinity"]
  replicas = `+replicas+`
}
`)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg("1"), Check: resource.TestCheckResourceAttr("docker_service.s", "version_index", "1")},
			{
				Config: cfg("2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.s", "version_index", "4"),
					resource.TestCheckResourceAttr("docker_service.s", "inspect.version.index", "4"),
					resource.TestCheckResourceAttr("docker_service.s", "inspect.update_status.state", "completed"),
				),
			},
		},
	})
}

func svcSettleClient(t *testing.T, host string) *client.Client {
	t.Helper()
	c, err := client.New(client.WithHost(host), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func svcSettleFake(t *testing.T, us *swarm.UpdateStatus) (*client.Client, string) {
	t.Helper()
	f, host := newSvcFakeDaemon(t)
	now := time.Now()
	us.StartedAt = &now
	f.services["svc1"] = &swarm.Service{ID: "svc1", Meta: swarm.Meta{Version: swarm.Version{Index: 7}, UpdatedAt: now},
		Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "x"}}, UpdateStatus: us}
	return svcSettleClient(t, host), "svc1"
}

func TestServiceWaitSettledTimesOut(t *testing.T) {
	c, id := svcSettleFake(t, &swarm.UpdateStatus{State: swarm.UpdateStateUpdating, Message: "update in progress"})
	start := time.Now()
	err := svcWaitSettled(context.Background(), c, id, 700*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Fatal("svcWaitSettled returned nil for a service that never settles")
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not settle within 700ms") ||
		!strings.Contains(err.Error(), "updating (update in progress) at version 7") {
		t.Errorf("error = %v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("wait ran %s past a 700ms timeout", el)
	}
}

func TestServiceWaitSettledStates(t *testing.T) {
	cases := []struct {
		state swarm.UpdateState
		want  string // "" = settles
	}{
		{swarm.UpdateStateCompleted, ""},
		{swarm.UpdateStatePaused, "service update paused: boom"},
		{swarm.UpdateStateRollbackPaused, "service update rollback_paused: boom"},
		{swarm.UpdateStateRollbackCompleted, "service update rolled back (rollback_completed): boom"},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			c, id := svcSettleFake(t, &swarm.UpdateStatus{State: tc.state, Message: "boom"})
			err := svcWaitSettled(context.Background(), c, id, 5*time.Second, 20*time.Millisecond)
			if tc.want == "" {
				if err != nil {
					t.Errorf("err = %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A status that started more than a second before the spec's UpdatedAt
// belongs to an earlier rollout and does not block or fail the wait.
func TestServiceWaitSettledIgnoresStaleStatus(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	now := time.Now()
	old := now.Add(-time.Hour)
	f.services["svc1"] = &swarm.Service{ID: "svc1", Meta: swarm.Meta{Version: swarm.Version{Index: 3}, UpdatedAt: now},
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateRollbackCompleted, StartedAt: &old}}
	if err := svcWaitSettled(context.Background(), svcSettleClient(t, host), "svc1", 5*time.Second, 20*time.Millisecond); err != nil {
		t.Errorf("err = %v", err)
	}
}
