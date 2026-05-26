#!/usr/bin/env bash
set -euo pipefail

version="${GO_VERSION:-$(awk '$1 == "go" { print $2; exit }' go.mod)}"
if [[ -z "${version}" ]]; then
  echo "could not determine Go version from go.mod" >&2
  exit 1
fi

if command -v go >/dev/null 2>&1; then
  current="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  if [[ "${current}" == "${version}" ]]; then
    go version
    exit 0
  fi
fi

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

root="${RUNNER_TEMP:-/tmp}/go-${version}"
archive="${RUNNER_TEMP:-/tmp}/go-${version}.linux-${arch}.tar.gz"
url="https://go.dev/dl/go${version}.linux-${arch}.tar.gz"

if [[ ! -x "${root}/bin/go" ]]; then
  rm -rf "${root}"
  mkdir -p "${root}"
  curl --fail --location --retry 5 --retry-delay 2 --connect-timeout 20 \
    --output "${archive}" "${url}"
  tar -C "${root}" --strip-components=1 -xzf "${archive}"
fi

if [[ -n "${GITHUB_PATH:-}" ]]; then
  echo "${root}/bin" >>"${GITHUB_PATH}"
fi
export PATH="${root}/bin:${PATH}"
go version
