# Monitoring Kapro

Kapro exposes Prometheus metrics from the controller-runtime metrics endpoint on
`:8080`. The examples in `examples/monitoring` are intentionally generic: they
do not contain real endpoints, credentials, or cluster-specific labels.

## Asset Locations

Kapro keeps monitoring examples under `examples/monitoring`:

| Path | Purpose |
| --- | --- |
| `examples/monitoring/kapro-alerts.yaml` | Generic Prometheus alert groups for direct import or adaptation. |
| `examples/monitoring/kapro-operations-dashboard.json` | Compact operations dashboard for the core Kapro metrics endpoint. |
| `examples/monitoring/prometheus-rules.yaml` | Prometheus Operator `PrometheusRule` example that combines Kapro metrics with kube-state-metrics CRD state metrics. |
| `examples/monitoring/grafana-dashboard.json` | Full example Grafana dashboard using Kapro metrics, CRD state metrics, and recording rules from `prometheus-rules.yaml`. |
| `examples/monitoring/kube-state-metrics-crd-config.yaml` | Example kube-state-metrics custom-resource state config for PromotionRuns, Triggers, and Plugins. |

Use `kapro-alerts.yaml` and `kapro-operations-dashboard.json` when you want
small, generic assets. Use the Prometheus Operator and kube-state-metrics
examples when your stack can install the CRD state metrics used by the richer
alerts.

## Examples

- `examples/monitoring/grafana-dashboard.json` provides a Grafana dashboard for
  controller health, reconcile latency, status writes, gate results, target
  transitions, stage duration, active promotionruns, promotionrun stuck candidates,
  blocked triggers, CloudEvents sink p99, FleetDriftReport phase/counts, and
  plugin probe failures. Import `prometheus-rules.yaml` first so panels backed
  by `kapro:slo_*` recording rules have data.
- `examples/monitoring/prometheus-rules.yaml` provides PrometheusRule-style
  alert examples for emitted Kapro metric names and kube-state-metrics custom
  resource state metrics.
- `examples/monitoring/kube-state-metrics-crd-config.yaml` provides a
  kube-state-metrics `CustomResourceStateMetrics` example for Kapro `PromotionRun`,
  `Trigger`, and `Plugin` status fields.

Install these through your normal observability delivery path. If you use the
Prometheus Operator, set the `metadata.namespace` and labels in
`prometheus-rules.yaml` to match the namespace and rule selector used by your
Prometheus instance.

Run `make validate-yaml-json` before changing monitoring assets. The validator
checks the Kubernetes `PrometheusRule` shape and, when `promtool` is installed,
extracts `spec.groups` before checking PromQL syntax.

For SLI definitions, suggested thresholds, and the current split between
first-class Kapro metrics and inferred kube-state-metrics signals, see
`docs/operator-slos.md`.

## Operator Metrics

The operator currently registers these Kapro-specific metric names:

