#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KAPRO_KIND_CLUSTER:-kapro-kind-demo}"
CTX="kind-${CLUSTER_NAME}"
KUBECTL=(kubectl --context "${CTX}")

usage() {
  cat <<EOF
Usage: scripts/kind-demo.sh <up|approve|fixtures|status|down>

Commands:
  up       Create the Kind cluster, install Kapro, apply demo config, and start the rollout.
  approve   Apply the production Approval objects so the rollout can finish.
  fixtures  Re-patch fake Flux and Cluster statuses after a manual reset.
  status    Print the key demo resources.
  down      Delete the Kind cluster.

Environment:
  KAPRO_KIND_CLUSTER  Kind cluster name (default: kapro-kind-demo)
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

create_cluster() {
  need kind
  need kubectl
  need docker
  need make

  if kind_cluster_exists; then
    echo "kind cluster ${CLUSTER_NAME} already exists"
  else
    kind create cluster --name "${CLUSTER_NAME}" --config "${ROOT}/examples/kind-demo/kind-cluster.yaml"
  fi
}

build_and_load_operator() {
  echo "building Kapro operator image"
  local tmpdir
  tmpdir="$(mktemp -d)"

  CGO_ENABLED=0 GOOS=linux GOARCH="${KAPRO_KIND_GOARCH:-$(go env GOARCH)}" \
    go build -trimpath -ldflags="-s -w" \
    -o "${tmpdir}/kapro-operator" \
    "${ROOT}/cmd/operator"

  cat >"${tmpdir}/Dockerfile" <<'EOF'
FROM gcr.io/distroless/static:nonroot
COPY kapro-operator /kapro-operator
USER 65532:65532
ENTRYPOINT ["/kapro-operator"]
EOF

  docker build -t kapro-operator:dev "${tmpdir}"
  kind load docker-image kapro-operator:dev --name "${CLUSTER_NAME}"
  rm -rf "${tmpdir}"
}

install_kapro() {
  echo "installing Kapro CRDs and demo fixture CRDs"
  "${KUBECTL[@]}" apply -f "${ROOT}/config/crd/bases"
  "${KUBECTL[@]}" apply -f "${ROOT}/internal/bootstrap/crds/resourcesets-crd.yaml"
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/crds"

  echo "installing Kapro operator"
  "${KUBECTL[@]}" apply -k "${ROOT}/examples/kind-demo/operator"
  "${KUBECTL[@]}" -n kapro-system rollout status deployment/kapro-operator --timeout=120s
}

apply_demo_objects() {
  echo "applying fake Flux fixtures"
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/fixtures"
  patch_fixture_status

  echo "applying Kapro demo config"
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/config/00-plugins.yaml"
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/config/01-clusters.yaml"
  patch_cluster_status checkout-canary
  patch_cluster_status checkout-prod-eu
  patch_cluster_status checkout-prod-us
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/config/02-plan.yaml"
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/config/03-promotion-trigger.yaml"
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/config/04-promotionrun.yaml"
}

patch_cluster_status() {
  local cluster="$1"
  local now
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  "${KUBECTL[@]}" patch cluster "${cluster}" \
    --subresource=status \
    --type=merge \
    -p "{\"status\":{\"phase\":\"Converged\",\"version\":\"v1.2.2-kind\",\"currentVersions\":{\"default\":\"v1.2.2-kind\"},\"deliverySystem\":\"flux\",\"lastHeartbeat\":\"${now}\",\"health\":{\"allWorkloadsReady\":true,\"readyWorkloads\":1,\"totalWorkloads\":1},\"conditions\":[{\"type\":\"Ready\",\"status\":\"True\",\"reason\":\"FixtureReady\",\"message\":\"Kind demo fixture reports ready\",\"lastTransitionTime\":\"${now}\"}]}}"
}

patch_fixture_status() {
  local now
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  "${KUBECTL[@]}" -n flux-system patch resourceset checkout-demo \
    --subresource=status \
    --type=merge \
    -p "{\"status\":{\"conditions\":[{\"type\":\"Ready\",\"status\":\"True\",\"reason\":\"FixtureReady\",\"message\":\"Kind demo ResourceSet fixture is ready\",\"lastTransitionTime\":\"${now}\"}],\"inventory\":{\"entries\":[{\"id\":\"flux-system_checkout-canary_helm.toolkit.fluxcd.io_HelmRelease\",\"v\":\"v2\"},{\"id\":\"flux-system_checkout-prod-eu_helm.toolkit.fluxcd.io_HelmRelease\",\"v\":\"v2\"},{\"id\":\"flux-system_checkout-prod-us_helm.toolkit.fluxcd.io_HelmRelease\",\"v\":\"v2\"}]}}}"

  for hr in checkout-canary checkout-prod-eu checkout-prod-us; do
    "${KUBECTL[@]}" -n flux-system patch helmrelease "${hr}" \
      --subresource=status \
      --type=merge \
      -p "{\"status\":{\"conditions\":[{\"type\":\"Ready\",\"status\":\"True\",\"reason\":\"FixtureReady\",\"message\":\"Kind demo HelmRelease fixture is ready\",\"lastTransitionTime\":\"${now}\"}]}}"
  done
}

wait_for_targets() {
  echo "waiting for Targets to be created"
  for _ in $(seq 1 60); do
    if "${KUBECTL[@]}" get targets >/dev/null 2>&1; then
      if [ "$("${KUBECTL[@]}" get targets --no-headers 2>/dev/null | wc -l | tr -d ' ')" != "0" ]; then
        return
      fi
    fi
    sleep 2
  done
  echo "timed out waiting for Targets; use scripts/kind-demo.sh status for details" >&2
}

approve() {
  "${KUBECTL[@]}" apply -f "${ROOT}/examples/kind-demo/approvals"
  echo "approvals applied"
}

fixtures() {
  patch_fixture_status
  patch_cluster_status checkout-canary
  patch_cluster_status checkout-prod-eu
  patch_cluster_status checkout-prod-us
  echo "fixture statuses patched"
}

status() {
  echo
  echo "Kapro operator"
  "${KUBECTL[@]}" -n kapro-system get pods
  echo
  echo "Trigger"
  "${KUBECTL[@]}" get triggers checkout-kind-trigger -o wide || true
  echo
  echo "PromotionRun"
  "${KUBECTL[@]}" get promotionruns checkout-kind -o wide || true
  echo
  echo "Targets"
  "${KUBECTL[@]}" get targets -o wide || true
  echo
  echo "Clusters"
  "${KUBECTL[@]}" get clusters -o wide || true
  echo
  echo "ResourceSet inputs"
  "${KUBECTL[@]}" -n flux-system get resourceset checkout-demo -o jsonpath='{.spec.inputs}' || true
  echo
  echo
}

up() {
  create_cluster
  build_and_load_operator
  install_kapro
  apply_demo_objects
  wait_for_targets
  status
  cat <<EOF
Demo is running.

The canary target should converge first. The prod stage uses maxParallel=1 and
manual approvals, so it will stop in WaitingApproval.

Next:
  scripts/kind-demo.sh approve
  scripts/kind-demo.sh status

Cleanup:
  scripts/kind-demo.sh down
EOF
}

down() {
  need kind
  kind delete cluster --name "${CLUSTER_NAME}"
}

cmd="${1:-}"
case "${cmd}" in
  up) up ;;
  approve) approve ;;
  fixtures) fixtures ;;
  status) status ;;
  down) down ;;
  -h|--help|help|"") usage ;;
  *)
    usage >&2
    exit 1
    ;;
esac
