resource "docker_container" "web" {
  name  = "web"
  image = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_cp.index]
    }
  }
}

# Copy a page into the running container (the container may also be stopped).
#   terraform apply -invoke=action.docker_container_cp.index
action "docker_container_cp" "index" {
  config {
    container_id = docker_container.web.id
    path         = "/usr/share/nginx/html/index.html"
    content      = "<h1>deployed by Terraform</h1>"
    mode         = "0644"
    uid          = 101 # nginx
    gid          = 101
    copy_uidgid  = true # map uid / gid through a user-namespace remapping (docker cp -a)
  }
}
