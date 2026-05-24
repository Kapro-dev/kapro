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
kapro init "${TMPDIR}/repo-first" --substrate argo --name checkout --clusters none --force >/dev/null
require_file "${TMPDIR}/repo-first/substrates/argo.yaml"
require_file "${TMPDIR}/repo-first/sources/checkout.yaml"
require_file "${TMPDIR}/repo-first/plans/checkout.yaml"
require_file "${TMPDIR}/repo-first/argo/applications/checkout.yaml"
reject_path "${TMPDIR}/repo-first/clusters"
reject_path "${TMPDIR}/repo-first/fleets"
reject_path "${TMPDIR}/repo-first/promotionruns"
reject_path "${TMPDIR}/repo-first/promotions"

echo "smoke: greenfield flux pull with cluster inventory"
kapro init "${TMPDIR}/greenfield-flux" --substrate flux --name checkout --mode pull --force >/dev/null
require_file "${TMPDIR}/greenfield-flux/substrates/flux.yaml"
require_file "${TMPDIR}/greenfield-flux/clusters/canary-eu.yaml"
require_file "${TMPDIR}/greenfield-flux/clusters/prod-eu.yaml"
require_file "${TMPDIR}/greenfield-flux/fleets/checkout.yaml"
require_file "${TMPDIR}/greenfield-flux/promotions/checkout-promotion.yaml"
require_text "${TMPDIR}/greenfield-flux/clusters/canary-eu.yaml" "substrateRef: flux"
require_text "${TMPDIR}/greenfield-flux/clusters/canary-eu.yaml" "ociRepository: checkout-bundle"
require_text "${TMPDIR}/greenfield-flux/clusters/canary-eu.yaml" "kapro.io/stage: canary"
require_text "${TMPDIR}/greenfield-flux/clusters/prod-eu.yaml" "kapro.io/stage: production"
require_text "${TMPDIR}/greenfield-flux/promotions/checkout-promotion.yaml" "kind: Promotion"
require_text "${TMPDIR}/greenfield-flux/promotions/checkout-promotion.yaml" "fleetRef: checkout"
require_text "${TMPDIR}/greenfield-flux/promotions/checkout-promotion.yaml" "timeout: 30m"

echo "smoke: guided bootstrap greenfield defaults"
kapro bootstrap greenfield "${TMPDIR}/bootstrap-greenfield" --name checkout --force >/dev/null
require_file "${TMPDIR}/bootstrap-greenfield/substrates/direct.yaml"
require_file "${TMPDIR}/bootstrap-greenfield/apps/checkout/deployment.yaml"
require_file "${TMPDIR}/bootstrap-greenfield/fleets/checkout.yaml"
require_file "${TMPDIR}/bootstrap-greenfield/promotions/checkout-promotion.yaml"
require_text "${TMPDIR}/bootstrap-greenfield/clusters/canary-eu.yaml" "mode: push"
require_text "${TMPDIR}/bootstrap-greenfield/clusters/canary-eu.yaml" "substrateRef: direct"

echo "smoke: bootstrap generate direct profile"
kapro bootstrap generate "${TMPDIR}/generate-direct" --profile direct --name checkout --force >/dev/null
require_file "${TMPDIR}/generate-direct/substrates/direct.yaml"
require_file "${TMPDIR}/generate-direct/apps/checkout/deployment.yaml"
require_file "${TMPDIR}/generate-direct/fleets/checkout.yaml"
require_text "${TMPDIR}/generate-direct/substrates/direct.yaml" "kind: KubernetesApplyConfig"
require_text "${TMPDIR}/generate-direct/substrates/direct.yaml" "name: kubernetes-apply"
require_text "${TMPDIR}/generate-direct/clusters/canary-eu.yaml" "substrateRef: direct"
require_text "${TMPDIR}/generate-direct/fleets/checkout.yaml" "substrateKind: KubernetesManifest"

