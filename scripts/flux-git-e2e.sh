#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="${KAPRO_FLUX_GIT_E2E_TMPDIR:-}"

usage() {
  cat <<EOF
Usage: scripts/flux-git-e2e.sh

Creates a disposable Git repo with common Flux brownfield files, runs
kapro source apply against the repo-native mapping, and verifies the intended
Flux/Kustomize fields changed.

Environment:
  KAPRO_FLUX_GIT_E2E_TMPDIR  Optional temp directory to reuse for debugging.
EOF
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

write_fixture_repo() {
  local repo="$1"
  mkdir -p \
    "${repo}/apps/web" \
    "${repo}/flux/sources" \
    "${repo}/flux/helmreleases" \
    "${repo}/kapro/sources"

  git -C "${repo}" init --initial-branch main
  git -C "${repo}" config user.email "kapro-flux-e2e@example.com"
  git -C "${repo}" config user.name "Kapro Flux E2E"

  cat >"${repo}/flux/sources/api-gitrepository.yaml" <<'YAML'
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: checkout-api
  namespace: flux-system
  labels:
    kapro.io/import: "true"
    service: api
spec:
  interval: 1m
  url: https://github.com/example/checkout-api.git
  ref:
    tag: v1
YAML

  cat >"${repo}/flux/sources/worker-ocirepository.yaml" <<'YAML'
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: checkout-worker
  namespace: flux-system
  labels:
    kapro.io/import: "true"
    service: worker
spec:
  interval: 1m
  url: oci://ghcr.io/example/checkout-worker
  ref:
    tag: 1.0.0
YAML

  cat >"${repo}/flux/helmreleases/payments.yaml" <<'YAML'
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: checkout-payments
  namespace: flux-system
  labels:
    kapro.io/import: "true"
    service: payments
spec:
  interval: 1m
  chart:
    spec:
      chart: checkout-payments
      version: 1.0.0
      sourceRef:
        kind: GitRepository
        name: checkout-charts
        namespace: flux-system
YAML

  cat >"${repo}/apps/web/kustomization.yaml" <<'YAML'
resources:
  - deploy.yaml
images:
  - name: ghcr.io/example/checkout-web
    newTag: 1.0.0
YAML

  cat >"${repo}/kapro/sources/flux.yaml" <<'YAML'
apiVersion: kapro.io/v1alpha1
kind: PromotionSource
metadata:
  name: checkout-flux
spec:
  backendRef: flux
  units:
    - name: api-git
      backendKind: GitYAMLField
      sourcePath: flux/sources/api-gitrepository.yaml
      versionField: spec.ref.tag
    - name: worker-oci
      backendKind: GitYAMLField
      sourcePath: flux/sources/worker-ocirepository.yaml
      versionField: spec.ref.tag
    - name: payments-chart
      backendKind: GitYAMLField
      sourcePath: flux/helmreleases/payments.yaml
      versionField: spec.chart.spec.version
    - name: web-image
      backendKind: KustomizeImage
      sourcePath: apps/web/kustomization.yaml
      versionField: ghcr.io/example/checkout-web
YAML

  git -C "${repo}" add .
  git -C "${repo}" commit -m "Initial Flux brownfield fixture"
}

assert_contains() {
  local path="$1"
  local expected="$2"
  if ! grep -Fq "${expected}" "${path}"; then
    echo "expected ${path} to contain: ${expected}" >&2
    echo "--- ${path}" >&2
    cat "${path}" >&2
    exit 1
  fi
}

run() {
  need git
  need go

  if [ -z "${WORKDIR}" ]; then
    WORKDIR="$(mktemp -d)"
  else
    mkdir -p "${WORKDIR}"
  fi
  echo "using temp dir ${WORKDIR}"

  local kapro="${WORKDIR}/kapro"
  go build -trimpath -o "${kapro}" "${ROOT}/cmd/kapro"

  local repo="${WORKDIR}/repo"
  mkdir -p "${repo}"
  write_fixture_repo "${repo}"

  "${kapro}" source apply \
    --repo "${repo}" \
    --source "${repo}/kapro/sources/flux.yaml" \
    --set api-git=v2 \
    --set worker-oci=2.0.0 \
    --set payments-chart=2.0.0 \
    --set web-image=2.0.0

  assert_contains "${repo}/flux/sources/api-gitrepository.yaml" "tag: v2"
  assert_contains "${repo}/flux/sources/worker-ocirepository.yaml" "tag: 2.0.0"
  assert_contains "${repo}/flux/helmreleases/payments.yaml" "version: 2.0.0"
  assert_contains "${repo}/apps/web/kustomization.yaml" "newTag: 2.0.0"

  git -C "${repo}" add flux apps
  git -C "${repo}" commit -m "Promote Flux brownfield fixture"

  echo "Flux Git-native E2E passed: GitRepository, OCIRepository, HelmRelease, and Kustomize image mappings updated."
}

case "${1:-run}" in
  run) run ;;
  -h|--help|help) usage ;;
  *)
    usage >&2
    exit 1
    ;;
esac
