#!/usr/bin/env bash
# Bring up the dind harness (manager + 4 workers, registry:2, Hub mirror, TLS
# daemon), seed the registry and write test/harness/.env. The docker CLI is used here, in the
# test harness only; the provider never shells out to it.
#
# Mirrors harness-up.ps1: generates throwaway TLS material for tlsnode into
# test/harness/tls/ (openssl), installs Terraform ${HARNESS_TF_VERSION:-1.14.9}
# into test/harness/.bin/ once (SHA256-checked) and exports
# TF_ACC_TERRAFORM_VERSION (+ TF_ACC_TERRAFORM_PATH when the install worked),
# and the docker compose release reference/SOURCES.md pins into the same
# .bin (TF_ACC_COMPOSE_CLI, for TestAccComposeParity).
# After the daemons answer it seeds the harness registry with the images in
# test/harness/seed-images.txt (TF_ACC_SEEDED_IMAGES), builds the tfacc plugin
# fixtures (TF_ACC_PLUGIN, TF_ACC_PLUGIN_DIR, TF_ACC_CSI_PLUGIN[_DIR]) and
# probes Docker Hub search (TPD_ACC_INTERNET=1 or TF_ACC_SKIP_INTERNET=1), all
# best effort, exactly like harness-up.ps1.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
COMPOSE_FILE="${HARNESS_DIR}/docker-compose.yml"
ENV_FILE="${HARNESS_DIR}/.env"
TLS_DIR="${HARNESS_DIR}/tls"
BIN_DIR="${HARNESS_DIR}/.bin"
PLUGINS_DIR="${HARNESS_DIR}/.plugins"
SEED_FILE="${HARNESS_DIR}/seed-images.txt"
REPO_ROOT="$(cd "${HARNESS_DIR}/../.." && pwd)"
MGR_HOST="tcp://127.0.0.1:12375"
REGISTRY="127.0.0.1:12380"
REGISTRY_NODES="manager:12380"
TF_VERSION="${HARNESS_TF_VERSION:-1.14.9}"

# compose must talk to the HOST daemon, not a stale harness manager value.
if [ "${DOCKER_HOST:-}" = "tcp://127.0.0.1:12375" ]; then unset DOCKER_HOST; fi

# Paths handed to Go: on Git Bash/MSYS convert /c/x to C:/x (Go on Windows
# does not understand MSYS paths); elsewhere unchanged.
native_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# ---------------------------------------------------------------------------
# TLS material: CA, server cert (SAN tlsnode, localhost, 127.0.0.1), client
# cert. Same file names as harness-up.ps1.
gen_tls() {
  command -v openssl >/dev/null 2>&1 || { echo "ERROR: openssl not on PATH (needed for the tlsnode certificates)" >&2; return 1; }
  mkdir -p "${TLS_DIR}"
  local d="${TLS_DIR}" tmp
  tmp="$(mktemp -d)"
  # Git Bash/MSYS would rewrite "/CN=..." into a Windows path; exclude only
  # those arguments (file paths still need the conversion for a native openssl).
  export MSYS2_ARG_CONV_EXCL="/CN"
  openssl genrsa -out "${d}/ca-key.pem" 2048 2>/dev/null
  # Self-sign through x509 -req -signkey so only these extensions are set
  # (req -x509 would add the config file's basicConstraints a second time).
  openssl req -new -key "${d}/ca-key.pem" -subj "/CN=tfdocker-e2e harness CA" -out "${tmp}/ca.csr"
  printf 'basicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\nsubjectKeyIdentifier=hash\n' > "${tmp}/ca.ext"
  openssl x509 -req -in "${tmp}/ca.csr" -signkey "${d}/ca-key.pem" -days 30 -sha256 -extfile "${tmp}/ca.ext" -out "${d}/ca.pem" 2>/dev/null
  openssl genrsa -out "${d}/server-key.pem" 2048 2>/dev/null
  openssl req -new -key "${d}/server-key.pem" -subj "/CN=tlsnode" -out "${tmp}/server.csr"
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:tlsnode,DNS:localhost,IP:127.0.0.1\n' > "${tmp}/server.ext"
  openssl x509 -req -in "${tmp}/server.csr" -CA "${d}/ca.pem" -CAkey "${d}/ca-key.pem" -CAcreateserial \
    -CAserial "${tmp}/ca.srl" -days 30 -sha256 -extfile "${tmp}/server.ext" -out "${d}/server-cert.pem" 2>/dev/null
  openssl genrsa -out "${d}/key.pem" 2048 2>/dev/null
  openssl req -new -key "${d}/key.pem" -subj "/CN=tfdocker-e2e client" -out "${tmp}/client.csr"
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=clientAuth\n' > "${tmp}/client.ext"
  openssl x509 -req -in "${tmp}/client.csr" -CA "${d}/ca.pem" -CAkey "${d}/ca-key.pem" \
    -CAserial "${tmp}/ca.srl" -days 30 -sha256 -extfile "${tmp}/client.ext" -out "${d}/cert.pem" 2>/dev/null
  rm -f "${d}/ca-key.pem"
  rm -rf "${tmp}"
  unset MSYS2_ARG_CONV_EXCL
  echo "Generated TLS material in ${d} (CA, server cert for tlsnode/localhost/127.0.0.1, client cert)."
}