echo "smoke: bootstrap generate Argo CD profile"
kapro bootstrap generate "${TMPDIR}/generate-argo" --profile argo --name checkout --force >/dev/null
require_file "${TMPDIR}/generate-argo/substrates/argo.yaml"
require_file "${TMPDIR}/generate-argo/argo/applications/checkout.yaml"
require_file "${TMPDIR}/generate-argo/apps/checkout/deployment.yaml"
require_file "${TMPDIR}/generate-argo/apps/checkout/service.yaml"
require_text "${TMPDIR}/generate-argo/substrates/argo.yaml" "kind: ArgoCDSubstrateConfig"
require_text "${TMPDIR}/generate-argo/argo/applications/checkout.yaml" "name: checkout-canary-eu"
require_text "${TMPDIR}/generate-argo/argo/applications/checkout.yaml" "name: checkout-prod-eu"
require_text "${TMPDIR}/generate-argo/argo/applications/checkout.yaml" "kapro.io/managed-by: kapro"
require_text "${TMPDIR}/generate-argo/clusters/canary-eu.yaml" "application: checkout-canary-eu"

echo "smoke: bootstrap generate Flux profile"
kapro bootstrap generate "${TMPDIR}/generate-flux" --profile flux --name checkout --force >/dev/null
require_file "${TMPDIR}/generate-flux/substrates/flux.yaml"
require_file "${TMPDIR}/generate-flux/apps/checkout/deployment.yaml"
require_file "${TMPDIR}/generate-flux/apps/checkout/kustomization.yaml"
require_file "${TMPDIR}/generate-flux/flux/kustomizations/checkout.yaml"
require_text "${TMPDIR}/generate-flux/substrates/flux.yaml" "kind: FluxSubstrateConfig"
require_text "${TMPDIR}/generate-flux/substrates/flux.yaml" "name: flux"
require_text "${TMPDIR}/generate-flux/flux/kustomizations/checkout.yaml" "kind: GitRepository"
require_text "${TMPDIR}/generate-flux/clusters/canary-eu.yaml" "substrateRef: flux"

echo "smoke: bootstrap generate OCI profile"
kapro bootstrap generate "${TMPDIR}/generate-oci" --profile oci --name checkout --force >/dev/null
require_file "${TMPDIR}/generate-oci/substrates/oci.yaml"
require_file "${TMPDIR}/generate-oci/fleets/checkout.yaml"
require_file "${TMPDIR}/generate-oci/promotions/checkout-promotion.yaml"
require_text "${TMPDIR}/generate-oci/substrates/oci.yaml" "kind: OCIBundleApplyConfig"
require_text "${TMPDIR}/generate-oci/substrates/oci.yaml" "mode: spoke-pull"
require_text "${TMPDIR}/generate-oci/clusters/canary-eu.yaml" "substrateRef: oci"

echo "smoke: public quickstart direct default"
kapro quickstart "${TMPDIR}/quickstart-direct" --name checkout --force >/dev/null
require_file "${TMPDIR}/quickstart-direct/substrates/direct.yaml"
require_file "${TMPDIR}/quickstart-direct/apps/checkout/deployment.yaml"
require_text "${TMPDIR}/quickstart-direct/substrates/direct.yaml" "kind: KubernetesApplyConfig"
require_text "${TMPDIR}/quickstart-direct/clusters/canary-eu.yaml" "mode: push"
require_text "${TMPDIR}/quickstart-direct/clusters/canary-eu.yaml" "substrateRef: direct"

echo "smoke: existing Argo CD connect"
kapro connect argo "${TMPDIR}/connect-argo" --namespace argocd --selector kapro.io/import=true,team=checkout --force >/dev/null
require_file "${TMPDIR}/connect-argo/substrates/argo-observe.yaml"
require_text "${TMPDIR}/connect-argo/substrates/argo-observe.yaml" "kind: argo"
require_text "${TMPDIR}/connect-argo/substrates/argo-observe.yaml" "mode: hub-push"
require_text "${TMPDIR}/connect-argo/substrates/argo-observe.yaml" "managementPolicy: Observe"
require_text "${TMPDIR}/connect-argo/substrates/argo-observe.yaml" "team: \"checkout\""
require_text "${TMPDIR}/connect-argo/README.md" "switch managementPolicy from Observe to"
require_text "${TMPDIR}/connect-argo/README.md" "Adopt"
require_text "${TMPDIR}/connect-argo/README.md" "does not copy Argo CD or Flux credentials"

