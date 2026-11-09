data "docker_container_stats" "db" {
  container = "db"
}

output "db_memory_usage_bytes" {
  value = data.docker_container_stats.db.stats.memory_stats.usage
}
