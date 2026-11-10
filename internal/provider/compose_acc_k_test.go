package provider

// Pass 5.3 Lane K acceptance tests: local-mode hooks, links, volume
// labels, the owner label and D5 (TestAccComposeLifecycleK), and swarm-mode
// D6 + template_driver (TestAccComposeSwarmEnvK, -SwarmInit pass). They
// skip without TF_ACC; the swarm one also unless DOCKER_HOST is a manager.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/moby/moby/client"
)

const composeAccLifecycleK = `
provider "docker" {
  owner = "tfacc-k"
}

resource "docker_compose" "k" {
  name    = "tfacc-k53"
  content = <<-EOT
    services:
      db:
        image: busybox:1.36
        command: ["sleep", "3600"]
      web:
        image: busybox:1.36
        command: ["sleep", "3600"]
        links: ["db:database"]
        environment:
          A: "1"%s
        volumes:
          - data:/data
          - type: volume
            target: /scratch
            volume:
              labels: {tier: scratch}
        pre_start:
          - command: ["sh", "-c", "echo initialised > /data/init"]
        post_start:
          - command: ["sh", "-c", "echo started > /tmp/started"]
        pre_stop:
          - command: ["sh", "-c", "echo stopping"]
    volumes:
      data: {}
  EOT
}
`

func composeAccExecK(ctx context.Context, c *client.Client, ctr string, cmd ...string) (string, error) {
	out, err := copExecRun(ctx, c, copExecSpec{Container: ctr, Opts: client.ExecCreateOptions{Cmd: cmd}, Timeout: time.Minute})
	if err != nil {
		return "", err
	}
	if out.ExitCode != 0 {
		return "", fmt.Errorf("%v exited %d: %s", cmd, out.ExitCode, out.Stderr)
	}
	return strings.TrimSpace(out.Stdout), nil
}

func TestAccComposeLifecycleK(t *testing.T) {
	testAccPreCheck(t)
	ctx := context.Background()
	var firstID string
	check := func(t *testing.T, wantGone bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			c := testAccDockerClient(t)
			insp, err := c.ContainerInspect(ctx, "tfacc-k53-web-1", client.ContainerInspectOptions{})
			if err != nil {
				return err
			}
			ctr := insp.Container
			if ctr.Config.Labels[ownerLabelKey] != "tfacc-k" {
				return fmt.Errorf("owner label: %v", ctr.Config.Labels)
			}
			// On a user-defined network the daemon keeps legacy links on
			// the endpoint, not in HostConfig.Links.
			var links []string
			links = append(links, ctr.HostConfig.Links...)
			if ctr.NetworkSettings != nil {
				for _, ep := range ctr.NetworkSettings.Networks {
					links = append(links, ep.Links...)
				}
			}
			if !slices.ContainsFunc(links, func(l string) bool { return strings.Contains(l, "tfacc-k53-db-1") && strings.Contains(l, "database") }) {
				return fmt.Errorf("links %v", links)
			}
			// The bare "GONE" (D5 step) is stored as sent; the daemon unsets
			// it in the process environment.
			if slices.Contains(ctr.Config.Env, "GONE") != wantGone {
				return fmt.Errorf("GONE in env: %v", ctr.Config.Env)
			}
			if s, err := composeAccExecK(ctx, c, ctr.ID, "cat", "/data/init"); err != nil || s != "initialised" {
				return fmt.Errorf("pre_start: %q %v", s, err)
			}
			if s, err := composeAccExecK(ctx, c, ctr.ID, "cat", "/tmp/started"); err != nil || s != "started" {
				return fmt.Errorf("post_start: %q %v", s, err)
			}
			for _, m := range ctr.Mounts {
				if m.Destination == "/scratch" {
					v, err := c.VolumeInspect(ctx, m.Name, client.VolumeInspectOptions{})
					if err != nil {
						return err
					}
					if v.Volume.Labels["tier"] != "scratch" {
						return fmt.Errorf("volume labels %v", v.Volume.Labels)
					}
				}
			}
			if v, err := c.VolumeInspect(ctx, "tfacc-k53_data", client.VolumeInspectOptions{}); err != nil || v.Volume.Labels[ownerLabelKey] != "tfacc-k" {
				return fmt.Errorf("data volume owner: %v %v", v.Volume.Labels, err)
			}
			if wantGone && ctr.ID == firstID {
				return fmt.Errorf("D5: adding GONE: null must recreate web")
			}
			firstID = ctr.ID
			return nil
		}
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: fmt.Sprintf(composeAccLifecycleK, ""), Check: check(t, false)},
			{
				Config: fmt.Sprintf(composeAccLifecycleK, "\n          GONE: null"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.k", plancheck.ResourceActionUpdate),
				}},
				Check: check(t, true),
			},
			{
				Config: fmt.Sprintf(composeAccLifecycleK, "\n          GONE: null"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("docker_compose.k", plancheck.ResourceActionNoop),
				}},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			c := testAccDockerClient(t)
			res, err := c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", composeLabelProject+"=tfacc-k53")})
			if err != nil {
				return err
			}
			if len(res.Items) != 0 {
				return fmt.Errorf("%d containers left", len(res.Items))
			}
			return nil
		},
	})
}

func TestAccComposeSwarmEnvK(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t) // skips unless DOCKER_HOST is a swarm manager
	ctx := context.Background()
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "docker" {
  owner = "tfacc-k"
}

resource "docker_compose" "s" {
  mode    = "swarm"
  name    = "tfacck53s"
  content = <<-EOT
services:
  web:
    image: busybox:1.36
    command: ["sleep", "3600"]
    environment:
      A: "1"
      GONE: null
    configs: [{source: tpl, target: /etc/tpl}]
    deploy: {replicas: 1}
configs:
  tpl:
    content: "service {{ .Service.Name }}"
    template_driver: golang
EOT
}
`,
			Check: func(*terraform.State) error {
				c := testAccDockerClient(t)
				res, err := c.ServiceInspect(ctx, "tfacck53s_web", client.ServiceInspectOptions{})
				if err != nil {
					return err
				}
				s := res.Service
				if env := s.Spec.TaskTemplate.ContainerSpec.Env; !slices.Contains(env, "GONE") || !slices.Contains(env, "A=1") {
					return fmt.Errorf("D6: service env %v", env)
				}
				if s.Spec.Labels[ownerLabelKey] != "tfacc-k" {
					return fmt.Errorf("service labels %v", s.Spec.Labels)
				}
				cfgs, err := c.ConfigList(ctx, client.ConfigListOptions{Filters: client.Filters{}.Add("label", labelStackNamespace+"=tfacck53s")})
				if err != nil {
					return err
				}
				if len(cfgs.Items) != 1 || cfgs.Items[0].Spec.Templating == nil || cfgs.Items[0].Spec.Templating.Name != "golang" ||
					cfgs.Items[0].Spec.Labels[ownerLabelKey] != "tfacc-k" {
					return fmt.Errorf("config %+v", cfgs.Items)
				}
				return nil
			},
		}},
	})
}
