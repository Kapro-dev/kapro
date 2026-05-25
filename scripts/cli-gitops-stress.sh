#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

COUNT="${KAPRO_CLI_STRESS_COUNT:-1200}"
KAPRO_BIN="${KAPRO_BIN:-${TMPDIR}/kapro}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require_text() {
  local path="$1"
  local text="$2"
  if ! grep -Fq -- "${text}" "${path}"; then
    echo "expected ${path} to contain: ${text}" >&2
    exit 1
  fi
}

require_min_count() {
  local path="$1"
  local text="$2"
  local min="$3"
  local count
  count="$(grep -Fc -- "${text}" "${path}" || true)"
  if (( count < min )); then
    echo "expected at least ${min} occurrences of ${text} in ${path}, got ${count}" >&2
    exit 1
  fi
}

init_repo() {
  local repo="$1"
  git -C "${repo}" init --initial-branch main >/dev/null
  git -C "${repo}" config user.email "kapro-stress@example.com"
  git -C "${repo}" config user.name "Kapro Stress"
  git -C "${repo}" add .
}

write_argo_repo() {
  local repo="$1"
  local i id
  mkdir -p "${repo}/argocd"
  for ((i = 0; i < COUNT; i++)); do
    printf -v id "%05d" "${i}"
    cat >"${repo}/argocd/app-${id}.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: app-${id}
  namespace: argocd
  labels:
    kapro.io/import: "true"
spec:
  source:
    repoURL: https://example.com/app-${id}.git
    targetRevision: 1.0.0
    path: apps/app-${id}
YAML
  done
  init_repo "${repo}"
}

write_flux_repo() {
  local repo="$1"
  local i id app_dir
  for ((i = 0; i < COUNT; i++)); do
    printf -v id "%05d" "${i}"
    app_dir="${repo}/apps/service-${id}"
    mkdir -p "${app_dir}"
    cat >"${app_dir}/kustomization.yaml" <<YAML
resources:
  - deployment.yaml
images:
  - name: ghcr.io/example/service-${id}
    newTag: 1.0.0
YAML
  done
  init_repo "${repo}"
}

run() {
  need git
  need go

  if [[ ! "${COUNT}" =~ ^[0-9]+$ ]] || (( COUNT < 100 )); then
    echo "KAPRO_CLI_STRESS_COUNT must be an integer >= 100" >&2
    exit 1
  fi

  cd "${ROOT}"
  if [[ ! -x "${KAPRO_BIN}" ]]; then
    echo "building kapro CLI stress binary"
    go build -trimpath -o "${KAPRO_BIN}" ./cmd/kapro
  fi

  echo "stress: Argo discovery with ${COUNT} Applications"
  local argo_repo="${TMPDIR}/argo-repo"
  local argo_out="${TMPDIR}/argo-out"
  mkdir -p "${argo_repo}"
  write_argo_repo "${argo_repo}"
  "${KAPRO_BIN}" discover argo "${argo_repo}" \
    --out "${argo_out}" \
    --name argo-stress \
    --max-files 0 \
    --max-units 0 \
    --force >/dev/null
  require_min_count "${argo_out}/deliveryunits/argo-stress.yaml" "substrateKind: ArgoApplicationSource" "${COUNT}"
  require_text "${argo_out}/discovery/argo-discovery.yaml" "promotionUnits: ${COUNT}"
  require_text "${argo_out}/discovery/review-summary.yaml" "selectedUnits: ${COUNT}"

  if "${KAPRO_BIN}" discover argo "${argo_repo}" \
    --out "${TMPDIR}/argo-limit-out" \
    --name argo-limit \
    --max-files 10 \
    --max-units 0 \
    --force >/dev/null 2>"${TMPDIR}/argo-limit.err"; then
    echo "expected Argo discovery to reject --max-files=10" >&2
    exit 1
  fi
  require_text "${TMPDIR}/argo-limit.err" "discovery candidate file limit exceeded"

  echo "stress: Flux discovery with ${COUNT} Kustomize image mappings"
  local flux_repo="${TMPDIR}/flux-repo"
  local flux_out="${TMPDIR}/flux-out"
  mkdir -p "${flux_repo}"
  write_flux_repo "${flux_repo}"
  "${KAPRO_BIN}" discover flux "${flux_repo}" \
    --out "${flux_out}" \
    --name flux-stress \
    --max-files 0 \
    --max-units 0 \
    --force >/dev/null
  require_min_count "${flux_out}/deliveryunits/flux-stress.yaml" "substrateKind: KustomizeImage" "${COUNT}"
  require_text "${flux_out}/discovery/flux-discovery.yaml" "promotionUnits: ${COUNT}"
  require_text "${flux_out}/discovery/review-summary.yaml" "selectedUnits: ${COUNT}"

  echo "stress: Flux discovery cache reuse"
  "${KAPRO_BIN}" discover flux "${flux_repo}" \
    --out "${flux_out}" \
    --name flux-stress \
    --max-files 0 \
    --max-units 0 \
    --force >/dev/null
  require_text "${flux_out}/discovery/flux-discovery.yaml" "hits: ${COUNT}"

  echo "CLI GitOps stress passed"
}

case "${1:-run}" in
  run) run ;;
  -h|--help|help)
    cat <<EOF
Usage: scripts/cli-gitops-stress.sh

Generates large disposable Argo and Flux GitOps repositories, runs Kapro
discovery/import-scale paths, verifies max-file guardrails, and checks cache
reuse. Set KAPRO_CLI_STRESS_COUNT to tune fixture size (default: 1200).
EOF
    ;;
  *)
    echo "unknown command: $1" >&2
    exit 1
    ;;
esac
