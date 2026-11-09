package provider

import (
	"context"
	"fmt"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStackPlanObjects(t *testing.T) {
	desired := map[string]string{"new": "a", "same": "b", "changed": "c2", "noprior": "d"}
	prior := map[string]string{"same": "b", "changed": "c1", "gone": "e"}
	existing := map[string]bool{"same": true, "changed": true, "noprior": true, "gone": true, "orphan": true}
	got := stackPlanObjects(desired, prior, existing)
	want := stackObjPlan{
		Create:  []string{"new"},
		Replace: []string{"changed", "noprior"},
		Remove:  []string{"gone", "orphan"},
		Keep:    []string{"same"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestStackAddUnfingerprinted(t *testing.T) {
	p := stackPlanObjects(map[string]string{}, nil, map[string]bool{"x": true, "old": true})
	stackAddUnfingerprinted(&p, []string{"x", "y"}, map[string]bool{"x": true, "old": true})
	if !reflect.DeepEqual(p.Replace, []string{"x"}) || !reflect.DeepEqual(p.Create, []string{"y"}) || !reflect.DeepEqual(p.Remove, []string{"old"}) {
		t.Errorf("got %+v", p)
	}
}

func TestStackPlanServices(t *testing.T) {
	one := uint64(1)
	repl := swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &one}}
	withSecret := swarm.ServiceSpec{Mode: repl, TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
		Secrets: []*swarm.SecretReference{{SecretID: "sec-old"}}}}}
	withNet := swarm.ServiceSpec{Mode: repl, TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{},
		Networks: []swarm.NetworkAttachmentConfig{{Target: "net-keep"}}}}
	existing := map[string]swarm.ServiceSpec{
		"api":    withSecret, // uses a replaced secret -> recreate
		"web":    withNet,    // untouched -> update
		"agent":  withNet,    // switches to global -> recreate
		"legacy": withNet,    // not desired -> remove
		"batch":  withNet,    // switches to replicated_job -> recreate
	}
	desired := map[string]string{"api": "replicated", "web": "", "agent": "global", "worker": "replicated", "batch": "replicated_job"}
	got := stackPlanServices(desired, existing, map[string]bool{"sec-old": true})
	want := stackSvcPlan{
		Remove: []string{"agent", "api", "batch", "legacy"},
		Create: []string{"agent", "api", "batch", "worker"},
		Update: []string{"web"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestStackServiceRefs(t *testing.T) {
	spec := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{
		Networks: []swarm.NetworkAttachmentConfig{{Target: "n1"}},
		ContainerSpec: &swarm.ContainerSpec{
			Secrets: []*swarm.SecretReference{{SecretID: "s1"}},
			Configs: []*swarm.ConfigReference{{ConfigID: "c1"}},
		},
	}}
	if got := stackServiceRefs(spec); !reflect.DeepEqual(got, map[string]bool{"n1": true, "s1": true, "c1": true}) {
		t.Errorf("got %v", got)
	}
}

func TestStackReferenceRewriting(t *testing.T) {
	ctx := context.Background()
	lk := svcTestLookup()
	lk.stackPrefix = "app_"
	m := svcTestModel()
	m.Name = stringOrNull("web")
	m.Networks = svcNetworksValue(m.Networks, map[string]svcNetworkAttachModel{"frontend": svcNetworkAttachment(nil, nil), "backend": svcNetworkAttachment(nil, nil)})
	m.Secrets = m.Secrets[:1] // secret_id = "db_password" -> app_db_password
	m.Configs = nil
	spec, diags := svcExpand(ctx, &m, lk, svcExpandOpts{FullName: "app_web", Namespace: "app"})
	if diags.HasError() {
		t.Fatal(diags)
	}
	nets := map[string][]string{}
	for _, n := range spec.TaskTemplate.Networks {
		nets[n.Target] = n.Aliases
	}
	if !reflect.DeepEqual(nets, map[string][]string{"net222": {"web"}, "net333": {"web"}}) {
		t.Errorf("networks = %v", nets)
	}
	s := spec.TaskTemplate.ContainerSpec.Secrets[0]
	if s.SecretID != "sec222" || s.SecretName != "app_db_password" || s.File.Name != "db_password" {
		t.Errorf("secret = %+v %+v", s, s.File)
	}
	if spec.Name != "app_web" || spec.Labels[labelStackNamespace] != "app" || spec.TaskTemplate.ContainerSpec.Labels[labelStackNamespace] != "app" {
		t.Errorf("names/labels: %s %v %v", spec.Name, spec.Labels, spec.TaskTemplate.ContainerSpec.Labels)
	}

	// Volumes: rewrite then reverse.
	vols := map[string]stackVolumeDef{"data": {Driver: "local", DriverOpts: map[string]string{"type": "tmpfs"}, Labels: map[string]string{"tier": "db"}}}
	spec.TaskTemplate.ContainerSpec.Mounts = []mount.Mount{
		{Type: mount.TypeVolume, Source: "data", Target: "/data"},
		{Type: mount.TypeVolume, Source: "external", Target: "/ext"},
		{Type: mount.TypeBind, Source: "data", Target: "/bind"},
	}
	stackRewriteMounts(&spec, "app", vols)
	mts := spec.TaskTemplate.ContainerSpec.Mounts
	if mts[0].Source != "app_data" || mts[0].VolumeOptions.DriverConfig.Name != "local" ||
		mts[0].VolumeOptions.Labels["tier"] != "db" || mts[0].VolumeOptions.Labels[labelStackNamespace] != "app" {
		t.Errorf("rewritten = %+v %+v", mts[0], mts[0].VolumeOptions)
	}
	if mts[1].Source != "external" || mts[1].VolumeOptions != nil || mts[2].Source != "data" {
		t.Errorf("untouched mounts changed: %+v", mts[1:])
	}
	daemon := spec
	stackUnrewriteMounts(&daemon, "app", vols)
	if m0 := daemon.TaskTemplate.ContainerSpec.Mounts[0]; m0.Source != "data" || m0.VolumeOptions != nil {
		t.Errorf("unrewritten = %+v", m0)
	}
	if spec.TaskTemplate.ContainerSpec.Mounts[0].Source != "app_data" {
		t.Error("unrewrite mutated the original spec")
	}

	// Flatten back: short names come back.
	got := svcFlatten(daemon, m, lk, svcFlattenOpts{ShortName: "web", StripStack: true})
	if got.Secrets[0].SecretID.ValueString() != "db_password" {
		t.Errorf("secret ref = %v", got.Secrets[0].SecretID)
	}
	if !got.Networks.Equal(m.Networks) {
		t.Errorf("networks = %v", got.Networks)
	}
	if !got.Labels.Equal(m.Labels) || !got.ContainerLabels.IsNull() {
		t.Errorf("labels = %v / %v", got.Labels, got.ContainerLabels)
	}
}

func TestStackFingerprintStable(t *testing.T) {
	a := stackFingerprint("overlay", true, map[string]string{"b": "2", "a": "1"})
	b := stackFingerprint("overlay", true, map[string]string{"a": "1", "b": "2"})
	c := stackFingerprint("overlay", false, map[string]string{"a": "1", "b": "2"})
	if a != b || a == c {
		t.Errorf("%q %q %q", a, b, c)
	}
}

func stackFakeConfig(host, secretValue, extra string) string {
	return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_stack" "app" {
  name = "app"

  network {
    name = "front"
  }

  volume {
    name   = "data"
    driver = "local"
  }

  secret {
    name = "db_pw"
    data = base64encode(%q)
  }

  config {
    name = "conf"
    data = base64encode("listen 80;")
  }

  service {
    name     = "web"
    image    = "nginx:1.27"
    replicas = 2
    networks = { front = {} }

    mounts {
      type   = "volume"
      source = "data"
      target = "/data"
    }

    configs {
      config_id = "conf"
      file_name = "/etc/nginx/conf.d/default.conf"
    }

    endpoint_spec {
      ports {
        target_port    = 80
        published_port = 8080
      }
    }
  }

  service {
    name  = "db"
    image = "postgres:16"
    networks = { front = {} }

    secrets {
      secret_id = "db_pw"
    }

    converge_config {
      timeout = "10s"
      delay   = "100ms"
    }
  }
%s
}
`, secretValue, extra))
}

func TestStackFakeDaemonLifecycle(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	check := func(fn func(f *svcFakeDaemon) error) resource.TestCheckFunc {
		return func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			return fn(f)
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: stackFakeConfig(host, "pw1", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_stack.app", "id", "app"),
					resource.TestCheckResourceAttr("docker_stack.app", "service.#", "2"),
					resource.TestCheckResourceAttr("docker_stack.app", "service.1.secrets.0.secret_id", "db_pw"),
					resource.TestCheckResourceAttr("docker_stack.app", "network.0.driver", "overlay"),
					check(func(f *svcFakeDaemon) error {
						web := f.findService("app_web")
						db := f.findService("app_db")
						if web == nil || db == nil {
							return fmt.Errorf("services missing: %v", f.services)
						}
						if web.Spec.Labels[labelStackNamespace] != "app" {
							return fmt.Errorf("web labels %v", web.Spec.Labels)
						}
						m := web.Spec.TaskTemplate.ContainerSpec.Mounts[0]
						if m.Source != "app_data" || m.VolumeOptions == nil || m.VolumeOptions.DriverConfig.Name != "local" {
							return fmt.Errorf("volume mount %+v", m)
						}
						sec := db.Spec.TaskTemplate.ContainerSpec.Secrets[0]
						if sec.SecretName != "app_db_pw" || sec.File.Name != "db_pw" {
							return fmt.Errorf("db secret %+v %+v", sec, sec.File)
						}
						net := f.networks[web.Spec.TaskTemplate.Networks[0].Target]
						if net == nil || net.Name != "app_front" {
							return fmt.Errorf("web network %+v", web.Spec.TaskTemplate.Networks)
						}
						return nil
					}),
				),
			},
			{
				// Change the secret: db (its user) is recreated, web is not.
				// Add a service and nothing else changes.
				Config: stackFakeConfig(host, "pw2", `
  service {
    name  = "worker"
    image = "busybox"
    mode  = "global"
    args  = ["sleep", "infinity"]
  }
`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_stack.app", plancheck.ResourceActionUpdate),
				}},
				Check: check(func(f *svcFakeDaemon) error {
					if f.creates["app_db"] != 2 || f.creates["app_web"] != 1 || f.creates["app_worker"] != 1 {
						return fmt.Errorf("creates = %v", f.creates)
					}
					if len(f.secrets) != 1 {
						return fmt.Errorf("secrets = %d", len(f.secrets))
					}
					return nil
				}),
			},
			{
				// Drop worker: the orphan service is removed.
				Config: stackFakeConfig(host, "pw2", ""),
				Check: check(func(f *svcFakeDaemon) error {
					if f.findService("app_worker") != nil {
						return fmt.Errorf("orphan service left")
					}
					return nil
				}),
			},
			{
				ResourceName: "docker_stack.app",
				ImportState:  true,
				// Import has no prior order or config: services come back
				// sorted by name, secret data is unknown, volumes (not daemon
				// objects) are absent and mounts show the rewritten volume.
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("%d states", len(states))
					}
					a := states[0].Attributes
					want := map[string]string{
						"name": "app", "service.#": "2", "service.0.name": "db", "service.1.name": "web",
						"service.0.secrets.0.secret_id": "db_pw", "service.1.networks.front.%": "2",
						"service.1.configs.0.config_id": "conf", "network.#": "1", "network.0.name": "front",
						"secret.#": "1", "secret.0.name": "db_pw", "config.0.data": "bGlzdGVuIDgwOw==",
					}
					for k, v := range want {
						if a[k] != v {
							return fmt.Errorf("%s = %q, want %q", k, a[k], v)
						}
					}
					if _, ok := a["secret.0.data"]; ok {
						return fmt.Errorf("secret data should be unknown after import")
					}
					return nil
				},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.services)+len(f.secrets)+len(f.configs) != 0 {
				return fmt.Errorf("left behind: %d services, %d secrets, %d configs", len(f.services), len(f.secrets), len(f.configs))
			}
			for _, n := range f.networks {
				if strings.HasPrefix(n.Name, "app_") {
					return fmt.Errorf("network %s left behind", n.Name)
				}
			}
			return nil
		},
	})
}

func TestAccStackBasic(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_stack" "test" {
  name = "tfacc"

  network {
    name = "net"
  }

  config {
    name = "index"
    data = base64encode("<h1>hello</h1>")
  }

  service {
    name     = "web"
    image    = "nginx:alpine"
    networks = { net = {} }

    configs {
      config_id = "index"
      file_name = "/usr/share/nginx/html/index.html"
    }

    converge_config {
      timeout = "2m"
    }
  }
}
`,
			Check: resource.TestCheckResourceAttr("docker_stack.test", "id", "tfacc"),
		}},
		CheckDestroy: func(*terraform.State) error {
			c := testAccDockerClient(t)
			svcsRes, err := c.ServiceList(context.Background(), client.ServiceListOptions{Filters: client.Filters{}.Add("label", labelStackNamespace+"=tfacc")})
			if err != nil {
				return err
			}
			svcs := svcsRes.Items
			if len(svcs) != 0 {
				return fmt.Errorf("%d stack services left", len(svcs))
			}
			return nil
		},
	})
}

// TestStackExamplesPlan plans the lane's example files against the fake
// daemon so they stay valid against the schema.
func TestStackExamplesPlan(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	for _, name := range []string{"docker_secret", "docker_config", "docker_service", "docker_stack"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "examples", "resources", name, "resource.tf"))
			if err != nil {
				t.Fatal(err)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:             svcFakeConfig(host, string(b)),
					PlanOnly:           true,
					ExpectNonEmptyPlan: true,
				}},
			})
		})
	}
}
