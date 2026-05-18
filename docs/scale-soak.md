# Scale Soak — 500 Simulated FleetClusters

This is the harness the architecture audit (Q1.5) called for: stand up a
hub + 500 simulated FleetClusters + a "spoke pretender" loop, watch the
operator's behaviour through Prometheus, and use the results as a
regression baseline before any Q2 feature work.

500 real kind clusters is ~500 GB RAM. The harness here is ~5 MB —
because the FleetClusters are CR objects and the "spokes" are a bash
loop that patches their Lease + status as if they were real. Every
hub-side code path that matters (heartbeat reconciler, sharded
controller dispatch, Decision API pagination, AgentPolicy enforcement
at fleet scale, status conflict rates) is exercised. The things that
genuinely need real spokes (CSR bootstrap, OCI Delivery Core apply)
are covered separately by `examples/kind-demo` and `examples/greenfield`.

## Files

| File | Purpose |
|---|---|
| `hack/scale/seed-fleet.sh` | Create N FleetCluster CRs with realistic label distribution (env / tier / country / shard). Default N=500. |
| `hack/scale/heartbeat-bot.sh` | Loop forever (or `ONESHOT=1` for once) patching the heartbeat Lease + status.delivery on every scale-test FleetCluster. Mimics a healthy spoke. |
| `hack/scale/dashboard.sh` | Curl the operator `/metrics` endpoint (via the metrics Service that the gate-observability commit added) and print a one-screen summary. Wrap with `watch -n 5` for live view. |

## End-to-end on a single kind cluster

```bash
# 1. Stand up a hub with the chart.
kind create cluster --name kapro-hub
make install
helm install kapro-operator charts/kapro-operator \
  -n kapro-system --create-namespace \
  --set metrics.serviceMonitor.enabled=false

# 2. Seed 500 FleetClusters.
HUB_CTX=kind-kapro-hub ./hack/scale/seed-fleet.sh 500

# 3. Start the heartbeat bot in the background.
HUB_CTX=kind-kapro-hub ./hack/scale/heartbeat-bot.sh &

# 4. Watch the dashboard.
HUB_CTX=kind-kapro-hub watch -n 5 ./hack/scale/dashboard.sh
```

Expected (steady-state, on a 16 GB workstation):

- `FleetClusters tracked`: 500
- `Max consecutive misses`: 0 (heartbeat-bot keeps every Lease fresh)
- `conflict rate`: < 1% (with gate-IsConflict + gate-v6-fsm-conflict
  helpers in place; this is the regression bar)
- `kapro-operator` pod CPU < 200m steady, < 500m during the seed burst
- `kapro-operator` pod memory < 256 Mi

If `Max consecutive misses` ticks above 0 with the bot running, the
heartbeat reconciler is starving — that's the queue-depth signal Q3's
sharding work is meant to fix.

## Soak scenarios to run before Q2

| Scenario | Setup | Pass criteria |
|---|---|---|
| **Cold start** | Apply 500 FleetClusters at once, no bot yet. | All 500 reach `Phase=Pending` and `Ready=Unknown` within 60s. |
| **Steady state** | Bot at default 30s cadence for 30 minutes. | `conflict rate < 1%`, `Max misses == 0`. |
| **Heartbeat starvation** | Bot at 5s cadence for 10 minutes. | Hub CPU < 500m, no apiserver throttling on the operator SA. |
| **Cluster churn** | Re-run seed-fleet.sh halfway through bot loop. | New FleetClusters reach Ready within one heartbeat cycle; existing ones unaffected. |
| **Operator restart** | `kubectl delete pod -l app=kapro-operator` during steady state. | Recovery < 30s; `Max consecutive misses` may spike to 1-2 then return to 0. |
| **Bot stop** | Kill bot, wait `ConsecutiveFailureThreshold * heartbeat-window`. | All 500 transition to `Ready=False, Phase=Unreachable` in lockstep. |

## Cleanup

```bash
kubectl delete fleetclusters -l scale-test=true
```

Or nuke the kind cluster.

## What this does NOT cover

- Real CSR bootstrap (no spoke binaries running).
- Real OCI artifact pull + two-phase apply (no spokes).
- Real plugin gRPC traffic (no plugin endpoints).
- Argo / Flux brownfield observation (no spoke-side Flux installed).

For those, run `examples/kind-demo` (single-cluster end-to-end) or
the per-driver demos in `examples/brownfield` and `examples/greenfield`.

The soak harness is intentionally a hub-only stress test — it answers
"can the hub controllers keep up with 500-cluster fleet state changes",
not "can the hub orchestrate 500 real workload clusters." Those are
different questions; this one is the cheaper of the two and surfaces
real bottlenecks before more features pile in.
