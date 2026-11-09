resource "docker_image" "app" {
  name = "nginx:1.27"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_image_save.bundle]
    }
  }
}

# Writes the image to a tar for air-gapped hosts. On demand:
#   terraform apply -invoke=action.docker_image_save.bundle
action "docker_image_save" "bundle" {
  config {
    images      = [docker_image.app.name]
    output      = "${path.module}/out/images.tar"
    result_path = "${path.module}/out/save-result.json"
  }
}
