# Layer history of a local image, newest layer first (docker image history).
data "docker_image_history" "nginx" {
  name = "nginx:1.27"
}

# created_by is sensitive (it can hold build arguments), so the output must be too.
output "nginx_layers" {
  value     = [for h in data.docker_image_history.nginx.history : h.created_by]
  sensitive = true
}
