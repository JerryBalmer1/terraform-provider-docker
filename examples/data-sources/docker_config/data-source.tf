data "docker_config" "nginx" {
  name = "nginx-default.conf"
}

output "nginx_conf" {
  value = base64decode(data.docker_config.nginx.inspect.spec.data)
}
