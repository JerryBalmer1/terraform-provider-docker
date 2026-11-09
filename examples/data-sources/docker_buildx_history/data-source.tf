# The newest build record of the daemon's builder. ref also accepts a
# builder/node/ref value from docker_buildx_bake's build_refs, or "^1" for
# the build before the newest.
data "docker_buildx_history" "last" {
  builder = "default"
  ref     = "^0"
}

output "last_build" {
  value = {
    name     = data.docker_buildx_history.last.name
    status   = data.docker_buildx_history.last.record.status
    cached   = "${data.docker_buildx_history.last.record.num_cached_steps}/${data.docker_buildx_history.last.record.num_total_steps}"
    revision = data.docker_buildx_history.last.vcs_revision
  }
}
