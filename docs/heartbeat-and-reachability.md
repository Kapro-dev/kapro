# Cluster Heartbeat and Reachability

This document explains how Kapro decides whether a spoke cluster is reachable,
how to tune that decision, and what changes for in-flight promotions when a
cluster goes Unreachable.

For the spoke side (how a cluster joins the fleet and starts heartbeating),
see [cluster-bootstrap.md](cluster-bootstrap.md). For the hub-side bootstrap
reconciler that issues per-cluster identities, see the inline doc on
`FleetClusterSpec.Bootstrap` in `api/v1alpha1/types.go`.

## Model in one diagram

```
  spoke cluster                      hub cluster
  ─────────────                      ────────────
  kapro-cluster-controller           FleetClusterHeartbeatReconciler
     │                                  │
     │ every spec.heartbeatInterval     │ every heartbeatFreshTimeout / 2
     │ (default 30s) PATCH the Lease    │ (default 1m) READ the Lease
     ▼                                  ▼
  Lease kapro-heartbeat-<name>  ─────▶  observed = renewTime / acquireTime / creationTime
   (coordination.k8s.io/v1)             │
   namespace: kapro-system              ▼
                                     freshness verdict
                                        │
                                        ├─ fresh:  Ready=True  reason=HeartbeatFresh, misses=0
                                        ├─ stale & misses < threshold:
                                        │           Ready=Unknown reason=HeartbeatStale
                                        └─ stale & misses ≥ threshold:
                                                    Ready=False  reason=Unreachable
                                                    Phase=Unreachable
                                                    Event ClusterUnreachable
```

## What the threshold means

`FleetCluster.spec.consecutiveFailureThreshold` (default `3`, range `1..100`)
is the number of consecutive reconciles where the cluster's Lease is stale or
missing before the reconciler flips `Ready=False reason=Unreachable` and
`Phase=Unreachable`.

With the default heartbeat interval of 30s and freshness window of 2m, three
consecutive stale reconciles ≈ a sustained ~2m + 2 × (reconcile cadence)
window of no heartbeat. Set the threshold higher for clusters on flaky
networks; lower for highly observable production clusters.

The threshold does NOT control recovery. Any single fresh observation snaps
the cluster back to `Ready=True` immediately. Hysteresis on the failure edge
only — recovery is eager.

## Single-writer rule

| Field | Sole writer |
|---|---|
| `status.heartbeat` | `FleetClusterHeartbeatReconciler` |
| `status.conditions[Ready]` | `FleetClusterHeartbeatReconciler` |
| `status.phase` | `kapro_controller` (reads `conditions[Ready]` to decide `Unreachable`) |

When the heartbeat reconciler sets `Ready=False reason=Unreachable`,
`kapro_controller`'s phase computation surfaces `Phase=Unreachable` on the
next reconcile. There is no race because there is only one writer per field;
`kapro_controller` simply reads what the heartbeat reconciler published.

## What changes for in-flight promotions

The `PromotionTargetReconciler` honors the cluster's reachability:

| Cluster state | What the target does |
|---|---|
| `Ready=True` | Proceeds. Per-target stale counters are cleared. |
| `Ready=False reason=Unreachable` (Phase=Unreachable) | **Defers** (does NOT fail). Emits `ClusterUnreachable` event. Requeues. |
| `Ready=Unknown` (Stale, Suspended, NotRegistered, missing) | Defers. Requeues. |

**v0.4 → v0.5 behavior change**: prior versions failed the target after a
fixed 5m window of stale heartbeat. v0.5 defers indefinitely — a transient
network blip will not trash an in-flight promotion. Operators who want to
give up on a stuck target can:

- `kapro reject <promotionrun>/<target> --reason ...` — explicit operator
  override.
- Suspend the FleetCluster (`spec.suspend: true`) — the target will defer
  with `reason=Suspended` instead of `reason=Unreachable`. Cleaner for
  planned maintenance windows.
- Delete the FleetCluster — the target reports a missing-cluster error and
  fails naturally.

## Reason codes (Ready condition)

