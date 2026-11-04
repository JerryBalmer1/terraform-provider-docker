resource "docker_image" "app" {
  name = "registry.example.com/team/app:1.0"
  build {
    context = "${path.module}/app"
  }
}

# Push the built image. triggers re-pushes whenever the image is rebuilt.
resource "docker_registry_image" "app" {
  name          = docker_image.app.name
  keep_remotely = true
  push_retries  = 5
  triggers = {
    image_id = docker_image.app.image_id
  }
}

output "app_digest" {
  value = docker_registry_image.app.sha256_digest
}
