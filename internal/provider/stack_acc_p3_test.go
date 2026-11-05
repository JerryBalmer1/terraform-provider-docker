package provider

// Pass 3 acceptance tests for docker_stack on a live Swarm manager (the
// -SwarmInit harness pass; names start with TestAccStack).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/api/types/volume"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func stackP3CheckGone(t *testing.T, stack string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c := testAccDockerClient(t)
		ctx := context.Background()
		f := filters.NewArgs(filters.Arg("label", labelStackNamespace+"="+stack))
		if svcs, err := c.ServiceList(ctx, swarm.ServiceListOptions{Filters: f}); err != nil || len(svcs) != 0 {
			return fmt.Errorf("stack %s: %d services left (err=%v)", stack, len(svcs), err)
		}
		if nets, err := c.NetworkList(ctx, network.ListOptions{Filters: f}); err != nil || len(nets) != 0 {
			return fmt.Errorf("stack %s: %d networks left (err=%v)", stack, len(nets), err)
		}
		return nil
	}
}

func stackP3Service(t *testing.T, name string) (swarm.Service, error) {
	svc, _, err := testAccDockerClient(t).ServiceInspectWithRaw(context.Background(), name, swarm.ServiceInspectOptions{})
	return svc, err
}

// TestAccStackP3_aliasAndVolumes: every stack service gets its short name
// as a network alias (a job in the stack resolves and fetches http://web/),
// a stack volume maps to <stack>_<name> in the service mount and reads back
// as the short name, and the provider never removes that volume (tasks
// created it) (Pass 2 map: L4 stack alias = short name; volumes never
// created/removed).
func TestAccStackP3_aliasAndVolumes(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	stack := "tfaccp3a" + strings.ToLower(acctest.RandString(4))
	volName := stack + "_data"
	t.Cleanup(func() {
		// Task containers go away asynchronously after the services.
		c := testAccDockerClient(t)
		_ = waitFor(context.Background(), time.Minute, 2*time.Second, func(ctx context.Context) (bool, error) {
			err := c.VolumeRemove(ctx, volName, true)
			return err == nil || isNotFound(err), nil
		})
	})
	cfg := fmt.Sprintf(`
resource "docker_stack" "a" {
  name = %q

  network {
    name = "net"
  }

  volume {
    name   = "data"
    labels = { purpose = "p3" }
  }

  service {
    name     = "web"
    image    = "nginx:1.27-alpine"
    networks = ["net"]

    mounts {
      type   = "volume"
      source = "data"
      target = "/data"
    }

    converge_config {
      timeout = "3m"
    }
  }

  service {
    name              = "probe"
    image             = "busybox:1.36"
    mode              = "replicated_job"
    total_completions = 1
    networks          = ["net"]
    command           = ["sh", "-c", "for i in $(seq 1 30); do wget -q -T 3 -O /dev/null http://web/ && exit 0; sleep 2; done; exit 1"]

    restart_policy {
      condition = "none"
    }

    converge_config {
      timeout = "3m"
    }
  }
}
`, stack)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: resource.ComposeTestCheckFunc(
			stackP3CheckGone(t, stack),
			func(*terraform.State) error {
				// Created by the web task on the node, never by the provider,
				// so destroying the stack leaves it.
				if _, err := testAccDockerClient(t).VolumeInspect(context.Background(), volName); err != nil {
					return fmt.Errorf("volume %s should survive stack destroy (provider never removes stack volumes): %v", volName, err)
				}
				return nil
			},
		),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_stack.a", "service.0.mounts.0.source", "data"),
					resource.TestCheckResourceAttr("docker_stack.a", "service.0.networks.#", "1"),
					func(*terraform.State) error {
						ctx := context.Background()
						c := testAccDockerClient(t)
						web, err := stackP3Service(t, stack+"_web")
						if err != nil {
							return err
						}
						nets := web.Spec.TaskTemplate.Networks
						if len(nets) != 1 || !strings.Contains(","+strings.Join(nets[0].Aliases, ",")+",", ",web,") {
							return fmt.Errorf("web network attachments %+v lack alias \"web\"", nets)
						}
						ms := web.Spec.TaskTemplate.ContainerSpec.Mounts
						if len(ms) != 1 || ms[0].Source != volName {
							return fmt.Errorf("web mount %+v, want source %s", ms, volName)
						}
						probe, err := stackP3Service(t, stack+"_probe")
						if err != nil {
							return err
						}
						tasks, err := p3ServiceTasks(ctx, c, probe.ID)
						if err != nil {
							return err
						}
						done := false
						for _, tk := range tasks {
							done = done || tk.Status.State == swarm.TaskStateComplete
						}
						if !done {
							return fmt.Errorf("probe job did not complete: alias web not reachable (%d tasks)", len(tasks))
						}
						// The volume exists now only because the web task created it.
						vols, err := c.VolumeList(ctx, volume.ListOptions{Filters: filters.NewArgs(filters.Arg("name", volName))})
						if err != nil {
							return err
						}
						if len(vols.Volumes) != 1 {
							return fmt.Errorf("volume %s not created by the task (%d found)", volName, len(vols.Volumes))
						}
						if vols.Volumes[0].Labels["purpose"] != "p3" {
							t.Logf("note: task-created volume labels %v (stack volume labels travel via mount VolumeOptions)", vols.Volumes[0].Labels)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccStackP3_updateResubmits: replacing a stack secret recreates the
// service that uses it and re-submits (ServiceUpdate, same ID, higher
// version) every other service; same for a config (Pass 2 map: L4 stack
// update re-submits services when a network/secret/config is replaced).
func TestAccStackP3_updateResubmits(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	stack := "tfaccp3u" + strings.ToLower(acctest.RandString(4))
	cfg := func(secret, conf string) string {
		return fmt.Sprintf(`
resource "docker_stack" "u" {
  name = %q

  network {
    name = "net"
  }

  secret {
    name = "s"
    data = base64encode(%q)
  }

  config {
    name = "c"
    data = base64encode(%q)
  }

  service {
    name     = "a"
    image    = "busybox:1.36"
    command  = ["sleep", "3600"]
    networks = ["net"]

    secrets {
      secret_id = "s"
    }

    converge_config {
      timeout = "3m"
    }
  }

  service {
    name     = "b"
    image    = "busybox:1.36"
    command  = ["sleep", "3600"]
    networks = ["net"]

    configs {
      config_id = "c"
    }

    converge_config {
      timeout = "3m"
    }
  }

  service {
    name     = "z"
    image    = "busybox:1.36"
    command  = ["sleep", "3600"]
    networks = ["net"]

    converge_config {
      timeout = "3m"
    }
  }
}
`, stack, secret, conf)
	}
	type rev struct {
		id  string
		ver uint64
	}
	prev := map[string]rev{}
	snapshot := func() (map[string]rev, error) {
		out := map[string]rev{}
		for _, n := range []string{"a", "b", "z"} {
			svc, err := stackP3Service(t, stack+"_"+n)
			if err != nil {
				return nil, err
			}
			out[n] = rev{svc.ID, svc.Version.Index}
		}
		return out, nil
	}
	// expect: recreated service gets a new ID; the others keep the ID with a
	// higher version (re-submitted).
	expect := func(recreated string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			cur, err := snapshot()
			if err != nil {
				return err
			}
			defer func() { prev = cur }()
			if len(prev) == 0 {
				return nil
			}
			var errs []string
			for n, c := range cur {
				p := prev[n]
				switch {
				case n == recreated && c.id == p.id:
					errs = append(errs, fmt.Sprintf("%s uses the replaced object but kept ID %s", n, c.id))
				case n != recreated && c.id != p.id:
					errs = append(errs, fmt.Sprintf("%s was recreated (%s -> %s), want re-submitted in place", n, p.id, c.id))
				case n != recreated && c.ver <= p.ver:
					errs = append(errs, fmt.Sprintf("%s not re-submitted (version %d -> %d)", n, p.ver, c.ver))
				}
			}
			if len(errs) > 0 {
				return fmt.Errorf("%s", strings.Join(errs, "; "))
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             stackP3CheckGone(t, stack),
		Steps: []resource.TestStep{
			{Config: cfg("v1", "k=1"), Check: expect("")},
			{Config: cfg("v2", "k=1"), Check: expect("a")},
			{Config: cfg("v2", "k=2"), Check: expect("b")},
		},
	})
}
