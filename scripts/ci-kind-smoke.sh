#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${KAPRO_CI_KIND_CLUSTER:-kapro-ci-smoke}"
KIND_IMAGE="${KAPRO_CI_KIND_IMAGE:-kindest/node:v1.30.0}"
IMAGE_REPOSITORY="${KAPRO_CI_IMAGE_REPOSITORY:-kapro-operator}"
IMAGE_TAG="${KAPRO_CI_IMAGE_TAG:-ci-smoke}"
CTX="kind-${CLUSTER}"
REFRESH_PIDS=()

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

cleanup() {
  local status="${1:-0}"
  local pid
  for pid in "${REFRESH_PIDS[@]:-}"; do
    kill "${pid}" >/dev/null 2>&1 || true
  done
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
    clusters.kapro.io \
    clustertemplates.kapro.io \
    decisiontraces.runtime.kapro.io \
    fleets.kapro.io \
    plans.kapro.io \
    plugins.kapro.io \
    policies.kapro.io \
    promotions.kapro.io \
    promotionruns.runtime.kapro.io \
    sources.kapro.io \
    substrateclasses.kapro.io \
    substrates.kapro.io \
    targets.runtime.kapro.io \
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
  kubectl --context "${CTX}" get promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io,clusters.kapro.io -o wide || true
  kubectl --context "${CTX}" -n kapro-system logs deploy/kapro-kapro-operator --tail=120 || true
  exit 1
}

wait_for_clusters() {
  local cluster
  for cluster in "$@"; do
    for _ in $(seq 1 90); do
      if kubectl --context "${CTX}" get "cluster/${cluster}" >/dev/null 2>&1; then
        break
      fi
      sleep 2
    done
    if ! kubectl --context "${CTX}" get "cluster/${cluster}" >/dev/null 2>&1; then
      echo "timed out waiting for cluster/${cluster}" >&2
      kubectl --context "${CTX}" get clusters -o wide || true
      kubectl --context "${CTX}" -n kapro-system logs deploy/kapro-kapro-operator --tail=120 || true
      exit 1
    fi
  done
}

wait_for_substrate_ready() {
  local name="$1"
  kubectl --context "${CTX}" wait "substrate/${name}" \
    --for=condition=Ready \
    --timeout=120s
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
  local clusters=("$@")
  (
    while true; do
      local cluster
      for cluster in "${clusters[@]}"; do
        mark_cluster_converged "${cluster}" >/dev/null 2>&1 || true
      done
      sleep 30
    done
  ) &
  REFRESH_PIDS+=("$!")
}

install_fake_argo_applications() {
  kubectl --context "${CTX}" apply -f - <<'EOF'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: applications.argoproj.io
spec:
  group: argoproj.io
  names:
    kind: Application
    plural: applications
    singular: application
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
      subresources:
        status: {}
EOF
  kubectl --context "${CTX}" wait crd/applications.argoproj.io --for=condition=Established --timeout=60s
  kubectl --context "${CTX}" create namespace argocd --dry-run=client -o yaml | kubectl --context "${CTX}" apply -f -
  local app
  for app in checkout-argo-canary checkout-argo-production; do
    kubectl --context "${CTX}" -n argocd apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${app}
  labels:
    app.kubernetes.io/name: checkout
spec:
  source:
    repoURL: https://github.com/example/platform-config
    path: apps/checkout
    targetRevision: old
  destination:
    server: https://kubernetes.default.svc
    namespace: checkout
EOF
    kubectl --context "${CTX}" -n argocd patch "application/${app}" --subresource=status --type=merge -p '{
      "status": {
        "sync": {"status": "Synced"},
        "health": {"status": "Healthy"}
      }
    }'
  done
}

