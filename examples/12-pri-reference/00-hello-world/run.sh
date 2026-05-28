#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
search_dir="$script_dir"
while [[ "$search_dir" != "/" && ! -f "$search_dir/go.mod" ]]; do
  search_dir="$(dirname "$search_dir")"
done
if [[ ! -f "$search_dir/go.mod" ]]; then
  printf 'could not find repository root from %s\n' "$script_dir" >&2
  exit 1
fi

command_name="${1:-check}"

case "$command_name" in
  check|test)
    (cd "$search_dir" && go run ./cmd/kapro pri validate examples/12-pri-reference/00-hello-world)
    ;;
  run)
    (cd "$search_dir" && go run ./cmd/kapro pri validate examples/12-pri-reference/00-hello-world)
    (cd "$search_dir" && go run ./cmd/kapro pri profile)
    ;;
  apply)
    printf 'This PRI contract example has no Kubernetes resources to apply.\n'
    ;;
  *)
    printf 'usage: %s [check|test|run|apply]\n' "$0" >&2
    exit 1
    ;;
esac
