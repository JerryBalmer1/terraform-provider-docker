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
