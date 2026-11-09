# A compose project on the provider's daemon, the way `docker compose up -d`
# would run it: containers shop-web-1, shop-db-1, networks shop_default and a
# shop_pgdata volume, all labelled com.docker.compose.project=shop.
resource "docker_compose" "shop" {
  name              = "shop"
  project_directory = path.module

  # Only these variables are visible to ${...} interpolation.
  env = {
    WEB_TAG     = "1.27-alpine"
    DB_PASSWORD = "change-me"
  }

  content = <<-EOT
    services:
      web:
        image: nginx:$${WEB_TAG}
        ports: ["8080:80"]
        configs:
          - source: site
            target: /etc/nginx/conf.d/default.conf
        depends_on:
          db: {condition: service_healthy}
      db:
        image: postgres:16-alpine
        environment:
          POSTGRES_PASSWORD_FILE: /run/secrets/db_password
        secrets: [db_password]
        volumes: ["pgdata:/var/lib/postgresql/data"]
        healthcheck:
          test: ["CMD", "pg_isready", "-U", "postgres"]
          interval: 5s
    volumes:
      pgdata: {}
    secrets:
      db_password:
        environment: DB_PASSWORD
    configs:
      site:
        content: |
          server { listen 80; location / { return 200 "ok"; } }
  EOT
}

# The same file format deployed as a Swarm stack (needs a Swarm manager).
resource "docker_compose" "stack" {
  name = "shopstack"
  mode = "swarm"

  content = <<-EOT
    services:
      web:
        image: nginx:1.27-alpine
        ports: ["8081:80"]
        deploy:
          replicas: 2
          placement:
            constraints: ["node.role==worker"]
  EOT
}

output "web_container_ids" {
  value = docker_compose.shop.services["web"]
}

# The normalized project (what `docker compose config` prints) and its hash,
# for a downstream runner.
output "compose_yaml_sha256" {
  value = docker_compose.shop.content_sha256
}

output "web_image" {
  value = docker_compose.shop.project.services["web"].image
}
