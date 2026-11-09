#!/usr/bin/env bash
# Turn the running harness into the swarm the -SwarmInit acceptance pass
# expects (bash twin of harness-swarm-init.ps1; run it after harness-up.sh and
# `source test/harness/.env`):
#   1. docker swarm init on the harness manager (never on the host daemon);
#   2. join worker4 (DOCKER_WORKER4_HOST) as a worker and wait until it is
#      Ready (worker1-3 stay outside the swarm: the docker_swarm_node and
#      TestAccE2E* tests join and remove them themselves);
#   3. create and enable the tfacc CSI plugin (TF_ACC_CSI_PLUGIN from
#      TF_ACC_CSI_PLUGIN_DIR) on the manager, best effort.
# The docker CLI is used here, in the test harness only.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/../.env"
if [ -z "${DOCKER_HOST:-}" ] && [ -f "${ENV_FILE}" ]; then
  # shellcheck disable=SC1090
  set -a; . "${ENV_FILE}"; set +a
fi

MGR="${DOCKER_HOST:-}"
if [ "${MGR}" != "tcp://127.0.0.1:12375" ]; then
  echo "ERROR: refusing to run: DOCKER_HOST is '${MGR}', not the harness manager" >&2
  exit 1
fi
ADV="${E2E_ADVERTISE_ADDR:-eth0}"
MGR_ADDR="${E2E_MANAGER_ADDR:-manager:2377}"
WORKER="${DOCKER_WORKER4_HOST:-tcp://127.0.0.1:12381}"

docker -H "${MGR}" swarm init --advertise-addr "${ADV}"
TOKEN="$(docker -H "${MGR}" swarm join-token -q worker)"
docker -H "${WORKER}" swarm join --token "${TOKEN}" --advertise-addr "${ADV}" "${MGR_ADDR}"
ready=""
for _ in $(seq 1 60); do
  if docker -H "${MGR}" node ls --filter role=worker --format '{{.Hostname}} {{.Status}} {{.Availability}}' 2>/dev/null | grep -qx 'worker4 Ready Active'; then
    ready=1
    break
  fi
  sleep 2
done
if [ -z "${ready}" ]; then
  echo "ERROR: worker4 did not become a Ready, Active worker within 120s" >&2
  exit 1
fi
echo "worker4 joined the harness swarm as a worker (Ready, Active)."

if [ -n "${TF_ACC_CSI_PLUGIN:-}" ] && [ -n "${TF_ACC_CSI_PLUGIN_DIR:-}" ] && [ -d "${TF_ACC_CSI_PLUGIN_DIR}" ]; then
  if docker -H "${MGR}" plugin create "${TF_ACC_CSI_PLUGIN}" "${TF_ACC_CSI_PLUGIN_DIR}" && docker -H "${MGR}" plugin enable "${TF_ACC_CSI_PLUGIN}"; then
    echo "CSI plugin ${TF_ACC_CSI_PLUGIN} enabled on the manager."
  else
    echo "WARNING: CSI plugin ${TF_ACC_CSI_PLUGIN} not enabled; TestAccObjClusterVolume will skip." >&2
  fi
else
  echo "WARNING: TF_ACC_CSI_PLUGIN / TF_ACC_CSI_PLUGIN_DIR not set (harness-up could not build the plugin fixtures); TestAccObjClusterVolume will skip." >&2
fi
