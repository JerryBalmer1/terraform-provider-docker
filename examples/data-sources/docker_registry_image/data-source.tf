# Current digest of a tag in its registry (nothing is pulled).
data "docker_registry_image" "alpine" {
  name = "alpine:3.20"
}

output "alpine_digest" {
  value = data.docker_registry_image.alpine.digest
}

# A registry with a self-signed certificate, read directly by the provider
# with certificate verification off (unset: read through the daemon).
data "docker_registry_image" "internal" {
  name                 = "registry.internal:5443/team/app:1.0"
  insecure_skip_verify = true
}
