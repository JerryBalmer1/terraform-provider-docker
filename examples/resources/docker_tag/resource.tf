resource "docker_image" "app" {
  name = "nginx:1.27"
}

# Same image under a second name, e.g. before pushing it to a private registry.
resource "docker_tag" "release" {
  source_image = docker_image.app.image_id
  target_image = "registry.example.com/web/nginx:stable"
}

output "tagged_image_id" {
  value = docker_tag.release.source_image_id
}
