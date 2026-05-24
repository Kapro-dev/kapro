#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="${KAPRO_FLUX_GIT_E2E_TMPDIR:-}"

usage() {
  cat <<EOF
Usage: scripts/flux-git-e2e.sh

Creates a disposable Git repo with common Flux existing-GitOps files, runs
kapro discover flux, applies the generated repo-native mapping, and verifies
the intended Flux/Kustomize/Helm fields changed.

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
    "${repo}/charts/checkout" \
    "${repo}/flux/sources" \
    "${repo}/flux/helmreleases" \
    "${repo}/flux/kustomizations"

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
  values:
    image:
      repository: ghcr.io/example/checkout-payments
      tag: 1.0.0
    containers:
      api:
        image: ghcr.io/example/checkout-payments-api
        tag: 1.0.0
YAML

  cat >"${repo}/flux/kustomizations/web.yaml" <<'YAML'
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: checkout-web
  namespace: flux-system
  labels:
    kapro.io/import: "true"
    service: web
spec:
  interval: 1m
  path: ./apps/web
  sourceRef:
    kind: GitRepository
    name: platform
YAML

  cat >"${repo}/apps/web/kustomization.yaml" <<'YAML'
resources:
  - deploy.yaml
images:
  - name: ghcr.io/example/checkout-web
    newTag: 1.0.0
YAML

  cat >"${repo}/charts/checkout/Chart.yaml" <<'YAML'
apiVersion: v2
name: checkout
version: 1.0.0
appVersion: 1.0.0
YAML

  git -C "${repo}" add .
  git -C "${repo}" commit -m "Initial Flux existing-GitOps fixture"
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

  "${kapro}" discover flux "${repo}" \
    --out "${repo}/kapro-connect" \
    --name checkout-flux \
    --force

  assert_contains "${repo}/kapro-connect/sources/checkout-flux.yaml" "name: api"
  assert_contains "${repo}/kapro-connect/sources/checkout-flux.yaml" "versionField: spec.ref.tag"
  assert_contains "${repo}/kapro-connect/sources/checkout-flux.yaml" "name: worker"
  assert_contains "${repo}/kapro-connect/sources/checkout-flux.yaml" "versionField: spec.chart.spec.version"
  assert_contains "${repo}/kapro-connect/sources/checkout-flux.yaml" "name: payments-image"
  assert_contains "${repo}/kapro-connect/sources/checkout-flux.yaml" "name: web-image"
  assert_contains "${repo}/kapro-connect/sources/checkout-flux.yaml" "name: checkout-chart"
  assert_contains "${repo}/kapro-connect/discovery/flux-discovery.yaml" "skippedObjects:"
  assert_contains "${repo}/kapro-connect/discovery/flux-discovery.yaml" "Flux Kustomization has no canonical version field"

  "${kapro}" source apply \
    --repo "${repo}" \
    --source "${repo}/kapro-connect/sources/checkout-flux.yaml" \
    --set api=v2 \
    --set worker=2.0.0 \
    --set payments=2.0.0 \
    --set payments-image=2.1.0 \
    --set payments-containers-api-tag=2.2.0 \
    --set web-image=2.3.0 \
    --set checkout-chart=2.4.0 \
    --set checkout-app=2.5.0

  assert_contains "${repo}/flux/sources/api-gitrepository.yaml" "tag: v2"
  assert_contains "${repo}/flux/sources/worker-ocirepository.yaml" "tag: 2.0.0"
  assert_contains "${repo}/flux/helmreleases/payments.yaml" "version: 2.0.0"
  assert_contains "${repo}/flux/helmreleases/payments.yaml" "tag: 2.1.0"
  assert_contains "${repo}/flux/helmreleases/payments.yaml" "tag: 2.2.0"
  assert_contains "${repo}/apps/web/kustomization.yaml" "newTag: 2.3.0"
  assert_contains "${repo}/charts/checkout/Chart.yaml" "version: 2.4.0"
  assert_contains "${repo}/charts/checkout/Chart.yaml" "appVersion: 2.5.0"

  git -C "${repo}" add flux apps charts kapro-connect
  git -C "${repo}" commit -m "Promote Flux existing-GitOps fixture"

  echo "Flux Git-native E2E passed: discovery generated GitRepository, OCIRepository, HelmRelease, Kustomize image, and Helm chart mappings."
}

case "${1:-run}" in
  run) run ;;
  -h|--help|help) usage ;;
  *)
    usage >&2
    exit 1
    ;;
esac
