# Volumes no container uses.
data "docker_volumes" "unused" {
  dangling = true
}

output "unused_volume_names" {
  value = data.docker_volumes.unused.names
}
