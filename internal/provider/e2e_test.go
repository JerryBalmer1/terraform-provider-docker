package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// e2eParams are the inputs for the end-to-end configuration. All of them
// come from test/harness/.env (see test/harness/README.md).
type e2eParams struct {
	Suffix        string // random suffix for object names
	AdvertiseAddr string // E2E_ADVERTISE_ADDR, e.g. "eth0"
	ManagerAddr   string // E2E_MANAGER_ADDR, e.g. "manager:2377"
	Worker1Host   string // DOCKER_WORKER1_HOST
	Worker2Host   string // DOCKER_WORKER2_HOST
}

const e2eImage = "nginx:1.27-alpine"

// e2eConfig renders the full image -> service chain.
//
// Ordering note: the build brief lists "image -> network -> swarm_init", but
// an overlay network can only be created on an active swarm manager, so the
// network depends_on docker_swarm_init here (image -> swarm_init -> network
// -> nodes -> secret -> service).
func e2eConfig(p e2eParams) string {
	return fmt.Sprintf(`
resource "docker_image" "e2e" {
  name         = %[2]q
  keep_locally = false
}

resource "docker_swarm_init" "e2e" {
  advertise_addr = %[3]q
}

# Overlay networks need an active swarm: depend on swarm_init explicitly.
resource "docker_network" "e2e" {
  name       = "e2e-net-%[1]s"
  driver     = "overlay"
  attachable = true

  depends_on = [docker_swarm_init.e2e]
}

resource "docker_swarm_node" "e2e_worker1" {
  host            = %[5]q
  join_token      = docker_swarm_init.e2e.worker_join_token
  manager_address = %[4]q
  advertise_addr  = %[3]q
}

resource "docker_swarm_node" "e2e_worker2" {
  host            = %[6]q
  join_token      = docker_swarm_init.e2e.worker_join_token
  manager_address = %[4]q
  advertise_addr  = %[3]q
}

resource "docker_secret" "e2e" {
  name = "e2e-secret-%[1]s"
  data = base64encode("e2e-secret-value-%[1]s")

  depends_on = [docker_swarm_init.e2e]
}

resource "docker_service" "e2e" {
  name     = "e2e-svc-%[1]s"
  image    = docker_image.e2e.name
  replicas = 3
  networks = [docker_network.e2e.id]

  secrets {
    secret_id   = docker_secret.e2e.id
    secret_name = docker_secret.e2e.name
    file_name   = "/run/secrets/e2e"
  }

  placement {
    constraints = ["node.role==worker"]
  }

  converge_config {
    timeout = "5m"
  }

  depends_on = [
    docker_swarm_node.e2e_worker1,
    docker_swarm_node.e2e_worker2,
  ]
}
`, p.Suffix, e2eImage, p.AdvertiseAddr, p.ManagerAddr, p.Worker1Host, p.Worker2Host)
}

// TestE2EConfigRenders checks the generated HCL without a daemon: every
// resource type is present and the text is syntactically valid HCL.
func TestE2EConfigRenders(t *testing.T) {
	cfg := e2eConfig(e2eParams{
		Suffix:        "abc123",
		AdvertiseAddr: "eth0",
		ManagerAddr:   "manager:2377",
		Worker1Host:   "tcp://127.0.0.1:12376",
		Worker2Host:   "tcp://127.0.0.1:12377",
	})
	for _, want := range []string{
		`resource "docker_image" "e2e"`,
		`resource "docker_swarm_init" "e2e"`,
		`resource "docker_network" "e2e"`,
		`resource "docker_swarm_node" "e2e_worker1"`,
		`resource "docker_swarm_node" "e2e_worker2"`,
		`resource "docker_secret" "e2e"`,
		`resource "docker_service" "e2e"`,
		`"tcp://127.0.0.1:12376"`,
		`"tcp://127.0.0.1:12377"`,
		`"manager:2377"`,
		`"e2e-svc-abc123"`,
		`docker_swarm_init.e2e.worker_join_token`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q", want)
		}
	}
	if strings.Contains(cfg, "required_providers") {
		t.Error("config must not contain a required_providers block")
	}

	f, diags := hclsyntax.ParseConfig([]byte(cfg), "e2e.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("HCL parse: %s", diags.Error())
	}
	body := f.Body.(*hclsyntax.Body)
	counts := map[string]int{}
	for _, b := range body.Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 {
			t.Errorf("unexpected top-level block %s %v", b.Type, b.Labels)
			continue
		}
		counts[b.Labels[0]]++
	}
	want := map[string]int{
		"docker_image": 1, "docker_swarm_init": 1, "docker_network": 1,
		"docker_swarm_node": 2, "docker_secret": 1, "docker_service": 1,
	}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("%s: got %d blocks, want %d", k, counts[k], n)
		}
	}
}