echo "smoke: existing Argo CD discover"
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
require_file "${TMPDIR}/discover-argo/substrates/checkout-observe.yaml"
require_file "${TMPDIR}/discover-argo/sources/checkout.yaml"
require_file "${TMPDIR}/discover-argo/discovery/argo-discovery.yaml"
require_file "${TMPDIR}/discover-argo/discovery/kapro-git-map.yaml"
require_text "${TMPDIR}/discover-argo/sources/checkout.yaml" "substrateKind: GitJSONField"
require_text "${TMPDIR}/discover-argo/sources/checkout.yaml" "argocd/environments/*.json:gkProjectVersion"
kapro adopt argo "${TMPDIR}/argo-repo" --out "${TMPDIR}/adopt-argo" --name checkout --force >/dev/null
require_file "${TMPDIR}/adopt-argo/discovery/kapro-git-map.yaml"
kapro source apply --repo "${TMPDIR}/argo-repo" --source "${TMPDIR}/discover-argo/sources/checkout.yaml" --set checkout-api=2.0.0 --all >/dev/null
require_text "${TMPDIR}/argo-repo/argocd/environments/dev.json" '"gkProjectVersion": "2.0.0"'

echo "smoke: existing Argo CD adoption alias"
kapro adopt argo "${TMPDIR}/argo-repo" --out "${TMPDIR}/bootstrap-argo" --name checkout --force >/dev/null
require_file "${TMPDIR}/bootstrap-argo/substrates/checkout-observe.yaml"
require_file "${TMPDIR}/bootstrap-argo/sources/checkout.yaml"
require_file "${TMPDIR}/bootstrap-argo/discovery/kapro-git-map.yaml"

echo "smoke: existing Flux connect"
kapro connect flux "${TMPDIR}/connect-flux" --namespace flux-system --selector kapro.io/import=true,team=checkout --force >/dev/null
require_file "${TMPDIR}/connect-flux/substrates/flux-observe.yaml"
require_text "${TMPDIR}/connect-flux/substrates/flux-observe.yaml" "kind: flux"
require_text "${TMPDIR}/connect-flux/substrates/flux-observe.yaml" "mode: hub-push"
require_text "${TMPDIR}/connect-flux/substrates/flux-observe.yaml" "managementPolicy: Observe"
require_text "${TMPDIR}/connect-flux/substrates/flux-observe.yaml" "team: \"checkout\""

echo "smoke: existing Flux adoption"
mkdir -p "${TMPDIR}/flux-repo/flux/sources" "${TMPDIR}/flux-repo/flux/kustomizations" "${TMPDIR}/flux-repo/apps/web"
cat >"${TMPDIR}/flux-repo/flux/sources/api-gitrepository.yaml" <<'YAML'
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
cat >"${TMPDIR}/flux-repo/flux/kustomizations/web.yaml" <<'YAML'
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: checkout-web
  namespace: flux-system
  labels:
    kapro.io/import: "true"
spec:
  interval: 1m
  path: ./apps/web
  sourceRef:
    kind: GitRepository
    name: checkout-api
YAML
cat >"${TMPDIR}/flux-repo/apps/web/kustomization.yaml" <<'YAML'
images:
  - name: ghcr.io/example/checkout-web
    newTag: v1
YAML
(cd "${TMPDIR}/flux-repo" && git init >/dev/null && git add .)
kapro adopt flux "${TMPDIR}/flux-repo" --out "${TMPDIR}/adopt-flux" --name checkout --force >/dev/null
require_file "${TMPDIR}/adopt-flux/substrates/checkout-observe.yaml"
require_file "${TMPDIR}/adopt-flux/sources/checkout.yaml"
require_file "${TMPDIR}/adopt-flux/discovery/kapro-git-map.yaml"
require_text "${TMPDIR}/adopt-flux/sources/checkout.yaml" "sourcePath: flux/sources/api-gitrepository.yaml"
require_text "${TMPDIR}/adopt-flux/sources/checkout.yaml" "substrateKind: GitYAMLField"
require_text "${TMPDIR}/adopt-flux/sources/checkout.yaml" "substrateKind: KustomizeImage"

echo "cli scaffold smoke passed"