| Metric | Type | Labels | Intent |
| --- | --- | --- | --- |
| `kapro_controller_reconciles_total` | Counter | `controller`, `result` | Reconcile attempts by controller and result. |
| `kapro_controller_reconcile_duration_seconds` | Histogram | `controller` | End-to-end reconcile latency. |
| `kapro_controller_status_writes_total` | Counter | `resource`, `result` | Status write attempts by resource and result. |
| `kapro_sync_transitions_total` | Counter | `phase`, `result` | Target rollout phase transitions. |
| `kapro_fsm_unexpected_transitions_total` | Counter | `from`, `to` | FSM transition attempts outside the declared graph. |
| `kapro_gate_evaluations_total` | Counter | `gate_type`, `result` | Gate evaluation outcomes. |
| `kapro_stage_duration_seconds` | Histogram | `plan` | Stage completion duration. |
| `kapro_promotionrun_active_total` | Gauge | none | Current non-terminal promotionrun count. |
| `kapro_wave_environments_promoted_total` | Gauge | `promotionrun`, `stage` | Number of promoted targets per promotionrun stage. |
| `kapro_spoke_reconciles_total` | Counter | `result` | Spoke controller reconcile attempts. |
| `kapro_spoke_reconciles_skipped_total` | Counter | none | Spoke reconciles skipped because the spec did not change. |
| `kapro_cluster_heartbeat_misses` | Gauge | `cluster` | Current consecutive heartbeat misses per Cluster. |
| `kapro_cluster_unreachable_transitions_total` | Counter | `cluster` | Cluster transitions to unreachable. |
| `kapro_cluster_recovered_transitions_total` | Counter | `cluster` | Cluster recoveries from unreachable. |
| `kapro_plugin_probe_results_total` | Counter | `type`, `result`, `reason` | Plugin capability probe results. |
| `kapro_plugin_probe_duration_seconds` | Histogram | `type`, `result` | Plugin capability probe latency. |
| `kapro_plugin_probe_ready` | Gauge | `type`, `name` | Latest plugin readiness by type and name. |
| `kapro_plugin_runtime_calls_total` | Counter | `type`, `name`, `method`, `result` | Runtime plugin adapter call results. |
| `kapro_plugin_runtime_call_duration_seconds` | Histogram | `type`, `name`, `method`, `result` | Runtime plugin adapter call latency. |
| `kapro_plugin_runtime_registered` | Gauge | `type` | Startup-time registered plugin adapters by plugin type. |
| `kapro_lifecycle_hook_invocations_total` | Counter | `kind`, `phase`, `result` | Promotion lifecycle webhook/Event/CloudEvents sink dispatch results. |
| `kapro_lifecycle_hook_duration_seconds` | Histogram | `kind`, `phase` | End-to-end lifecycle dispatch duration including retries and backoff. |
| `kapro_fleetdriftreport_targets` | Gauge | `report`, `state` | Latest FleetDriftReport target counts for total/current/drifted/pending/failed/unknown. |
| `kapro_fleetdriftreport_backend_objects` | Gauge | `report`, `state` | Latest FleetDriftReport backend object counts for total/drifted. |
| `kapro_fleetdriftreport_phase` | Gauge | `report`, `phase` | One-hot latest FleetDriftReport phase. |

Controller reconcile and status write metrics are emitted by the current
controllers. The remaining Kapro-specific metric names are registered for the
operator metrics endpoint and should be treated as rollout instrumentation
surfaces as their corresponding controller paths are wired.

## Spoke Metrics

`kapro-cluster-controller` exposes its own Prometheus endpoint on `/metrics`
when `KAPRO_METRICS_ADDR` is not `off`. The Helm chart enables this endpoint
and a metrics Service by default.

| Metric | Type | Labels | Intent |
| --- | --- | --- | --- |
| `kapro_spoke_delivery_reconciles_total` | Counter | `cluster`, `backend`, `phase`, `result` | Spoke delivery reconcile outcomes. |
| `kapro_spoke_delivery_reconcile_duration_seconds` | Histogram | `cluster`, `backend`, `phase`, `result` | Spoke delivery reconcile duration. |

## kube-state-metrics CRD Metrics

Kapro does not currently emit first-class counters or gauges for every
operational state needed by the alert examples. For those gaps, use
kube-state-metrics custom-resource state metrics with the config in
`examples/monitoring/kube-state-metrics-crd-config.yaml`.

That config emits these example metric names:

| Metric | Source | Intent |
| --- | --- | --- |
| `kapro_promotionrun_created` | `PromotionRun.metadata.creationTimestamp` | PromotionRun age calculations. |
| `kapro_promotionrun_status_phase` | `PromotionRun.status.phase` | Non-terminal promotionrun detection. |
| `kapro_promotionrun_status_condition` | `PromotionRun.status.conditions[]` | Stalled/ready promotionrun state. |
| `kapro_trigger_status_condition` | `Trigger.status.conditions[]` | Cooldown, max-active, source, signature, and Promotion update blocking reasons. |
| `kapro_trigger_status_active_promotionrun_count` | `Trigger.status.activePromotionRunCount` | Active attempt count for the trigger-managed Promotion. |
| `kapro_plugin_status_ready` | `Plugin.status.ready` | Plugin readiness. |
| `kapro_plugin_status_condition` | `Plugin.status.conditions[]` | Plugin probe status and failure reason. |

