#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${KAPRO_ARGO_E2E_CLUSTER:-kapro-argo-e2e}"
CTX="kind-${CLUSTER_NAME}"
KUBECTL=(kubectl --context "${CTX}")
ARGO_NAMESPACE="${KAPRO_ARGO_E2E_ARGO_NAMESPACE:-argocd}"
ARGO_INSTALL_URL="${KAPRO_ARGO_E2E_ARGO_INSTALL_URL:-https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml}"
ARGO_REDIS_IMAGE="${KAPRO_ARGO_E2E_ARGO_REDIS_IMAGE:-kapro-argo-e2e-redis:dev}"
GIT_IMAGE="${KAPRO_ARGO_E2E_GIT_IMAGE:-kapro-argo-e2e-git:dev}"
GIT_PORT="${KAPRO_ARGO_E2E_GIT_PORT:-9418}"
TMPDIR="${KAPRO_ARGO_E2E_TMPDIR:-}"
PF_PID=""

usage() {
  cat <<EOF
Usage: scripts/argo-e2e.sh <run|up|status|down>

Commands:
  run     Create everything, verify discover/adopt/promote/converge, then leave the cluster running.
  up      Alias for run.
  status  Print Kapro and Argo resources from the current E2E cluster.
  down    Delete the Kind cluster.

Environment:
  KAPRO_ARGO_E2E_CLUSTER          Kind cluster name (default: kapro-argo-e2e)
  KAPRO_ARGO_E2E_ARGO_INSTALL_URL Argo CD install manifest URL
  KAPRO_ARGO_E2E_ARGO_REDIS_IMAGE Redis image loaded into Kind for Argo CD
  KAPRO_ARGO_E2E_GIT_IMAGE        Git daemon image (default: locally built kapro-argo-e2e-git:dev)
  KAPRO_ARGO_E2E_GIT_PORT         Local port for Git daemon port-forward (default: 9418)
  KAPRO_ARGO_E2E_REUSE_CLUSTER    Reuse an existing same-name Kind cluster when true
  KAPRO_ARGO_E2E_CLEANUP          Delete the cluster after a successful run when true
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

cleanup_port_forward() {
  if [ -n "${PF_PID}" ]; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
  fi
}

trap cleanup_port_forward EXIT

create_cluster() {
  need kind
  need kubectl
  need docker
  need git
  need go

  if kind_cluster_exists; then
    if [ "${KAPRO_ARGO_E2E_REUSE_CLUSTER:-false}" = "true" ]; then
      echo "kind cluster ${CLUSTER_NAME} already exists; reusing it"
      return
    fi
    echo "kind cluster ${CLUSTER_NAME} already exists; deleting it for a clean E2E run"
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
  kind create cluster --name "${CLUSTER_NAME}"
}

build_and_load_operator() {
  echo "building Kapro operator image"
  local build_dir
  build_dir="$(mktemp -d)"

  CGO_ENABLED=0 GOOS=linux GOARCH="${KAPRO_KIND_GOARCH:-$(go env GOARCH)}" \
    go build -trimpath -ldflags="-s -w" \
    -o "${build_dir}/kapro-operator" \
    "${ROOT}/cmd/operator"

  cat >"${build_dir}/Dockerfile" <<'DOCKERFILE'
FROM gcr.io/distroless/static:nonroot
COPY kapro-operator /kapro-operator
USER 65532:65532
ENTRYPOINT ["/kapro-operator"]
DOCKERFILE

  docker build -t kapro-operator:argo-e2e "${build_dir}"
  kind load docker-image kapro-operator:argo-e2e --name "${CLUSTER_NAME}"
  rm -rf "${build_dir}"
}

build_and_load_git_server_image() {
  if [ -n "${KAPRO_ARGO_E2E_GIT_IMAGE:-}" ]; then
    echo "using caller-provided Git server image ${GIT_IMAGE}"
    return
  fi

  echo "building Git daemon image"
  local build_dir
  build_dir="$(mktemp -d)"
  cat >"${build_dir}/Dockerfile" <<'DOCKERFILE'
FROM alpine:3.20
RUN apk add --no-cache git git-daemon
DOCKERFILE

  docker build -t "${GIT_IMAGE}" "${build_dir}"
  kind load docker-image "${GIT_IMAGE}" --name "${CLUSTER_NAME}"
  rm -rf "${build_dir}"
}

build_and_load_argo_redis_image() {
  if [ -n "${KAPRO_ARGO_E2E_ARGO_REDIS_IMAGE:-}" ]; then
    echo "loading caller-provided Argo Redis image ${ARGO_REDIS_IMAGE}"
    docker pull "${ARGO_REDIS_IMAGE}"
    kind load docker-image "${ARGO_REDIS_IMAGE}" --name "${CLUSTER_NAME}"
    return
  fi

  echo "building Argo Redis image"
  local build_dir
  build_dir="$(mktemp -d)"
  cat >"${build_dir}/Dockerfile" <<'DOCKERFILE'
FROM alpine:3.20
RUN apk add --no-cache redis
ENTRYPOINT ["redis-server"]
DOCKERFILE

  docker build -t "${ARGO_REDIS_IMAGE}" "${build_dir}"
  kind load docker-image "${ARGO_REDIS_IMAGE}" --name "${CLUSTER_NAME}"
  rm -rf "${build_dir}"
}

build_kapro_cli() {
  mkdir -p "${TMPDIR}/bin"
  go build -trimpath -o "${TMPDIR}/bin/kapro" "${ROOT}/cmd/kapro"
}

install_kapro() {
  echo "installing Kapro CRDs and operator"
  "${KUBECTL[@]}" apply -f "${ROOT}/config/crd/bases"
  "${KUBECTL[@]}" apply -k "${ROOT}/examples/kind-demo/operator"
  "${KUBECTL[@]}" -n kapro-system set image deployment/kapro-operator manager=kapro-operator:argo-e2e
  "${KUBECTL[@]}" -n kapro-system set env deployment/kapro-operator \
    KAPRO_DEV_MODE=1 \
    KAPRO_CONTROLLERS=promotionrun,backend,approval,plugin
  "${KUBECTL[@]}" -n kapro-system rollout status deployment/kapro-operator --timeout=180s
}

install_argo() {
  echo "installing Argo CD"
  "${KUBECTL[@]}" create namespace "${ARGO_NAMESPACE}" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  "${KUBECTL[@]}" apply --server-side --force-conflicts -n "${ARGO_NAMESPACE}" -f "${ARGO_INSTALL_URL}"
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" set image deployment/argocd-redis redis="${ARGO_REDIS_IMAGE}"
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" patch deployment argocd-redis --type=json \
    -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" rollout status deployment/argocd-redis --timeout=300s
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" rollout status deployment/argocd-repo-server --timeout=300s
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" rollout status deployment/argocd-applicationset-controller --timeout=300s
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" rollout status statefulset/argocd-application-controller --timeout=300s
}

grant_kapro_argo_adopt_rbac() {
  echo "granting Kapro scoped Argo adopt permissions for the E2E namespace"
  cat <<YAML | "${KUBECTL[@]}" apply -f -
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kapro-argo-e2e-adopt
  namespace: ${ARGO_NAMESPACE}
rules:
  - apiGroups: ["argoproj.io"]
    resources: ["applications"]
    verbs: ["get", "list", "watch", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kapro-argo-e2e-adopt
  namespace: ${ARGO_NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: kapro-argo-e2e-adopt
subjects:
  - kind: ServiceAccount
    name: kapro-operator
    namespace: kapro-system
YAML
}

write_fixture_repo() {
  local repo="$1"
  local repo_url="git://kapro-argo-e2e-git.${ARGO_NAMESPACE}.svc.cluster.local/repo.git"
  mkdir -p \
    "${repo}/argocd/applications" \
    "${repo}/argocd/applicationsets" \
    "${repo}/argocd/children" \
    "${repo}/argocd/environments" \
    "${repo}/workloads/plain" \
    "${repo}/workloads/appset" \
    "${repo}/workloads/root-child"

  git -C "${repo}" init --initial-branch main
  git -C "${repo}" config user.email "kapro-e2e@example.com"
  git -C "${repo}" config user.name "Kapro E2E"

  cat >"${repo}/argocd/project.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: kapro-e2e
  namespace: ${ARGO_NAMESPACE}
spec:
  sourceRepos:
    - ${repo_url}
  clusterResourceWhitelist:
    - group: ""
      kind: Namespace
  destinations:
    - server: https://kubernetes.default.svc
      namespace: checkout
    - server: https://kubernetes.default.svc
      namespace: ${ARGO_NAMESPACE}
YAML

  cat >"${repo}/argocd/applications/plain.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: checkout-plain
  namespace: ${ARGO_NAMESPACE}
  labels:
    kapro.io/import: "true"
    kapro.io/unit: plain
    kapro.io/authorized-source: argo-e2e
spec:
  project: kapro-e2e
  source:
    repoURL: ${repo_url}
    targetRevision: v1
    path: workloads/plain
  destination:
    server: https://kubernetes.default.svc
    namespace: checkout
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
YAML

  cat >"${repo}/argocd/applications/multi-source.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: checkout-multi-source
  namespace: ${ARGO_NAMESPACE}
  labels:
    kapro.io/import: "true"
    kapro.io/unit: multi-source
    kapro.io/authorized-source: argo-e2e
spec:
  project: kapro-e2e
  sources:
    - repoURL: ${repo_url}
      targetRevision: v1
      path: workloads/multi-source
  destination:
    server: https://kubernetes.default.svc
    namespace: checkout
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
YAML

  cat >"${repo}/argocd/applications/root.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: platform-root
  namespace: ${ARGO_NAMESPACE}
  labels:
    kapro.io/import: "true"
    pattern: app-of-apps
spec:
  project: kapro-e2e
  source:
    repoURL: ${repo_url}
    targetRevision: main
    path: argocd/children
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ARGO_NAMESPACE}
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
YAML

  cat >"${repo}/argocd/children/root-child.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: checkout-root-child
  namespace: ${ARGO_NAMESPACE}
  labels:
    kapro.io/import: "true"
    kapro.io/unit: root-child
    kapro.io/authorized-source: argo-e2e
    pattern: app-of-apps-child
spec:
  project: kapro-e2e
  source:
    repoURL: ${repo_url}
    targetRevision: v1
    path: workloads/root-child
  destination:
    server: https://kubernetes.default.svc
    namespace: checkout
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
YAML

  cat >"${repo}/argocd/applicationsets/checkout-appset.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: checkout-appset
  namespace: ${ARGO_NAMESPACE}
  labels:
    kapro.io/import: "true"
spec:
  syncPolicy:
    applicationsSync: create-update
  generators:
    - matrix:
        generators:
          - git:
              repoURL: ${repo_url}
              revision: main
              files:
                - path: argocd/environments/*.json
          - list:
              elements:
                - appName: appset
                  cluster: prod
  template:
    metadata:
      name: checkout-appset-{{cluster}}
      labels:
        kapro.io/import: "true"
        kapro.io/unit: appset
        kapro.io/authorized-source: argo-e2e
    spec:
      project: kapro-e2e
      source:
        repoURL: ${repo_url}
        targetRevision: "{{gkProjectVersion}}"
        path: workloads/appset
      destination:
        server: https://kubernetes.default.svc
        namespace: checkout
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
          - CreateNamespace=true
YAML

  cat >"${repo}/argocd/applicationsets/checkout-yaml-appset.yaml" <<YAML
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: checkout-yaml-appset
  namespace: ${ARGO_NAMESPACE}
  labels:
    kapro.io/import: "true"
spec:
  syncPolicy:
    applicationsSync: create-update
  generators:
    - matrix:
        generators:
          - git:
              repoURL: ${repo_url}
              revision: main
              files:
                - path: argocd/environments/*.yaml
          - list:
              elements:
                - appName: yaml-appset
                  cluster: prod
  template:
    metadata:
      name: checkout-yaml-appset-{{cluster}}
      labels:
        kapro.io/import: "true"
        kapro.io/unit: yaml-appset
        kapro.io/authorized-source: argo-e2e
    spec:
      project: kapro-e2e
      source:
        repoURL: ${repo_url}
        targetRevision: "{{promotionrunVersion}}"
        path: workloads/yaml-appset
      destination:
        server: https://kubernetes.default.svc
        namespace: checkout
      syncPolicy:
        automated:
          prune: true
          selfHeal: true
        syncOptions:
          - CreateNamespace=true
YAML

  cat >"${repo}/argocd/environments/prod.json" <<'JSON'
{"env":"prod","gkProjectVersion":"v1"}
JSON

  cat >"${repo}/argocd/environments/prod.yaml" <<'YAML'
env: prod
promotionrunVersion: v1
YAML

  for unit in plain appset root-child multi-source yaml-appset; do
    mkdir -p "${repo}/workloads/${unit}"
    cat >"${repo}/workloads/${unit}/configmap.yaml" <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${unit}-version
  namespace: checkout
data:
  version: v1
YAML
  done

  git -C "${repo}" add .
  git -C "${repo}" commit -m "Initial Argo e2e fixture"
  git -C "${repo}" tag v1

  for unit in plain appset root-child multi-source yaml-appset; do
    cat >"${repo}/workloads/${unit}/configmap.yaml" <<YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${unit}-version
  namespace: checkout
data:
  version: v2
YAML
  done
  git -C "${repo}" add workloads
  git -C "${repo}" commit -m "Add v2 workload manifests"
  git -C "${repo}" tag v2
}

install_git_server() {
  local repo="$1"
  local bare="${TMPDIR}/repo.git"
  local tarball="${TMPDIR}/repo.git.tar.gz"
  git clone --bare "${repo}" "${bare}"
  tar -czf "${tarball}" -C "${TMPDIR}" repo.git

  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" create configmap kapro-argo-e2e-git-repo \
    --from-file=repo.git.tar.gz="${tarball}" \
    --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

  cat <<YAML | "${KUBECTL[@]}" apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kapro-argo-e2e-git
  namespace: ${ARGO_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kapro-argo-e2e-git
  template:
    metadata:
      labels:
        app: kapro-argo-e2e-git
    spec:
      initContainers:
        - name: seed
          image: ${GIT_IMAGE}
          command: ["/bin/sh", "-c"]
          args:
            - rm -rf /git/repo.git && tar -xzf /seed/repo.git.tar.gz -C /git && chown -R 0:0 /git/repo.git
          volumeMounts:
            - name: seed
              mountPath: /seed
            - name: git
              mountPath: /git
      containers:
        - name: git
          image: ${GIT_IMAGE}
          command: ["/bin/sh", "-c"]
          args:
            - git config --global --add safe.directory '*' && exec git daemon --verbose --export-all --enable=receive-pack --base-path=/git --reuseaddr /git
          ports:
            - containerPort: 9418
          volumeMounts:
            - name: git
              mountPath: /git
      volumes:
        - name: seed
          configMap:
            name: kapro-argo-e2e-git-repo
        - name: git
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: kapro-argo-e2e-git
  namespace: ${ARGO_NAMESPACE}
spec:
  selector:
    app: kapro-argo-e2e-git
  ports:
    - name: git
      port: 9418
      targetPort: 9418
YAML

  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" rollout status deployment/kapro-argo-e2e-git --timeout=180s
}

start_git_port_forward() {
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" port-forward svc/kapro-argo-e2e-git "${GIT_PORT}:9418" \
    >"${TMPDIR}/git-port-forward.log" 2>&1 &
  PF_PID="$!"
  for _ in $(seq 1 60); do
    if git ls-remote "git://127.0.0.1:${GIT_PORT}/repo.git" >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  cat "${TMPDIR}/git-port-forward.log" >&2 || true
  echo "timed out waiting for git port-forward on 127.0.0.1:${GIT_PORT}" >&2
  exit 1
}

discover_and_apply_kapro_mapping() {
  local repo="$1"
  local out="${TMPDIR}/kapro-connect"
  echo "running kapro adopt argo against fixture repo"
  "${TMPDIR}/bin/kapro" adopt argo "${repo}" --out "${out}" --name argo-e2e --force
  "${KUBECTL[@]}" apply -f "${out}/backends/argo-e2e-observe.yaml"
  "${KUBECTL[@]}" patch backend argo-e2e --type=merge \
    -p '{"spec":{"discovery":{"managementPolicy":"Adopt"}}}'
  "${KUBECTL[@]}" apply -f "${out}/sources/argo-e2e.yaml"
}

apply_argo_roots() {
  local repo="$1"
  echo "applying Argo project, plain Application, ApplicationSet, and app-of-apps root"
  "${KUBECTL[@]}" apply -f "${repo}/argocd/project.yaml"
  "${KUBECTL[@]}" apply -f "${repo}/argocd/applications/plain.yaml"
  "${KUBECTL[@]}" apply -f "${repo}/argocd/applications/multi-source.yaml"
  "${KUBECTL[@]}" apply -f "${repo}/argocd/applications/root.yaml"
  "${KUBECTL[@]}" apply -f "${repo}/argocd/applicationsets/checkout-appset.yaml"
  "${KUBECTL[@]}" apply -f "${repo}/argocd/applicationsets/checkout-yaml-appset.yaml"
}

promote_git_mapping_to_v2() {
  local repo="$1"
  local source="${TMPDIR}/kapro-connect/sources/argo-e2e.yaml"
  echo "using kapro source apply to update Git-native Argo mappings"
  "${TMPDIR}/bin/kapro" source apply \
    --repo "${repo}" \
    --source "${source}" \
    --set plain=v2 \
    --set appset=v2 \
    --set root-child=v2 \
    --set multi-source=v2 \
    --set yaml-appset=v2 \
    --all

  git -C "${repo}" remote add e2e "git://127.0.0.1:${GIT_PORT}/repo.git"
  git -C "${repo}" add argocd
  git -C "${repo}" commit -m "Promote Argo e2e apps to v2"
  git -C "${repo}" push e2e main

  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" annotate application platform-root argocd.argoproj.io/refresh=hard --overwrite || true
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" annotate applicationset checkout-appset argocd.argoproj.io/refresh=hard --overwrite || true
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" annotate applicationset checkout-yaml-appset argocd.argoproj.io/refresh=hard --overwrite || true
}

apply_kapro_rollout() {
  echo "creating Kapro Cluster and Plan"
  cat <<YAML | "${KUBECTL[@]}" apply -f -
apiVersion: kapro.io/v1alpha2
kind: Cluster
metadata:
  name: argo-e2e
  labels:
    kapro.io/e2e: argo
spec:
  delivery:
    mode: push
    backendRef: argo
    parameters:
      namespace: ${ARGO_NAMESPACE}
      authorizedSource: argo-e2e
      applicationSelector.plain: kapro.io/unit=plain
      applicationSelector.appset: kapro.io/unit=appset
      applicationSelector.root-child: kapro.io/unit=root-child
      applicationSelector.multi-source: kapro.io/unit=multi-source
      applicationSelector.yaml-appset: kapro.io/unit=yaml-appset
      versionField.multi-source: spec.sources[0].targetRevision
---
apiVersion: kapro.io/v1alpha2
kind: Plan
metadata:
  name: argo-e2e
spec:
  stages:
    - name: deploy
      selector:
        matchLabels:
          kapro.io/e2e: argo
YAML

  "${KUBECTL[@]}" patch cluster argo-e2e --subresource=status --type=merge \
    -p '{"status":{"health":{"allWorkloadsReady":true,"readyWorkloads":5,"totalWorkloads":5,"message":"Argo E2E fixture reports ready"}}}'

  echo "creating Kapro PromotionRun"
  cat <<YAML | "${KUBECTL[@]}" apply -f -
---
apiVersion: kapro.io/v1alpha2
kind: PromotionRun
metadata:
  name: argo-e2e
spec:
  versions:
    plain: v2
    appset: v2
    root-child: v2
    multi-source: v2
    yaml-appset: v2
  plans:
    - name: argo
      plan: argo-e2e
  timeout: 10m
YAML
}

wait_for_application() {
  local name="$1"
  local revision="$2"
  echo "waiting for Argo Application ${name} to become Synced/Healthy at ${revision}"
  for _ in $(seq 1 180); do
    if "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" get application "${name}" >/dev/null 2>&1; then
      local current single_source multi_source sync health
      single_source="$("${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" get application "${name}" -o jsonpath='{.spec.source.targetRevision}' 2>/dev/null || true)"
      multi_source="$("${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" get application "${name}" -o jsonpath='{.spec.sources[0].targetRevision}' 2>/dev/null || true)"
      current="${single_source:-${multi_source}}"
      sync="$("${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" get application "${name}" -o jsonpath='{.status.sync.status}' 2>/dev/null || true)"
      health="$("${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" get application "${name}" -o jsonpath='{.status.health.status}' 2>/dev/null || true)"
      if [ "${current}" = "${revision}" ] && [ "${sync}" = "Synced" ] && [ "${health}" = "Healthy" ]; then
        return
      fi
    fi
    sleep 2
  done
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" get application "${name}" -o yaml || true
  echo "timed out waiting for ${name} at ${revision}" >&2
  exit 1
}

wait_for_promotionrun_complete() {
  echo "waiting for Kapro PromotionRun to complete"
  for _ in $(seq 1 180); do
    local phase
    phase="$("${KUBECTL[@]}" get promotionrun argo-e2e -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    if [ "${phase}" = "Complete" ]; then
      return
    fi
    if [ "${phase}" = "Failed" ]; then
      "${KUBECTL[@]}" get promotionrun argo-e2e -o yaml || true
      "${KUBECTL[@]}" get targets -o yaml || true
      echo "Kapro PromotionRun failed" >&2
      exit 1
    fi
    sleep 2
  done
  "${KUBECTL[@]}" get promotionrun argo-e2e -o yaml || true
  "${KUBECTL[@]}" get targets -o yaml || true
  echo "timed out waiting for Kapro PromotionRun to complete" >&2
  exit 1
}

assert_backend_objects_reported() {
  local names count
  names="$("${KUBECTL[@]}" get targets -o jsonpath='{range .items[*]}{.status.backendObjects[*].name}{"\n"}{end}' || true)"
  count="$(printf "%s\n" "${names}" | tr ' ' '\n' | grep -E 'checkout-(plain|appset-prod|root-child|multi-source|yaml-appset-prod)' || true)"
  count="$(printf "%s\n" "${count}" | sed '/^$/d' | wc -l | tr -d ' ')"
  if [ "${count}" -lt 5 ]; then
    "${KUBECTL[@]}" get targets -o yaml || true
    echo "expected Target.status.backendObjects to include all five Argo Applications" >&2
    exit 1
  fi
}

run() {
  if [ -z "${TMPDIR}" ]; then
    TMPDIR="$(mktemp -d)"
  else
    mkdir -p "${TMPDIR}"
  fi
  echo "using temp dir ${TMPDIR}"

  create_cluster
  build_kapro_cli
  build_and_load_operator
  build_and_load_git_server_image
  build_and_load_argo_redis_image
  install_argo
  install_kapro
  grant_kapro_argo_adopt_rbac

  local repo="${TMPDIR}/repo"
  mkdir -p "${repo}"
  write_fixture_repo "${repo}"
  install_git_server "${repo}"
  start_git_port_forward

  discover_and_apply_kapro_mapping "${repo}"
  apply_argo_roots "${repo}"

  wait_for_application checkout-plain v1
  wait_for_application checkout-root-child v1
  wait_for_application checkout-appset-prod v1
  wait_for_application checkout-multi-source v1
  wait_for_application checkout-yaml-appset-prod v1

  promote_git_mapping_to_v2 "${repo}"
  apply_kapro_rollout

  wait_for_application checkout-plain v2
  wait_for_application checkout-root-child v2
  wait_for_application checkout-appset-prod v2
  wait_for_application checkout-multi-source v2
  wait_for_application checkout-yaml-appset-prod v2
  wait_for_promotionrun_complete
  assert_backend_objects_reported

  status
  echo "Argo E2E passed: discover/adopt/source-apply/promote/sync/converge all completed."

  if [ "${KAPRO_ARGO_E2E_CLEANUP:-false}" = "true" ]; then
    down
  fi
}

status() {
  echo
  echo "Kapro resources"
  "${KUBECTL[@]}" get backends,sources,clusters,plans,promotionruns,targets -o wide || true
  echo
  echo "Argo Applications"
  "${KUBECTL[@]}" -n "${ARGO_NAMESPACE}" get applications,applicationsets -o wide || true
}

down() {
  need kind
  kind delete cluster --name "${CLUSTER_NAME}"
}

cmd="${1:-run}"
case "${cmd}" in
  run|up) run ;;
  status) status ;;
  down) down ;;
  -h|--help|help) usage ;;
  *)
    usage >&2
    exit 1
    ;;
esac
