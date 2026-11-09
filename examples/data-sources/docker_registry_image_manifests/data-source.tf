data "docker_registry_image_manifests" "nginx" {
  name = "nginx:1.27"
}

# Pin the arm64 variant of a multi-platform tag.
locals {
  arm64 = one([for m in data.docker_registry_image_manifests.nginx.manifests : m.digest if m.os == "linux" && m.architecture == "arm64"])
}

output "nginx_arm64" {
  value = "nginx@${local.arm64}"
}

# SLSA provenance of a BuildKit-built image (null when it has none).
data "docker_registry_image_manifests" "app" {
  name = "ghcr.io/org/app:1.0"
}

output "app_provenance" {
  value     = try(jsondecode(data.docker_registry_image_manifests.app.provenance_json)["linux/amd64"].SLSA, null)
  sensitive = true
}
