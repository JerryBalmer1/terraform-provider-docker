data "docker_buildx_history_records" "recent" {
  builder = "default"
  limit   = 10
}

output "recent_builds" {
  value = [for r in data.docker_buildx_history_records.recent.records : "${r.name} ${r.status} ${r.duration}"]
}
