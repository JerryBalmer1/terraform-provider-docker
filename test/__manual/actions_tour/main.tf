# actions_tour: one container and an image, and the actions that operate on them. Some run on create
# (lifecycle.action_trigger); every one can be run on demand:
#   Invoke-Build _Apply -Scenario actions_tour -Invoke action.<type>.<name>
# Each writes its result JSON under out/ in the working copy. Plain harness (no swarm needed).
terraform {
  required_providers {
    docker = {
      source = "JerryBalmer1/docker"
    }
  }
}

provider "docker" {}
resource "docker_image" "busybox" {
  name         = "busybox:1.36"
  keep_locally = true
}

resource "docker_container" "box" {
  name    = "manual-box"
  image   = docker_image.busybox.id
  command = ["sh", "-c", "trap 'echo got HUP' HUP; while true; do sleep 1; done"]

  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_container_cp.motd, action.docker_exec.hello]
    }
  }
}

# Runs once, after the container is created.
resource "terraform_data" "after_box" {
  input = docker_container.box.id

  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.docker_container_kill.hup, action.docker_image_save.busybox]
    }
  }
}

action "docker_container_cp" "motd" {
  config {
    container_id = docker_container.box.id
    path         = "/etc/motd"
    content      = "copied by docker_container_cp\n"
    mode         = "0644"
  }
}

action "docker_exec" "hello" {
  config {
    container_id = docker_container.box.id
    command      = ["sh", "-c", "cat /etc/motd; echo hello from docker_exec; id"]
    env          = { GREETING = "hi" }
    result_path  = "${path.module}/out/exec.json"
  }
}

action "docker_container_kill" "hup" {
  config {
    container_id = docker_container.box.id
    signal       = "SIGHUP"
  }
}

action "docker_image_save" "busybox" {
  config {
    images      = [docker_image.busybox.name]
    output      = "${path.module}/out/busybox.tar"
    result_path = "${path.module}/out/save.json"
  }
}

# On demand only.
action "docker_container_pause" "box" {
  config {
    container_id = docker_container.box.id
    result_path  = "${path.module}/out/pause.json"
  }
}

action "docker_container_unpause" "box" {
  config {
    container_id = docker_container.box.id
    result_path  = "${path.module}/out/unpause.json"
  }
}

action "docker_container_restart" "box" {
  config {
    container_id = docker_container.box.id
    timeout      = 5
    result_path  = "${path.module}/out/restart.json"
  }
}

action "docker_container_export" "box" {
  config {
    container_id = docker_container.box.id
    output       = "${path.module}/out/box-rootfs.tar"
  }
}

action "docker_container_run" "oneshot" {
  config {
    image               = "busybox:1.36"
    command             = ["sh", "-c", "echo one-shot; exit 0"]
    expected_exit_codes = [0]
    result_path         = "${path.module}/out/run.json"
  }
}

action "docker_image_prune" "dangling" {
  config {
    dangling = true
  }
}

action "docker_container_prune" "stopped" {
  config {
    result_path = "${path.module}/out/container-prune.json"
  }
}
