#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KAPRO_FLUX_E2E_CLUSTER:-kapro-flux-e2e}"
CTX="kind-${CLUSTER_NAME}"
KUBECTL=(kubectl --context "${CTX}")
FLUX_NAMESPACE="${KAPRO_FLUX_E2E_NAMESPACE:-flux-system}"
FLUX_INSTALL_URL="${KAPRO_FLUX_E2E_INSTALL_URL:-https://github.com/fluxcd/flux2/releases/latest/download/install.yaml}"
GIT_IMAGE="${KAPRO_FLUX_E2E_GIT_IMAGE:-kapro-flux-e2e-git:dev}"
GIT_PORT="${KAPRO_FLUX_E2E_GIT_PORT:-9419}"
TMPDIR="${KAPRO_FLUX_E2E_TMPDIR:-}"
PF_PID=""

usage() {
  cat <<EOF
Usage: scripts/flux-e2e.sh <run|up|status|down>

Commands:
  run     Create everything, verify discover/source-apply/Flux reconcile, then leave the cluster running.
  up      Alias for run.
  status  Print Flux resources from the current E2E cluster.
  down    Delete the Kind cluster.

Environment:
  KAPRO_FLUX_E2E_CLUSTER       Kind cluster name (default: kapro-flux-e2e)
  KAPRO_FLUX_E2E_INSTALL_URL   Flux install manifest URL
  KAPRO_FLUX_E2E_GIT_IMAGE     Git daemon image (default: locally built kapro-flux-e2e-git:dev)
  KAPRO_FLUX_E2E_GIT_PORT      Local port for Git daemon port-forward (default: 9419)
  KAPRO_FLUX_E2E_REUSE_CLUSTER Reuse an existing same-name Kind cluster when true
  KAPRO_FLUX_E2E_CLEANUP       Delete the cluster after a successful run when true
EOF
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

kind_cluster_exists() {
  kind get clusters | grep -qx "${CLUSTER_NAME}"
}

cleanup_port_forward() {
  if [ -n "${PF_PID}" ]; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
  fi
}

trap cleanup_port_forward EXIT

create_cluster() {
  need kind
  need kubectl
  need docker
  need git
  need go
  need ssh-keygen

  if kind_cluster_exists; then
    if [ "${KAPRO_FLUX_E2E_REUSE_CLUSTER:-false}" = "true" ]; then
      echo "kind cluster ${CLUSTER_NAME} already exists; reusing it"
      return
    fi
    echo "kind cluster ${CLUSTER_NAME} already exists; deleting it for a clean E2E run"
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
  kind create cluster --name "${CLUSTER_NAME}"
}

build_kapro_cli() {
  mkdir -p "${TMPDIR}/bin"
  go build -trimpath -o "${TMPDIR}/bin/kapro" "${ROOT}/cmd/kapro"
}

build_and_load_git_server_image() {
  if [ -n "${KAPRO_FLUX_E2E_GIT_IMAGE:-}" ]; then
    echo "using caller-provided Git server image ${GIT_IMAGE}"
    return
  fi

  echo "building Git daemon image"
  local build_dir
  build_dir="$(mktemp -d)"
  cat >"${build_dir}/Dockerfile" <<'DOCKERFILE'
FROM alpine:3.20
RUN apk add --no-cache git git-daemon openssh-server
DOCKERFILE

  docker build -t "${GIT_IMAGE}" "${build_dir}"
  kind load docker-image "${GIT_IMAGE}" --name "${CLUSTER_NAME}"
  rm -rf "${build_dir}"
}

install_flux() {
  echo "installing Flux controllers"
  "${KUBECTL[@]}" apply -f "${FLUX_INSTALL_URL}"
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" rollout status deployment/source-controller --timeout=300s
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" rollout status deployment/kustomize-controller --timeout=300s
}

write_fixture_repo() {
  local repo="$1"
  local repo_url="ssh://git@kapro-flux-e2e-git.${FLUX_NAMESPACE}.svc.cluster.local/git/repo.git"
  mkdir -p \
    "${repo}/flux/sources" \
    "${repo}/flux/kustomizations" \
    "${repo}/workloads/app"

  git -C "${repo}" init --initial-branch main
  git -C "${repo}" config user.email "kapro-flux-e2e@example.com"
  git -C "${repo}" config user.name "Kapro Flux E2E"

  cat >"${repo}/flux/kustomization.yaml" <<'YAML'
resources:
  - sources/checkout.yaml
  - kustomizations/checkout.yaml
YAML

  cat >"${repo}/flux/sources/checkout.yaml" <<YAML
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: checkout
  namespace: ${FLUX_NAMESPACE}
  labels:
    kapro.io/import: "true"
    kapro.io/unit: checkout
spec:
  interval: 1s
  url: ${repo_url}
  secretRef:
    name: kapro-flux-e2e-ssh
  ref:
    tag: v1
YAML

  cat >"${repo}/flux/kustomizations/checkout.yaml" <<YAML
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: checkout
  namespace: ${FLUX_NAMESPACE}
  labels:
    kapro.io/import: "true"
    kapro.io/unit: checkout
spec:
  interval: 1s
  path: ./workloads/app
  prune: true
  wait: true
  timeout: 2m
  targetNamespace: default
  sourceRef:
    kind: GitRepository
    name: checkout
YAML

  cat >"${repo}/workloads/app/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: checkout-version
data:
  version: v1
YAML

  git -C "${repo}" add .
  git -C "${repo}" commit -m "Initial Flux e2e fixture"
  git -C "${repo}" tag v1

  cat >"${repo}/workloads/app/configmap.yaml" <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: checkout-version
data:
  version: v2
YAML

  git -C "${repo}" add workloads
  git -C "${repo}" commit -m "Add v2 workload manifest"
  git -C "${repo}" tag v2
}

install_git_server() {
  local repo="$1"
  local bare="${TMPDIR}/repo.git"
  local tarball="${TMPDIR}/repo.git.tar.gz"
  local identity="${TMPDIR}/flux-e2e-identity"
  local host_key="${TMPDIR}/flux-e2e-host"
  local host_key_pub="${host_key}.pub"
  local known_hosts="${TMPDIR}/known_hosts"
  git clone --bare "${repo}" "${bare}"
  tar -czf "${tarball}" -C "${TMPDIR}" repo.git
  ssh-keygen -t ed25519 -N "" -f "${identity}" >/dev/null
  ssh-keygen -t ed25519 -N "" -f "${host_key}" >/dev/null
  ssh-keygen -y -f "${host_key}" >"${host_key_pub}"
  printf 'kapro-flux-e2e-git.%s.svc.cluster.local %s\n' "${FLUX_NAMESPACE}" "$(cat "${host_key_pub}")" >"${known_hosts}"

  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" create configmap kapro-flux-e2e-git-repo \
    --from-file=repo.git.tar.gz="${tarball}" \
    --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" create secret generic kapro-flux-e2e-ssh \
    --from-file=identity="${identity}" \
    --from-file=identity.pub="${identity}.pub" \
    --from-file=known_hosts="${known_hosts}" \
    --from-file=ssh_host_ed25519_key="${host_key}" \
    --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

  cat <<YAML | "${KUBECTL[@]}" apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kapro-flux-e2e-git
  namespace: ${FLUX_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kapro-flux-e2e-git
  template:
    metadata:
      labels:
        app: kapro-flux-e2e-git
    spec:
      initContainers:
        - name: seed
          image: ${GIT_IMAGE}
          command: ["/bin/sh", "-c"]
          args:
            - rm -rf /git/repo.git && tar -xzf /seed/repo.git.tar.gz -C /git && chown -R 1000:1000 /git/repo.git
          volumeMounts:
            - name: seed
              mountPath: /seed
            - name: git
              mountPath: /git
      containers:
        - name: git
          image: ${GIT_IMAGE}
          command: ["/bin/sh", "-c"]
          args:
            - adduser -D -u 1000 -h /home/git git || true;
              passwd -d git >/dev/null 2>&1 || true;
              mkdir -p /home/git/.ssh /run/sshd;
              cp /ssh/identity.pub /home/git/.ssh/authorized_keys;
              cp /ssh/ssh_host_ed25519_key /etc/ssh/ssh_host_ed25519_key;
              chmod 700 /home/git/.ssh;
              chmod 600 /home/git/.ssh/authorized_keys /etc/ssh/ssh_host_ed25519_key;
              chown -R git:git /home/git /git/repo.git;
              git config --global --add safe.directory '*';
              git daemon --verbose --export-all --enable=receive-pack --base-path=/git --reuseaddr /git &
              exec /usr/sbin/sshd -D -e -o PasswordAuthentication=no -o PermitRootLogin=no -o HostKey=/etc/ssh/ssh_host_ed25519_key
          ports:
            - containerPort: 9418
            - containerPort: 22
          volumeMounts:
            - name: git
              mountPath: /git
            - name: ssh
              mountPath: /ssh
              readOnly: true
      volumes:
        - name: seed
          configMap:
            name: kapro-flux-e2e-git-repo
        - name: git
          emptyDir: {}
        - name: ssh
          secret:
            secretName: kapro-flux-e2e-ssh
---
apiVersion: v1
kind: Service
metadata:
  name: kapro-flux-e2e-git
  namespace: ${FLUX_NAMESPACE}
spec:
  selector:
    app: kapro-flux-e2e-git
  ports:
    - name: git
      port: 9418
      targetPort: 9418
    - name: ssh
      port: 22
      targetPort: 22
YAML

  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" rollout status deployment/kapro-flux-e2e-git --timeout=180s
}

start_git_port_forward() {
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" port-forward svc/kapro-flux-e2e-git "${GIT_PORT}:9418" \
    >"${TMPDIR}/git-port-forward.log" 2>&1 &
  PF_PID="$!"
  for _ in $(seq 1 60); do
    if git ls-remote "git://127.0.0.1:${GIT_PORT}/repo.git" >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  cat "${TMPDIR}/git-port-forward.log" >&2 || true
  echo "timed out waiting for git port-forward on 127.0.0.1:${GIT_PORT}" >&2
  exit 1
}

apply_flux_bootstrap() {
  local repo_url="ssh://git@kapro-flux-e2e-git.${FLUX_NAMESPACE}.svc.cluster.local/git/repo.git"
  echo "applying Flux bootstrap source and root Kustomization"
  cat <<YAML | "${KUBECTL[@]}" apply -f -
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: platform-main
  namespace: ${FLUX_NAMESPACE}
spec:
  interval: 1s
  url: ${repo_url}
  secretRef:
    name: kapro-flux-e2e-ssh
  ref:
    branch: main
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform-root
  namespace: ${FLUX_NAMESPACE}
spec:
  interval: 1s
  path: ./flux
  prune: true
  wait: true
  timeout: 2m
  sourceRef:
    kind: GitRepository
    name: platform-main
YAML
}

wait_for_config_version() {
  local expected="$1"
  for _ in $(seq 1 180); do
    local got
    got="$("${KUBECTL[@]}" get configmap checkout-version -o jsonpath='{.data.version}' 2>/dev/null || true)"
    if [ "${got}" = "${expected}" ]; then
      return
    fi
    sleep 1
  done
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" get gitrepositories,kustomizations -o wide || true
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" describe gitrepository checkout || true
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" describe kustomization checkout || true
  echo "timed out waiting for checkout-version ConfigMap data.version=${expected}" >&2
  exit 1
}

discover_and_promote() {
  local repo="$1"
  local out="${TMPDIR}/kapro-connect"
  echo "running kapro import flux against fixture repo"
  "${TMPDIR}/bin/kapro" import flux "${repo}" --out "${out}" --name flux-e2e --force

  "${TMPDIR}/bin/kapro" source apply \
    --repo "${repo}" \
    --source "${out}/sources/flux-e2e.yaml" \
    --set checkout=v2

  git -C "${repo}" remote add e2e "git://127.0.0.1:${GIT_PORT}/repo.git"
  git -C "${repo}" add flux
  git -C "${repo}" commit -m "Promote Flux e2e source to v2"
  git -C "${repo}" push e2e main

  local now
  now="$(date +%s)"
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" annotate gitrepository platform-main reconcile.fluxcd.io/requestedAt="${now}" --overwrite
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" annotate kustomization platform-root reconcile.fluxcd.io/requestedAt="${now}" --overwrite
}

run() {
  if [ -z "${TMPDIR}" ]; then
    TMPDIR="$(mktemp -d)"
  else
    mkdir -p "${TMPDIR}"
  fi
  echo "using temp dir ${TMPDIR}"

  create_cluster
  build_kapro_cli
  build_and_load_git_server_image
  install_flux

  local repo="${TMPDIR}/repo"
  mkdir -p "${repo}"
  write_fixture_repo "${repo}"
  install_git_server "${repo}"
  start_git_port_forward
  apply_flux_bootstrap
  wait_for_config_version v1
  discover_and_promote "${repo}"
  wait_for_config_version v2

  echo "Flux E2E passed: kapro import flux generated a mapping, source apply updated Git, and real Flux controllers reconciled v2."

  if [ "${KAPRO_FLUX_E2E_CLEANUP:-false}" = "true" ]; then
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
}

status() {
  "${KUBECTL[@]}" -n "${FLUX_NAMESPACE}" get gitrepositories,kustomizations -o wide
  "${KUBECTL[@]}" get configmap checkout-version -o yaml || true
}

down() {
  need kind
  kind delete cluster --name "${CLUSTER_NAME}"
}

case "${1:-run}" in
  run|up) run ;;
  status) status ;;
  down) down ;;
  -h|--help|help) usage ;;
  *)
    usage >&2
    exit 1
    ;;
esac
