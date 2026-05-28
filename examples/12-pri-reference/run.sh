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

"$search_dir/examples/12-pri-reference/00-hello-world/run.sh" "${1:-check}"
