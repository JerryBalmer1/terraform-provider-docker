# Search Docker Hub through the daemon (docker search).
data "docker_image_search" "redis" {
  term        = "redis"
  limit       = 10
  is_official = true
}

output "official_redis" {
  value = data.docker_image_search.redis.names
}