Installations must allow kube-state-metrics to list and watch these cluster
scoped CRDs:

- `promotionruns.kapro.io`
- `triggers.kapro.io`
- `plugins.kapro.io`

When using the kube-state-metrics Helm chart, mount the example as custom
resource state configuration and add matching `rbac.extraRules` for
`apiGroups: ["kapro.io"]`, `resources:
["promotionruns", "triggers", "plugins"]`, and `verbs:
["list", "watch"]`. The exact chart values vary by chart version, so keep the
example file as the source of the metric names used by the dashboard and rules.

## Alert Coverage

The PrometheusRule example includes alert expressions for:

- controller reconcile errors;
- controller reconcile p95 latency;
- gate failure ratio using `kapro_gate_evaluations_total`;
- rollout stage p95 duration using `kapro_stage_duration_seconds`;
- CloudEvents sink p99 dispatch latency using
  `kapro_lifecycle_hook_duration_seconds`;
- promotionrun stuck detection using `PromotionRun` phase and age from kube-state-metrics;
- plugin probe failures using `Plugin` readiness conditions from
  kube-state-metrics;
- blocked `Trigger` state using cooldown, max-active, source,
  signature, and Promotion update condition reasons from kube-state-metrics.
- sustained FleetDriftReport `Drifted`, `Unknown`, `Failed`, and `Pending`
  phases using first-class Kapro metrics.

These alerts are examples, not universal SLOs. Tune thresholds to your promotionrun
cadence, cluster count, and expected gate retry behavior.

## Runbook Mapping

Use alerts as routing signals, then follow the operational runbooks in
`docs/operations.md`.

| Alert | Primary runbook | Main data sources |
| --- | --- | --- |
| `KaproPromotionRunStuck` | Stuck PromotionRun | `PromotionRun.status`, `Target.status`, Events, dashboard promotionrun panels |
| `KaproGateFailureRateHigh` | Gate Failure | `Target.status.gates[]`, `kapro_gate_evaluations_total`, backend telemetry |
| `KaproPluginProbeFailure` / `KaproPluginProbeFailures` | Plugin Not Ready | `Plugin.status`, plugin probe metrics, operator logs |
| `KaproTriggerBlocked` | Blocked Trigger | `Trigger.status.conditions`, active attempts, OCI source health |
| `KaproRolloutDurationP95High` | Stuck PromotionRun or scalability review | stage duration histogram, stage `maxParallel`, backend latency |
| `KaproLifecycleSinkP99High` | First Response | lifecycle hook duration histogram, sink endpoint logs, retry/backoff settings |
| `KaproControllerReconcileErrors` | First Response | controller logs, status write metrics, Kubernetes Events |
| `KaproFleetDriftDetected` / `KaproFleetDriftSignalsIncomplete` / `KaproFleetDriftReportFailed` / `KaproFleetDriftReportPending` | Fleet Drift | FleetDriftReport status, drift metrics, Target and Cluster status |

Alert names differ slightly between the generic alert rules and the Prometheus
Operator examples, but they intentionally route to the same runbooks.

## Remaining First-Class Metric Gaps

The kube-state-metrics rules are intended to cover object-state alerts that are
not first-class Kapro metrics yet. Prefer adding controller-owned Kapro metrics
for these signals over parsing logs or relying on environment-specific queries.

| Operational question | Future metric |
| --- | --- |
| PromotionRun is stuck in a non-terminal state past its expected timeout. | `kapro_promotionrun_stuck_total{reason,plan,stage}` or a promotionrun age gauge by phase. |
| Trigger is blocked by cooldown, max-active, signature, or source errors. | `kapro_trigger_status_condition{type,reason}` until a controller-owned blocked counter exists. |
| Active promotionrun count by namespace or shard. | Extend `kapro_promotionrun_active_total` with bounded labels or emit per-shard gauges. |

When these metrics are implemented, update the concrete alert rules to use the
controller-owned metrics where they provide a stronger signal than inferred
Kubernetes object state.
