# The daemon's own BuildKit (buildx's "default" builder).
data "docker_buildx_builder" "default" {}

# A docker-container builder, e.g. one made by docker_buildx_builder.
data "docker_buildx_builder" "ci" {
  name = "ci"
}

output "ci_buildkit" {
  value = {
    version   = data.docker_buildx_builder.ci.buildkit_version
    platforms = data.docker_buildx_builder.ci.platforms
    status    = [for n in data.docker_buildx_builder.ci.builder.nodes : "${n.name}: ${n.status}"]
  }
}
