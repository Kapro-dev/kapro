#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${ROOT}/charts/kapro-operator"
BOOTSTRAP_CRDS="${ROOT}/internal/bootstrap/kaprocrds"

usage() {
  cat <<EOF
Usage: scripts/verify-install.sh <render|release-render|cluster|release-cluster|kind-demo|argo-e2e|flux-git-e2e|flux-e2e>

Modes:
  render          Validate local chart rendering, CRD sync, and kustomize output. Default.
  release-render  Validate rendering for the published GitHub Release chart package.
  cluster         Install the local Helm chart into the active Kubernetes context and verify rollout.
  release-cluster Install the published GitHub Release chart package and verify rollout.
  kind-demo       Run the local Kind demo through create, approve, status, and cleanup.
  argo-e2e        Run the Kind + real Argo CD brownfield promotion E2E.
  flux-git-e2e Run the Flux brownfield Git-native source apply E2E.
  flux-e2e       Run the Kind + real Flux controller brownfield promotion E2E.

Environment for cluster mode:
  KAPRO_VERIFY_NAMESPACE     Namespace to install into (default: kapro-system)
  KAPRO_VERIFY_RELEASE       Helm release name (default: kapro)
  KAPRO_IMAGE_REPOSITORY     Optional image repository override
  KAPRO_IMAGE_TAG            Optional image tag override
  KAPRO_VERIFY_WEBHOOKS      Enable admission webhooks (default: false)
  KAPRO_VERIFY_CLEANUP       Uninstall the Helm release and namespace after verification (default: false)

Environment for release-render and release-cluster modes:
  KAPRO_RELEASE_VERSION       Release tag to verify (default: v0.5.0)
  KAPRO_RELEASE_CHART_URL     Optional chart package URL override
EOF
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

check_crd_dir_sync() (
  # Subshell so the EXIT trap fires on every return path (normal,
  # `exit 1`, OR `set -e` abort on a failed find/diff/cmp) and never
  # leaks across the two invocations from check_crd_sync.
  local target_dir target_label config_list target_list
  target_dir="$1"
  target_label="$2"
  config_list="$(mktemp)"
  target_list="$(mktemp)"
  trap 'rm -f "${config_list}" "${target_list}"' EXIT
  find "${ROOT}/config/crd/bases" -maxdepth 1 -type f -name '*.yaml' -exec basename {} \; | sort >"${config_list}"
  find "${target_dir}" -maxdepth 1 -type f -name '*.yaml' -exec basename {} \; | sort >"${target_list}"
  if ! diff -u "${config_list}" "${target_list}"; then
    echo "${target_label} CRDs differ from config/crd/bases; run: make sync-crds" >&2
    exit 1
  fi
  local mismatched_crd
  mismatched_crd=""
  while IFS= read -r crd; do
    if ! cmp -s "${ROOT}/config/crd/bases/${crd}" "${target_dir}/${crd}"; then
      mismatched_crd="${crd}"
      break
    fi
  done <"${config_list}"
  if [ -n "${mismatched_crd}" ]; then
    echo "${target_label} CRD ${mismatched_crd} differs from config/crd/bases; run: make sync-crds" >&2
    exit 1
  fi
)

check_crd_sync() {
  check_crd_dir_sync "${CHART}/crds" "chart"
  check_crd_dir_sync "${BOOTSTRAP_CRDS}" "bootstrap"
}

expected_kapro_crds() {
  cat <<'EOF'
adapterpolicies.kapro.io
approvals.kapro.io
backends.kapro.io
clusters.kapro.io
clustertemplates.kapro.io
fleets.kapro.io
gateexpressions.kapro.io
plans.kapro.io
plugins.kapro.io
policies.kapro.io
promotionruns.kapro.io
promotions.kapro.io
sources.kapro.io
targets.kapro.io
triggers.kapro.io
EOF
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

download_release_chart() {
  need curl
  local version chart_url tmpdir chart_package
  version="${KAPRO_RELEASE_VERSION:-v0.5.0}"
  chart_url="${KAPRO_RELEASE_CHART_URL:-https://github.com/Kapro-dev/kapro/releases/download/${version}/kapro-operator-${version#v}.tgz}"
  tmpdir="$(mktemp -d)"
  chart_package="${tmpdir}/kapro-operator-${version#v}.tgz"

  echo "downloading published chart ${chart_url}" >&2
  curl -fsSL "${chart_url}" -o "${chart_package}"
  printf '%s\n' "${chart_package}"
}

release_render() (
  need helm
  local chart_package
  chart_package="$(download_release_chart)"
  trap 'rm -rf "$(dirname "${chart_package}")"' EXIT

  echo "running helm lint for ${chart_package}"
  helm lint "${chart_package}"

  echo "rendering published Helm chart with CRDs"
  helm template kapro "${chart_package}" --namespace kapro-system --include-crds >/tmp/kapro-release-helm-render.yaml

  echo "release render verification passed"
)

install_chart() {
  local chart_ref="$1"
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
  helm upgrade --install "${release}" "${chart_ref}" \
    --namespace "${namespace}" \
    --create-namespace \
    "${set_args[@]}"

  echo "waiting for operator rollout"
  kubectl -n "${namespace}" rollout status "deployment/${release}-kapro-operator" --timeout=180s

  echo "checking installed resources"
  local crds
  crds="$(kubectl get crd -o name)"
  local missing_crd
  missing_crd=""
  while IFS= read -r crd; do
    if ! grep -q "^customresourcedefinition.apiextensions.k8s.io/${crd}$" <<<"${crds}"; then
      missing_crd="${crd}"
      break
    fi
  done < <(expected_kapro_crds)
  if [ -n "${missing_crd}" ]; then
    echo "missing required kapro.io CRD: ${missing_crd}" >&2
    return 1
  fi
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

cluster() {
  install_chart "${CHART}"
}

release_cluster() (
  local version chart_package
  version="${KAPRO_RELEASE_VERSION:-v0.5.0}"
  chart_package="$(download_release_chart)"
  trap 'rm -rf "$(dirname "${chart_package}")"' EXIT
  KAPRO_IMAGE_TAG="${KAPRO_IMAGE_TAG:-${version}}" install_chart "${chart_package}"
)

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
  release-render) release_render ;;
  cluster) cluster ;;
  release-cluster) release_cluster ;;
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
