resource "terraform_data" "bundle" {
  input = filesha256("${path.module}/images.tar")

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_image_load.bundle]
    }
  }
}

# Loads every image in an archive written by `docker save`. On demand:
#   terraform apply -invoke=action.docker_image_load.bundle
action "docker_image_load" "bundle" {
  config {
    path = "${path.module}/images.tar"
  }
}
