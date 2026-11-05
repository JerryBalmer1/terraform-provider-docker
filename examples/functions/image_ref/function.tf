# Re-tag an image for a private mirror, keeping its repository path.
locals {
  src    = provider::docker::parse_image_ref("nginx:1.27")
  mirror = provider::docker::image_ref("registry.internal:5000", local.src.repository, local.src.tag, null)
  # mirror = "registry.internal:5000/library/nginx:1.27"

  pinned = provider::docker::image_ref("", "redis", "7", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
  # pinned = "docker.io/library/redis:7@sha256:0123..."
}

output "mirror" {
  value = local.mirror
}
