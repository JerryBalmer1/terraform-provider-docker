# Inspect an image that is already on the daemon (no pull).
data "docker_image" "nginx" {
  name = "nginx:1.27"
}

output "nginx_image_id" {
  value = data.docker_image.nginx.id
}

output "nginx_labels" {
  value = data.docker_image.nginx.labels
}
