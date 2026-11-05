data "docker_configs" "nginx" {
  name = "nginx"
}

output "nginx_configs" {
  value = { for c in data.docker_configs.nginx.configs : c.name => base64decode(c.data) }
}
