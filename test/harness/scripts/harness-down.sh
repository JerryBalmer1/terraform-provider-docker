#!/usr/bin/env bash
# Tear down the dind harness, its volumes, test/harness/.env, the plugin
# fixtures (test/harness/.plugins/) and the generated
# TLS material (test/harness/tls/). The cached Terraform CLI in
# test/harness/.bin/ is kept for the next run.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# compose must talk to the HOST daemon, not the harness manager.
if [ "${DOCKER_HOST:-}" = "tcp://127.0.0.1:12375" ]; then unset DOCKER_HOST; fi

rc=0
docker compose -f "${HARNESS_DIR}/docker-compose.yml" down -v --remove-orphans || rc=$?
rm -f "${HARNESS_DIR}/.env"
rm -rf "${HARNESS_DIR}/tls" "${HARNESS_DIR}/.plugins"
if [ "${rc}" -ne 0 ]; then
  echo "ERROR: 'docker compose down' failed (exit ${rc}); .env and tls/ removed anyway." >&2
  exit "${rc}"
fi
echo "Harness removed."
