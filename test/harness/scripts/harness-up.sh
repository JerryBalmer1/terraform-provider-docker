#!/usr/bin/env bash
# Bring up the dind swarm harness (manager + 2 workers) and write
# test/harness/.env for `make e2e`. The docker CLI is used here, in the test
# harness only; the provider never shells out to it.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
COMPOSE_FILE="${HARNESS_DIR}/docker-compose.yml"
ENV_FILE="${HARNESS_DIR}/.env"

docker compose -f "${COMPOSE_FILE}" up -d --wait

# --wait relies on the healthchecks; additionally confirm each published
# endpoint answers from the host side (port publishing can lag).
wait_daemon() {
  local name="$1" host="$2" i
  for i in $(seq 1 60); do
    if docker -H "${host}" info >/dev/null 2>&1; then
      echo "${name} ready at ${host}"
      return 0
    fi
    sleep 2
  done
  echo "ERROR: ${name} did not answer at ${host} within 120s" >&2
  docker compose -f "${COMPOSE_FILE}" logs --tail=50 "${name}" >&2 || true
  return 1
}

wait_daemon manager tcp://127.0.0.1:12375
wait_daemon worker1 tcp://127.0.0.1:12376
wait_daemon worker2 tcp://127.0.0.1:12377

cat > "${ENV_FILE}" <<'ENVEOF'
export DOCKER_HOST=tcp://127.0.0.1:12375
export DOCKER_WORKER1_HOST=tcp://127.0.0.1:12376
export DOCKER_WORKER2_HOST=tcp://127.0.0.1:12377
export E2E_MANAGER_ADDR=manager:2377
export E2E_ADVERTISE_ADDR=eth0
ENVEOF

echo "Wrote ${ENV_FILE}:"
cat "${ENV_FILE}"
