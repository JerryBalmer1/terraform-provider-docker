package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// ---------------------------------------------------------------------------
// Fake Swarm manager: enough of the Engine API for secrets, configs,
// networks, services and tasks, with the normalisations the real daemon
// applies (digest pin via distribution inspect, port/endpoint defaults,
// restart MaxAttempts always set, vxlan option on overlay networks).

type svcFakeDaemon struct {
	mu       sync.Mutex
	seq      int
	services map[string]*swarm.Service
	secrets  map[string]*swarm.Secret
	configs  map[string]*swarm.Config
	networks map[string]*network.Summary
	// creates counts ServiceCreate calls per name (to detect recreation).
	creates map[string]int
}

func newSvcFakeDaemon(t *testing.T) (*svcFakeDaemon, string) {
	f := &svcFakeDaemon{
		services: map[string]*swarm.Service{},
		secrets:  map[string]*swarm.Secret{},
		configs:  map[string]*swarm.Config{},
		networks: map[string]*network.Summary{},
		creates:  map[string]int{},
	}
	f.networks["extnet0000000001"] = &network.Summary{ID: "extnet0000000001", Name: "ext_net", Driver: "overlay", Scope: "swarm"}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, "tcp://" + srv.Listener.Addr().String()
}

var svcFakeVersionRe = regexp.MustCompile(`^/v[0-9.]+`)

func (f *svcFakeDaemon) id(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s%022d", prefix, f.seq)
}

func svcFakeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func svcFakeErr(w http.ResponseWriter, code int, msg string) {
	svcFakeJSON(w, code, map[string]string{"message": msg})
}

func svcFakeLabelsMatch(r *http.Request, labels map[string]string) bool {
	args, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return true
	}
	for _, kv := range args.Get("label") {
		k, v, _ := strings.Cut(kv, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (f *svcFakeDaemon) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := svcFakeVersionRe.ReplaceAllString(r.URL.Path, "")
	w.Header().Set("Api-Version", "1.47")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	switch {
	case p == "/_ping":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	case strings.HasPrefix(p, "/distribution/"):
		svcFakeJSON(w, 200, map[string]any{
			"Descriptor": map[string]any{"mediaType": "application/vnd.oci.image.index.v1+json", "digest": "sha256:" + strings.Repeat("a", 64), "size": 1},
			"Platforms":  []map[string]string{{"architecture": "amd64", "os": "linux"}},
		})
	case parts[0] == "networks":
		f.handleNetworks(w, r, parts)
	case parts[0] == "secrets":
		f.handleSecrets(w, r, parts)
	case parts[0] == "configs":
		f.handleConfigs(w, r, parts)
	case parts[0] == "services":
		f.handleServices(w, r, parts)
	case p == "/tasks":
		f.handleTasks(w, r)
	default:
		svcFakeErr(w, 404, "fake daemon: no route "+r.Method+" "+p)
	}
}

func (f *svcFakeDaemon) handleNetworks(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		out := []network.Summary{}
		for _, n := range f.networks {
			if svcFakeLabelsMatch(r, n.Labels) {
				out = append(out, *n)
			}
		}
		svcFakeJSON(w, 200, out)
	case len(parts) == 2 && parts[1] == "create":
		var req network.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, n := range f.networks {
			if n.Name == req.Name {
				svcFakeErr(w, 409, "network exists")
				return
			}
		}
		id := f.id("net")
		opts := map[string]string{}
		for k, v := range req.Options {
			opts[k] = v
		}
		if req.Driver == "overlay" {
			opts[stackVXLANOption] = "4097"
		}
		f.networks[id] = &network.Summary{ID: id, Name: req.Name, Driver: req.Driver, Scope: "swarm", Attachable: req.Attachable,
			Internal: req.Internal, Labels: req.Labels, Options: opts}
		svcFakeJSON(w, 201, network.CreateResponse{ID: id})
	case len(parts) == 2 && r.Method == http.MethodDelete:
		for id, s := range f.services {
			for _, n := range s.Spec.TaskTemplate.Networks {
				if n.Target == parts[1] {
					svcFakeErr(w, 403, "network in use by service "+id)
					return
				}
			}
		}
		if _, ok := f.networks[parts[1]]; !ok {
			svcFakeErr(w, 404, "no such network")
			return
		}
		delete(f.networks, parts[1])
		w.WriteHeader(204)
	default:
		svcFakeErr(w, 404, "fake: networks route")
	}
}

