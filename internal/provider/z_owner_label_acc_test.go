package provider

// Pass 5.3 Lane Z: the owner label across types, on the harness. One object
// of every ownerLabelledTypes resource is created with the provider's
// `owner` set; the label is checked through the Engine API, the framework's
// post-apply plan check proves the label never shows as drift, and the
// conflict steps create a same-named object owned by someone else first and
// expect Create to refuse it. docker_buildx_builder is covered by its unit
// tests only (the docker-container driver pulls moby/buildkit, which the
// harness does not seed).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

const (
	ownerAccA = "tfacc-owner-a"
	ownerAccB = "tfacc-owner-b"
)

var ownerAccConflict = regexp.MustCompile(`(?s)owned by.*` + ownerAccB)

func ownerAccProvider(owner string) string {
	return fmt.Sprintf("provider \"docker\" {\n  owner = %q\n}\n", owner)
}

func ownerAccWant(kind, name string, labels map[string]string) error {
	if got := labels[ownerLabelKey]; got != ownerAccA {
		return fmt.Errorf("%s %s: %s = %q, want %q", kind, name, ownerLabelKey, got, ownerAccA)
	}
	return nil
}

// TestAccOwnerLabel_singleHost: docker_network, docker_volume,
// docker_container, docker_image (build) and docker_compose (local).
func TestAccOwnerLabel_singleHost(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+hAccImage("busybox:1.36")+"\nRUN true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := ownerAccProvider(ownerAccA) + fmt.Sprintf(`
resource "docker_network" "n" {
  name = "tfacc-own-net"
}

resource "docker_volume" "v" {
  name = "tfacc-own-vol"
}

resource "docker_container" "c" {
  name    = "tfacc-own-ctr"
  image   = "busybox:1.36"
  command = ["sleep", "600"]
}

resource "docker_image" "i" {
  name = "tfacc-own-img:1"
  build {
    context = %q
  }
}

resource "docker_compose" "p" {
  name    = "tfaccown"
  content = <<-EOT
    services:
      web:
        image: busybox:1.36
        command: ["sleep", "600"]
  EOT
}
`, filepath.ToSlash(dir))
	check := func(*terraform.State) error {
		c := testAccDockerClient(t)
		n, err := c.NetworkInspect(ctx, "tfacc-own-net", client.NetworkInspectOptions{})
		if err != nil {
			return err
		}
		if err := ownerAccWant("network", "tfacc-own-net", n.Network.Labels); err != nil {
			return err
		}
		v, err := c.VolumeInspect(ctx, "tfacc-own-vol", client.VolumeInspectOptions{})
		if err != nil {
			return err
		}
		if err := ownerAccWant("volume", "tfacc-own-vol", v.Volume.Labels); err != nil {
			return err
		}
		ct, err := c.ContainerInspect(ctx, "tfacc-own-ctr", client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if err := ownerAccWant("container", "tfacc-own-ctr", ct.Container.Config.Labels); err != nil {
			return err
		}
		im, err := c.ImageInspect(ctx, "tfacc-own-img:1")
		if err != nil {
			return err
		}
		if im.Config == nil {
			return fmt.Errorf("image tfacc-own-img:1 has no config")
		}
		if err := ownerAccWant("image", "tfacc-own-img:1", im.Config.Labels); err != nil {
			return err
		}
		pc, err := c.ContainerInspect(ctx, "tfaccown-web-1", client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		return ownerAccWant("compose container", "tfaccown-web-1", pc.Container.Config.Labels)
	}
	// Conflicting objects: created out of band, owned by someone else.
	conflictSetup := func() {
		c := testAccDockerClient(t)
		other := map[string]string{ownerLabelKey: ownerAccB}
		if _, err := c.NetworkCreate(ctx, "tfacc-own-net-x", client.NetworkCreateOptions{Labels: other}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.VolumeCreate(ctx, client.VolumeCreateOptions{Name: "tfacc-own-vol-x", Labels: other}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = c.NetworkRemove(context.Background(), "tfacc-own-net-x", client.NetworkRemoveOptions{})
			_, _ = c.VolumeRemove(context.Background(), "tfacc-own-vol-x", client.VolumeRemoveOptions{Force: true})
		})
	}
	hAccTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg, Check: check},
			{
				PreConfig:   conflictSetup,
				Config:      ownerAccProvider(ownerAccA) + `resource "docker_network" "x" { name = "tfacc-own-net-x" }`,
				ExpectError: ownerAccConflict,
			},
			{
				Config:      ownerAccProvider(ownerAccA) + `resource "docker_volume" "x" { name = "tfacc-own-vol-x" }`,
				ExpectError: ownerAccConflict,
			},
		},
	})
}

// TestAccOwnerLabel_swarmInit: docker_swarm_init (plain pass: needs a
// daemon outside any swarm).
func TestAccOwnerLabel_swarmInit(t *testing.T) {
	testAccPreCheck(t)
	infoRes, err := testAccDockerClient(t).Info(context.Background(), client.InfoOptions{})
	if err != nil {
		t.Skipf("cannot reach DOCKER_HOST: %v", err)
	}
	if infoRes.Info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skip("DOCKER_HOST is already part of a swarm; docker_swarm_init needs a daemon outside any swarm")
	}
	hAccTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: ownerAccProvider(ownerAccA) + `
resource "docker_swarm_init" "this" {
  advertise_addr    = "127.0.0.1"
  default_addr_pool = ["10.21.0.0/16"]
  subnet_size       = 24
}
`,
			Check: func(*terraform.State) error {
				s, err := testAccDockerClient(t).SwarmInspect(context.Background(), client.SwarmInspectOptions{})
				if err != nil {
					return err
				}
				return ownerAccWant("swarm", "spec", s.Swarm.Spec.Labels)
			},
		}},
	})
}

// TestAccOwnerLabelSwarm_workload: docker_secret, docker_config,
// docker_service, docker_stack and docker_swarm_node (swarm pass).
func TestAccOwnerLabelSwarm_workload(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	worker := os.Getenv("DOCKER_WORKER1_HOST")
	node := ""
	if worker != "" {
		node = fmt.Sprintf(`
data "docker_swarm_tokens" "t" {}

resource "docker_swarm_node" "w1" {
  host            = %q
  join_token      = data.docker_swarm_tokens.t.worker
  manager_address = %q
}
`, worker, swarmAccManagerAddr(t))
	}
	cfg := ownerAccProvider(ownerAccA) + `
resource "docker_secret" "s" {
  name = "tfacc-own-secret"
  data = base64encode("x")
}

resource "docker_config" "c" {
  name = "tfacc-own-config"
  data = base64encode("x")
}

resource "docker_service" "svc" {
  name    = "tfacc-own-svc"
  image   = "busybox:1.36"
  command = ["sleep", "600"]
}

resource "docker_stack" "st" {
  name = "tfaccownst"
  service {
    name    = "web"
    image   = "busybox:1.36"
    command = ["sleep", "600"]
  }
}
` + node
	check := func(*terraform.State) error {
		c := testAccDockerClient(t)
		s, err := c.SecretInspect(ctx, "tfacc-own-secret", client.SecretInspectOptions{})
		if err != nil {
			return err
		}
		if err := ownerAccWant("secret", "tfacc-own-secret", s.Secret.Spec.Labels); err != nil {
			return err
		}
		cf, err := c.ConfigInspect(ctx, "tfacc-own-config", client.ConfigInspectOptions{})
		if err != nil {
			return err
		}
		if err := ownerAccWant("config", "tfacc-own-config", cf.Config.Spec.Labels); err != nil {
			return err
		}
		sv, err := c.ServiceInspect(ctx, "tfacc-own-svc", client.ServiceInspectOptions{})
		if err != nil {
			return err
		}
		if err := ownerAccWant("service", "tfacc-own-svc", sv.Service.Spec.Labels); err != nil {
			return err
		}
		st, err := c.ServiceInspect(ctx, "tfaccownst_web", client.ServiceInspectOptions{})
		if err != nil {
			return err
		}
		if err := ownerAccWant("stack service", "tfaccownst_web", st.Service.Spec.Labels); err != nil {
			return err
		}
		if worker == "" {
			return nil
		}
		wc, err := client.New(client.WithHost(worker))
		if err != nil {
			return err
		}
		info, err := wc.Info(ctx, client.InfoOptions{})
		if err != nil {
			return err
		}
		n, err := c.NodeInspect(ctx, info.Info.Swarm.NodeID, client.NodeInspectOptions{})
		if err != nil {
			return err
		}
		return ownerAccWant("node", info.Info.Swarm.NodeID, n.Node.Spec.Labels)
	}
	conflictSetup := func() {
		c := testAccDockerClient(t)
		ann := swarm.Annotations{Name: "tfacc-own-secret-x", Labels: map[string]string{ownerLabelKey: ownerAccB}}
		if _, err := c.SecretCreate(ctx, client.SecretCreateOptions{Spec: swarm.SecretSpec{Annotations: ann, Data: []byte("x")}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = c.SecretRemove(context.Background(), "tfacc-own-secret-x", client.SecretRemoveOptions{})
		})
	}
	steps := []resource.TestStep{
		{Config: cfg, Check: check},
		{
			PreConfig:   conflictSetup,
			Config:      ownerAccProvider(ownerAccA) + "resource \"docker_secret\" \"x\" {\n  name = \"tfacc-own-secret-x\"\n  data = base64encode(\"y\")\n}\n",
			ExpectError: ownerAccConflict,
		},
	}
	tc := resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	}
	if worker != "" {
		tc.CheckDestroy = swarmAccCheckNodesGone
	}
	hAccTest(t, tc)
}
