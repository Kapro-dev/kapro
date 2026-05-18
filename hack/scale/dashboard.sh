#!/usr/bin/env bash
# hack/scale/dashboard.sh
#
# One-screen Prometheus-style snapshot of operator metrics for the running
# soak. Curls the operator's /metrics endpoint via the metrics Service that
# the operator chart ships (servicemonitor target), greps for the gauges and
# histograms that matter, and prints a small table you can refresh every few
# seconds with watch(1).
#
# Usage:
#   watch -n 5 ./hack/scale/dashboard.sh
#   ./hack/scale/dashboard.sh > /tmp/baseline.txt   # capture baseline
#
# Requires: kubectl with port-forward perm OR direct cluster network access
# to the metrics Service.

set -euo pipefail

NAMESPACE="${NAMESPACE:-kapro-system}"
SERVICE="${SERVICE:-kapro-operator-metrics}"
PORT="${PORT:-8080}"
HUB_CTX="${HUB_CTX:-}"

if [[ -n "$HUB_CTX" ]]; then
  KUBECTL="kubectl --context=$HUB_CTX"
else
  KUBECTL="kubectl"
fi

# One-off port-forward into the metrics Service so we don't need direct
# pod-IP access. Trap cleanup.
forward_log="$(mktemp -t kapro-scale-pf.XXXXXX.log)"
$KUBECTL -n "$NAMESPACE" port-forward "svc/$SERVICE" "$PORT:$PORT" >"$forward_log" 2>&1 &
pf_pid=$!
trap 'kill $pf_pid 2>/dev/null; rm -f "$forward_log"' EXIT
# Give port-forward a moment.
sleep 1

if ! metrics=$(curl -sf "http://localhost:$PORT/metrics" 2>/dev/null); then
  echo "ERROR: could not scrape metrics from svc/$SERVICE — check port-forward"
  cat "$forward_log"
  exit 1
fi

count_l() { grep -cE "$1" <<< "$metrics" || echo 0; }
get_sum() {
  # Sum gauge values matching a pattern (last numeric column).
  grep -E "$1" <<< "$metrics" | awk '{ s += $NF } END { print (s ? s : 0) }'
}
get_max() {
  grep -E "$1" <<< "$metrics" | awk 'BEGIN { m = 0 } { if ($NF > m) m = $NF } END { print m }'
}
get_one() {
  # First numeric value matching pattern.
  grep -E "$1" <<< "$metrics" | head -1 | awk '{ print $NF }'
}

clusters_total=$(count_l '^kapro_fleetcluster_heartbeat_misses')
misses_max=$(get_max '^kapro_fleetcluster_heartbeat_misses')
status_success=$(get_sum '^kapro_status_writes_total\{.*result="success"')
status_error=$(get_sum '^kapro_status_writes_total\{.*result="error"')
status_conflict=$(get_sum '^kapro_status_writes_total\{.*result="conflict"')
plugin_probe_success=$(get_sum '^kapro_plugin_probe_results_total\{.*outcome="success"')
plugin_probe_error=$(get_sum '^kapro_plugin_probe_results_total\{.*outcome="error"')

cat <<EOF
=== Kapro Operator Soak Dashboard $(date '+%Y-%m-%d %H:%M:%S') ===

FleetClusters tracked      : $clusters_total
Max consecutive misses     : $misses_max  (threshold default 3)

Status writes
  success                  : $status_success
  conflict (retried)       : $status_conflict
  error (other)            : $status_error
  conflict rate            : $(awk -v c="$status_conflict" -v s="$status_success" 'BEGIN { tot = c + s; if (tot == 0) print "n/a"; else printf "%.2f%%\n", (c/tot)*100 }')

Plugin probes
  success                  : $plugin_probe_success
  error                    : $plugin_probe_error

Top reconcile-duration histograms (sum):
$(grep -E '_duration_seconds_sum\b' <<< "$metrics" | awk '{ printf "  %-50s %s\n", $1, $2 }' | head -8)
EOF
