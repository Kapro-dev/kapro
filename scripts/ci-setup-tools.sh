#!/usr/bin/env bash
set -euo pipefail

bin="${RUNNER_TEMP:-/tmp}/kapro-ci-bin"
mkdir -p "${bin}"
if [[ -n "${GITHUB_PATH:-}" ]]; then
  echo "${bin}" >>"${GITHUB_PATH}"
fi
export PATH="${bin}:${PATH}"

download() {
  local url="$1"
  local out="$2"
  curl --fail --location --retry 5 --retry-delay 2 --connect-timeout 20 \
    --output "${out}" "${url}"
}

install_helm() {
  if command -v helm >/dev/null 2>&1; then
    helm version --short
    return
  fi
  local version="${HELM_VERSION:-v3.17.3}"
  local archive="${RUNNER_TEMP:-/tmp}/helm-${version}.tar.gz"
  local checksums="${RUNNER_TEMP:-/tmp}/helm-${version}.sha256sum"
  local asset="helm-${version}-linux-amd64.tar.gz"
  local base="https://get.helm.sh"
  download "${base}/${asset}" "${archive}"
  download "${base}/${asset}.sha256sum" "${checksums}"
  (cd "$(dirname "${archive}")" && sha256sum -c "$(basename "${checksums}")")
  tar -xzf "${archive}" -C "${RUNNER_TEMP:-/tmp}" linux-amd64/helm
  mv "${RUNNER_TEMP:-/tmp}/linux-amd64/helm" "${bin}/helm"
  rmdir "${RUNNER_TEMP:-/tmp}/linux-amd64"
  chmod +x "${bin}/helm"
  helm version --short
}

install_kubectl() {
  if command -v kubectl >/dev/null 2>&1; then
    kubectl version --client=true
    return
  fi
  local version="${KUBECTL_VERSION:-v1.31.0}"
  local target="${bin}/kubectl"
  local checksum="${RUNNER_TEMP:-/tmp}/kubectl.sha256"
  local base="https://dl.k8s.io/release/${version}/bin/linux/amd64"
  download "${base}/kubectl" "${target}"
  download "${base}/kubectl.sha256" "${checksum}"
  echo "$(cat "${checksum}")  ${target}" | sha256sum -c -
  chmod +x "${target}"
  kubectl version --client=true
}

install_kind() {
  if command -v kind >/dev/null 2>&1; then
    kind version
    return
  fi
  local version="${KIND_VERSION:-v0.25.0}"
  local target="${bin}/kind"
  download "https://kind.sigs.k8s.io/dl/${version}/kind-linux-amd64" "${target}"
  chmod +x "${target}"
  kind version
}

install_helm
install_kubectl
if [[ "${CI_SETUP_KIND:-false}" == "true" ]]; then
  install_kind
fi
