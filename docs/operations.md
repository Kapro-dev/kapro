# Operations

This guide documents the operational posture for running Kapro as a fleet
promotion controller.

## Metrics Endpoint

The operator exposes Prometheus metrics on `:8080` by default. The Helm chart
and kustomize manifests should expose this port as `metrics`.

Kapro-specific metrics use the `kapro_` namespace:

| Metric | Type | Use |
|---|---|---|
| `kapro_controller_reconciles_total` | counter | Reconcile volume and error rate by controller |
| `kapro_controller_reconcile_duration_seconds` | histogram | Controller reconcile latency |
| `kapro_controller_status_writes_total` | counter | Status write success and failure rate |
| `kapro_sync_transitions_total` | counter | Target FSM phase transitions |
| `kapro_gate_evaluations_total` | counter | Gate pass, fail, inconclusive, and error rate |
| `kapro_stage_duration_seconds` | histogram | Stage duration by pipeline |
| `kapro_release_active_total` | gauge | Non-terminal Releases |
| `kapro_wave_environments_promoted_total` | gauge | Promoted targets by release and stage |
| `kapro_plugin_probe_results_total` | counter | Plugin probe success and failure rate |
| `kapro_plugin_probe_duration_seconds` | histogram | Plugin probe latency |
| `kapro_plugin_probe_ready` | gauge | Latest plugin readiness by type and name |
| `kapro_plugin_runtime_calls_total` | counter | Runtime plugin call result counts |
| `kapro_plugin_runtime_call_duration_seconds` | histogram | Runtime plugin latency |
| `kapro_plugin_runtime_registered` | gauge | Startup-time registered plugin adapters |

Controller-runtime and Go runtime metrics are also exposed from the same
endpoint.

## Dashboard and Alerts

Example assets are provided under `monitoring/`:

- `monitoring/grafana/kapro-operations-dashboard.json`
- `monitoring/prometheus/kapro-alerts.yaml`

The dashboard covers:

- release backlog and active Releases;
- release stuck symptoms through controller error rate and active backlog;
- gate failure rate;
- plugin probe failures and readiness;
- trigger blocked symptoms through ReleaseTrigger reconcile errors;
- rollout duration p95 via stage duration histogram.

The alert rules cover:

| Alert | Signal |
|---|---|
| `KaproReleaseStuck` | Active Releases remain non-terminal for a sustained window |
| `KaproGateFailureRateHigh` | Gate failures/errors exceed 10% of evaluations |
| `KaproPluginProbeFailures` | Plugin probe failures or plugin readiness drops |
| `KaproReleaseTriggerBlocked` | ReleaseTrigger reconciles are failing |
| `KaproRolloutDurationP95High` | Stage duration p95 exceeds the configured threshold |
| `KaproControllerReconcileErrors` | Any controller has sustained reconcile errors |

Tune alert windows and thresholds per fleet size. Small test clusters should use
longer `for` windows to avoid noise from deliberate failure tests.

## Rate Limits and Workqueue Tuning

Kapro uses controller-runtime workqueues and Kubernetes API backoff. The
operator currently sets manager-wide `MaxConcurrentReconciles` to `5`.

Operational guidance:

- Start with `5` concurrent reconciles for hub clusters below 500 targets.
- Raise concurrency only after watching API server throttling and status write
  errors.
- Keep plugin timeouts short. A slow plugin call occupies reconcile capacity.
- Prefer gate `interval` values of at least `30s`; the runtime clamps invalid or
  too-small metric intervals to safe defaults.
- Use controller sharding before pushing a single manager beyond the Kubernetes
  API server's comfortable QPS budget.

## Sharding

Set `KAPRO_SHARD` on an operator replica set to enable shard selection. The
controller logs the shard name at startup and uses shard predicates from
`internal/shard`.

Recommended model:

- Run one shard per major environment or region.
- Assign objects using a stable shard label such as `kapro.io/shard`.
- Keep one unsharded controller only in small development clusters.
- Do not run overlapping shards against the same object set unless leader
  election and selectors make ownership unambiguous.

## Large Fleet Assumptions

Kapro is designed for hub-and-spoke promotion where the hub stores desired
promotion state and spoke controllers or GitOps backends converge local
workloads.

Current practical assumptions:

- Kubernetes API is the source of truth for release state.
- Plugins are idempotent and bounded by request context.
- One target rollout is represented by one `ReleaseTarget`.
- Stage fan-out is controlled by pipeline strategy, not by unbounded goroutines.
- Status updates are small and append bounded summaries rather than complete
  historical logs.

For fleets above roughly 1,000 targets per hub, use sharding, conservative stage
`maxParallel`, and external long-term event storage. Kapro Events and status are
operational state, not an infinite audit warehouse.

## Failure Handling

When a release appears stuck:

1. Check `Release.status.phase`, `Release.status.report`, and pending approvals.
2. Check `ReleaseTarget.status.phase` for `WaitingApproval`, `MetricsCheck`,
   `Verification`, or `Failed`.
3. Check `kapro_gate_evaluations_total` for gate failures or inconclusive loops.
4. Check plugin metrics when the blocked gate or actuator is external.
5. Check controller reconcile errors and status write failures.

Use Kubernetes Events for the exact transition reason, then use the dashboard to
determine whether the problem is isolated or fleet-wide.
