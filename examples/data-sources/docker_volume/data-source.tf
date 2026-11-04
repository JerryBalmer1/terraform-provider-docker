data "docker_volume" "data" {
  name = "app-data"
}

output "data_mountpoint" {
  value = data.docker_volume.data.mountpoint
}
