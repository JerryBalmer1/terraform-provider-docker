# Two single-platform images already pushed to the registry ...
resource "docker_registry_image" "amd64" {
  name = "registry.example.com/team/app:1.0-amd64"
}

resource "docker_registry_image" "arm64" {
  name = "registry.example.com/team/app:1.0-arm64"
}

# ... combined into one multi-platform tag (docker manifest create + push).
resource "docker_registry_manifest_list" "app" {
  name = "registry.example.com/team/app:1.0"

  manifests = [
    { image = docker_registry_image.amd64.image_digest_ref },
    {
      image   = docker_registry_image.arm64.image_digest_ref
      variant = "v8" # docker manifest annotate --variant v8
    },
  ]

  annotations = {
    "org.opencontainers.image.source" = "https://github.com/team/app"
  }
}

output "app_index" {
  value = docker_registry_manifest_list.app.image_digest_ref
}
