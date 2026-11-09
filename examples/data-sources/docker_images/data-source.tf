# Every tagged nginx image on the daemon (docker image ls).
data "docker_images" "nginx" {
  reference = "nginx"
  dangling  = false
}

output "nginx_image_ids" {
  value = data.docker_images.nginx.ids
}
