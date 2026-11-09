data "docker_system_df" "usage" {}

output "reclaimable_bytes" {
  value = {
    images      = data.docker_system_df.usage.df.image_usage.reclaimable
    containers  = data.docker_system_df.usage.df.container_usage.reclaimable
    volumes     = data.docker_system_df.usage.df.volume_usage.reclaimable
    build_cache = data.docker_system_df.usage.df.build_cache_usage.reclaimable
  }
}
