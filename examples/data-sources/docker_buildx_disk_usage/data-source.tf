data "docker_buildx_disk_usage" "cache" {
  builder = "default"
  filters = {
    type = "regular"
  }
}

output "build_cache_bytes" {
  value = data.docker_buildx_disk_usage.cache.summary
}
