# Join tokens as ephemeral values (Terraform 1.10+): they are used during the
# run but never written to plan or state. Ephemeral values can only flow into
# write-only arguments, provider blocks and other ephemeral resources; here the
# worker token becomes a swarm secret a bootstrap service hands to new nodes.
ephemeral "docker_swarm_tokens" "current" {}

resource "docker_secret" "worker_join_token" {
  name            = "swarm-worker-join-token"
  data_wo         = base64encode(ephemeral.docker_swarm_tokens.current.worker)
  data_wo_version = 1
}