# ---------------------------------------------------------------------------
# Terraform CLI for terraform-plugin-testing (one download, cached in .bin).
install_terraform() {
  local os arch exe target zip base want got
  case "$(uname -s)" in
    Linux*) os=linux ;; Darwin*) os=darwin ;; MINGW*|MSYS*|CYGWIN*) os=windows ;; *) return 1 ;;
  esac
  case "$(uname -m)" in
    aarch64|arm64) arch=arm64 ;; *) arch=amd64 ;;
  esac
  exe=terraform; [ "${os}" = windows ] && exe=terraform.exe
  target="${BIN_DIR}/terraform_${TF_VERSION}"
  if [ -x "${target}/${exe}" ] || [ -f "${target}/${exe}" ]; then echo "${target}/${exe}"; return 0; fi
  command -v curl >/dev/null 2>&1 && command -v unzip >/dev/null 2>&1 || return 1
  mkdir -p "${target}"
  zip="terraform_${TF_VERSION}_${os}_${arch}.zip"
  base="https://releases.hashicorp.com/terraform/${TF_VERSION}"
  curl -fsSL -o "${target}/${zip}" "${base}/${zip}" || return 1
  want="$(curl -fsSL "${base}/terraform_${TF_VERSION}_SHA256SUMS" | awk -v f="${zip}" '$2==f {print $1}')"
  if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "${target}/${zip}" | awk '{print $1}')"; else got="$(shasum -a 256 "${target}/${zip}" | awk '{print $1}')"; fi
  if [ -z "${want}" ] || [ "${got}" != "${want}" ]; then rm -rf "${target}"; echo "SHA256 mismatch for ${zip}" >&2; return 1; fi
  unzip -o -q "${target}/${zip}" -d "${target}"
  rm -f "${target}/${zip}"
  chmod +x "${target}/${exe}" 2>/dev/null || true
  echo "${target}/${exe}"
}

# docker compose CLI for TestAccComposeParity: the release reference/SOURCES.md pins (the docker/compose row), never
# whatever compose ships with Docker Desktop. One download into .bin, SHA256-checked against checksums.txt.
install_compose() {
  local os arch asset target base want got ver
  ver="${HARNESS_COMPOSE_VERSION:-$(grep -oE 'github\.com/docker/compose/tree/v[0-9]+\.[0-9]+\.[0-9]+/' "${REPO_ROOT}/reference/SOURCES.md" | head -1 | sed -E 's#.*/tree/(v[^/]+)/##')}"
  [ -n "${ver}" ] || return 1
  case "$(uname -s)" in
    Linux*) os=linux ;; Darwin*) os=darwin ;; MINGW*|MSYS*|CYGWIN*) os=windows ;; *) return 1 ;;
  esac
  case "$(uname -m)" in
    aarch64|arm64) arch=aarch64 ;; *) arch=x86_64 ;;
  esac
  asset="docker-compose-${os}-${arch}"; [ "${os}" = windows ] && asset="${asset}.exe"
  target="${BIN_DIR}/compose_${ver}"
  if [ -f "${target}/${asset}" ]; then echo "${target}/${asset}"; return 0; fi
  command -v curl >/dev/null 2>&1 || return 1
  mkdir -p "${target}"
  base="https://github.com/docker/compose/releases/download/${ver}"
  curl -fsSL -o "${target}/${asset}" "${base}/${asset}" || return 1
  want="$(curl -fsSL "${base}/checksums.txt" | awk -v f="${asset}" '$2==f || $2=="*"f {print $1}')"
  if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "${target}/${asset}" | awk '{print $1}')"; else got="$(shasum -a 256 "${target}/${asset}" | awk '{print $1}')"; fi
  if [ -z "${want}" ] || [ "${got}" != "${want}" ]; then rm -rf "${target}"; echo "SHA256 mismatch for ${asset}" >&2; return 1; fi
  chmod +x "${target}/${asset}" 2>/dev/null || true
  echo "${target}/${asset}"
}

