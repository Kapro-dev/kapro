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

exec "$search_dir/examples/run-example.sh" "$script_dir" "${1:-check}"
