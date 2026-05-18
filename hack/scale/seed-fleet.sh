#!/usr/bin/env bash
# hack/scale/seed-fleet.sh
#
# Seed N simulated FleetCluster CRs against a running hub. Each FleetCluster
# is shaped exactly like a real outbound-agent registration, with a deterministic
# name (scale-NNNN), realistic labels (env, tier, country, shard), and a
# delivery profile pointing at the oci-greenfield backend (or whatever
# BACKEND_REF env var says). No spoke clusters are created — heartbeat-bot.sh
# pretends to be the spokes in the same fleet.
#
# Why simulated: a real 500-kind-cluster fleet needs ~500 GB of RAM. This
# script costs ~5 MB (the CR specs themselves) and exercises every hub-side
# code path: heartbeat reconciler at scale, Decision API pagination,
# sharded controller dispatch, FleetCluster watch fan-out, AgentPolicy
# evaluation across many targets.
#
# Usage:
#   ./hack/scale/seed-fleet.sh 500
#   N=500 BACKEND_REF=oci-greenfield HUB_CTX=kind-hub ./hack/scale/seed-fleet.sh

set -euo pipefail

N="${1:-${N:-500}}"
BACKEND_REF="${BACKEND_REF:-oci-greenfield}"
HUB_CTX="${HUB_CTX:-}"
NAMESPACE_LABEL_PREFIX="${LABEL_PREFIX:-kapro.io}"

# Distribution across realistic dimensions so stage selectors actually do
# something. Tune to taste.
COUNTRIES=(de at fi se nl be pl es)
TIERS=(canary general)
SHARDS=(shard-a shard-b shard-c shard-d)

if [[ -n "$HUB_CTX" ]]; then
  KUBECTL="kubectl --context=$HUB_CTX"
else
  KUBECTL="kubectl"
fi

echo "Seeding $N FleetClusters (backend=$BACKEND_REF) ..."

tmp="$(mktemp -t kapro-scale.XXXXXX.yaml)"
trap 'rm -f "$tmp"' EXIT

# Build one giant YAML stream so a single kubectl apply runs (10–100x
# faster than per-CR apply at this scale).
> "$tmp"
for ((i = 1; i <= N; i++)); do
  name=$(printf "scale-%04d" "$i")
  country=${COUNTRIES[$((i % ${#COUNTRIES[@]}))]}
  # Tier: 5% canary, 95% general — matches a real wave.
  if (( i % 20 == 0 )); then
    tier=canary
  else
    tier=general
  fi
  shard=${SHARDS[$((i % ${#SHARDS[@]}))]}
  # Deterministic token hash so re-runs are idempotent. NEVER use this in
  # a real fleet — it's a deliberately predictable test value.
  token_hash=$(printf "sha256:%064d" "$i")

  cat >> "$tmp" <<EOF
---
apiVersion: kapro.io/v1alpha1
kind: FleetCluster
metadata:
  name: $name
  labels:
    ${NAMESPACE_LABEL_PREFIX}/team: scale-test
    ${NAMESPACE_LABEL_PREFIX}/shard: $shard
    env: prod
    tier: $tier
    country: $country
    scale-test: "true"
spec:
  provider:
    kind: outbound-agent
    parameters:
      hubURL: https://kapro-hub.scale.test:6443
  bootstrap:
    tokenHash: $token_hash
    ttl: 24h
  delivery:
    backendRef: $BACKEND_REF
    mode: pull
    parameters:
      ociRepository: oci://harbor.scale.test/kapro/bundle-$country
  consecutiveFailureThreshold: 3
EOF
done

$KUBECTL apply -f "$tmp"
echo "Seeded $N FleetClusters."
echo "Run hack/scale/heartbeat-bot.sh next to simulate the spokes."