# Reuse existing material: a running tlsnode keeps the certs it started with
# (harness-down removes tls/, so a fresh harness always gets new ones).
if [ -f "${TLS_DIR}/ca.pem" ] && [ -f "${TLS_DIR}/cert.pem" ] && [ -f "${TLS_DIR}/key.pem" ] && [ -f "${TLS_DIR}/server-cert.pem" ] && [ -f "${TLS_DIR}/server-key.pem" ]; then
  echo "Reusing TLS material in ${TLS_DIR}."
else
  gen_tls
fi

COMPOSE_CLI=""
if p="$(install_compose)"; then
  COMPOSE_CLI="$(native_path "${p}")"
  echo "docker compose at ${COMPOSE_CLI}"
else
  echo "WARNING: could not install the pinned docker compose; TF_ACC_COMPOSE_CLI is empty and TestAccComposeParity skips." >&2
fi

TF_PATH=""
if p="$(install_terraform)"; then
  TF_PATH="$(native_path "${p}")"
  echo "Terraform ${TF_VERSION} at ${TF_PATH}"
else
  echo "WARNING: could not install Terraform ${TF_VERSION}; exporting TF_ACC_TERRAFORM_VERSION only (terraform-plugin-testing installs it per test)." >&2
fi

# The Docker Hub pull-through cache keeps its data in an external volume so
# it survives harness-down (`compose down -v` leaves external volumes alone).
docker volume inspect tfdocker-e2e-mirror-cache >/dev/null 2>&1 || docker volume create tfdocker-e2e-mirror-cache >/dev/null

docker compose -f "${COMPOSE_FILE}" up -d --wait

# --wait relies on the healthchecks; additionally confirm each published
# endpoint answers from the host side (port publishing can lag).
wait_daemon() {
  local name="$1" i
  shift
  for i in $(seq 1 60); do
    if docker "$@" info >/dev/null 2>&1; then
      echo "${name} ready at $2"
      return 0
    fi
    sleep 2
  done
  echo "ERROR: ${name} did not answer at $2 within 120s" >&2
  docker compose -f "${COMPOSE_FILE}" logs --tail=50 "${name}" >&2 || true
  return 1
}

wait_http() {
  local name="$1" url="$2" i
  for i in $(seq 1 60); do
    if curl -fsS -o /dev/null "${url}" 2>/dev/null; then
      echo "${name} ready at ${url}"
      return 0
    fi
    sleep 2
  done
  echo "ERROR: ${name} did not answer at ${url} within 120s" >&2
  docker compose -f "${COMPOSE_FILE}" logs --tail=50 "${name}" >&2 || true
  return 1
}

TLS_CA="$(native_path "${TLS_DIR}/ca.pem")"
TLS_CERT="$(native_path "${TLS_DIR}/cert.pem")"
TLS_KEY="$(native_path "${TLS_DIR}/key.pem")"

wait_daemon manager -H tcp://127.0.0.1:12375
wait_daemon worker1 -H tcp://127.0.0.1:12376
wait_daemon worker2 -H tcp://127.0.0.1:12377
wait_daemon worker3 -H tcp://127.0.0.1:12379
wait_daemon worker4 -H tcp://127.0.0.1:12381
wait_daemon cdnode -H tcp://127.0.0.1:12383
wait_daemon tlsnode -H tcp://127.0.0.1:12378 --tlsverify --tlscacert "${TLS_CA}" --tlscert "${TLS_CERT}" --tlskey "${TLS_KEY}"
wait_http registry http://127.0.0.1:12380/v2/

