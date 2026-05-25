#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  examples/run-example.sh <example-dir> [check|apply|run|test|oci-prep]

Commands:
  check    Validate README/script shape, YAML/JSON examples, shell syntax, and Go package tests.
  apply    Apply Kubernetes YAML in the example directory with kubectl.
  run      Run a Go example when the directory contains main.go; otherwise apply YAML.
  test     Run go test for the example package, or validate YAML/shell assets.
  oci-prep Start a local zot registry and push a small ORAS artifact for OCI examples.

The default command is check. Run from any directory.
USAGE
}

find_repo_root() {
  local dir="${1:-$PWD}"
  while [[ "$dir" != "/" ]]; do
    if [[ -f "$dir/go.mod" && -d "$dir/examples" ]]; then
      printf '%s\n' "$dir"
      return 0
    fi
    dir="$(dirname "$dir")"
  done
  printf 'could not find repository root from %s\n' "$PWD" >&2
  return 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'required command not found: %s\n' "$1" >&2
    return 1
  }
}

runner_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(find_repo_root "$runner_dir")"
example_dir="${1:-}"
command_name="${2:-check}"

if [[ -z "$example_dir" || "$example_dir" == "-h" || "$example_dir" == "--help" ]]; then
  usage
  exit 0
fi

case "$example_dir" in
  /*) ;;
  *) example_dir="$PWD/$example_dir" ;;
esac
example_dir="$(cd "$example_dir" && pwd)"
rel_dir="${example_dir#"$repo_root"/}"

if [[ ! -d "$example_dir" ]]; then
  printf 'example directory does not exist: %s\n' "$example_dir" >&2
  exit 1
fi

has_go=false
has_yaml=false
has_shell=false
[[ -f "$example_dir/main.go" ]] && has_go=true
if find "$example_dir" -maxdepth 1 \( -name '*.yaml' -o -name '*.yml' -o -name '*.json' \) | grep -q .; then
  has_yaml=true
fi
if find "$example_dir" -maxdepth 1 -name '*.sh' | grep -q .; then
  has_shell=true
fi

case "$command_name" in
  check)
    [[ -f "$example_dir/README.md" ]] || { printf '%s is missing README.md\n' "$rel_dir" >&2; exit 1; }
    if [[ "$has_shell" == true ]]; then
      find "$example_dir" -maxdepth 1 -name '*.sh' -print0 | xargs -0 -n1 bash -n
    fi
    if [[ "$has_go" == true ]]; then
      (cd "$repo_root" && go test "./$rel_dir")
    elif [[ "$has_yaml" == true ]]; then
      if [[ "${KAPRO_EXAMPLE_SKIP_GLOBAL_VALIDATE:-}" == "true" ]]; then
        printf 'checked %s; global YAML/JSON validation handled by run-all.sh\n' "$rel_dir"
      else
        (cd "$repo_root" && scripts/validate-yaml-json)
      fi
    else
      printf 'checked %s\n' "$rel_dir"
    fi
    ;;
  apply)
    require_cmd kubectl
    if [[ -f "$example_dir/kustomization.yaml" ]]; then
      kubectl apply -k "$example_dir"
    elif [[ "$has_yaml" == true ]]; then
      kubectl apply -f "$example_dir"
    else
      printf '%s has no Kubernetes YAML to apply\n' "$rel_dir" >&2
      exit 1
    fi
    ;;
  run)
    if [[ "$has_go" == true ]]; then
      (cd "$repo_root" && go run "./$rel_dir")
    else
      "$repo_root/examples/run-example.sh" "$example_dir" apply
    fi
    ;;
  test)
    if [[ "$has_go" == true ]]; then
      (cd "$repo_root" && go test "./$rel_dir")
    elif [[ "$has_yaml" == true ]]; then
      if [[ "${KAPRO_EXAMPLE_SKIP_GLOBAL_VALIDATE:-}" == "true" ]]; then
        printf 'checked %s; global YAML/JSON validation handled by run-all.sh\n' "$rel_dir"
      else
        (cd "$repo_root" && scripts/validate-yaml-json)
      fi
    else
      "$repo_root/examples/run-example.sh" "$example_dir" check
    fi
    ;;
  oci-prep)
    require_cmd docker
    require_cmd oras
    if docker inspect kapro-registry >/dev/null 2>&1; then
      docker start kapro-registry >/dev/null
    else
      docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest >/dev/null
    fi
    printf 'hello from kapro %s\n' "$rel_dir" > /tmp/kapro-example-artifact.txt
    oras push --plain-http localhost:5001/kapro/example:v0.1.0 \
      --artifact-type application/vnd.kapro.example \
      /tmp/kapro-example-artifact.txt:text/plain
    oras discover --plain-http localhost:5001/kapro/example:v0.1.0
    ;;
  *)
    usage
    exit 1
    ;;
esac
