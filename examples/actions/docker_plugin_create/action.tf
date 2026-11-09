resource "terraform_data" "plugin_src" {
  input = filesha256("${path.module}/plugin/config.json")

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_plugin_create.myplugin]
    }
  }
}

# Creates a plugin from ./plugin (config.json + rootfs/) whenever its config changes. On demand:
#   terraform apply -invoke=action.docker_plugin_create.myplugin
action "docker_plugin_create" "myplugin" {
  config {
    name        = "registry.example.com/org/myplugin:1.0"
    context_dir = "${path.module}/plugin"
    result_path = "${path.module}/.plugin/create.json"
  }
}