func (f *svcFakeDaemon) inUse(secret bool, id string) bool {
	for _, s := range f.services {
		cs := s.Spec.TaskTemplate.ContainerSpec
		if secret {
			for _, ref := range cs.Secrets {
				if ref.SecretID == id {
					return true
				}
			}
		} else {
			for _, ref := range cs.Configs {
				if ref.ConfigID == id {
					return true
				}
			}
		}
	}
	return false
}

func (f *svcFakeDaemon) handleSecrets(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		out := []swarm.Secret{}
		for _, s := range f.secrets {
			if svcFakeLabelsMatch(r, s.Spec.Labels) {
				c := *s
				c.Spec.Data = nil
				out = append(out, c)
			}
		}
		svcFakeJSON(w, 200, out)
	case len(parts) == 2 && parts[1] == "create":
		var spec swarm.SecretSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		for _, s := range f.secrets {
			if s.Spec.Name == spec.Name {
				svcFakeErr(w, 409, "secret exists")
				return
			}
		}
		id := f.id("sec")
		f.secrets[id] = &swarm.Secret{ID: id, Spec: spec}
		svcFakeJSON(w, 201, swarm.SecretCreateResponse{ID: id})
	case len(parts) == 2 && r.Method == http.MethodGet:
		s, ok := f.secrets[parts[1]]
		if !ok {
			svcFakeErr(w, 404, "no such secret")
			return
		}
		c := *s
		c.Spec.Data = nil
		svcFakeJSON(w, 200, c)
	case len(parts) == 2 && r.Method == http.MethodDelete:
		if _, ok := f.secrets[parts[1]]; !ok {
			svcFakeErr(w, 404, "no such secret")
			return
		}
		if f.inUse(true, parts[1]) {
			svcFakeErr(w, 400, "secret in use")
			return
		}
		delete(f.secrets, parts[1])
		w.WriteHeader(204)
	default:
		svcFakeErr(w, 404, "fake: secrets route")
	}
}

func (f *svcFakeDaemon) handleConfigs(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		out := []swarm.Config{}
		for _, s := range f.configs {
			if svcFakeLabelsMatch(r, s.Spec.Labels) {
				out = append(out, *s)
			}
		}
		svcFakeJSON(w, 200, out)
	case len(parts) == 2 && parts[1] == "create":
		var spec swarm.ConfigSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		for _, s := range f.configs {
			if s.Spec.Name == spec.Name {
				svcFakeErr(w, 409, "config exists")
				return
			}
		}
		id := f.id("cfg")
		f.configs[id] = &swarm.Config{ID: id, Spec: spec}
		svcFakeJSON(w, 201, swarm.ConfigCreateResponse{ID: id})
	case len(parts) == 2 && r.Method == http.MethodGet:
		s, ok := f.configs[parts[1]]
		if !ok {
			svcFakeErr(w, 404, "no such config")
			return
		}
		svcFakeJSON(w, 200, s)
	case len(parts) == 2 && r.Method == http.MethodDelete:
		if _, ok := f.configs[parts[1]]; !ok {
			svcFakeErr(w, 404, "no such config")
			return
		}
		if f.inUse(false, parts[1]) {
			svcFakeErr(w, 400, "config in use")
			return
		}
		delete(f.configs, parts[1])
		w.WriteHeader(204)
	default:
		svcFakeErr(w, 404, "fake: configs route")
	}
}

