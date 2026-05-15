#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${ROOT}/charts/kapro-operator"
RELEASE_WORKFLOW="${ROOT}/.github/workflows/release.yml"

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

need helm
need shasum

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

echo "packaging Helm chart"
helm package "${CHART}" --destination "${tmpdir}"

chart_packages=("${tmpdir}"/kapro-operator-*.tgz)
if [[ ! -f "${chart_packages[0]}" ]]; then
  echo "helm package did not produce a kapro-operator chart archive" >&2
  exit 1
fi
if [[ "${#chart_packages[@]}" -ne 1 ]]; then
  echo "expected one kapro-operator chart archive, found ${#chart_packages[@]}" >&2
  exit 1
fi

echo "generating Helm chart checksum"
shasum -a 256 "${chart_packages[0]}" >"${tmpdir}/checksums.txt"
grep -Fq "$(basename "${chart_packages[0]}")" "${tmpdir}/checksums.txt"

echo "checking release workflow packages the chart and publishes checksums"
require_workflow_line "uses: azure/setup-helm@v4"
require_workflow_line "name: Package Helm chart"
require_workflow_line "helm package charts/kapro-operator --destination dist"
require_workflow_line "shasum -a 256 dist/* > dist/checksums.txt"
require_workflow_line "dist/*"
reject_workflow_line "body_path: docs/release-v0.1.0-alpha.md"

echo "release smoke verification passed"
