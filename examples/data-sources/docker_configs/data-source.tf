data "docker_configs" "nginx" {
  name = "nginx"
}

# Each element is the Engine config object; spec.data is base64.
output "nginx_configs" {
  value = { for c in data.docker_configs.nginx.configs : c.spec.name => base64decode(c.spec.data) }
}
