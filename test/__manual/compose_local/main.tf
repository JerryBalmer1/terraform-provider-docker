# compose_local: a docker_compose project in local mode (what `docker compose up -d` does, through the Engine API):
# three services, two networks, a named volume, a secret and a config, healthcheck-gated depends_on, a scaled
# service and lifecycle hooks; then the docker_compose_* data sources and one triggered action. Plain harness.
terraform {
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

provider "docker" {}
resource "docker_compose" "shop" {
  name              = "manualshop"
  project_directory = path.module

  env = {
    WEB_TAG     = "1.27-alpine"
    DB_PASSWORD = "change-me"
  }

  content = <<-EOT
    services:
      web:
        image: nginx:$${WEB_TAG}
        ports: ["8090:80"]
        networks: [front, back]
        configs:
          - source: site
            target: /etc/nginx/conf.d/default.conf
        depends_on:
          api: {condition: service_started}
          db: {condition: service_healthy}
        labels:
          com.example.tier: web
        post_start:
          - command: ["sh", "-c", "nginx -t"]
      api:
        image: busybox:1.36
        command: ["sh", "-c", "while true; do echo api tick; sleep 5; done"]
        networks: [back]
        deploy:
          replicas: 2
        environment:
          API_MODE: demo
      db:
        image: postgres:16-alpine
        environment:
          POSTGRES_PASSWORD_FILE: /run/secrets/db_password
        secrets: [db_password]
        volumes: ["pgdata:/var/lib/postgresql/data"]
        networks: [back]
        healthcheck:
          test: ["CMD", "pg_isready", "-U", "postgres"]
          interval: 5s
          retries: 20
    networks:
      front: {}
      back:
        internal: true
    volumes:
      pgdata:
        labels:
          com.example.backup: "daily"
    secrets:
      db_password:
        environment: DB_PASSWORD
    configs:
      site:
        content: |
          server { listen 80; location / { return 200 "manualshop ok\n"; } }
  EOT

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_compose_exec.check]
    }
  }
}

# nginx -t inside manualshop-web-1 after every apply; result written beside the state.
action "docker_compose_exec" "check" {
  config {
    name        = docker_compose.shop.name
    service     = "web"
    command     = ["nginx", "-t"]
    result_path = "${path.module}/out/nginx-t.json"
  }
}

data "docker_compose_containers" "shop" {
  name       = docker_compose.shop.name
  all        = true
  depends_on = [docker_compose.shop]
}

data "docker_compose_port" "web" {
  name         = docker_compose.shop.name
  service      = "web"
  private_port = 80
  depends_on   = [docker_compose.shop]
}

output "containers" {
  value = { for c in data.docker_compose_containers.shop.containers : c.names[0] => c.state }
}

output "web_url" {
  value = "http://127.0.0.1:${data.docker_compose_port.web.published_port} (inside the dind manager)"
}

output "normalized_yaml_sha256" {
  value = docker_compose.shop.content_sha256
}
