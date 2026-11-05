resource "terraform_data" "rootfs" {
  # Re-import whenever the tarball changes.
  input = filesha256("${path.module}/rootfs.tar")

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_image_import.rootfs]
    }
  }
}

# On demand:
#   terraform apply -invoke=action.docker_image_import.rootfs
action "docker_image_import" "rootfs" {
  config {
    source     = "${path.module}/rootfs.tar" # or an https:// URL
    repository = "example/rootfs"
    tag        = "v1"
    changes    = ["CMD [\"/bin/sh\"]", "ENV LANG=C.UTF-8"]
    message    = "imported by Terraform"
  }
}
