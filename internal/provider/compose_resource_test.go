package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// composeTestYAML is a web+db+cache project with a secret from env, a
// config from a file, a named volume and a healthcheck dependency.
const composeTestYAML = `
name: shop
services:
  web:
    image: nginx:%s
    ports: ["8080:80"]
    depends_on:
      db: {condition: service_healthy}
    configs:
      - source: site
        target: /etc/nginx/conf.d/default.conf
    networks: [front, back]
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/db_pw
    secrets: [db_pw]
    volumes: ["pgdata:/var/lib/postgresql/data"]
    healthcheck:
      test: ["CMD", "pg_isready"]
      interval: 1s
    networks: [back]
%s
networks:
  front: {}
  back: {}
volumes:
  pgdata: {}
secrets:
  db_pw:
    environment: DB_PW
configs:
  site:
    file: ./site.conf
`

func composeTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "site.conf"), []byte("server { listen 80; }"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func composeTestConfig(host, dir, tag, extra string) string {
	return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_compose" "shop" {
  working_dir = %q
  env         = { DB_PW = "s3cret" }
  content     = <<-EOT
%s
EOT
}
`, filepath.ToSlash(dir), fmt.Sprintf(composeTestYAML, tag, extra)))
}

func TestComposeFakeLocalLifecycle(t *testing.T) {
	f, host := newComposeFakeEngine(t)
	dir := composeTestDir(t)
	check := func(fn func(f *composeFakeEngine) error) resource.TestCheckFunc {
		return func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			return fn(f)
		}
	}
	cache := `  cache:
    image: redis:7
    networks: [back]
    deploy: {replicas: 2}`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: composeTestConfig(host, dir, "1.27", cache),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_compose.shop", "id", "shop"),
					resource.TestCheckResourceAttr("docker_compose.shop", "project_name", "shop"),
					resource.TestCheckResourceAttr("docker_compose.shop", "mode", "local"),
					resource.TestCheckResourceAttr("docker_compose.shop", "services.%", "3"),
					resource.TestCheckResourceAttr("docker_compose.shop", "services.cache.#", "2"),
					resource.TestCheckResourceAttr("docker_compose.shop", "networks.%", "2"),
					resource.TestCheckResourceAttr("docker_compose.shop", "volumes.pgdata", "shop_pgdata"),
					resource.TestMatchResourceAttr("docker_compose.shop", "spec_hash", regexp.MustCompile(`^[0-9a-f]{64}$`)),
					check(func(f *composeFakeEngine) error {
						web := f.byName("shop-web-1")
						db := f.byName("shop-db-1")
						if web == nil || db == nil || f.byName("shop-cache-2") == nil {
							return fmt.Errorf("containers missing: %d", len(f.containers))
						}
						for k, v := range map[string]string{composeLabelProject: "shop", composeLabelService: "web", composeLabelNumber: "1", composeLabelOneoff: "False"} {
							if web.Config.Labels[k] != v {
								return fmt.Errorf("web label %s = %q", k, web.Config.Labels[k])
							}
						}
						if web.State != "running" || db.State != "running" {
							return fmt.Errorf("states %s %s", web.State, db.State)
						}
						if _, ok := web.Nets["shop_front"]; !ok {
							return fmt.Errorf("web networks %v", web.Nets)
						}
						if _, ok := web.Nets["shop_back"]; !ok {
							return fmt.Errorf("web not connected to back: %v", web.Nets)
						}
						if got := web.Nets["shop_front"].Aliases; len(got) == 0 || got[0] != "web" {
							return fmt.Errorf("web aliases %v", got)
						}
						// config from a file: read-only bind mount
						var bind bool
						for _, m := range web.Host.Mounts {
							if m.Target == "/etc/nginx/conf.d/default.conf" && m.ReadOnly && strings.HasSuffix(filepath.ToSlash(m.Source), "/site.conf") {
								bind = true
							}
						}
						if !bind {
							return fmt.Errorf("web mounts %+v", web.Host.Mounts)
						}
						// secret from env: copied in before start
						if got := db.Files["/run/secrets/db_pw"]; got != "s3cret|444|0:0" {
							return fmt.Errorf("db files %v", db.Files)
						}
						if db.Host.Mounts[0].Source != "shop_pgdata" {
							return fmt.Errorf("db mounts %+v", db.Host.Mounts)
						}
						if pb := web.Host.PortBindings[network.MustParsePort("80/tcp")]; len(pb) != 1 || pb[0].HostPort != "8080" {
							return fmt.Errorf("web ports %v", web.Host.PortBindings)
						}
						if c, n, v := f.projectObjects("shop"); c != 4 || n != 2 || v != 1 {
							return fmt.Errorf("objects %d/%d/%d", c, n, v)
						}
						return nil
					}),
				),
			},
			{
				// Out-of-band removal of a container and a stopped one are
				// drift: refresh marks spec_hash and the plan is an update.
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					delete(f.containers, f.byName("shop-cache-2").ID)
					c := f.byName("shop-db-1")
					c.State, c.Exit = "exited", 137
				},
				Config: composeTestConfig(host, dir, "1.27", cache),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.shop", plancheck.ResourceActionUpdate),
				}},
				Check: check(func(f *composeFakeEngine) error {
					if f.byName("shop-cache-2") == nil {
						return fmt.Errorf("cache-2 not recreated")
					}
					if c := f.byName("shop-db-1"); c.State != "running" || f.creates["shop-db-1"] != 1 || c.Started != 2 {
						return fmt.Errorf("db: state %s creates %d starts %d", c.State, f.creates["shop-db-1"], c.Started)
					}
					if f.creates["shop-web-1"] != 1 {
						return fmt.Errorf("web recreated")
					}
					return nil
				}),
			},
			{
				// Changing one service recreates only it; dropping cache
				// removes its containers (orphans).
				Config: composeTestConfig(host, dir, "1.28", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.shop", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_compose.shop", "services.%", "2"),
					check(func(f *composeFakeEngine) error {
						if f.creates["shop-web-1"] != 2 || f.creates["shop-db-1"] != 1 {
							return fmt.Errorf("creates %v", f.creates)
						}
						if f.byName("shop-cache-1") != nil {
							return fmt.Errorf("orphan cache left")
						}
						if f.byName("shop-web-1").Config.Image != "nginx:1.28" {
							return fmt.Errorf("web image %s", f.byName("shop-web-1").Config.Image)
						}
						return nil
					}),
				),
			},
			{
				ResourceName:      "docker_compose.shop",
				ImportState:       true,
				ImportStateId:     "shop",
				ImportStateVerify: false,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					a := states[0].Attributes
					if a["project_name"] != "shop" || a["mode"] != "local" || a["services.%"] != "2" {
						return fmt.Errorf("import attributes %v", a)
					}
					return nil
				},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if c, n, v := f.projectObjects("shop"); c+n+v != 0 {
				return fmt.Errorf("left behind: %d containers, %d networks, %d volumes", c, n, v)
			}
			if f.findNetwork("shared") == nil {
				return fmt.Errorf("unrelated network removed")
			}
			return nil
		},
	})
}

// TestComposeFakeLocalFilesAndProfiles uses files, env_file, profiles and an
// external network; keep_volumes leaves the volume on destroy.
func TestComposeFakeLocalFilesAndProfiles(t *testing.T) {
	f, host := newComposeFakeEngine(t)
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yml", `
services:
  app:
    image: busybox:${TAG}
    command: ["sleep", "infinity"]
    volumes: [data:/data]
    networks: [shared]
  debug:
    image: busybox
    profiles: [debug]
volumes:
  data: {}
networks:
  shared: {external: true}
`)
	write("override.yml", "services:\n  app:\n    environment: [MODE=prod]\n")
	write("vars.env", "TAG=1.36\n")
	cfg := func(profiles string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_compose" "p" {
  project_name = "demo"
  files        = [%q, %q]
  env_file     = ["vars.env"]
  profiles     = %s
  keep_volumes = true
}
`, filepath.ToSlash(filepath.Join(dir, "compose.yml")), filepath.ToSlash(filepath.Join(dir, "override.yml")), profiles))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("[]"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_compose.p", "services.%", "1"),
					resource.TestCheckResourceAttr("docker_compose.p", "networks.%", "0"),
					func(*terraform.State) error {
						f.mu.Lock()
						defer f.mu.Unlock()
						app := f.byName("demo-app-1")
						if app == nil || app.Config.Image != "busybox:1.36" || app.Config.Env[0] != "MODE=prod" {
							return fmt.Errorf("app %+v", app)
						}
						if _, ok := app.Nets["shared"]; !ok {
							return fmt.Errorf("app nets %v", app.Nets)
						}
						return nil
					},
				),
			},
			{
				Config: cfg(`["debug"]`),
				Check:  resource.TestCheckResourceAttr("docker_compose.p", "services.%", "2"),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if c, _, v := f.projectObjects("demo"); c != 0 || v != 1 {
				return fmt.Errorf("after destroy: %d containers, %d volumes (want 0, 1)", c, v)
			}
			return nil
		},
	})
}