// normalize applies what the daemon's gRPC round trip does to a spec.
func svcFakeNormalize(spec *swarm.ServiceSpec) {
	if es := spec.EndpointSpec; es != nil {
		if es.Mode == "" {
			es.Mode = swarm.ResolutionModeVIP
		}
		for i := range es.Ports {
			if es.Ports[i].Protocol == "" {
				es.Ports[i].Protocol = swarm.PortConfigProtocolTCP
			}
			if es.Ports[i].PublishMode == "" {
				es.Ports[i].PublishMode = swarm.PortConfigPublishModeIngress
			}
		}
	}
	if rp := spec.TaskTemplate.RestartPolicy; rp != nil {
		if rp.MaxAttempts == nil {
			z := uint64(0)
			rp.MaxAttempts = &z
		}
		if rp.Condition == "" {
			rp.Condition = swarm.RestartPolicyConditionAny
		}
	}
	if j := spec.Mode.ReplicatedJob; j != nil {
		if j.MaxConcurrent == nil {
			one := uint64(1)
			j.MaxConcurrent = &one
		}
		if j.TotalCompletions == nil {
			tc := *j.MaxConcurrent
			j.TotalCompletions = &tc
		}
	}
	if spec.Mode.Replicated == nil && spec.Mode.Global == nil && spec.Mode.ReplicatedJob == nil && spec.Mode.GlobalJob == nil {
		one := uint64(1)
		spec.Mode.Replicated = &swarm.ReplicatedService{Replicas: &one}
	}
}

// svcFakeJobStatus bumps the job iteration of a job-mode service (each
// create/update starts a new run).
func svcFakeJobStatus(s *swarm.Service) {
	if s.Spec.Mode.ReplicatedJob == nil && s.Spec.Mode.GlobalJob == nil {
		s.JobStatus = nil
		return
	}
	if s.JobStatus == nil {
		s.JobStatus = &swarm.JobStatus{}
	}
	s.JobStatus.JobIteration.Index = s.Version.Index
}

func (f *svcFakeDaemon) findService(ref string) *swarm.Service {
	if s, ok := f.services[ref]; ok {
		return s
	}
	for _, s := range f.services {
		if s.Spec.Name == ref {
			return s
		}
	}
	return nil
}

func (f *svcFakeDaemon) handleServices(w http.ResponseWriter, r *http.Request, parts []string) {
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		out := []swarm.Service{}
		for _, s := range f.services {
			if svcFakeLabelsMatch(r, s.Spec.Labels) {
				out = append(out, *s)
			}
		}
		svcFakeJSON(w, 200, out)
	case len(parts) == 2 && parts[1] == "create":
		var spec swarm.ServiceSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		if f.findService(spec.Name) != nil {
			svcFakeErr(w, 409, "service exists")
			return
		}
		svcFakeNormalize(&spec)
		id := f.id("svc")
		s := &swarm.Service{ID: id, Spec: spec}
		s.Version.Index = 1
		svcFakeJobStatus(s)
		f.services[id] = s
		f.creates[spec.Name]++
		svcFakeJSON(w, 201, swarm.ServiceCreateResponse{ID: id})
	case len(parts) == 2 && r.Method == http.MethodGet:
		s := f.findService(parts[1])
		if s == nil {
			svcFakeErr(w, 404, "service "+parts[1]+" not found")
			return
		}
		svcFakeJSON(w, 200, s)
	case len(parts) == 3 && parts[2] == "update":
		s := f.findService(parts[1])
		if s == nil {
			svcFakeErr(w, 404, "service not found")
			return
		}
		if r.URL.Query().Get("version") != fmt.Sprint(s.Version.Index) {
			svcFakeErr(w, 500, "update out of sequence")
			return
		}
		var spec swarm.ServiceSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		if svcModeOf(spec) != svcModeOf(s.Spec) {
			svcFakeErr(w, 400, "service mode change is not allowed")
			return
		}
		svcFakeNormalize(&spec)
		s.Spec = spec
		s.Version.Index++
		svcFakeJobStatus(s)
		svcFakeJSON(w, 200, swarm.ServiceUpdateResponse{})
	case len(parts) == 2 && r.Method == http.MethodDelete:
		s := f.findService(parts[1])
		if s == nil {
			svcFakeErr(w, 404, "service not found")
			return
		}
		delete(f.services, s.ID)
		w.WriteHeader(200)
	default:
		svcFakeErr(w, 404, "fake: services route")
	}
}