// TestAccE2EImageToService runs the whole chain against the dind harness in
// test/harness (make e2e).
func TestAccE2EImageToService(t *testing.T) {
	testAccPreCheck(t)
	p := e2eParams{
		Suffix:        acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum),
		AdvertiseAddr: os.Getenv("E2E_ADVERTISE_ADDR"),
		ManagerAddr:   os.Getenv("E2E_MANAGER_ADDR"),
		Worker1Host:   os.Getenv("DOCKER_WORKER1_HOST"),
		Worker2Host:   os.Getenv("DOCKER_WORKER2_HOST"),
	}
	if p.Worker1Host == "" || p.Worker2Host == "" || p.ManagerAddr == "" {
		t.Skip("DOCKER_WORKER1_HOST, DOCKER_WORKER2_HOST and E2E_MANAGER_ADDR must be set (source test/harness/.env)")
	}
	if p.AdvertiseAddr == "" {
		p.AdvertiseAddr = "eth0"
	}
	cfg := e2eConfig(p)

	// Captured by e2eCheckServiceID so CheckDestroy knows what to look for.
	var serviceID, secretID string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             e2eCheckDestroy(t, &serviceID, &secretID),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("docker_image.e2e", "image_id"),
					resource.TestCheckResourceAttrSet("docker_swarm_init.e2e", "cluster_id"),
					resource.TestCheckResourceAttrSet("docker_swarm_init.e2e", "node_id"),
					resource.TestCheckResourceAttrSet("docker_network.e2e", "id"),
					resource.TestCheckResourceAttr("docker_network.e2e", "scope", "swarm"),
					resource.TestCheckResourceAttrSet("docker_swarm_node.e2e_worker1", "node_id"),
					resource.TestCheckResourceAttrSet("docker_swarm_node.e2e_worker2", "node_id"),
					resource.TestCheckResourceAttrSet("docker_secret.e2e", "id"),
					resource.TestCheckResourceAttrSet("docker_service.e2e", "id"),
					resource.TestCheckResourceAttr("docker_service.e2e", "replicas", "3"),
					e2eCaptureIDs(&serviceID, &secretID),
					e2eCheckTasksSpread(t, &serviceID, 3, 2),
				),
			},
			{
				// Re-plan the same config: Read must not drift.
				Config:   cfg,
				PlanOnly: true,
			},
		},
	})
}

func e2eCaptureIDs(serviceID, secretID *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for addr, dst := range map[string]*string{"docker_service.e2e": serviceID, "docker_secret.e2e": secretID} {
			rs, ok := s.RootModule().Resources[addr]
			if !ok || rs.Primary == nil || rs.Primary.ID == "" {
				return fmt.Errorf("%s not in state", addr)
			}
			*dst = rs.Primary.ID
		}
		return nil
	}
}

// e2eCheckTasksSpread waits until the service has exactly `replicas` running
// tasks spread over exactly `nodes` distinct nodes.
func e2eCheckTasksSpread(t *testing.T, serviceID *string, replicas, nodes int) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		cli := testAccDockerClient(t)
		defer cli.Close()
		var last string
		err := waitFor(context.Background(), 3*time.Minute, 3*time.Second, func(ctx context.Context) (bool, error) {
			tasks, err := cli.TaskList(ctx, swarm.TaskListOptions{Filters: filters.NewArgs(
				filters.Arg("service", *serviceID),
				filters.Arg("desired-state", "running"),
			)})
			if err != nil {
				return false, err
			}
			running := 0
			nodeIDs := map[string]bool{}
			for _, tk := range tasks {
				if tk.Status.State == swarm.TaskStateRunning {
					running++
					nodeIDs[tk.NodeID] = true
				}
			}
			last = fmt.Sprintf("%d tasks desired running, %d running, on %d nodes", len(tasks), running, len(nodeIDs))
			return len(tasks) == replicas && running == replicas && len(nodeIDs) == nodes, nil
		})
		if err != nil {
			return fmt.Errorf("service %s did not converge (want %d running on %d nodes; last: %s): %w", *serviceID, replicas, nodes, last, err)
		}
		return nil
	}
}

