package provider

// Pass 6c lane S acceptance tests: the structured `inspect` output of a
// densely configured object equals its raw inspect_json, JSON-decoded with
// field names mapped, value for value (round trip on the dind harness), and
// config_sha256 is stable across reads of a running container whose health
// log keeps changing.
//
// Run: pwsh -NoProfile -File test/harness/Invoke-E2E.ps1 -SwarmInit -Run 'TestAccP6cS' -Timeout 40m
// (TestAccP6cSService needs the swarm pass; the others run on either pass.)

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// p6cFlat renders the value v of node n the way Terraform state flattens it
// (list "#", map "%", map keys verbatim), with names mapped by the contract.
func p6cFlat(n *contractNode, v any, prefix string, out map[string]string, skipped *[]string) {
	if v == nil {
		return
	}
	switch n.Kind {
	case cnString, cnJSON:
		switch t := v.(type) {
		case string:
			out[prefix] = t
		case json.Number:
			out[prefix] = t.String()
		case bool:
			out[prefix] = fmt.Sprint(t)
		default:
			b, _ := rCanonicalJSON(t)
			out[prefix] = string(b)
		}
	case cnNumber:
		out[prefix] = fmt.Sprint(v)
	case cnBool:
		out[prefix] = fmt.Sprint(v)
	case cnDynamic:
		*skipped = append(*skipped, prefix+" (dynamic)")
	case cnList, cnSet:
		arr, _ := v.([]any)
		if len(arr) > 0 {
			out[prefix+".#"] = fmt.Sprint(len(arr))
		}
		if n.Kind == cnSet {
			*skipped = append(*skipped, prefix+" (set elements)")
			return
		}
		for i, x := range arr {
			p6cFlat(n.Elem, x, fmt.Sprintf("%s.%d", prefix, i), out, skipped)
		}
	case cnMap:
		m, _ := v.(map[string]any)
		if len(m) > 0 {
			out[prefix+".%"] = fmt.Sprint(len(m))
		}
		for k, x := range m {
			p6cFlat(n.Elem, x, prefix+"."+k, out, skipped)
		}
	case cnObject:
		m, _ := v.(map[string]any)
		for _, a := range n.Attrs {
			x, _ := contractLookupKey(m, a)
			p6cFlat(a.Node, x, prefix+"."+a.Name, out, skipped)
		}
	}
}

// p6cUnknownKeys lists keys of raw that the node has no field for.
func p6cUnknownKeys(n *contractNode, v any, prefix string, out *[]string) {
	switch n.Kind {
	case cnList, cnSet:
		arr, _ := v.([]any)
		for i, x := range arr {
			p6cUnknownKeys(n.Elem, x, fmt.Sprintf("%s.%d", prefix, i), out)
		}
	case cnMap:
		m, _ := v.(map[string]any)
		for k, x := range m {
			p6cUnknownKeys(n.Elem, x, prefix+"."+k, out)
		}
	case cnObject:
		m, _ := v.(map[string]any)
		up := map[string]*contractNode{}
		for _, a := range n.Attrs {
			up[a.Upstream] = a.Node
		}
		for k, x := range m {
			if c, ok := up[k]; ok {
				p6cUnknownKeys(c, x, prefix+"."+k, out)
			} else {
				*out = append(*out, prefix+"."+k)
			}
		}
	}
}

func p6cSameValue(want, got string) bool {
	if want == got {
		return true
	}
	a, okA := new(big.Float).SetString(want)
	b, okB := new(big.Float).SetString(got)
	return okA && okB && a.Cmp(b) == 0
}

// testAccP6cRoundTrip checks addr's structured output against its raw one.
func testAccP6cRoundTrip(t *testing.T, addr, itemID, structured, raw string) resource.TestCheckFunc {
	return testAccP6cRoundTripAt(t, addr, itemID, structured, raw, "", 0)
}

