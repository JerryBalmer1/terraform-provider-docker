# swarm_stack: the swarm workload layer on the harness swarm (needs Invoke-Build _Up -Swarm): docker_secret,
# docker_config, an overlay docker_network, a docker_service using all three, and a docker_stack with its own
# network, secret, config and two services.
terraform {
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

provider "docker" {}
resource "docker_secret" "api_key" {
  name   = "manual-api-key"
  data   = base64encode("s3cr3t")
  labels = { scenario = "swarm_stack" }
}

resource "docker_config" "site" {
  name   = "manual-site.conf"
  data   = base64encode("server { listen 80; location / { return 200 \"swarm_stack ok\\n\"; } }")
  labels = { scenario = "swarm_stack" }
}

resource "docker_network" "mesh" {
  name       = "manual-mesh"
  driver     = "overlay"
  attachable = true
  labels     = { scenario = "swarm_stack" }
}

resource "docker_service" "web" {
  name     = "manual-web"
  image    = "nginx:1.27-alpine"
  replicas = 2

  networks = {
    (docker_network.mesh.name) = { aliases = ["web"] }
  }

  env    = { TZ = "UTC" }
  labels = { scenario = "swarm_stack" }

  secrets {
    secret_id = docker_secret.api_key.id
    file_name = "api_key"
  }

  configs {
    config_id = docker_config.site.id
    file_name = "/etc/nginx/conf.d/default.conf"
  }

  endpoint_spec {
    ports {
      name           = "http"
      target_port    = 80
      published_port = 8091
    }
  }

  update_config {
    parallelism    = 1
    delay          = "5s"
    failure_action = "rollback"
    order          = "start-first"
  }

  restart_policy {
    condition    = "on-failure"
    max_attempts = 3
  }

  healthcheck {
    test     = ["CMD", "wget", "-q", "-O-", "http://127.0.0.1/"]
    interval = "10s"
    timeout  = "3s"
    retries  = 3
  }

  converge_config {
    timeout = "3m"
  }
}

resource "docker_stack" "shop" {
  name = "manualstack"

  network {
    name       = "backend"
    attachable = true
  }

  secret {
    name = "token"
    data = base64encode("stack-token")
  }

  config {
    name = "motd"
    data = base64encode("hello from manualstack")
  }

  service {
    name     = "api"
    image    = "busybox:1.36"
    args     = ["sh", "-c", "cat /run/secrets/token; cat /motd; sleep 3600"]
    replicas = 2
    networks = { backend = {} }

    secrets {
      secret_id = "token"
    }

    configs {
      config_id = "motd"
      file_name = "/motd"
    }

    converge_config {}
  }

  service {
    name     = "worker"
    image    = "busybox:1.36"
    args     = ["sleep", "3600"]
    networks = { backend = {} }

    converge_config {}
  }
}

data "docker_stack" "shop" {
  name       = docker_stack.shop.name
  depends_on = [docker_stack.shop]
}

data "docker_tasks" "web" {
  service_id    = docker_service.web.id
  desired_state = "running"
}

output "stack_services" {
  value = { for s in data.docker_stack.shop.services : s.name => s.replicas }
}

output "web_tasks" {
  value = { for t in data.docker_tasks.web.tasks : t.id => t.status.state }
}
