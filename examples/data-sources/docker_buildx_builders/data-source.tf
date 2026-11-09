data "docker_buildx_builders" "all" {}

output "builders" {
  # e.g. { default = "default", ci = "docker-container://buildx_buildkit_ci0" }
  value = data.docker_buildx_builders.all.endpoints
}
