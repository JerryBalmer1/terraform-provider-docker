data "docker_plugin" "sshfs" {
  alias = "sshfs:latest"
}

output "sshfs_enabled" {
  value = data.docker_plugin.sshfs.enabled
}