wait_for_quickstart() {
  local promotion="$1"
  local target_count="$2"
  wait_for_count promotionruns 1 "kapro.io/promotion=${promotion}"
  local run
  run="$(kubectl --context "${CTX}" get promotionrun -l "kapro.io/promotion=${promotion}" -o jsonpath='{.items[0].metadata.name}')"
  wait_for_count targets "${target_count}" "kapro.io/promotionrun=${run}"
  kubectl --context "${CTX}" wait "promotionrun/${run}" \
    --for=jsonpath='{.status.phase}'=Complete \
    --timeout=600s
  kubectl --context "${CTX}" wait target -l "kapro.io/promotionrun=${run}" \
    --for=jsonpath='{.status.phase}'=Converged \
    --timeout=600s
}

run_flux_quickstart() {
  echo "running Flux quickstart"
  kubectl --context "${CTX}" apply -f examples/quickstart/substrates/flux.yaml
  wait_for_substrate_ready flux
  kubectl --context "${CTX}" apply -f examples/quickstart/kapro.yaml
  wait_for_clusters checkout-canary-eu checkout-production-eu
  mark_cluster_converged checkout-canary-eu
  mark_cluster_converged checkout-production-eu
  start_cluster_convergence_refresher checkout-canary-eu checkout-production-eu
  kubectl --context "${CTX}" apply -f examples/quickstart/promotion.yaml
  wait_for_quickstart checkout-v1-2-3 2
}

run_argo_quickstart() {
  echo "running Argo CD quickstart"
  install_fake_argo_applications
  kubectl --context "${CTX}" apply -f examples/quickstart-argo/substrates/argo.yaml
  wait_for_substrate_ready argo
  kubectl --context "${CTX}" apply -f examples/quickstart-argo/fleet.yaml
  wait_for_clusters checkout-argo-canary checkout-argo-production
  mark_cluster_converged checkout-argo-canary
  mark_cluster_converged checkout-argo-production
  start_cluster_convergence_refresher checkout-argo-canary checkout-argo-production
  kubectl --context "${CTX}" apply -f examples/quickstart-argo/promotion.yaml
  wait_for_quickstart checkout-argo-v1-2-3 2
}

run_oci_quickstart() {
  echo "running OCI quickstart"
  kubectl --context "${CTX}" apply -f examples/quickstart-oci/substrates/oci.yaml
  wait_for_substrate_ready oci
  kubectl --context "${CTX}" apply -f examples/quickstart-oci/fleet.yaml
  wait_for_clusters checkout-oci-canary checkout-oci-production
  mark_cluster_converged checkout-oci-canary
  mark_cluster_converged checkout-oci-production
  start_cluster_convergence_refresher checkout-oci-canary checkout-oci-production
  kubectl --context "${CTX}" apply -f examples/quickstart-oci/promotion.yaml
  wait_for_quickstart checkout-oci-v1-2-3 2
}

run_configured_quickstarts() {
  local quickstarts="${KAPRO_CI_QUICKSTARTS:-flux}"
  local quickstart
  IFS=',' read -r -a selected <<<"${quickstarts}"
  for quickstart in "${selected[@]}"; do
    case "${quickstart}" in
      flux) run_flux_quickstart ;;
      argo) run_argo_quickstart ;;
      oci) run_oci_quickstart ;;
      "") ;;
      *)
        echo "unknown quickstart ${quickstart}; expected flux, argo, or oci" >&2
        exit 1
        ;;
    esac
  done
}

run_upgrade_smoke() {
  echo "running Helm upgrade smoke with PR image"
  KAPRO_IMAGE_REPOSITORY="${IMAGE_REPOSITORY}" \
    KAPRO_IMAGE_TAG="${IMAGE_TAG}" \
    KAPRO_VERIFY_CLEANUP=false \
    KAPRO_VERIFY_WEBHOOKS=true \
    "${ROOT}/scripts/verify-install.sh" cluster
  wait_for_crds
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
  run_upgrade_smoke

  echo "running configured quickstarts: ${KAPRO_CI_QUICKSTARTS:-flux}"
  run_configured_quickstarts

  echo "kind install and quickstart smoke passed"
}

if [ "${KAPRO_CI_KIND_SMOKE_INNER:-}" != "1" ] && command -v timeout >/dev/null 2>&1; then
  export KAPRO_CI_KIND_SMOKE_INNER=1
  exec timeout 20m "$0" "$@"
fi

main "$@"