# Engine version, API version and image store of the swarm manager and of
# cdnode, from the daemons themselves. TF_ACC_CONTAINERD_HOST is exported
# only when cdnode really runs the containerd image store.
CONTAINERD_HOST=""
for node in manager=tcp://127.0.0.1:12375 cdnode=tcp://127.0.0.1:12383; do
  n="${node%%=*}"; h="${node#*=}"
  ver="$(docker -H "${h}" version --format '{{.Server.Version}} API {{.Server.APIVersion}}' 2>&1 || true)"
  store="$(docker -H "${h}" info --format '{{.Driver}} {{range .DriverStatus}}{{index . 0}}={{index . 1}} {{end}}' 2>&1 || true)"
  echo "${n}: Engine ${ver}, storage ${store}"
  case "${n}:${store}" in
    manager:*io.containerd.snapshotter*)
      echo "WARNING: the manager runs the containerd image store; the overlay2 graph-driver coverage is gone (check docker-compose.yml)." >&2 ;;
    cdnode:*io.containerd.snapshotter*)
      CONTAINERD_HOST="${h}" ;;
    cdnode:*)
      echo "WARNING: cdnode does not run the containerd image store; not exporting TF_ACC_CONTAINERD_HOST (the containerd tests skip)." >&2 ;;
  esac
done
# The self-signed TLS registry: best effort, only its one test needs it.
TLS_REGISTRY=""
for i in $(seq 1 30); do
  if curl -kfsS -o /dev/null https://127.0.0.1:12382/v2/ 2>/dev/null; then
    TLS_REGISTRY="127.0.0.1:12382"
    echo "tlsregistry ready at https://${TLS_REGISTRY}/v2/"
    break
  fi
  sleep 2
done
if [ -z "${TLS_REGISTRY}" ]; then
  echo "WARNING: tlsregistry did not answer over HTTPS; TestAccRegistryImageDataInsecureSkipVerify will skip." >&2
fi

mgr() { docker -H "${MGR_HOST}" "$@"; }

# Seed the harness registry with the Docker Hub images the tests use.
SEEDED=""
total=0
while IFS= read -r ref || [ -n "${ref}" ]; do
  ref="$(printf '%s' "${ref}" | tr -d '\r' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
  case "${ref}" in ''|'#'*) continue ;; esac
  total=$((total + 1))
  copy="${REGISTRY_NODES}/${ref}"
  if out="$(mgr pull --quiet "${ref}" 2>&1 && mgr tag "${ref}" "${copy}" 2>&1 && mgr push --quiet "${copy}" 2>&1)"; then
    SEEDED="${SEEDED:+${SEEDED},}${ref}"
  else
    echo "WARNING: could not seed ${ref} into the harness registry; the tests keep pulling it from Docker Hub. ${out}" >&2
  fi
  # Leave the manager's image store as empty as compose up made it.
  mgr rmi "${copy}" "${ref}" >/dev/null 2>&1 || true
done < "${SEED_FILE}"
echo "Seeded $(printf '%s' "${SEEDED}" | awk -F, '{print (NF ? NF : 0)}')/${total} images into the harness registry as ${REGISTRY_NODES}/<ref>."

# tfacc plugin fixtures: one static linux binary, two plugin directories
# (config.json + rootfs/); the docker CLI uploads the directory to the manager.
build_plugins() {
  local arch goarch kind d
  command -v go >/dev/null 2>&1 || { echo "go is not on PATH" >&2; return 1; }
  arch="$(mgr info --format '{{.Architecture}}')" || return 1
  case "${arch}" in aarch64|arm64) goarch=arm64 ;; *) goarch=amd64 ;; esac
  rm -rf "${PLUGINS_DIR}"
  mkdir -p "${PLUGINS_DIR}"
  (cd "${REPO_ROOT}" && GOOS=linux GOARCH="${goarch}" CGO_ENABLED=0 go build -trimpath -o "${PLUGINS_DIR}/tfaccplugin" ./test/harness/tfaccplugin) || return 1
  for kind in volume csi; do
    for d in run/docker/plugins dev proc sys tmp etc; do mkdir -p "${PLUGINS_DIR}/${kind}/rootfs/${d}"; done
    cp "${PLUGINS_DIR}/tfaccplugin" "${PLUGINS_DIR}/${kind}/rootfs/tfaccplugin"
    chmod 0755 "${PLUGINS_DIR}/${kind}/rootfs/tfaccplugin"
    cp "${HARNESS_DIR}/tfaccplugin/${kind}/config.json" "${PLUGINS_DIR}/${kind}/config.json"
  done
  rm -f "${PLUGINS_DIR}/tfaccplugin"
}
PLUGIN_REF=""
PLUGIN_DIR=""
CSI_PLUGIN_DIR=""
if build_plugins; then
  PLUGIN_DIR="$(native_path "${PLUGINS_DIR}/volume")"
  CSI_PLUGIN_DIR="$(native_path "${PLUGINS_DIR}/csi")"
  ref="${REGISTRY}/tfacc/noop-volume:1"
  if out="$(mgr plugin create "${ref}" "${PLUGINS_DIR}/volume" 2>&1 && mgr plugin push "${ref}" 2>&1)"; then
    PLUGIN_REF="${ref}"
    echo "tfacc plugins built in ${PLUGINS_DIR}; volume plugin pushed as ${ref}."
  else
    echo "WARNING: could not push the tfacc volume plugin: ${out}" >&2
  fi
  mgr plugin rm --force "${ref}" >/dev/null 2>&1 || true
