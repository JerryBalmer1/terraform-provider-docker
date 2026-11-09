data "docker_compose_port" "web" {
  name         = "shop"
  service      = "web"
  private_port = 80
}

output "web_url" {
  value = "http://localhost:${data.docker_compose_port.web.published_port}"
}
