# Enabled volume driver plugins.
data "docker_plugins" "volume_drivers" {
  capability = "volumedriver"
  enabled    = true
}

output "volume_driver_plugins" {
  value = data.docker_plugins.volume_drivers.names
}