func (f *svcFakeDaemon) handleTasks(w http.ResponseWriter, r *http.Request) {
	args, _ := filters.FromJSON(r.URL.Query().Get("filters"))
	var only *swarm.Service
	if refs := args.Get("service"); len(refs) > 0 {
		if only = f.findService(refs[0]); only == nil {
			svcFakeErr(w, 404, "service not found")
			return
		}
	}
	out := []swarm.Task{}
	for _, s := range f.services {
		if only != nil && s != only {
			continue
		}
		if j := s.Spec.Mode.ReplicatedJob; j != nil || s.Spec.Mode.GlobalJob != nil {
			// Job tasks of the current run, all completed.
			n := 1
			if j != nil {
				n = int(*j.TotalCompletions)
			}
			for i := 0; i < n; i++ {
				out = append(out, swarm.Task{ID: fmt.Sprintf("%s-j%d", s.ID, i), ServiceID: s.ID, DesiredState: swarm.TaskStateComplete,
					Status: swarm.TaskStatus{State: swarm.TaskStateComplete}, JobIteration: &swarm.Version{Index: s.JobStatus.JobIteration.Index}})
			}
			continue
		}
		n := 1
		if s.Spec.Mode.Replicated != nil {
			n = int(*s.Spec.Mode.Replicated.Replicas)
		}
		for i := 0; i < n; i++ {
			out = append(out, swarm.Task{ID: fmt.Sprintf("%s-t%d", s.ID, i), ServiceID: s.ID, DesiredState: swarm.TaskStateRunning,
				Status: swarm.TaskStatus{State: swarm.TaskStateRunning}})
		}
	}
	svcFakeJSON(w, 200, out)
}

// ---------------------------------------------------------------------------
// Unit tests through Terraform against the fake daemon (no real daemon).

func svcFakeConfig(host, body string) string {
	return fmt.Sprintf("provider \"docker\" {\n  host = %q\n}\n%s", host, body)
}

func TestServiceFakeDaemonLifecycle(t *testing.T) {
	f, host := newSvcFakeDaemon(t)
	cfg := func(image string, replicas int) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_secret" "pw" {
  name = "pw"
  data = base64encode("s3cret")
}

resource "docker_config" "conf" {
  name   = "conf"
  data   = base64encode("x=1")
  labels = { a = "b" }
}