// testAccP6cRoundTripAt is testAccP6cRoundTrip against the rawKey member of
// the raw body ("" = the whole body), with at least minLeaves leaves (0 =
// the default for the item).
func testAccP6cRoundTripAt(t *testing.T, addr, itemID, structured, raw, rawKey string, minLeaves int) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		attrs := rs.Primary.Attributes
		body := attrs[raw]
		if body == "" {
			return fmt.Errorf("%s.%s is empty", addr, raw)
		}
		d := json.NewDecoder(bytes.NewReader([]byte(body)))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			return fmt.Errorf("%s.%s: %v", addr, raw, err)
		}
		if rawKey != "" {
			obj, _ := v.(map[string]any)
			if v = obj[rawKey]; v == nil {
				return fmt.Errorf("%s.%s has no %s", addr, raw, rawKey)
			}
		}
		it := contractMustItem(itemID)
		var node *contractNode
		for _, o := range it.Outputs {
			if o.Name == structured {
				node = contractOutputNode(it, o)
			}
		}
		if node == nil {
			return fmt.Errorf("%s has no output %s", itemID, structured)
		}
		want := map[string]string{}
		var skipped, unknown []string
		p6cFlat(node, v, structured, want, &skipped)
		p6cUnknownKeys(node, v, structured, &unknown)
		var bad []string
		for k, w := range want {
			g, ok := attrs[k]
			if !ok {
				bad = append(bad, fmt.Sprintf("%s: missing in state, raw has %q", k, w))
				continue
			}
			if !p6cSameValue(w, g) {
				bad = append(bad, fmt.Sprintf("%s: state %q, raw %q", k, g, w))
			}
		}
		sort.Strings(bad)
		sort.Strings(unknown)
		t.Logf("%s: %d leaves compared, %d skipped %v, %d raw keys without a field %v", addr, len(want), len(skipped), skipped, len(unknown), unknown)
		if len(bad) > 0 {
			if len(bad) > 40 {
				bad = append(bad[:40], fmt.Sprintf("... %d more", len(bad)-40))
			}
			return fmt.Errorf("%s: %s differs from %s:\n%s", addr, structured, raw, strings.Join(bad, "\n"))
		}
		if minLeaves == 0 {
			minLeaves = 20
			if itemID == "engine:volume" {
				minLeaves = 6 // a volume inspect has few fields
			}
		}
		if len(want) < minLeaves {
			return fmt.Errorf("%s: only %d leaves compared; the dense config did not land", addr, len(want))
		}
		return nil
	}
}

const p6cContainerCfg = `
resource "docker_image" "p6cs" {
  name         = "nginx:1.27-alpine"
  keep_locally = true
}

resource "docker_network" "p6cs" {
  name   = "p6cs-net"
  labels = { "com.example.Team" = "web", plain = "x" }
  options = { "com.docker.network.bridge.enable_icc" = "true" }
  ipam_config {
    subnet  = "10.78.0.0/24"
    gateway = "10.78.0.1"
  }
}

resource "docker_volume" "p6cs" {
  name   = "p6cs-vol"
  labels = { "com.example.Team" = "web" }
}

resource "docker_container" "p6cs" {
  name   = "p6cs-web"
  image  = docker_image.p6cs.id
  labels = { "com.example.Team" = "web", MixedCase = "y" }
  env    = { P6CS = "1" }

  ports {
    container_port = 80
    host_port      = 18088
  }
  ports {
    container_port = 443
    host_ip        = "127.0.0.1"
    host_port      = 18443
  }

  mounts {
    type   = "volume"
    source = docker_volume.p6cs.name
    target = "/usr/share/nginx/html"
  }
  mounts {
    type   = "tmpfs"
    target = "/var/cache/nginx"
  }

  networks = {
    (docker_network.p6cs.name) = {
      aliases      = ["www"]
      ipv4_address = "10.78.0.10"
    }
  }

  healthcheck {
    test     = ["CMD", "wget", "-q", "-O-", "http://127.0.0.1/"]
    interval = "2s"
    timeout  = "2s"
    retries  = 3
  }

  ulimits {
    name = "nofile"
    soft = 65536
    hard = 65536
  }

  sysctls = { "net.core.somaxconn" = "1024" }

  logging {
    driver  = "json-file"
    options = { max-size = "10m", max-file = "3" }
  }

  state = "running"
  wait  = true
}
`