func TestComposePlanErrors(t *testing.T) {
	_, host := newComposeFakeEngine(t)
	for name, tc := range map[string]struct{ body, want string }{
		"neither": {`resource "docker_compose" "x" { project_name = "x" }`, `(?s)one of.*content.*files`},
		"both": {`resource "docker_compose" "x" {
  content = "services: {}"
  files   = ["a.yml"]
}`, `(?s)content.*files`},
		"bad yaml": {`resource "docker_compose" "x" {
  project_name = "x"
  content = "services:\n  a:\n    image: [1"
}`, `Invalid compose project`},
		"build only": {`resource "docker_compose" "x" {
  project_name = "x"
  content = "services:\n  a:\n    build: ."
}`, `does not build images`},
		"external secret local": {`resource "docker_compose" "x" {
  project_name = "x"
  content = "services:\n  a:\n    image: busybox\n    secrets: [s]\nsecrets:\n  s: {external: true}"
}`, `external secrets and configs exist only in\s+Swarm`},
		"bad mode": {`resource "docker_compose" "x" {
  content = "services: {}"
  mode    = "k8s"
}`, `value must be one of`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      svcFakeConfig(host, tc.body),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

// TestComposeFakeSwarmLifecycle runs swarm mode against the docker_service
// fake manager.
func TestComposeFakeSwarmLifecycle(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	cfg := func(pw string, replicas int) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_compose" "s" {
  mode         = "swarm"
  project_name = "app"
  env          = { PW = %q }
  content      = <<-EOT
services:
  web:
    image: nginx:1.27
    ports: ["8080:80"]
    configs: [{source: conf, target: /etc/nginx/conf.d/default.conf}]
    deploy:
      replicas: %d
      placement: {constraints: ["node.role==worker"]}
      labels: {tier: front}
  db:
    image: postgres:16
    secrets: [pw]
    volumes: [data:/var/lib/postgresql/data]
    networks: [back]
networks:
  back: {driver: overlay}
volumes:
  data: {}
secrets:
  pw: {environment: PW}
configs:
  conf: {content: "listen 80;"}
EOT
}
`, pw, replicas))
	}
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
				Config: cfg("one", 2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_compose.s", "services.%", "2"),
					resource.TestCheckResourceAttr("docker_compose.s", "volumes.data", "app_data"),
					resource.TestCheckResourceAttr("docker_compose.s", "networks.%", "2"),
					check(func(f *svcFakeDaemon) error {
						web, db := f.findService("app_web"), f.findService("app_db")
						if web == nil || db == nil {
							return fmt.Errorf("services %v", f.services)
						}
						if web.Spec.Labels[labelStackNamespace] != "app" || web.Spec.Labels["tier"] != "front" {
							return fmt.Errorf("web labels %v", web.Spec.Labels)
						}
						if *web.Spec.Mode.Replicated.Replicas != 2 || web.Spec.TaskTemplate.Placement.Constraints[0] != "node.role==worker" {
							return fmt.Errorf("web mode/placement %+v %+v", web.Spec.Mode, web.Spec.TaskTemplate.Placement)
						}
						sec := db.Spec.TaskTemplate.ContainerSpec.Secrets[0]
						if sec.SecretName != "app_pw" || sec.File.Name != "pw" || f.secrets[sec.SecretID] == nil {
							return fmt.Errorf("db secret %+v", sec)
						}
						if string(f.secrets[sec.SecretID].Spec.Data) != "one" {
							return fmt.Errorf("secret data %q", f.secrets[sec.SecretID].Spec.Data)
						}
						net := f.networks[db.Spec.TaskTemplate.Networks[0].Target]
						if net == nil || net.Name != "app_back" {
							return fmt.Errorf("db network %+v", db.Spec.TaskTemplate.Networks)
						}
						m := db.Spec.TaskTemplate.ContainerSpec.Mounts[0]
						if m.Source != "app_data" || m.VolumeOptions.Labels[labelStackNamespace] != "app" {
							return fmt.Errorf("db mount %+v", m)
						}
						if p := web.Spec.EndpointSpec.Ports[0]; p.PublishedPort != 8080 || p.TargetPort != 80 {
							return fmt.Errorf("web ports %+v", web.Spec.EndpointSpec.Ports)
						}
						return nil
					}),
				),
			},
			{
				// Out-of-band scale is drift.
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					three := uint64(3)
					f.findService("app_web").Spec.Mode.Replicated.Replicas = &three
				},
				Config: cfg("one", 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.s", plancheck.ResourceActionUpdate),
				}},
				Check: check(func(f *svcFakeDaemon) error {
					if r := *f.findService("app_web").Spec.Mode.Replicated.Replicas; r != 2 {
						return fmt.Errorf("replicas %d", r)
					}
					if f.creates["app_web"] != 1 {
						return fmt.Errorf("web recreated: %v", f.creates)
					}
					return nil
				}),
			},
			{
				// A changed secret replaces it and recreates its user only.
				Config: cfg("two", 2),
				Check: check(func(f *svcFakeDaemon) error {
					if f.creates["app_db"] != 2 || f.creates["app_web"] != 1 {
						return fmt.Errorf("creates %v", f.creates)
					}
					if len(f.secrets) != 1 {
						return fmt.Errorf("secrets %d", len(f.secrets))
					}
					return nil
				}),
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
					return fmt.Errorf("network %s left", n.Name)
				}
			}
			return nil
		},
	})
}

// TestComposeExamplePlan plans the example against the fake engine.
func TestComposeExamplePlan(t *testing.T) {
	_, host := newComposeFakeEngine(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "resources", "docker_compose", "resource.tf"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "examples", "resources", "docker_compose")
	abs, _ := filepath.Abs(dir)
	body := strings.ReplaceAll(string(b), "path.module", fmt.Sprintf("%q", filepath.ToSlash(abs)))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:             svcFakeConfig(host, body),
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

// ---------------------------------------------------------------------------
// Acceptance

func TestAccComposeLocal(t *testing.T) {
	testAccPreCheck(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>compose</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := func(replicas int) string {
		return fmt.Sprintf(`
resource "docker_compose" "acc" {
  project_name = "tfacc-compose"
  working_dir  = %q
  env          = { MSG = "hello" }
  content      = <<-EOT
services:
  web:
    image: nginx:alpine
    configs: [{source: index, target: /usr/share/nginx/html/index.html}]
    healthcheck:
      test: ["CMD", "wget", "-q", "-O-", "http://127.0.0.1/"]
      interval: 2s
    networks: [app]
  worker:
    image: busybox:1.36
    command: ["sh", "-c", "cat /run/secrets/msg && sleep 3600"]
    secrets: [msg]
    depends_on: {web: {condition: service_healthy}}
    deploy: {replicas: %d}
    volumes: [data:/data]
    networks: [app]
networks:
  app: {}
volumes:
  data: {}
secrets:
  msg: {environment: MSG}
configs:
  # Inline content is copied into the container, so it works against a remote
  # daemon (the harness); a file source would be a host-path bind mount.
  index: {content: "<h1>compose</h1>"}
EOT
}
`, filepath.ToSlash(dir), replicas)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg(1), Check: resource.TestCheckResourceAttr("docker_compose.acc", "services.worker.#", "1")},
			{Config: cfg(2), Check: resource.TestCheckResourceAttr("docker_compose.acc", "services.worker.#", "2")},
		},
		CheckDestroy: func(*terraform.State) error {
			c := testAccDockerClient(t)
			f := client.Filters{}.Add("label", composeLabelProject+"=tfacc-compose")
			csRes, err := c.ContainerList(context.Background(), client.ContainerListOptions{All: true, Filters: f})
			if err != nil {
				return err
			}
			cs := csRes.Items
			nsRes, err := c.NetworkList(context.Background(), client.NetworkListOptions{Filters: f})
			if err != nil {
				return err
			}
			ns := nsRes.Items
			if len(cs)+len(ns) != 0 {
				return fmt.Errorf("left behind: %d containers, %d networks", len(cs), len(ns))
			}
			return nil
		},
	})
}

func TestAccComposeSwarm(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_compose" "acc" {
  mode         = "swarm"
  project_name = "tfacccompose"
  content      = <<-EOT
services:
  web:
    image: nginx:alpine
    configs: [{source: index, target: /usr/share/nginx/html/index.html}]
    networks: [net]
    deploy: {replicas: 1}
networks:
  net: {}
configs:
  index: {content: "<h1>hello</h1>"}
EOT
}
`,
			Check: resource.TestCheckResourceAttr("docker_compose.acc", "services.%", "1"),
		}},
		CheckDestroy: func(*terraform.State) error {
			c := testAccDockerClient(t)
			svcsRes, err := c.ServiceList(context.Background(), client.ServiceListOptions{Filters: client.Filters{}.Add("label", labelStackNamespace+"=tfacccompose")})
			if err != nil {
				return err
			}
			svcs := svcsRes.Items
			if len(svcs) != 0 {
				return fmt.Errorf("%d services left", len(svcs))
			}
			return nil
		},
	})
}
