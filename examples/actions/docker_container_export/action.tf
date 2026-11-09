resource "docker_container" "build" {
  name    = "rootfs-build"
  image   = "alpine:3.20"
  command = ["sh", "-c", "apk add --no-cache curl && sleep infinity"]

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_container_export.rootfs]
    }
  }
}

# Writes the container filesystem to a local tar. On demand:
#   terraform apply -invoke=action.docker_container_export.rootfs
action "docker_container_export" "rootfs" {
  config {
    container_id = docker_container.build.id
    output       = "${path.module}/out/rootfs.tar"
    result_path  = "${path.module}/out/rootfs.json"
  }
}