// TestAccP6cSContainerRoundTrip: container, image, network and volume
// inspect outputs mirror their inspect_json (a, b, c, d, h), and
// config_sha256 is stable across two reads 15 seconds apart (f).
func TestAccP6cSContainerRoundTrip(t *testing.T) {
	testAccPreCheck(t)
	var firstConfig, firstContent string
	capture := func(s *terraform.State) error {
		a := s.RootModule().Resources["docker_container.p6cs"].Primary.Attributes
		firstConfig, firstContent = a["config_sha256"], a["content_sha256"]
		if len(firstConfig) != 64 {
			return fmt.Errorf("config_sha256 %q", firstConfig)
		}
		return nil
	}
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: p6cContainerCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccP6cRoundTrip(t, "docker_container.p6cs", "engine:container", "inspect", "inspect_json"),
					testAccP6cRoundTrip(t, "docker_image.p6cs", "engine:image", "inspect", "inspect_json"),
					testAccP6cRoundTrip(t, "docker_network.p6cs", "engine:network", "inspect", "inspect_json"),
					testAccP6cRoundTrip(t, "docker_volume.p6cs", "engine:volume", "inspect", "inspect_json"),
					// a: exposed ports are a map.
					resource.TestCheckResourceAttr("docker_image.p6cs", "inspect.config.exposed_ports.%", "1"),
					// b: nested objects addressable by attribute path.
					resource.TestCheckResourceAttr("docker_container.p6cs", "inspect.network_settings.networks.p6cs-net.ip_address", "10.78.0.10"),
					resource.TestCheckResourceAttr("docker_container.p6cs", "inspect.host_config.port_bindings.80/tcp.0.host_port", "18088"),
					resource.TestCheckResourceAttr("docker_container.p6cs", "inspect.host_config.ulimits.0.name", "nofile"),
					resource.TestCheckResourceAttr("docker_network.p6cs", "inspect.ipam.config.0.subnet", "10.78.0.0/24"),
					// c: enums are plain strings.
					resource.TestCheckTypeSetElemNestedAttrs("docker_container.p6cs", "inspect.mounts.*", map[string]string{"type": "tmpfs"}),
					resource.TestCheckTypeSetElemNestedAttrs("docker_container.p6cs", "inspect.host_config.mounts.*", map[string]string{"type": "volume"}),
					// d: user-data map keys are untouched.
					resource.TestCheckResourceAttr("docker_container.p6cs", "inspect.host_config.log_config.config.max-file", "3"),
					resource.TestCheckResourceAttr("docker_container.p6cs", "inspect.config.labels.MixedCase", "y"),
					resource.TestCheckResourceAttr("docker_network.p6cs", "inspect.labels.com.example.Team", "web"),
					capture,
				),
			},
			{
				PreConfig: func() { time.Sleep(15 * time.Second) },
				Config:    p6cContainerCfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccP6cRoundTrip(t, "docker_container.p6cs", "engine:container", "inspect", "inspect_json"),
					resource.TestCheckResourceAttrSet("docker_container.p6cs", "inspect.state.health.log.0.output"),
					func(s *terraform.State) error {
						a := s.RootModule().Resources["docker_container.p6cs"].Primary.Attributes
						t.Logf("content_sha256 %s -> %s (runtime state; may change)", firstContent, a["content_sha256"])
						if a["config_sha256"] != firstConfig {
							return fmt.Errorf("config_sha256 changed between two reads 15s apart: %s -> %s", firstConfig, a["config_sha256"])
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccP6cSService: service inspect mirrors inspect_json (swarm pass).
func TestAccP6cSService(t *testing.T) {
	testAccPreCheck(t)
	_ = swarmAccManagerAddr(t)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "docker_service" "p6cs" {
  name     = "p6cs-svc"
  image    = "nginx:1.27-alpine"
  replicas = 1
  labels   = { "com.example.Team" = "web" }
  env      = { P6CS = "1" }

  mounts {
    type   = "volume"
    source = "p6cs-cache"
    target = "/var/cache/nginx"
  }
  mounts {
    type   = "tmpfs"
    target = "/tmp"
  }

  endpoint_spec {
    ports {
      target_port    = 80
      published_port = 18089
    }
  }

  placement {
    constraints = ["node.role==manager"]
  }
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				testAccP6cRoundTrip(t, "docker_service.p6cs", "swarm:service", "inspect", "inspect_json"),
				resource.TestCheckResourceAttr("docker_service.p6cs", "inspect.spec.labels.com.example.Team", "web"),
				resource.TestCheckTypeSetElemNestedAttrs("docker_service.p6cs", "inspect.spec.task_template.container_spec.mounts.*", map[string]string{"type": "tmpfs"}),
				resource.TestCheckResourceAttrSet("docker_service.p6cs", "config_sha256"),
			),
		}},
	})
}

