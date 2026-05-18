#!/usr/bin/env bash
# hack/scale/heartbeat-bot.sh
#
# Pretend to be N spokes. For every FleetCluster labeled scale-test=true,
# patch the heartbeat Lease in kapro-system AND patch the FleetCluster.status
# delivery map so the hub sees a converged, healthy spoke. Runs once per
# INTERVAL until SIGINT.
#
# This is what makes the hub heartbeat reconciler, ConsecutiveFailureThreshold
# state machine, and the Decision API status surface go through their paces
# without spinning up actual workload clusters.
#
# Usage:
#   ./hack/scale/heartbeat-bot.sh                # default 30s loop forever
#   INTERVAL=10 ./hack/scale/heartbeat-bot.sh    # 10s heartbeat cadence
#   ONESHOT=1 ./hack/scale/heartbeat-bot.sh      # send once and exit (useful for CI smoke)
#
# Required: jq, kubectl with cluster-admin (Lease writes + FleetCluster status patches).

set -euo pipefail

INTERVAL="${INTERVAL:-30}"
ONESHOT="${ONESHOT:-0}"
NAMESPACE="${NAMESPACE:-kapro-system}"
HUB_CTX="${HUB_CTX:-}"
LABEL_SELECTOR="${LABEL_SELECTOR:-scale-test=true}"

if [[ -n "$HUB_CTX" ]]; then
  KUBECTL="kubectl --context=$HUB_CTX"
else
  KUBECTL="kubectl"
fi

beat_once() {
  local now
  now="$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)"
  local count=0
  local fails=0

  # Pull cluster names in one go to avoid an apiserver list-storm.
  local names
  names=$($KUBECTL get fleetclusters -l "$LABEL_SELECTOR" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')

  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    count=$((count + 1))

    # Lease patch — the hub heartbeat reconciler reads this for freshness.
    if ! $KUBECTL -n "$NAMESPACE" patch lease "kapro-heartbeat-$name" \
        --type=merge -p "{\"spec\":{\"renewTime\":\"$now\",\"holderIdentity\":\"scale-bot/$name\"}}" \
        >/dev/null 2>&1; then
      # Lease doesn't exist yet — create it.
      $KUBECTL -n "$NAMESPACE" apply -f - >/dev/null <<EOF || fails=$((fails + 1))
apiVersion: coordination.k8s.io/v1
kind: Lease
metadata:
  name: kapro-heartbeat-$name
  namespace: $NAMESPACE
spec:
  holderIdentity: scale-bot/$name
  leaseDurationSeconds: 60
  renewTime: $now
EOF
    fi

    # FleetCluster.status delivery — pretend everything is Converged so
    # promotion targets can progress in soak scenarios.
    $KUBECTL patch fleetcluster "$name" --subresource=status --type=merge -p "$(cat <<EOF
{
  "status": {
    "delivery": {
      "default": {
        "phase": "Converged",
        "desiredVersion": "v1.0.0",
        "observedDigest": "sha256:scaletest",
        "format": "raw-yaml",
        "appliedObjects": 1,
        "lastAttemptedAt": "$now",
        "lastAppliedAt": "$now"
      }
    },
    "currentVersions": {"default": "v1.0.0"}
  }
}
EOF
)" >/dev/null 2>&1 || fails=$((fails + 1))
  done <<< "$names"

  echo "[$(date +%H:%M:%S)] heartbeat tick: count=$count fails=$fails"
}

if (( ONESHOT == 1 )); then
  beat_once
  exit 0
fi

echo "heartbeat-bot starting (interval=${INTERVAL}s, selector=$LABEL_SELECTOR)"
trap 'echo "stopping heartbeat-bot"; exit 0' INT TERM
while true; do
  beat_once
  sleep "$INTERVAL"
done
