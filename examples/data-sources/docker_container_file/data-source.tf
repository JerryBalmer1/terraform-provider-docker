# Read a configuration file a container generated on first start.
data "docker_container_file" "generated" {
  container = "app"
  path      = "/var/lib/app/generated.conf"
}

output "generated_conf" {
  value     = base64decode(data.docker_container_file.generated.content_base64)
  sensitive = true
}

output "generated_conf_size" {
  value = data.docker_container_file.generated.stat.size
}