// TestAccP6cImageDataRoundTrip: the two image data sources on the harness
// (open finding 4 of Pass 6c, image half). nginx is pulled, tagged into the
// harness registry and pushed; data.docker_image's inspect and config mirror
// its inspect_json (config.exposed_ports and volumes are maps, the shape that
// ExampleTests caught as "want a list, got map"), and data.docker_registry_image's
// inspect mirrors its inspect_json (DistributionInspect, descriptor and
// platforms typed).
func TestAccP6cImageDataRoundTrip(t *testing.T) {
	testAccPreCheck(t)
	reg := os.Getenv("TF_ACC_REGISTRY")
	if reg == "" {
		t.Skip("TF_ACC_REGISTRY not set (the harness registry, host:port); skipping")
	}
	cfg := fmt.Sprintf(`
resource "docker_image" "p6cd" {
  name         = "nginx:1.27-alpine"
  keep_locally = true
}

resource "docker_tag" "p6cd" {
  source_image = docker_image.p6cd.name
  target_image = "%s/tfacc/p6cd-nginx:1.27-alpine"
}

resource "docker_registry_image" "p6cd" {
  name          = docker_tag.p6cd.target_image
  keep_remotely = false
  push_retries  = 1
}

data "docker_image" "p6cd" {
  name = docker_image.p6cd.name
}

data "docker_registry_image" "p6cd" {
  name = docker_registry_image.p6cd.name
}
`, reg)
	hAccTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				testAccP6cRoundTrip(t, "data.docker_image.p6cd", "engine:image-data", "inspect", "inspect_json"),
				testAccP6cRoundTripAt(t, "data.docker_image.p6cd", "engine:image-data", "config", "inspect_json", "Config", 8),
				resource.TestCheckResourceAttr("data.docker_image.p6cd", "config.exposed_ports.%", "1"),
				resource.TestCheckResourceAttr("data.docker_image.p6cd", "inspect.config.exposed_ports.%", "1"),
				resource.TestCheckResourceAttr("data.docker_image.p6cd", "config.stop_signal", "SIGQUIT"),
				testAccP6cRoundTripAt(t, "data.docker_registry_image.p6cd", "engine:registry-image-data", "inspect", "inspect_json", "", 3),
				resource.TestCheckResourceAttrPair("data.docker_registry_image.p6cd", "inspect.descriptor.digest",
					"data.docker_registry_image.p6cd", "digest"),
				resource.TestMatchResourceAttr("data.docker_registry_image.p6cd", "inspect.platforms.0.os", regexp.MustCompile(`^linux$`)),
			),
		}},
	})
}
