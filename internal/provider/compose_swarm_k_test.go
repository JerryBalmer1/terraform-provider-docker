package provider

// Pass 5.3 Lane K: swarm-mode unit tests (D6 null environment, secret
// driver / template_driver, credential_spec).

import (
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/swarm"
)

const composeSwarmYAMLK = `
name: sk
services:
  web:
    image: nginx:1.27
    environment:
      A: "1"
      GONE: null
    credential_spec:
      config: cred
  plain:
    image: nginx:1.27
    environment: {B: "2"}
    secrets: [vault, plain]
    configs: [tpl]
secrets:
  vault:
    environment: SEC
    driver: vaultdrv
    driver_opts: {path: kv/x}
    template_driver: golang
  plain:
    environment: SEC
configs:
  tpl:
    content: "{{ .Service.Name }}"
    template_driver: golang
  cred:
    content: "{}"
`

func TestComposeSwarmFieldsK(t *testing.T) {
	d := composeMustLoad(t, composeInputs{Content: composeSwarmYAMLK, Env: map[string]string{"SEC": "x"}})
	plan, err := composeSwarmExpand(d.Project)
	if err != nil {
		t.Fatal(err)
	}
	svc := map[string]composeSwarmService{}
	for _, s := range plan.Services {
		svc[s.Key] = s
	}
	// D6: docker stack deploy sends the bare key (docker/cli
	// convert/service.go:614-626).
	web := svc["web"]
	if got := web.Spec.TaskTemplate.ContainerSpec.Env; !reflect.DeepEqual(got, []string{"A=1", "GONE"}) {
		t.Errorf("web env %v", got)
	}
	if web.CLIOnly == "" || svc["plain"].CLIOnly != "" {
		t.Errorf("CLIOnly web %q plain %q", web.CLIOnly, svc["plain"].CLIOnly)
	}
	// The hash keeps the frozen KEY=VALUE list.
	spec := web.Spec
	cs := *spec.TaskTemplate.ContainerSpec
	cs.Env = []string{"A=1"}
	spec.TaskTemplate.ContainerSpec = &cs
	spec.Labels = map[string]string{}
	for k, v := range web.Spec.Labels {
		if k != composeLabelConfigHash {
			spec.Labels[k] = v
		}
	}
	if composeHash(spec) != web.Hash {
		t.Error("the swarm service hash must not include the bare keys")
	}
	// Drift: a live service without the bare key differs.
	live := swarm.Service{Spec: web.Spec}
	lcs := *web.Spec.TaskTemplate.ContainerSpec
	lcs.Env = []string{"A=1"}
	live.Spec.TaskTemplate.ContainerSpec = &lcs
	if d := composeSwarmServiceDrift(web, live); d != "has a different environment" {
		t.Errorf("drift %q", d)
	}
	if d := composeSwarmServiceDrift(web, swarm.Service{Spec: web.Spec}); d != "" {
		t.Errorf("no drift expected, got %q", d)
	}
	// credential_spec config: a Runtime config reference and the spec.
	pr := web.Spec.TaskTemplate.ContainerSpec.Privileges
	if pr == nil || pr.CredentialSpec == nil || pr.CredentialSpec.Config != "sk_cred" {
		t.Fatalf("privileges %+v", pr)
	}
	var runtime bool
	for _, c := range web.Spec.TaskTemplate.ContainerSpec.Configs {
		if c.ConfigName == "sk_cred" && c.Runtime != nil && c.File == nil {
			runtime = true
		}
	}
	if !runtime {
		t.Error("no runtime config reference for the credential spec")
	}
	// composeSwarmResolve turns the credential spec's config name into
	// the config ID (docker/cli service.go:680-715).
	lk := &svcLookup{configs: []namedObject{{ID: "cfgid", Name: "sk_cred"}}, networks: []namedObject{{ID: "netid", Name: "sk_default"}}}
	rs := web.Spec
	if err := composeSwarmResolve(&rs, lk); err != nil {
		t.Fatal(err)
	}
	if got := rs.TaskTemplate.ContainerSpec.Privileges.CredentialSpec.Config; got != "cfgid" {
		t.Errorf("resolved credential spec config %q", got)
	}
	if web.Spec.TaskTemplate.ContainerSpec.Privileges.CredentialSpec.Config != "sk_cred" {
		t.Error("resolve must not change the plan's spec")
	}
	// Secrets and configs: driver and templating.
	blobs := map[string]composeSwarmBlob{}
	for _, b := range append(append([]composeSwarmBlob{}, plan.Secrets...), plan.Configs...) {
		blobs[b.Name] = b
	}
	v := blobs["sk_vault"]
	if v.Driver == nil || v.Driver.Name != "vaultdrv" || v.Driver.Options["path"] != "kv/x" || v.Templating == nil || v.Templating.Name != "golang" || v.Data != nil {
		t.Errorf("vault %+v", v)
	}
	if tpl := blobs["sk_tpl"]; tpl.Templating == nil || tpl.Templating.Name != "golang" || string(tpl.Data) == "" {
		t.Errorf("tpl %+v", tpl)
	}
	if p := blobs["sk_plain"]; p.Driver != nil || p.Templating != nil || p.Hash != composeHash([]any{composeSha([]byte("x")), func() map[string]string {
		l := map[string]string{}
		for k, v := range p.Labels {
			if k != composeLabelConfigHash {
				l[k] = v
			}
		}
		return l
	}()}) {
		t.Errorf("plain secret hash changed form: %+v", p)
	}
	for _, w := range plan.Warnings {
		t.Logf("warning: %s", w)
	}
}
