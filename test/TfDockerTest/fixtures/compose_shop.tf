# Prerequisite: compose project "shop" (web on 8080, a worker, a named volume) for the docker_compose_* data sources.
resource "docker_compose" "shop" {
  name    = "shop"
  content = <<-EOT
    services:
      web:
        image: nginx:1.27-alpine
        ports: ["8080:80"]
        volumes: ["data:/data"]
      worker:
        image: busybox:1.36
        command: ["sh", "-c", "echo worker up; sleep 3600"]
    volumes:
      data: {}
  EOT
}
