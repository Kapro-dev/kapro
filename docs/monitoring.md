# Monitoring Kapro

Kapro exposes Prometheus metrics from the controller-runtime metrics endpoint on
`:8080`. The examples in `examples/monitoring` are intentionally generic: they
do not contain real endpoints, credentials, or cluster-specific labels.

## Examples

- `examples/monitoring/grafana-dashboard.json` provides a Grafana dashboard for
  controller health, reconcile latency, status writes, gate results, target
  transitions, stage duration, and active releases.
- `examples/monitoring/prometheus-rules.yaml` provides PrometheusRule-style
  alert examples for emitted Kapro metric names.

Install these through your normal observability delivery path. If you use the
Prometheus Operator, set the `metadata.namespace` and labels in
`prometheus-rules.yaml` to match the namespace and rule selector used by your
Prometheus instance.

## Current Metrics

The operator currently registers these Kapro-specific metric names:

| Metric | Type | Labels | Intent |
| --- | --- | --- | --- |
| `kapro_controller_reconciles_total` | Counter | `controller`, `result` | Reconcile attempts by controller and result. |
| `kapro_controller_reconcile_duration_seconds` | Histogram | `controller` | End-to-end reconcile latency. |
| `kapro_controller_status_writes_total` | Counter | `resource`, `result` | Status write attempts by resource and result. |
| `kapro_sync_transitions_total` | Counter | `phase`, `result` | Target rollout phase transitions. |
| `kapro_gate_evaluations_total` | Counter | `gate_type`, `result` | Gate evaluation outcomes. |
| `kapro_stage_duration_seconds` | Histogram | `pipeline` | Stage completion duration. |
| `kapro_release_active_total` | Gauge | none | Current non-terminal release count. |
| `kapro_wave_environments_promoted_total` | Gauge | `release`, `stage` | Number of promoted targets per release stage. |
| `kapro_spoke_reconciles_total` | Counter | `result` | Spoke controller reconcile attempts. |
| `kapro_spoke_reconciles_skipped_total` | Counter | none | Spoke reconciles skipped because the spec did not change. |

Controller reconcile and status write metrics are emitted by the current
controllers. The remaining Kapro-specific metric names are registered for the
operator metrics endpoint and should be treated as rollout instrumentation
surfaces as their corresponding controller paths are wired.

## Alert Coverage

The PrometheusRule example includes alert expressions for:

- controller reconcile errors;
- controller reconcile p95 latency;
- gate failure ratio using `kapro_gate_evaluations_total`;
- rollout stage p95 duration using `kapro_stage_duration_seconds`.

These alerts are examples, not universal SLOs. Tune thresholds to your release
cadence, cluster count, and expected gate retry behavior.

## Future Metrics

Some operational questions need first-class metrics that are not fully emitted
today. Do not alert on ad hoc log parsing for these cases; add explicit metrics
first so the signal is stable.

| Operational question | Future metric |
| --- | --- |
| Release is stuck in a non-terminal state past its expected timeout. | `kapro_release_stuck_total{reason,pipeline,stage}` or a release age gauge by phase. |
| Plugin probe failures are increasing. | `kapro_plugin_probe_attempts_total{plugin,type,result}` and probe duration histogram. |
| ReleaseTrigger is blocked by cooldown, max-active, signature, or source errors. | `kapro_release_trigger_blocked_total{trigger,reason}`. |
| Active release count by namespace or shard. | Extend `kapro_release_active_total` with bounded labels or emit per-shard gauges. |

When these metrics are implemented, add concrete alert rules beside the existing
examples instead of relying on inferred Kubernetes object state.
