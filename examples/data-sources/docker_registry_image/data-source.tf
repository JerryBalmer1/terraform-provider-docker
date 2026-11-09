# Current digest of a tag in its registry (nothing is pulled).
data "docker_registry_image" "alpine" {
  name = "alpine:3.20"
}

output "alpine_digest" {
  value = data.docker_registry_image.alpine.digest
}
