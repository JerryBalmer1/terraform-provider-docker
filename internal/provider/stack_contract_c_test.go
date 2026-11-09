package provider

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/moby/moby/api/types/swarm"
)

// TestStackContractC checks docker_stack's Pass 5 outputs against the fake
// Swarm daemon.
func TestStackContractC(t *testing.T) {
	_, host := newSvcFakeDaemon(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: stackFakeConfig(host, "pw1", ""),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("docker_stack.app", "namespace", "app"),
				resource.TestCheckResourceAttr("docker_stack.app", "labels.com.docker.stack.namespace", "app"),
				resource.TestCheckResourceAttrSet("docker_stack.app", "service_ids.web"),
				resource.TestCheckResourceAttrSet("docker_stack.app", "network_ids.front"),
				resource.TestCheckResourceAttrSet("docker_stack.app", "secret_ids.db_pw"),
				resource.TestCheckResourceAttrSet("docker_stack.app", "config_ids.conf"),
				resource.TestMatchResourceAttr("docker_stack.app", "stack_yaml", regexp.MustCompile(`image: nginx:1.27`)),
				resource.TestMatchResourceAttr("docker_stack.app", "content_sha256", regexp.MustCompile(`^[0-9a-f]{64}$`)),
				resource.TestCheckResourceAttr("docker_stack.app", "stack.name", "app"),
				resource.TestMatchResourceAttr("docker_stack.app", "stack.services.web.image", regexp.MustCompile(`^nginx:1.27(@sha256:[0-9a-f]{64})?$`)),
				resource.TestCheckResourceAttr("docker_stack.app", "service_status.#", "2"),
				resource.TestCheckResourceAttr("docker_stack.app", "compose_spec_version", contractComposeSpecVersion),
			),
		}},
	})
}

func TestStackComposeServiceC(t *testing.T) {
	rep := uint64(3)
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "app_web", Labels: map[string]string{labelStackNamespace: "app", "tier": "web"}},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx", Args: []string{"-g"}, Env: []string{"A=1"},
			Secrets: []*swarm.SecretReference{{SecretName: "app_pw", File: &swarm.SecretReferenceFileTarget{Name: "pw"}}}},
			Placement: &swarm.Placement{Constraints: []string{"node.role==worker"}}},
		Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &rep}},
		EndpointSpec: &swarm.EndpointSpec{Ports: []swarm.PortConfig{{TargetPort: 80, PublishedPort: 8080, Protocol: "tcp"}}},
	}
	got := stackComposeService("app", spec)
	d := got["deploy"].(map[string]any)
	if got["image"] != "nginx" || d["replicas"] != rep || d["mode"] != "replicated" || d["labels"].(map[string]string)["tier"] != "web" {
		t.Errorf("service %v", got)
	}
	if s := got["secrets"].([]any)[0].(map[string]any); s["source"] != "pw" || s["target"] != "pw" {
		t.Errorf("secrets %v", s)
	}
	if stackPortString(swarm.PortConfig{TargetPort: 53, Protocol: "udp"}) != "53/udp" || !strings.HasPrefix(stackPortString(spec.EndpointSpec.Ports[0]), "*:8080->80") {
		t.Error("port strings")
	}
	if stackServiceMode(swarm.ServiceMode{GlobalJob: &swarm.GlobalJob{}}) != "global job" {
		t.Error("mode")
	}
}
