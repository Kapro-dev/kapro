# Monitoring Kapro

Kapro exposes Prometheus metrics from the controller-runtime metrics endpoint on
`:8080`. The examples in `examples/monitoring` are intentionally generic: they
do not contain real endpoints, credentials, or cluster-specific labels.

## Examples

- `examples/monitoring/grafana-dashboard.json` provides a Grafana dashboard for
  controller health, reconcile latency, status writes, gate results, target
  transitions, stage duration, active releases, release stuck candidates,
  blocked triggers, and plugin probe failures.
- `examples/monitoring/prometheus-rules.yaml` provides PrometheusRule-style
  alert examples for emitted Kapro metric names and kube-state-metrics custom
  resource state metrics.
- `examples/monitoring/kube-state-metrics-crd-config.yaml` provides a
  kube-state-metrics `CustomResourceStateMetrics` example for Kapro `Release`,
  `ReleaseTrigger`, and `PluginRegistration` status fields.

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

## kube-state-metrics CRD Metrics

Kapro does not currently emit first-class counters or gauges for every
operational state needed by the alert examples. For those gaps, use
kube-state-metrics custom-resource state metrics with the config in
`examples/monitoring/kube-state-metrics-crd-config.yaml`.

That config emits these example metric names:

| Metric | Source | Intent |
| --- | --- | --- |
| `kapro_release_created` | `Release.metadata.creationTimestamp` | Release age calculations. |
| `kapro_release_status_phase` | `Release.status.phase` | Non-terminal release detection. |
| `kapro_release_status_condition` | `Release.status.conditions[]` | Stalled/ready release state. |
| `kapro_releasetrigger_status_condition` | `ReleaseTrigger.status.conditions[]` | Cooldown, max-active, source, signature, and create blocking reasons. |
| `kapro_releasetrigger_status_active_release_count` | `ReleaseTrigger.status.activeReleaseCount` | Trigger-owned active release count. |
| `kapro_pluginregistration_status_ready` | `PluginRegistration.status.ready` | Plugin readiness. |
| `kapro_pluginregistration_status_condition` | `PluginRegistration.status.conditions[]` | Plugin probe status and failure reason. |

Installations must allow kube-state-metrics to list and watch these cluster
scoped CRDs:

- `releases.kapro.io`
- `releasetriggers.kapro.io`
- `pluginregistrations.kapro.io`

When using the kube-state-metrics Helm chart, mount the example as custom
resource state configuration and add matching `rbac.extraRules` for
`apiGroups: ["kapro.io"]`, `resources:
["releases", "releasetriggers", "pluginregistrations"]`, and `verbs:
["list", "watch"]`. The exact chart values vary by chart version, so keep the
example file as the source of the metric names used by the dashboard and rules.

## Alert Coverage

The PrometheusRule example includes alert expressions for:

- controller reconcile errors;
- controller reconcile p95 latency;
- gate failure ratio using `kapro_gate_evaluations_total`;
- rollout stage p95 duration using `kapro_stage_duration_seconds`;
- release stuck detection using `Release` phase and age from kube-state-metrics;
- plugin probe failures using `PluginRegistration` readiness conditions from
  kube-state-metrics;
- blocked `ReleaseTrigger` state using cooldown, max-active, source,
  signature, and release creation condition reasons from kube-state-metrics.

These alerts are examples, not universal SLOs. Tune thresholds to your release
cadence, cluster count, and expected gate retry behavior.

## Future First-Class Metrics

The kube-state-metrics rules are intended to keep operations covered while the
controller-owned metric surface catches up. Prefer adding first-class Kapro
metrics for these signals over parsing logs or relying on environment-specific
queries.

| Operational question | Future metric |
| --- | --- |
| Release is stuck in a non-terminal state past its expected timeout. | `kapro_release_stuck_total{reason,pipeline,stage}` or a release age gauge by phase. |
| Plugin probe failures are increasing. | `kapro_plugin_probe_attempts_total{plugin,type,result}` and probe duration histogram. |
| ReleaseTrigger is blocked by cooldown, max-active, signature, or source errors. | `kapro_release_trigger_blocked_total{trigger,reason}`. |
| Active release count by namespace or shard. | Extend `kapro_release_active_total` with bounded labels or emit per-shard gauges. |

When these metrics are implemented, update the concrete alert rules to use the
controller-owned metrics where they provide a stronger signal than inferred
Kubernetes object state.
