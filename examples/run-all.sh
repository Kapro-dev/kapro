#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
command_name="${1:-check}"

status=0
while IFS= read -r run_script; do
  rel_path="${run_script#"$repo_root"/}"
  printf '==> %s %s\n' "$rel_path" "$command_name"
  if ! "$run_script" "$command_name"; then
    status=1
  fi
done < <(find "$script_dir" -type f -name run.sh ! -path '*/.*' | sort)

exit "$status"
