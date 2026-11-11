# dockerfile_image_container: data.docker_dockerfile parses app/Dockerfile, docker_image builds its last stage
# (rebuilt when the parsed Dockerfile changes), docker_container runs it. A second data.docker_dockerfile renders a
# Dockerfile from blocks, shown as an output. Plain harness (no swarm needed).
terraform {
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

provider "docker" {}
data "docker_dockerfile" "app" {
  path = "${path.module}/app/Dockerfile"
}

locals {
  target = data.docker_dockerfile.app.stage_names[length(data.docker_dockerfile.app.stage_names) - 1]
}

resource "docker_image" "app" {
  name = "manual/dockerfile-app:1"
  build {
    context = "${path.module}/app"
    target  = local.target
    args    = { BASE = "busybox:1.36" }
    labels  = { scenario = "dockerfile_image_container" }
  }
  triggers = {
    dockerfile_sha256 = data.docker_dockerfile.app.dockerfile_sha256
  }
}

resource "docker_container" "app" {
  name  = "manual-dockerfile-app"
  image = docker_image.app.id
  ports {
    container_port = 8080
    host_port      = 8089
  }
  wait         = true
  wait_timeout = "1m"
}

# The same kind of file, written from blocks instead of parsed.
data "docker_dockerfile" "generated" {
  syntax = "docker/dockerfile:1"
  stage {
    name = "runtime"
    from = "busybox:1.36"
    instruction {
      copy {
        sources     = ["index.html"]
        destination = "/srv/index.html"
      }
    }
    instruction {
      expose {
        ports = [{ port = "8080" }]
      }
    }
    instruction {
      cmd {
        exec_form = ["httpd", "-f", "-p", "8080", "-h", "/srv"]
      }
    }
  }
}

output "stages" {
  value = data.docker_dockerfile.app.stage_names
}

output "built_target" {
  value = local.target
}

output "image" {
  value = {
    id     = docker_image.app.id
    stages = [for s in docker_image.app.stages : s.name]
  }
}

output "generated_dockerfile" {
  value     = data.docker_dockerfile.generated.dockerfile
  sensitive = true
}
