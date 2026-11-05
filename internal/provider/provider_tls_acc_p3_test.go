package provider

// Pass 3 acceptance tests for the provider's TLS endpoint configuration,
// against the harness tlsnode daemon (mutual TLS on TF_ACC_TLS_HOST).

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func tlsP3VolumeCheck(t *testing.T, ep endpointConfig, name string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, err := newDockerClient(ep)
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = c.VolumeInspect(context.Background(), name)
		switch {
		case want && err != nil:
			return fmt.Errorf("volume %s not on the TLS daemon: %v", name, err)
		case !want && !isNotFound(err):
			return fmt.Errorf("volume %s still on the TLS daemon (err=%v)", name, err)
		}
		return nil
	}
}

// TestAccProviderP3_TLS: provider tls block with file paths, then the same
// material as PEM content (file()), against a daemon that requires client
// certificates; the volume lands on the TLS daemon and nothing drifts when
// only the provider's spelling of the material changes (Pass 2 map: L0 tls
// ca/cert/key PEM-or-path).
func TestAccProviderP3_TLS(t *testing.T) {
	testAccPreCheck(t)
	ep := p3TLSEndpoint(t)
	name := acctest.RandomWithPrefix("tfacc-tls")
	paths := fmt.Sprintf(`
provider "docker" {
  host = %q
  tls {
    ca   = %q
    cert = %q
    key  = %q
  }
}
`, ep.Host, p3HCLPath(ep.CA), p3HCLPath(ep.Cert), p3HCLPath(ep.Key))
	pems := fmt.Sprintf(`
provider "docker" {
  host = %q
  tls {
    ca   = file(%q)
    cert = file(%q)
    key  = file(%q)
  }
}
`, ep.Host, p3HCLPath(ep.CA), p3HCLPath(ep.Cert), p3HCLPath(ep.Key))
	vol := fmt.Sprintf(`
resource "docker_volume" "v" {
  name   = %q
  labels = { via = "tls" }
}
`, name)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             tlsP3VolumeCheck(t, ep, name, false),
		Steps: []resource.TestStep{
			{
				Config: paths + vol,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("docker_volume.v", "id", name),
					tlsP3VolumeCheck(t, ep, name, true),
					func(*terraform.State) error {
						// And not on the plain manager DOCKER_HOST points at.
						if _, err := testAccDockerClient(t).VolumeInspect(context.Background(), name); !isNotFound(err) {
							return fmt.Errorf("volume %s unexpectedly on DOCKER_HOST (err=%v)", name, err)
						}
						return nil
					},
				),
			},
			{Config: pems + vol, PlanOnly: true},
			{Config: pems + vol, Check: tlsP3VolumeCheck(t, ep, name, true)},
		},
	})
}

// TestAccProviderP3_TLSFromEnv: no tls block; DOCKER_HOST + DOCKER_TLS_VERIFY
// + DOCKER_CERT_PATH (ca.pem/cert.pem/key.pem) configure the client (Pass 2
// map: L0 DOCKER_TLS_VERIFY/DOCKER_CERT_PATH).
func TestAccProviderP3_TLSFromEnv(t *testing.T) {
	testAccPreCheck(t)
	ep := p3TLSEndpoint(t)
	certPath := p3Env(t, "TF_ACC_TLS_CERT_PATH")[0]
	t.Setenv("DOCKER_HOST", ep.Host)
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", certPath)
	name := acctest.RandomWithPrefix("tfacc-tlsenv")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             tlsP3VolumeCheck(t, ep, name, false),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "docker_volume" "v" {
  name = %q
}
`, name),
			Check: tlsP3VolumeCheck(t, ep, name, true),
		}},
	})
}

// TestAccProviderP3_TLSRequiresClientCert: with only the CA (no client
// certificate) the TLS daemon rejects the provider; the error surfaces at the
// first CRUD call (lazy Configure).
func TestAccProviderP3_TLSRequiresClientCert(t *testing.T) {
	testAccPreCheck(t)
	ep := p3TLSEndpoint(t)
	name := acctest.RandomWithPrefix("tfacc-tlsno")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             tlsP3VolumeCheck(t, ep, name, false),
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "docker" {
  host = %q
  tls {
    ca = %q
  }
}

resource "docker_volume" "v" {
  name = %q
}
`, ep.Host, p3HCLPath(ep.CA), name),
			ExpectError: regexp.MustCompile(`(?i)(certificate|tls|handshake|bad)`),
		}},
	})
}
