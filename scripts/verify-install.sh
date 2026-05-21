#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${ROOT}/charts/kapro-operator"
BOOTSTRAP_CRDS="${ROOT}/internal/bootstrap/kaprocrds"

usage() {
  cat <<EOF
Usage: scripts/verify-install.sh <render|cluster|kind-demo|argo-e2e|flux-git-e2e|flux-e2e>

Modes:
  render     Validate chart rendering, CRD sync, and kustomize output. Default.
  cluster    Install the Helm chart into the active Kubernetes context and verify rollout.
  kind-demo  Run the local Kind demo through create, approve, status, and cleanup.
  argo-e2e   Run the Kind + real Argo CD brownfield promotion E2E.
  flux-git-e2e Run the Flux brownfield Git-native source apply E2E.
  flux-e2e  Run the Kind + real Flux controller brownfield promotion E2E.

Environment for cluster mode:
  KAPRO_VERIFY_NAMESPACE     Namespace to install into (default: kapro-system)
  KAPRO_VERIFY_RELEASE       Helm release name (default: kapro)
  KAPRO_IMAGE_REPOSITORY     Optional image repository override
  KAPRO_IMAGE_TAG            Optional image tag override
  KAPRO_VERIFY_WEBHOOKS      Enable admission webhooks (default: false)
  KAPRO_VERIFY_CLEANUP       Uninstall the Helm release and namespace after verification (default: false)
EOF
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

check_crd_dir_sync() {
  local target_dir target_label config_list target_list
  target_dir="$1"
  target_label="$2"
  config_list="$(mktemp)"
  target_list="$(mktemp)"
  find "${ROOT}/config/crd/bases" -maxdepth 1 -type f -name '*.yaml' -exec basename {} \; | sort >"${config_list}"
  find "${target_dir}" -maxdepth 1 -type f -name '*.yaml' -exec basename {} \; | sort >"${target_list}"
  if ! diff -u "${config_list}" "${target_list}"; then
    echo "${target_label} CRDs differ from config/crd/bases; run: make sync-crds" >&2
    rm -f "${config_list}" "${target_list}"
    exit 1
  fi
  while IFS= read -r crd; do
    if ! cmp -s "${ROOT}/config/crd/bases/${crd}" "${target_dir}/${crd}"; then
      echo "${target_label} CRD ${crd} differs from config/crd/bases; run: make sync-crds" >&2
      rm -f "${config_list}" "${target_list}"
      exit 1
    fi
  done <"${config_list}"
  rm -f "${config_list}" "${target_list}"
}

check_crd_sync() {
  check_crd_dir_sync "${CHART}/crds" "chart"
  check_crd_dir_sync "${BOOTSTRAP_CRDS}" "bootstrap"
}

render() {
  need helm
  need kubectl

  echo "checking CRDs are synced into the Helm chart"
  check_crd_sync

  echo "running helm lint"
  helm lint "${CHART}"

  echo "rendering Helm chart with CRDs"
  helm template kapro "${CHART}" --namespace kapro-system --include-crds >/tmp/kapro-helm-render.yaml

  echo "rendering kustomize bundle"
  kubectl kustomize "${ROOT}/config/default" >/tmp/kapro-kustomize-render.yaml

  echo "install render verification passed"
}

cluster() {
  need helm
  need kubectl

  local namespace release webhook cleanup
  namespace="${KAPRO_VERIFY_NAMESPACE:-kapro-system}"
  release="${KAPRO_VERIFY_RELEASE:-kapro}"
  webhook="${KAPRO_VERIFY_WEBHOOKS:-false}"
  cleanup="${KAPRO_VERIFY_CLEANUP:-false}"

  local set_args=("--set" "webhook.enabled=${webhook}")
  if [ -n "${KAPRO_IMAGE_REPOSITORY:-}" ]; then
    set_args+=("--set" "image.repository=${KAPRO_IMAGE_REPOSITORY}")
  fi
  if [ -n "${KAPRO_IMAGE_TAG:-}" ]; then
    set_args+=("--set" "image.tag=${KAPRO_IMAGE_TAG}")
  fi

  echo "installing ${release} into namespace ${namespace}"
  helm upgrade --install "${release}" "${CHART}" \
    --namespace "${namespace}" \
    --create-namespace \
    "${set_args[@]}"

  echo "waiting for operator rollout"
  kubectl -n "${namespace}" rollout status "deployment/${release}-kapro-operator" --timeout=180s

  echo "checking installed resources"
  kubectl get crd -o name | grep -q '^customresourcedefinition.apiextensions.k8s.io/.*\.kapro\.io$'
  kubectl -n "${namespace}" get deploy,svc,sa
  kubectl auth can-i get promotionruns.kapro.io \
    --as="system:serviceaccount:${namespace}:${release}-kapro-operator"

  if [ "${cleanup}" = "true" ]; then
    echo "cleaning up ${release} from namespace ${namespace}"
    helm uninstall "${release}" --namespace "${namespace}"
    kubectl delete namespace "${namespace}" --ignore-not-found
  fi

  echo "cluster install verification passed"
}

kind_demo() {
  "${ROOT}/scripts/kind-demo.sh" up
  "${ROOT}/scripts/kind-demo.sh" approve
  "${ROOT}/scripts/kind-demo.sh" fixtures
  "${ROOT}/scripts/kind-demo.sh" status
  "${ROOT}/scripts/kind-demo.sh" down
}

argo_e2e() {
  KAPRO_ARGO_E2E_CLEANUP="${KAPRO_ARGO_E2E_CLEANUP:-true}" "${ROOT}/scripts/argo-e2e.sh" run
}

flux_git_e2e() {
  "${ROOT}/scripts/flux-git-e2e.sh" run
}

flux_e2e() {
  KAPRO_FLUX_E2E_CLEANUP="${KAPRO_FLUX_E2E_CLEANUP:-true}" "${ROOT}/scripts/flux-e2e.sh" run
}

cmd="${1:-render}"
case "${cmd}" in
  render) render ;;
  cluster) cluster ;;
  kind-demo) kind_demo ;;
  argo-e2e) argo_e2e ;;
  flux-git-e2e) flux_git_e2e ;;
  flux-e2e) flux_e2e ;;
  -h|--help|help) usage ;;
  *)
    usage >&2
    exit 1
    ;;
esac