// e2eCheckDestroy verifies the service and secret are gone and every node
// has left the swarm.
func e2eCheckDestroy(t *testing.T, serviceID, secretID *string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		ctx := context.Background()
		cli := testAccDockerClient(t)
		defer cli.Close()

		info, err := cli.Info(ctx)
		if err != nil {
			return fmt.Errorf("manager info: %w", err)
		}
		if st := info.Swarm.LocalNodeState; st != swarm.LocalNodeStateInactive {
			// Swarm still up: the objects must at least be gone.
			if *serviceID != "" {
				if _, _, err := cli.ServiceInspectWithRaw(ctx, *serviceID, swarm.ServiceInspectOptions{}); !isNotFound(err) {
					return fmt.Errorf("service %s still exists (err=%v)", *serviceID, err)
				}
			}
			if *secretID != "" {
				if _, _, err := cli.SecretInspectWithRaw(ctx, *secretID); !isNotFound(err) {
					return fmt.Errorf("secret %s still exists (err=%v)", *secretID, err)
				}
			}
			return fmt.Errorf("manager swarm state is %q, want inactive", st)
		}
		// With the manager out of the swarm the service and secret are gone
		// with it; also make sure the workers left.
		for _, env := range []string{"DOCKER_WORKER1_HOST", "DOCKER_WORKER2_HOST"} {
			wc, err := newDockerClient(endpointConfig{Host: os.Getenv(env)})
			if err != nil {
				return fmt.Errorf("%s client: %w", env, err)
			}
			wi, err := wc.Info(ctx)
			wc.Close()
			if err != nil {
				return fmt.Errorf("%s info: %w", env, err)
			}
			if st := wi.Swarm.LocalNodeState; st != swarm.LocalNodeStateInactive {
				return fmt.Errorf("%s swarm state is %q, want inactive", env, st)
			}
		}
		return nil
	}
}

// TestE2EConfigMatchesSchema checks, without a daemon, that every attribute
// and nested block e2eConfig sets exists in the provider's registered schema
// and is settable (not computed-only), so a schema rename in any lane fails
// here instead of only on a live harness run.
func TestE2EConfigMatchesSchema(t *testing.T) {
	srv, err := testAccProtoV6ProviderFactories["docker"]()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e2eConfig(e2eParams{
		Suffix:        "abc123",
		AdvertiseAddr: "eth0",
		ManagerAddr:   "manager:2377",
		Worker1Host:   "tcp://127.0.0.1:12376",
		Worker2Host:   "tcp://127.0.0.1:12377",
	})
	f, diags := hclsyntax.ParseConfig([]byte(cfg), "e2e.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("HCL parse: %s", diags.Error())
	}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 {
			continue
		}
		s, ok := resp.ResourceSchemas[b.Labels[0]]
		if !ok || s == nil || s.Block == nil {
			t.Errorf("resource type %s is not registered", b.Labels[0])
			continue
		}
		e2eCheckBodyAgainstSchema(t, b.Labels[0]+"."+b.Labels[1], b.Body, s.Block, true)
	}
}

// e2eCheckBodyAgainstSchema walks one HCL body against a protocol schema
// block. top marks a resource body, where Terraform meta-arguments are legal.
func e2eCheckBodyAgainstSchema(t *testing.T, path string, body *hclsyntax.Body, blk *tfprotov6.SchemaBlock, top bool) {
	t.Helper()
	meta := map[string]bool{"depends_on": true, "count": true, "for_each": true, "provider": true}
	attrs := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range blk.Attributes {
		attrs[a.Name] = a
	}
	blocks := map[string]*tfprotov6.SchemaNestedBlock{}
	for _, nb := range blk.BlockTypes {
		blocks[nb.TypeName] = nb
	}
	for name := range body.Attributes {
		if top && meta[name] {
			continue
		}
		a, ok := attrs[name]
		switch {
		case !ok:
			if _, isBlock := blocks[name]; isBlock {
				t.Errorf("%s: %q is a block in the schema but set as an attribute", path, name)
			} else {
				t.Errorf("%s: attribute %q does not exist in the schema", path, name)
			}
		case !a.Optional && !a.Required:
			t.Errorf("%s: attribute %q is computed-only and cannot be set", path, name)
		}
	}
	for _, nb := range body.Blocks {
		if top && nb.Type == "lifecycle" {
			continue
		}
		sb, ok := blocks[nb.Type]
		if !ok {
			if _, isAttr := attrs[nb.Type]; isAttr {
				t.Errorf("%s: %q is an attribute in the schema but written as a block", path, nb.Type)
			} else {
				t.Errorf("%s: block %q does not exist in the schema", path, nb.Type)
			}
			continue
		}
		e2eCheckBodyAgainstSchema(t, path+"."+nb.Type, nb.Body, sb.Block, false)
	}
}
