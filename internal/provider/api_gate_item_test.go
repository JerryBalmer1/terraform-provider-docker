package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/moby/moby/client"
)

func TestGateItemAPI(t *testing.T) {
	ctx := context.Background()
	if contractMustItem("engine:image-attestations").MinAPI != "1.55" {
		t.Fatal("worklist item engine:image-attestations should need 1.55")
	}

	// Daemon too old: error naming type, needed and negotiated versions.
	old := fakeVersionEngine(t, "1.54", nil)
	old.MinAPIVersion = "1.44"
	var d diag.Diagnostics
	if old.gateItemAPI(ctx, "engine:image-attestations", &d) || !d.HasError() {
		t.Fatalf("want failure, got %v", d)
	}
	msg := d.Errors()[0].Detail()
	for _, want := range []string{"data source docker_image_attestations", "1.55", "1.54"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}

	// New enough.
	ok := fakeVersionEngine(t, "1.55", nil)
	d = nil
	if !ok.gateItemAPI(ctx, "engine:image-attestations", &d) || d.HasError() {
		t.Errorf("1.55: %v", d)
	}

	// Floor at or above min_api: no daemon contact (nil client would panic).
	floor := &providerData{MinAPIVersion: "1.55"}
	d = nil
	if !floor.gateItemAPI(ctx, "engine:image-attestations", &d) || d.HasError() {
		t.Errorf("floor: %v", d)
	}
	// No min_api: pass without daemon.
	if !(&providerData{}).gateItemAPI(ctx, "engine:container", &d) {
		t.Error("no min_api")
	}
	// nil providerData (validation): pass.
	var nilPD *providerData
	if !nilPD.gateItemAPI(ctx, "engine:image-attestations", &d) {
		t.Error("nil pd")
	}
	// Unknown item: provider bug.
	d = nil
	if (&providerData{}).gateItemAPI(ctx, "nope:nope", &d) || !d.HasError() {
		t.Error("unknown item must fail")
	}
	// Unreachable daemon: warning, pass.
	c, err := newDockerClient(endpointConfig{Host: "tcp://" + closedTCPAddr(t)})
	if err != nil {
		t.Fatal(err)
	}
	down := &providerData{Client: c, MinAPIVersion: "1.44"}
	d = nil
	if !down.gateItemAPI(ctx, "engine:image-attestations", &d) || d.HasError() || d.WarningsCount() != 1 {
		t.Errorf("unreachable: %v", d)
	}
}

// TestFloorHookOnClient: a client built with MinAPIVersion refuses
// versioned requests below the floor even without pd.APIVersion.
func TestFloorHookOnClient(t *testing.T) {
	srv := fakeEngineAPI(t, "1.41")
	host := "tcp://" + srv.Listener.Addr().String()
	c, err := newDockerClient(endpointConfig{Host: host, APIVersion: "1.41", MinAPIVersion: "1.44"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ServerVersion(context.Background(), client.ServerVersionOptions{})
	var floor *apiFloorError
	if !errors.As(err, &floor) || floor.Version != "1.41" || floor.Floor != "1.44" {
		t.Fatalf("want apiFloorError, got %v", err)
	}
	// Unversioned /_ping passes the hook.
	if _, err := c.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Errorf("ping: %v", err)
	}
	// Without a floor nothing is refused.
	c2, _ := newDockerClient(endpointConfig{Host: host, APIVersion: "1.41"})
	if _, err := c2.ServerVersion(context.Background(), client.ServerVersionOptions{}); err != nil {
		t.Errorf("no floor: %v", err)
	}
}

// TestConfigureCarriesFloor: Configure puts min_api_version on the endpoint.
func TestConfigureCarriesFloor(t *testing.T) {
	srv := fakeEngineAPI(t, "1.47")
	_, pd := configureProvider(t, map[string]tftypes.Value{
		"host":            tfStr("tcp://" + srv.Listener.Addr().String()),
		"min_api_version": tfStr("1.45"),
	})
	if pd == nil || pd.Endpoint.MinAPIVersion != "1.45" {
		t.Fatalf("endpoint floor: %+v", pd)
	}
}