resource "docker_service" "web" {
  name     = "web"
  image    = %q
  replicas = %d
  networks = ["ext_net"]
  env      = { A = "1" }
  labels   = {}

  mounts {
    type   = "volume"
    source = "data"
    target = "/data"
  }

  endpoint_spec {
    ports {
      target_port    = 80
      published_port = 8080
    }
  }

  secrets {
    secret_id = docker_secret.pw.id
  }

  configs {
    config_id = docker_config.conf.name
    file_name = "/etc/conf"
  }

  update_config {
    parallelism = 1
    delay       = "1m"
  }

  restart_policy {
    condition = "on-failure"
  }

  placement {
    constraints = ["node.role==worker"]
  }

  converge_config {
    timeout = "10s"
    delay   = "100ms"
  }
}
`, image, replicas))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("nginx:1.27", 2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_service.web", "image", "nginx:1.27"),
					resource.TestCheckResourceAttr("docker_service.web", "mode", "replicated"),
					resource.TestCheckNoResourceAttr("docker_service.web", "secrets.0.file_name"),
					resource.TestCheckResourceAttr("docker_service.web", "endpoint_spec.ports.0.protocol", "tcp"),
					resource.TestCheckResourceAttr("docker_service.web", "networks.0", "ext_net"),
					func(*terraform.State) error {
						f.mu.Lock()
						defer f.mu.Unlock()
						s := f.findService("web")
						cs := s.Spec.TaskTemplate.ContainerSpec
						if cs.Secrets[0].File.Name != "pw" || cs.Secrets[0].SecretName != "pw" {
							return fmt.Errorf("secret ref = %+v %+v", cs.Secrets[0], cs.Secrets[0].File)
						}
						if !strings.Contains(cs.Image, "@sha256:") {
							return fmt.Errorf("image not pinned: %s", cs.Image)
						}
						if s.Spec.TaskTemplate.Networks[0].Target != "extnet0000000001" {
							return fmt.Errorf("network not resolved: %+v", s.Spec.TaskTemplate.Networks)
						}
						return nil
					},
				),
			},
			{
				Config: cfg("nginx:1.28", 3),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.web", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					s := f.findService("web")
					if *s.Spec.Mode.Replicated.Replicas != 3 || !strings.HasPrefix(s.Spec.TaskTemplate.ContainerSpec.Image, "nginx:1.28@") {
						return fmt.Errorf("update not applied: %+v", s.Spec.TaskTemplate.ContainerSpec.Image)
					}
					if f.creates["web"] != 1 {
						return fmt.Errorf("service was recreated %d times", f.creates["web"])
					}
					return nil
				},
			},
			{
				ResourceName:            "docker_service.web",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"converge_config", "image", "networks", "configs.0.config_id", "secrets.0.secret_id", "labels.%"},
			},
			{
				ResourceName:            "docker_secret.pw",
				ImportState:             true,
				ImportStateId:           "pw",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"data"},
			},
			{
				ResourceName:      "docker_config.conf",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
		CheckDestroy: func(*terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.services)+len(f.secrets)+len(f.configs) != 0 {
				return fmt.Errorf("left behind: %d services, %d secrets, %d configs", len(f.services), len(f.secrets), len(f.configs))
			}
			return nil
		},
	})
}

func TestServiceFakeDaemonModeChangeReplaces(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	cfg := func(mode string) string {
		return svcFakeConfig(host, fmt.Sprintf(`
resource "docker_service" "agent" {
  name  = "agent"
  image = "busybox"
  mode  = %q
  args  = ["sleep", "infinity"]
}
`, mode))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg("replicated"), Check: resource.TestCheckResourceAttr("docker_service.agent", "image", "busybox")},
			{
				Config: cfg("global"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_service.agent", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: resource.TestCheckResourceAttr("docker_service.agent", "mode", "global"),
			},
		},
	})
}

// ---------------------------------------------------------------------------
// Acceptance (real Swarm manager; skipped without TF_ACC and DOCKER_HOST).

func TestAccServiceBasic(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "docker_service" "test" {
  name     = "tfacc-svc"
  image    = "nginx:alpine"
  replicas = 1

  endpoint_spec {
    ports {
      target_port    = 80
      published_port = 18080
    }
  }

  converge_config {
    timeout = "2m"
  }
}
`,
				Check: resource.TestCheckResourceAttrSet("docker_service.test", "id"),
			},
			{ResourceName: "docker_service.test", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"converge_config", "image"}},
		},
		CheckDestroy: func(s *terraform.State) error {
			c := testAccDockerClient(t)
			for _, rs := range s.RootModule().Resources {
				if rs.Type != "docker_service" {
					continue
				}
				if _, _, err := c.ServiceInspectWithRaw(context.Background(), rs.Primary.ID, swarm.ServiceInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("service %s still exists (err=%v)", rs.Primary.ID, err)
				}
			}
			return nil
		},
	})
}
