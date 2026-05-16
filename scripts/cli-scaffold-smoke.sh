#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

kapro() {
  go run ./cmd/kapro "$@"
}

require_file() {
  local path="$1"
  if [[ ! -f "${path}" ]]; then
    echo "expected file was not generated: ${path}" >&2
    exit 1
  fi
}

reject_path() {
  local path="$1"
  if [[ -e "${path}" ]]; then
    echo "path should not exist: ${path}" >&2
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

cd "${ROOT}"

echo "smoke: greenfield argo repo-first"
kapro init "${TMPDIR}/repo-first" --backend argo --name checkout --clusters none --force >/dev/null
require_file "${TMPDIR}/repo-first/backends/argo.yaml"
require_file "${TMPDIR}/repo-first/sources/checkout.yaml"
require_file "${TMPDIR}/repo-first/pipelines/checkout.yaml"
require_file "${TMPDIR}/repo-first/argo/applications/checkout.yaml"
reject_path "${TMPDIR}/repo-first/clusters"
reject_path "${TMPDIR}/repo-first/kapro"
reject_path "${TMPDIR}/repo-first/releases"

echo "smoke: greenfield flux pull with cluster inventory"
kapro init "${TMPDIR}/greenfield-flux" --backend flux --name checkout --mode pull --force >/dev/null
require_file "${TMPDIR}/greenfield-flux/backends/flux.yaml"
require_file "${TMPDIR}/greenfield-flux/clusters/canary.yaml"
require_file "${TMPDIR}/greenfield-flux/clusters/prod.yaml"
require_file "${TMPDIR}/greenfield-flux/kapro/checkout.yaml"
require_file "${TMPDIR}/greenfield-flux/releases/checkout-release.yaml"
require_text "${TMPDIR}/greenfield-flux/clusters/canary.yaml" "backendRef: flux"
require_text "${TMPDIR}/greenfield-flux/clusters/canary.yaml" "ociRepository: checkout-bundle"

echo "smoke: brownfield argo connect"
kapro connect argo "${TMPDIR}/connect-argo" --namespace argocd --selector kapro.io/import=true,team=checkout --force >/dev/null
require_file "${TMPDIR}/connect-argo/backends/argo-observe.yaml"
require_text "${TMPDIR}/connect-argo/backends/argo-observe.yaml" "driver: argo"
require_text "${TMPDIR}/connect-argo/backends/argo-observe.yaml" "managementPolicy: Observe"
require_text "${TMPDIR}/connect-argo/backends/argo-observe.yaml" "team: \"checkout\""
require_text "${TMPDIR}/connect-argo/README.md" "switch managementPolicy from Observe to"
require_text "${TMPDIR}/connect-argo/README.md" "Adopt"
require_text "${TMPDIR}/connect-argo/README.md" "does not copy Argo CD or Flux credentials"

echo "smoke: brownfield argo discover"
mkdir -p "${TMPDIR}/argo-repo/argocd/applicationsets" "${TMPDIR}/argo-repo/argocd/environments"
cat >"${TMPDIR}/argo-repo/argocd/applicationsets/checkout.yaml" <<'YAML'
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: checkout
  namespace: argocd
spec:
  generators:
  - matrix:
      generators:
      - git:
          repoURL: https://example.com/platform.git
          revision: main
          files:
          - path: argocd/environments/*.json
      - list:
          elements:
          - appName: checkout-api
  template:
    metadata:
      name: '{{.appName}}-{{.env}}'
      labels:
        kapro.io/import: "true"
    spec:
      source:
        repoURL: '{{.repoUrl}}'
        targetRevision: '{{.gkProjectVersion}}'
        path: apps/checkout
YAML
cat >"${TMPDIR}/argo-repo/argocd/environments/dev.json" <<'JSON'
{"env":"dev","gkProjectVersion":"1.0.0"}
JSON
(cd "${TMPDIR}/argo-repo" && git init >/dev/null && git add .)
kapro discover argo "${TMPDIR}/argo-repo" --out "${TMPDIR}/discover-argo" --name checkout --force >/dev/null
require_file "${TMPDIR}/discover-argo/backends/checkout-observe.yaml"
require_file "${TMPDIR}/discover-argo/sources/checkout.yaml"
require_file "${TMPDIR}/discover-argo/discovery/argo-discovery.yaml"
require_file "${TMPDIR}/discover-argo/discovery/kapro-git-map.yaml"
require_text "${TMPDIR}/discover-argo/sources/checkout.yaml" "backendKind: GitJSONField"
require_text "${TMPDIR}/discover-argo/sources/checkout.yaml" "argocd/environments/*.json:gkProjectVersion"
kapro source apply --repo "${TMPDIR}/argo-repo" --source "${TMPDIR}/discover-argo/sources/checkout.yaml" --set checkout-api=2.0.0 --all >/dev/null
require_text "${TMPDIR}/argo-repo/argocd/environments/dev.json" '"gkProjectVersion": "2.0.0"'

echo "smoke: brownfield flux connect"
kapro connect flux "${TMPDIR}/connect-flux" --namespace flux-system --selector kapro.io/import=true,team=checkout --force >/dev/null
require_file "${TMPDIR}/connect-flux/backends/flux-observe.yaml"
require_text "${TMPDIR}/connect-flux/backends/flux-observe.yaml" "driver: flux"
require_text "${TMPDIR}/connect-flux/backends/flux-observe.yaml" "managementPolicy: Observe"
require_text "${TMPDIR}/connect-flux/backends/flux-observe.yaml" "team: \"checkout\""

echo "cli scaffold smoke passed"