else
  echo "WARNING: tfacc plugin fixtures not built; the plugin tests will skip." >&2
fi

# Docker Hub search goes from the daemon straight to index.docker.io (no
# mirror, no credentials): probe it the way docker_image_search does.
INTERNET=""
if [ -n "$(mgr search --limit 1 --format '{{.Name}}' busybox 2>/dev/null)" ]; then
  INTERNET=1
else
  echo "WARNING: the manager cannot reach Docker Hub search; exporting TF_ACC_SKIP_INTERNET=1 (the Internet tests skip)." >&2
fi

# Same values, same order as harness-up.ps1.
{
  echo "export DOCKER_HOST=\"tcp://127.0.0.1:12375\""
  echo "export DOCKER_WORKER1_HOST=\"tcp://127.0.0.1:12376\""
  echo "export DOCKER_WORKER2_HOST=\"tcp://127.0.0.1:12377\""
  echo "export DOCKER_WORKER3_HOST=\"tcp://127.0.0.1:12379\""
  echo "export DOCKER_WORKER4_HOST=\"tcp://127.0.0.1:12381\""
  if [ -n "${CONTAINERD_HOST}" ]; then echo "export TF_ACC_CONTAINERD_HOST=\"${CONTAINERD_HOST}\""; fi
  echo "export E2E_MANAGER_ADDR=\"manager:2377\""
  echo "export E2E_ADVERTISE_ADDR=\"eth0\""
  echo "export TF_ACC_REGISTRY=\"127.0.0.1:12380\""
  echo "export TF_ACC_REGISTRY_NODES=\"manager:12380\""
  echo "export TF_ACC_TLS_HOST=\"tcp://127.0.0.1:12378\""
  echo "export TF_ACC_TLS_CA=\"${TLS_CA}\""
  echo "export TF_ACC_TLS_CERT=\"${TLS_CERT}\""
  echo "export TF_ACC_TLS_KEY=\"${TLS_KEY}\""
  echo "export TF_ACC_TLS_CERT_PATH=\"$(native_path "${TLS_DIR}")\""
  if [ -n "${TLS_REGISTRY}" ]; then echo "export TF_ACC_TLS_REGISTRY=\"${TLS_REGISTRY}\""; fi
  echo "export TF_ACC_GPU_NODE_LABEL=\"gpu=nvidia\""
  echo "export TF_ACC_HARNESS_COMPOSE=\"$(native_path "${COMPOSE_FILE}")\""
  echo "export TF_ACC_COMPOSE_CLI=\"${COMPOSE_CLI}\""
  echo "export TF_ACC_TERRAFORM_VERSION=\"${TF_VERSION}\""
  if [ -n "${TF_PATH}" ]; then echo "export TF_ACC_TERRAFORM_PATH=\"${TF_PATH}\""; fi
  echo "export TF_ACC_SEEDED_IMAGES=\"${SEEDED}\""
  if [ -n "${PLUGIN_DIR}" ]; then
    echo "export TF_ACC_PLUGIN_DIR=\"${PLUGIN_DIR}\""
    echo "export TF_ACC_CSI_PLUGIN_DIR=\"${CSI_PLUGIN_DIR}\""
    echo "export TF_ACC_CSI_PLUGIN=\"tfacc-csi\""
  fi
  if [ -n "${PLUGIN_REF}" ]; then echo "export TF_ACC_PLUGIN=\"${PLUGIN_REF}\""; fi
  if [ -n "${INTERNET}" ]; then echo "export TPD_ACC_INTERNET=\"1\""; else echo "export TF_ACC_SKIP_INTERNET=1"; fi
} > "${ENV_FILE}"

echo "Wrote ${ENV_FILE}:"
cat "${ENV_FILE}"
