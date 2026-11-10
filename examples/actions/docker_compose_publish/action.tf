resource "docker_compose" "shop" {
  name    = "shop"
  content = <<-EOT
    services:
      web:
        image: nginx:1.27-alpine
        ports: ["8080:80"]
      worker:
        image: busybox:1.36
        command: ["sh", "-c", "sleep 3600"]
  EOT

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_compose_publish.this]
    }
  }
}

# Publishes the compose file as an OCI artifact. On demand:
#   terraform apply -invoke=action.docker_compose_publish.this
action "docker_compose_publish" "this" {
  config {
    name       = "shop"
    content    = docker_compose.shop.content
    repository = "registry.example.com/apps/shop:1.0"

    # Pin every image to its digest in an extra image-digests.yaml layer,
    # and push an OCI 1.0 manifest for registries without artifact types.
    resolve_image_digests = true
    oci_version           = "1.0"
  }
}
