resource "docker_container" "debug" {
  name    = "debug"
  image   = "busybox:1.36"
  command = ["sh", "-c", "sleep 3600"]

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_image_commit.snapshot]
    }
  }
}

# Snapshots the container's filesystem as an image (docker container commit).
# On demand:
#   terraform apply -invoke=action.docker_image_commit.snapshot
action "docker_image_commit" "snapshot" {
  config {
    container_id = docker_container.debug.id
    reference    = "registry.example.com/debug/snapshot:latest"
    message      = "snapshot taken by Terraform"
    changes      = ["LABEL snapshot=true"]
    result_path  = "${path.module}/out/commit-result.json"

    # Optional: the image configuration as the commit body (ContainerConfig).
    # Unset fields such as env, labels and cmd fall back to the container's.
    config = {
      cmd         = ["sh"]
      env         = ["SNAPSHOT=1"]
      working_dir = "/"
      healthcheck = { test = ["CMD", "true"], interval = "30s" }
    }
  }
}
