#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${ROOT}/charts/kapro-operator"
CLUSTER_CONTROLLER_CHART="${ROOT}/charts/kapro-cluster-controller"
RELEASE_WORKFLOW="${ROOT}/.github/workflows/release.yml"
EXPECTED_RELEASE_TAG="${VERSION:-}"
if [[ -z "${EXPECTED_RELEASE_TAG}" && "${GITHUB_REF_TYPE:-}" == "tag" ]]; then
  EXPECTED_RELEASE_TAG="${GITHUB_REF_NAME:-}"
fi

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require_workflow_line() {
  local pattern="$1"
  if ! grep -Fq -- "$pattern" "${RELEASE_WORKFLOW}"; then
    echo "release workflow is missing required line: ${pattern}" >&2
    exit 1
  fi
}

reject_workflow_line() {
  local pattern="$1"
  if grep -Fq -- "$pattern" "${RELEASE_WORKFLOW}"; then
    echo "release workflow must not contain pinned release-specific line: ${pattern}" >&2
    exit 1
  fi
}

chart_value() {
  local chart_dir="$1"
  local key="$2"
  awk -F: -v key="${key}" '
    $1 == key {
      value = $0
      sub(/^[^:]+:[[:space:]]*/, "", value)
      sub(/[[:space:]]*#.*/, "", value)
      gsub(/^"|"$/, "", value)
      print value
      exit
    }
  ' "${chart_dir}/Chart.yaml"
}

expected_chart_version() {
	if [[ -z "${EXPECTED_RELEASE_TAG}" ]]; then
		return 0
	fi
	if [[ "${EXPECTED_RELEASE_TAG}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]; then
		printf '%s\n' "${EXPECTED_RELEASE_TAG#v}"
		return 0
	fi
	echo "release tag ${EXPECTED_RELEASE_TAG} is not a supported semver tag; expected vMAJOR.MINOR.PATCH[-PRE]" >&2
	exit 1
}

assert_packaged_file_matches() {
  local package_path="$1"
  local chart_name="$2"
  local source_path="$3"
  local package_rel="$4"
  local package_member="${chart_name}/${package_rel}"

  if ! tar -tzf "${package_path}" "${package_member}" >/dev/null 2>&1; then
    echo "${package_path} is missing ${package_member}" >&2
    exit 1
  fi
  if ! tar -xOf "${package_path}" "${package_member}" | cmp -s "${source_path}" -; then
    echo "${package_member} in ${package_path} differs from ${source_path}" >&2
    exit 1
  fi
}

check_chart_package() {
  local chart_dir="$1"
  local chart_name="$2"
  local package_path="$3"
  local version
  local app_version

  version="$(chart_value "${chart_dir}" version)"
  app_version="$(chart_value "${chart_dir}" appVersion)"
  if [[ -z "${version}" || -z "${app_version}" ]]; then
    echo "${chart_dir}/Chart.yaml must set version and appVersion" >&2
    exit 1
  fi
  if [[ "${app_version}" != "v${version}" ]]; then
    echo "${chart_dir}/Chart.yaml appVersion ${app_version} must match chart version v${version}" >&2
    exit 1
  fi
  local expected_version
  expected_version="$(expected_chart_version)"
  if [[ -n "${expected_version}" && "${version}" != "${expected_version}" ]]; then
    echo "${chart_dir}/Chart.yaml version ${version} must match release tag ${EXPECTED_RELEASE_TAG}" >&2
    exit 1
  fi
  if [[ "$(basename "${package_path}")" != "${chart_name}-${version}.tgz" ]]; then
    echo "packaged chart name $(basename "${package_path}") does not match ${chart_name}-${version}.tgz" >&2
    exit 1
  fi

  helm show chart "${package_path}" | grep -Fxq "name: ${chart_name}"
  helm show chart "${package_path}" | grep -Fxq "version: ${version}"
  helm show chart "${package_path}" | grep -Fxq "appVersion: ${app_version}"
  assert_packaged_file_matches "${package_path}" "${chart_name}" "${chart_dir}/values.yaml" "values.yaml"
  if [[ -f "${chart_dir}/README.md" ]]; then
    assert_packaged_file_matches "${package_path}" "${chart_name}" "${chart_dir}/README.md" "README.md"
  fi

  if [[ -d "${chart_dir}/crds" ]]; then
    local crd
    for crd in "${chart_dir}"/crds/*.yaml; do
      [[ -f "${crd}" ]] || continue
      local base
      base="$(basename "${crd}")"
      if ! cmp -s "${ROOT}/config/crd/bases/${base}" "${crd}"; then
        echo "${crd} differs from config/crd/bases/${base}; chart CRD is stale" >&2
        exit 1
      fi
      assert_packaged_file_matches "${package_path}" "${chart_name}" "${crd}" "crds/${base}"
    done
  fi
}

need helm
need tar
need cmp
need shasum

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

echo "packaging Helm charts"
helm package "${CHART}" --destination "${tmpdir}"
helm package "${CLUSTER_CONTROLLER_CHART}" --destination "${tmpdir}"

chart_packages=("${tmpdir}"/kapro-operator-*.tgz)
if [[ ! -f "${chart_packages[0]}" ]]; then
  echo "helm package did not produce a kapro-operator chart archive" >&2
  exit 1
fi
if [[ "${#chart_packages[@]}" -ne 1 ]]; then
  echo "expected one kapro-operator chart archive, found ${#chart_packages[@]}" >&2
  exit 1
fi

cluster_controller_packages=("${tmpdir}"/kapro-cluster-controller-*.tgz)
if [[ ! -f "${cluster_controller_packages[0]}" ]]; then
  echo "helm package did not produce a kapro-cluster-controller chart archive" >&2
  exit 1
fi
if [[ "${#cluster_controller_packages[@]}" -ne 1 ]]; then
  echo "expected one kapro-cluster-controller chart archive, found ${#cluster_controller_packages[@]}" >&2
  exit 1
fi

echo "checking packaged Helm chart metadata and published files"
if grep -Fxq '  - "*"' "${CHART}/values.yaml"; then
  echo "kapro-operator values.yaml must not default controllers to wildcard; opt-in controllers can require extra configuration" >&2
  exit 1
fi
check_chart_package "${CHART}" kapro-operator "${chart_packages[0]}"
check_chart_package "${CLUSTER_CONTROLLER_CHART}" kapro-cluster-controller "${cluster_controller_packages[0]}"

echo "generating Helm chart checksums"
shasum -a 256 "${chart_packages[0]}" "${cluster_controller_packages[0]}" >"${tmpdir}/checksums.txt"
grep -Fq "$(basename "${chart_packages[0]}")" "${tmpdir}/checksums.txt"
grep -Fq "$(basename "${cluster_controller_packages[0]}")" "${tmpdir}/checksums.txt"

echo "checking release workflow packages the charts and publishes checksums"
require_workflow_line "uses: azure/setup-helm@v4"
require_workflow_line "name: Package Helm charts"
require_workflow_line "helm package charts/kapro-operator --destination dist"
require_workflow_line "helm package charts/kapro-cluster-controller --destination dist"
require_workflow_line 'VERSION="${{ github.ref_name }}" scripts/ci-release-smoke.sh'
require_workflow_line "name: Sign Helm chart artifacts with cosign (sign-blob)"
require_workflow_line "cosign sign-blob --yes"
require_workflow_line "name: Generate checksums for release assets"
require_workflow_line "shasum -a 256 dist/* kapro-operator.spdx.json kapro-cluster-controller.spdx.json \\"
require_workflow_line "> dist/checksums.txt"
require_workflow_line "dist/*"
require_workflow_line "startsWith(github.ref_name, 'v0.')"
require_workflow_line "generate_release_notes: true"
reject_workflow_line "body_path: docs/release-v0.1.0-alpha.md"
reject_workflow_line "kapro-operator-0.1.0.tgz"
reject_workflow_line "kapro-cluster-controller-0.1.0.tgz"

echo "checking release workflow builds and signs both images"
require_workflow_line "file: Dockerfile"
require_workflow_line "file: Dockerfile.cluster-controller"
require_workflow_line "ghcr.io/kapro-dev/kapro-operator:"
require_workflow_line "ghcr.io/kapro-dev/kapro-cluster-controller:"
require_workflow_line "cosign sign --yes ghcr.io/kapro-dev/kapro-operator"
require_workflow_line "cosign sign --yes ghcr.io/kapro-dev/kapro-cluster-controller"
require_workflow_line "security-events: write"
require_workflow_line "uses: anchore/sbom-action@e22c389904149dbc22b58101806040fa8d37a610 # v0.24.0"
reject_workflow_line "uses: anchore/sbom-action@v0"

echo "release smoke verification passed"
