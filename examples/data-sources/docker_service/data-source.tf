data "docker_service" "web" {
  name = "web"
}

# Pin a downstream deployment to the exact image the service runs.
output "web_image" {
  value = data.docker_service.web.image_digest_ref
}

output "web_published_ports" {
  value = [for p in data.docker_service.web.published_ports : p.published_port]
}
