package provider

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/api/types/container"
)

// TestCopContainerPauseUnpauseIdempotent (Pass 6b Lane Z): pausing a paused
// container and unpausing a container that is not paused skip the Engine
// call (which would answer 409), as docker_compose_pause / _unpause do.
func TestCopContainerPauseUnpauseIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		new      func() action.Action
		verb     string
		paused   bool
		wantCall bool
	}{
		{"pause running", func() action.Action { return NewContainerPauseAction() }, "pause", false, true},
		{"pause paused", func() action.Action { return NewContainerPauseAction() }, "pause", true, false},
		{"unpause paused", func() action.Action { return NewContainerUnpauseAction() }, "unpause", true, true},
		{"unpause running", func() action.Action { return NewContainerUnpauseAction() }, "unpause", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, pd := copFake(t)
			status := container.ContainerState("running")
			if tc.paused {
				status = "paused"
			}
			insp := container.InspectResponse{ID: "c1", Name: "/web", Image: "sha256:img1",
				State: &container.State{Status: status, Running: true, Paused: tc.paused, Pid: 9}}
			f.json("GET /containers/web/json", insp)
			f.json("GET /containers/c1/json", insp)
			var calls atomic.Int32
			f.on("POST /containers/c1/"+tc.verb, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				msg := "container c1 is already paused"
				if tc.verb == "unpause" {
					msg = "container c1 is not paused"
				}
				if tc.paused == (tc.verb == "pause") {
					p3eWriteJSON(w, http.StatusConflict, map[string]string{"message": msg})
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			res := actInvokePD(t, pd, tc.new(), map[string]tftypes.Value{"container_id": actTfStr("web")})
			if res.Diags.HasError() {
				t.Fatal(res.errText())
			}
			if got := calls.Load() > 0; got != tc.wantCall {
				t.Errorf("engine call = %v, want %v", got, tc.wantCall)
			}
			if !tc.wantCall && !strings.Contains(strings.Join(res.Progress, "\n"), "nothing to do") {
				t.Errorf("progress = %q, want a no-op message", res.Progress)
			}
		})
	}
}
