# SBOM and provenance attestations stored with a local image
# (Engine API 1.55+, containerd image store).
data "docker_image_attestations" "app" {
  name              = "ghcr.io/example/app:1.0"
  predicate_types   = ["https://spdx.dev/Document"]
  include_statement = true
}

output "sbom_digests" {
  value = data.docker_image_attestations.app.digests
}