| Reason | Status | Meaning |
|---|---|---|
| `HeartbeatFresh` | True | Lease renewed within the freshness window. |
| `HeartbeatStale` | Unknown | Lease stale, but consecutive misses below threshold. Transient. |
| `Unreachable` | False | Stale long enough to cross `consecutiveFailureThreshold`. Phase flips to Unreachable. |
| `Suspended` | Unknown | `spec.suspend=true`. Heartbeat tracking disabled until clear. |
| `PushModeNoHeartbeat` | True | `spec.delivery.mode=push`. No spoke agent, no Lease — heartbeat is N/A. |
| `NotRegistered` | Unknown | `status.bootstrap.used` is false. Spoke has never completed registration. Day-0 problem (chart not installed yet) vs Unreachable (Day-1+). |

## Metrics

Three Prometheus series exposed on the operator metrics endpoint:

| Metric | Type | Labels | What it tracks |
|---|---|---|---|
| `kapro_fleetcluster_heartbeat_misses` | gauge | `cluster` | Current consecutive miss count. Mirrors `status.heartbeat.consecutiveMisses`. |
| `kapro_fleetcluster_unreachable_transitions_total` | counter | `cluster` | Total transitions to `Ready=False reason=Unreachable`. Alert on `rate(...[5m]) > 0`. |
| `kapro_fleetcluster_recovered_transitions_total` | counter | `cluster` | Total transitions out of Unreachable back to Ready. |

## Tuning

```yaml
apiVersion: kapro.io/v1alpha1
kind: FleetCluster
metadata:
  name: de-prod-01
spec:
  delivery:
    mode: pull
    backendRef: flux
  # Default 3. Raise to 5–10 for high-latency / flaky-network clusters.
  # Lower (1–2) only if you want very fast Unreachable transitions and your
  # spoke heartbeat interval is short.
  consecutiveFailureThreshold: 3
```

The heartbeat interval itself is configured on the spoke side via the chart
value `heartbeat.intervalSeconds` (minimum 5s) — see the
[kapro-cluster-controller chart values](../charts/kapro-cluster-controller/values.yaml).

## Troubleshooting

### Cluster oscillates between Ready and Stale but never reaches Unreachable

The heartbeat is making it in just before the freshness window most of the
time, but the spoke's heartbeat interval is too close to the window. Lower
the spoke's `heartbeat.intervalSeconds` so each renewal has comfortable
headroom inside the 2m freshness window (default 30s × 4 cycles).

### Cluster stuck on `Ready=Unknown reason=NotRegistered`

The spoke has never completed the CSR exchange. Check:

```bash
kubectl get fleetcluster <name> -o jsonpath='{.status.bootstrap}{"\n"}'
kubectl -n kapro-system logs deployment/<your-operator-release>-kapro-operator | grep -i bootstrap
```

If `status.bootstrap.issuedBootstrapKubeconfig` is empty, the hub bootstrap
reconciler hasn't run. If it's set but the spoke pod isn't applying the
Secret, follow the
[cluster-bootstrap troubleshooting section](cluster-bootstrap.md#troubleshooting).

### Cluster flipped to Unreachable but I know the spoke is up

Check the Lease directly:

```bash
kubectl -n kapro-system get lease kapro-heartbeat-<name> -o yaml
```

If `spec.renewTime` is fresh, the reconciler is reading a stale cache or has
a clock skew problem. Restart the operator. If the Lease itself is stale,
look at the spoke pod's logs:

```bash
kubectl --context <spoke> -n kapro-system logs deployment/kapro-cluster-controller-kapro-cluster-controller
```

The spoke writes a heartbeat tick log line per renewal.

### Stuck promotion target waiting for Unreachable cluster

Two clean exits:

```bash
# Stop the promotion explicitly
kapro reject my-release/de-prod-01 --reason "cluster down — see incident #1234"

# Or pause the cluster for maintenance
kubectl patch fleetcluster de-prod-01 --type=merge -p '{"spec":{"suspend":true}}'
```

Suspend is preferred for maintenance windows: it freezes heartbeat tracking
cleanly and the target reports the reason as `Suspended` rather than
`Unreachable`.
