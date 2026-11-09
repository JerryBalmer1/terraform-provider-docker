package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccDockerfileDataSource_basic: content mode and builder mode through
// Terraform (no Engine call), plus image mode on a pulled image (Engine
// API history). Not a swarm-manager test.
func TestAccDockerfileDataSource_basic(t *testing.T) {
	testAccPreCheck(t)
	cfg := fmt.Sprintf(`
data "docker_dockerfile" "content" {
  content = %q
}

data "docker_dockerfile" "built" {
  syntax = "docker/dockerfile:1"
  check {
    skip = ["JSONArgsRecommended"]
  }
  global_arg {
    name    = "BASE"
    default = "alpine:3.20"
  }
  stage {
    name = "build"
    from = "$${BASE}"
    instruction {
      workdir {
        path = "/src"
      }
    }
    instruction {
      run {
        shell_form = "echo built > /out"
        mount = [{ type = "cache", target = "/root/.cache" }]
      }
    }
  }
  stage {
    from = "alpine:3.20"
    instruction {
      copy {
        from        = "build"
        sources     = ["/out"]
        destination = "/out"
      }
    }
    instruction {
      cmd {
        exec_form = ["cat", "/out"]
      }
    }
  }
}

resource "docker_image" "alpine" {
  name         = "alpine:3.20"
  keep_locally = true
}

data "docker_dockerfile" "image" {
  image = docker_image.alpine.id
}
`, "FROM alpine:3.20 AS base\nLABEL a=b\nEXPOSE 80\n")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.docker_dockerfile.content", "source_type", "content"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.content", "fidelity", "exact"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.content", "final_stage", "base"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.content", "labels.a", "b"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.content", "exposed_ports.0.port", "80"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.built", "source_type", "blocks"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.built", "stage_names.#", "2"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.built", "stage.0.instruction.1.instruction", "RUN"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.built", "stage.0.instruction.1.instruction_index", "2"),
				resource.TestMatchResourceAttr("data.docker_dockerfile.built", "dockerfile_sha256", regexp.MustCompile(`^[0-9a-f]{64}$`)),
				resource.TestCheckResourceAttr("data.docker_dockerfile.image", "source_type", "image"),
				resource.TestCheckResourceAttr("data.docker_dockerfile.image", "fidelity", "approximate"),
				resource.TestCheckResourceAttrSet("data.docker_dockerfile.image", "engine_api_version"),
				resource.TestCheckResourceAttrSet("data.docker_dockerfile.image", "history.#"),
			),
		}},
	})
}
