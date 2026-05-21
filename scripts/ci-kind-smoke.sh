#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${KAPRO_CI_KIND_CLUSTER:-kapro-ci-smoke}"
KIND_IMAGE="${KAPRO_CI_KIND_IMAGE:-kindest/node:v1.30.0}"
IMAGE_REPOSITORY="${KAPRO_CI_IMAGE_REPOSITORY:-kapro-operator}"
IMAGE_TAG="${KAPRO_CI_IMAGE_TAG:-ci-smoke}"
CTX="kind-${CLUSTER}"
REFRESH_PID=""

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

cleanup() {
  local status="${1:-0}"
  if [ -n "${REFRESH_PID}" ]; then
    kill "${REFRESH_PID}" >/dev/null 2>&1 || true
  fi
  if [ "${status}" != "0" ] && [ "${KAPRO_CI_KEEP_CLUSTER_ON_FAILURE:-false}" = "true" ]; then
    echo "keeping kind cluster ${CLUSTER} for failure diagnostics"
    return
  fi
  kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
}

wait_for_crds() {
  local crd
  for crd in \
    approvals.kapro.io \
    backends.kapro.io \
    clusters.kapro.io \
    clustertemplates.kapro.io \
    fleets.kapro.io \
    plans.kapro.io \
    plugins.kapro.io \
    policies.kapro.io \
    promotions.kapro.io \
    promotionruns.kapro.io \
    sources.kapro.io \
    targets.kapro.io \
    triggers.kapro.io; do
    kubectl --context "${CTX}" wait "crd/${crd}" --for=condition=Established --timeout=60s
  done
}

wait_for_count() {
  local resource="$1"
  local min_count="$2"
  local label_selector="${3:-}"
  local args=("${resource}")
  if [ -n "${label_selector}" ]; then
    args+=("-l" "${label_selector}")
  fi
  for _ in $(seq 1 90); do
    local out=""
    # Capture stdout separately so a kubectl failure (CRD discovery lag,
    # transient API error) under `set -euo pipefail` doesn't abort the
    # whole script — we just treat it as count=0 and keep polling.
    out="$(kubectl --context "${CTX}" get "${args[@]}" --no-headers 2>/dev/null)" || out=""
    local count=0
    if [ -n "${out}" ]; then
      count="$(printf '%s\n' "${out}" | wc -l | tr -d ' ')"
    fi
    if [ "${count}" -ge "${min_count}" ]; then
      return
    fi
    sleep 2
  done
  echo "timed out waiting for at least ${min_count} ${resource}" >&2
  kubectl --context "${CTX}" get promotions,promotionruns,targets,clusters -o wide || true
  kubectl --context "${CTX}" -n kapro-system logs deploy/kapro-kapro-operator --tail=120 || true
  exit 1
}

mark_cluster_converged() {
  local name="$1"
  local now
  now="$(date -u +"%Y-%m-%dT%H:%M:%S.000000Z")"

  kubectl --context "${CTX}" -n kapro-system apply -f - <<EOF
apiVersion: coordination.k8s.io/v1
kind: Lease
metadata:
  name: kapro-heartbeat-${name}
  namespace: kapro-system
spec:
  holderIdentity: kapro-ci-kind-smoke
  renewTime: "${now}"
EOF

  kubectl --context "${CTX}" patch "cluster/${name}" --subresource=status --type=merge -p "$(cat <<EOF
{
  "status": {
    "phase": "Converged",
    "version": "v1.2.3",
    "currentVersions": {
      "default": "v1.2.3",
      "checkout": "v1.2.3"
    },
    "health": {
      "allWorkloadsReady": true,
      "readyWorkloads": 1,
      "totalWorkloads": 1,
      "message": "ci-kind-smoke synthetic spoke convergence"
    },
    "conditions": [
      {
        "type": "Ready",
        "status": "True",
        "observedGeneration": 1,
        "lastTransitionTime": "${now}",
        "reason": "HeartbeatFresh",
        "message": "ci-kind-smoke synthetic heartbeat"
      }
    ],
    "heartbeat": {
      "observedAt": "${now}",
      "leaseObservedAt": "${now}",
      "lastTransitionAt": "${now}",
      "consecutiveMisses": 0,
      "reason": "HeartbeatFresh"
    }
  }
}
EOF
)"
}

start_cluster_convergence_refresher() {
  (
    while true; do
      mark_cluster_converged checkout-canary-eu >/dev/null 2>&1 || true
      mark_cluster_converged checkout-production-eu >/dev/null 2>&1 || true
      sleep 30
    done
  ) &
  REFRESH_PID="$!"
}

main() {
  need docker
  need helm
  need kind
  need kubectl

  cd "${ROOT}"
  cleanup
  trap 'cleanup "$?"' EXIT

  echo "building ${IMAGE_REPOSITORY}:${IMAGE_TAG}"
  docker build -t "${IMAGE_REPOSITORY}:${IMAGE_TAG}" -f Dockerfile .

  echo "creating kind cluster ${CLUSTER} with ${KIND_IMAGE}"
  kind create cluster --name "${CLUSTER}" --image "${KIND_IMAGE}"
  kubectl config use-context "${CTX}"

  echo "loading image into kind"
  kind load docker-image "${IMAGE_REPOSITORY}:${IMAGE_TAG}" --name "${CLUSTER}"

  echo "installing local Helm chart with PR image"
  # Exercise the chart's default webhook-on install path so CI catches
  # regressions in the validating webhook handshake. Without this the
  # smoke installs with webhook.enabled=false (verify-install.sh default)
  # and misses chart-default regressions.
  KAPRO_IMAGE_REPOSITORY="${IMAGE_REPOSITORY}" \
    KAPRO_IMAGE_TAG="${IMAGE_TAG}" \
    KAPRO_VERIFY_CLEANUP=false \
    KAPRO_VERIFY_WEBHOOKS=true \
    "${ROOT}/scripts/verify-install.sh" cluster
  wait_for_crds

  echo "applying quickstart API objects"
  kubectl --context "${CTX}" apply -f examples/quickstart/backend-flux.yaml
  kubectl --context "${CTX}" wait backend/flux --for=condition=Ready --timeout=90s
  kubectl --context "${CTX}" apply -f examples/quickstart/kapro.yaml
  wait_for_count clusters 2
  mark_cluster_converged checkout-canary-eu
  mark_cluster_converged checkout-production-eu
  start_cluster_convergence_refresher
  kubectl --context "${CTX}" apply -f examples/quickstart/promotion.yaml
  wait_for_count promotionruns 1 "kapro.io/promotion=checkout-v1-2-3"
  wait_for_count targets 2 "kapro.io/promotionrun"
  kubectl --context "${CTX}" wait promotionrun -l kapro.io/promotion=checkout-v1-2-3 \
    --for=jsonpath='{.status.phase}'=Complete \
    --timeout=600s
  kubectl --context "${CTX}" wait target -l kapro.io/promotionrun \
    --for=jsonpath='{.status.phase}'=Converged \
    --timeout=600s

  echo "kind install and quickstart smoke passed"
}

if [ "${KAPRO_CI_KIND_SMOKE_INNER:-}" != "1" ] && command -v timeout >/dev/null 2>&1; then
  export KAPRO_CI_KIND_SMOKE_INNER=1
  exec timeout 20m "$0" "$@"
fi

main "$@"
