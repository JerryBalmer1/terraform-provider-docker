resource "terraform_data" "plugin_version" {
  input = "1.0"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.docker_plugin_push.myplugin]
    }
  }
}

# Pushes the locally created plugin when its version changes. On demand:
#   terraform apply -invoke=action.docker_plugin_push.myplugin
action "docker_plugin_push" "myplugin" {
  config {
    name        = "registry.example.com/org/myplugin:${terraform_data.plugin_version.input}"
    result_path = "${path.module}/.plugin/push.json"
  }
}
