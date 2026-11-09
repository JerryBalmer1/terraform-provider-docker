resource "docker_image" "app" {
  name = "nginx:1.27"
}

# Same image under a second name, e.g. before pushing it to a private registry.
resource "docker_tag" "release" {
  source_image = docker_image.app.id
  target_image = "registry.example.com/web/nginx:stable"

  # Re-tag whenever the source image is rebuilt.
  triggers = { source = docker_image.app.id }
}

output "tag_ref" {
  value = docker_tag.release.image_ref
}

output "tagged_image_id" {
  value = docker_tag.release.source_image_id
}
