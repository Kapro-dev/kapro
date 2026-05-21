#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${KAPRO_CI_KIND_CLUSTER:-kapro-ci-smoke}"
IMAGE_REPOSITORY="${KAPRO_CI_IMAGE_REPOSITORY:-kapro-operator}"
IMAGE_TAG="${KAPRO_CI_IMAGE_TAG:-ci-smoke}"
CTX="kind-${CLUSTER}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

cleanup() {
  kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
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

main() {
  need docker
  need helm
  need kind
  need kubectl

  cd "${ROOT}"
  cleanup
  trap cleanup EXIT

  echo "building ${IMAGE_REPOSITORY}:${IMAGE_TAG}"
  docker build -t "${IMAGE_REPOSITORY}:${IMAGE_TAG}" -f Dockerfile .

  echo "creating kind cluster ${CLUSTER}"
  kind create cluster --name "${CLUSTER}"
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

  echo "applying quickstart API objects"
  kubectl --context "${CTX}" apply -f examples/quickstart/backend-flux.yaml
  kubectl --context "${CTX}" wait backend/flux --for=condition=Ready --timeout=90s
  kubectl --context "${CTX}" apply -f examples/quickstart/kapro.yaml
  wait_for_count clusters 2
  kubectl --context "${CTX}" apply -f examples/quickstart/promotion.yaml
  wait_for_count promotionruns 1 "kapro.io/promotion=checkout-v1-2-3"
  wait_for_count targets 1 "kapro.io/promotionrun"

  echo "kind install and quickstart smoke passed"
}

main "$@"
