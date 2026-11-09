# Parse an existing Dockerfile and use its structure.
data "docker_dockerfile" "app" {
  path = "${path.module}/app/Dockerfile"
}

output "build_targets" {
  value = data.docker_dockerfile.app.stage_names
}

# Rebuild the image when the parsed Dockerfile changes.
resource "docker_image" "app" {
  name = "registry.example.com/app:latest"
  build {
    context = "${path.module}/app"
  }
  triggers = {
    dockerfile_sha256 = data.docker_dockerfile.app.dockerfile_sha256
  }
}

# Write a Dockerfile with ordered blocks: one `instruction` block per
# instruction, each holding exactly one keyword block.
data "docker_dockerfile" "generated" {
  syntax = "docker/dockerfile:1"
  check {
    skip = ["JSONArgsRecommended"]
  }

  global_arg {
    name    = "GO_VERSION"
    default = "1.22"
  }

  stage {
    name     = "build"
    from     = "golang:$${GO_VERSION}"
    platform = "$BUILDPLATFORM"
    instruction {
      workdir {
        path = "/src"
      }
    }
    instruction {
      copy {
        sources     = ["."]
        destination = "."
      }
    }
    instruction {
      run {
        shell_form = "go build -o /out/app ./cmd/app"
        mount = [
          { type = "cache", target = "/root/.cache/go-build" },
        ]
      }
    }
  }

  stage {
    from = "gcr.io/distroless/static"
    instruction {
      label {
        # MAINTAINER is deprecated: use the OCI authors label.
        labels = { "org.opencontainers.image.authors" = "ops@example.com" }
      }
    }
    instruction {
      copy {
        from        = "build"
        sources     = ["/out/app"]
        destination = "/app"
        link        = true
      }
    }
    instruction {
      expose {
        ports = [{ port = "8080" }]
      }
    }
    instruction {
      entrypoint {
        exec_form = ["/app"]
      }
    }
  }
}

# The rendered text is sensitive (ENV values and ARG defaults are inline).
output "generated_dockerfile" {
  value     = data.docker_dockerfile.generated.dockerfile
  sensitive = true
}

# Approximate Dockerfile of a local image, rebuilt from its history.
data "docker_dockerfile" "nginx" {
  image = "nginx:latest"
}
