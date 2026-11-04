#!/usr/bin/env bash
# Tear down the dind swarm harness, its volumes, and test/harness/.env.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

docker compose -f "${HARNESS_DIR}/docker-compose.yml" down -v --remove-orphans
rm -f "${HARNESS_DIR}/.env"
echo "Harness removed."
