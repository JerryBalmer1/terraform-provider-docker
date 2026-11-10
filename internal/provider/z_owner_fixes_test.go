package provider

import (
	"context"
	"testing"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/moby/api/types/swarm"
)

// TestSwarmApplyNodeSpecKeepsOwnerZ: docker_node's `labels` replaces the
// node's labels but keeps the owner label docker_swarm_node put there; a
// label set that names the owner label (docker_swarm_node's own update)
// still sets it.
func TestSwarmApplyNodeSpecKeepsOwnerZ(t *testing.T) {
	spec := swarm.NodeSpec{}
	spec.Labels = map[string]string{ownerLabelKey: "team-a", "zone": "a"}
	want := map[string]string{"zone": "b"}
	out, changed := swarmApplyNodeSpec(spec, swarmNodeSpecChange{Labels: &want})
	if !changed || out.Labels["zone"] != "b" || out.Labels[ownerLabelKey] != "team-a" || len(out.Labels) != 2 {
		t.Errorf("labels = %v (changed %v)", out.Labels, changed)
	}
	if len(want) != 1 {
		t.Errorf("the change's map was modified: %v", want)
	}
	same := map[string]string{"zone": "a"}
	if _, changed := swarmApplyNodeSpec(spec, swarmNodeSpecChange{Labels: &same}); changed {
		t.Error("only the owner label differs from the config: no update")
	}
	adopt := map[string]string{"zone": "a", ownerLabelKey: "team-b"}
	out, _ = swarmApplyNodeSpec(spec, swarmNodeSpecChange{Labels: &adopt})
	if out.Labels[ownerLabelKey] != "team-b" {
		t.Errorf("an explicit owner label must win: %v", out.Labels)
	}
	if got := ownerStrip(out.Labels); got[ownerLabelKey] != "" {
		t.Errorf("docker_node reports labels without the owner label: %v", got)
	}
}

// TestImgBuildKitAuthProviderZ: with the provider attached, BuildKit's
// session auth resolves through pd.registryCredentialFor (registry_auth
// first), so builds see the same credentials as every other registry call.
func TestImgBuildKitAuthProviderZ(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	pd := &providerData{RegistryAuth: []registryCredential{{Address: "registry.example.com", Username: "u", Password: "p"}}}
	a := &imgBuildKitAuth{}
	imgBuildKitAuthUseProvider([]session.Attachable{a}, pd)
	res, err := a.Credentials(context.Background(), &auth.CredentialsRequest{Host: "registry.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Username != "u" || res.Secret != "p" {
		t.Errorf("credentials = %+v", res)
	}
	res, err = a.Credentials(context.Background(), &auth.CredentialsRequest{Host: "other.example.com"})
	if err != nil || res.Username != "" || res.Secret != "" {
		t.Errorf("unknown host = %+v, %v", res, err)
	}
}
