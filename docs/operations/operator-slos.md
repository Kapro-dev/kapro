# Operator SLOs

This page defines the SLI signals Kapro operators can alert on in `v0.1.2`.
It intentionally separates first-class Kapro metrics from inferred signals that
come from kube-state-metrics or the Kubernetes API server. Do not alert on a
metric name unless it is listed in `internal/metrics/metrics.go` or exported by
your platform stack.

## Recommended SLIs

| SLI | Source | Query sketch | Suggested alert |
| --- | --- | --- | --- |
| Controller reconcile error rate | Kapro metric | `sum by (controller) (rate(kapro_controller_reconciles_total{result="error"}[10m]))` | Page only for sustained errors on production controllers; warn for any non-zero rate over 10m. |
| Controller reconcile p95 latency | Kapro metric | `histogram_quantile(0.95, sum by (controller, le) (rate(kapro_controller_reconcile_duration_seconds_bucket[10m])))` | Warn when p95 stays above 30s for 15m. Tune up for large fleets. |
| Status write failure rate | Kapro metric | `sum by (resource) (rate(kapro_controller_status_writes_total{result="error"}[10m]))` | Warn on sustained API write failures; check Kubernetes API throttling first. |
| Gate failure ratio | Kapro metric | `sum by (gate_type) (rate(kapro_gate_evaluations_total{result=~"failed|error"}[15m])) / clamp_min(sum by (gate_type) (rate(kapro_gate_evaluations_total[15m])), 0.001)` | Warn above 20% for 15m, then inspect target gate evidence. |
| Rollout stage p95 duration | Kapro metric | `histogram_quantile(0.95, sum by (plan, le) (rate(kapro_stage_duration_seconds_bucket[30m])))` | Warn above 1h for 30m unless the plan normally soaks longer. |
| Active PromotionRun backlog | Kapro metric | `kapro_promotionrun_active_total` | Warn when active runs exceed expected fleet concurrency for 30m. |
| PromotionRun stuck age | kube-state-metrics CRD metric | PromotionRun creation timestamp joined with non-terminal phase | Warn when non-terminal age exceeds the PromotionRun timeout or local rollout SLO. |
| CloudEvents sink dispatch p99 | Kapro metric | `histogram_quantile(0.99, sum by (kind, le) (rate(kapro_lifecycle_hook_duration_seconds_bucket{kind="Sink"}[10m])))` | Warn above 5s for 10m; page if event delivery is part of production automation. |
| Plugin probe failure rate | Kapro metric | `sum by (type, reason) (rate(kapro_plugin_probe_results_total{result!="success"}[10m]))` | Warn on sustained failures; inspect Plugin readiness and endpoint health. |
| Cluster heartbeat misses | Kapro metric | `max_over_time(kapro_cluster_heartbeat_misses[5m])` | Warn when a spoke cluster exceeds its configured miss threshold. |

Webhook admission handshakes do not have a Kapro-specific metric in `v0.1.2`.
For webhook availability, use Kubernetes API server admission metrics, webhook
configuration status, and operator logs. Add a Kapro metric only after its label
cardinality and failure semantics are specified.

## PrometheusRule Coverage

`examples/monitoring/prometheus-rules.yaml` includes recording rules for the
core SLI queries above and alert rules that route to `docs/operations.md`.
Rules that inspect PromotionRun, Trigger, or Plugin object state require the
kube-state-metrics custom-resource configuration in
`examples/monitoring/kube-state-metrics-crd-config.yaml`.

Validate monitoring examples with `make validate-yaml-json`. The check extracts
`spec.groups` from Prometheus Operator `PrometheusRule` manifests before running
`promtool check rules`, because `promtool` expects raw Prometheus rule groups
rather than Kubernetes CRD YAML.

## Triage Flow

1. Start with the alert annotation and the linked runbook in
   `docs/operations.md`.
2. Check whether the signal is first-class Kapro telemetry or inferred CRD state.
3. For gate and rollout alerts, inspect the owning `PromotionRun` and child
   `Target` objects before changing the current `Plan`; existing targets keep
   the gate snapshot they were created with.
4. For sink and plugin alerts, verify endpoint health before increasing
   timeouts. Longer timeouts can hide retry pressure and occupy reconcile
   capacity.

## Metric Gaps

These are useful future metrics, but they are not exported as first-class Kapro
metrics in `v0.1.2`:

| Gap | Current workaround |
| --- | --- |
| PromotionRun stamp-to-terminal duration as a single histogram | Use stage duration plus kube-state-metrics PromotionRun age and phase. |
| Trigger-to-PromotionRun attempt lag as a single histogram | Use Trigger condition state and active attempt count as blocked-trigger symptoms; the shipped kube-state-metrics config does not expose enough timestamps to compute true lag. |
| Admission webhook handshake failure rate with Kapro-owned labels | Use Kubernetes API server admission metrics and webhook logs. |

When a gap becomes a real metric, add it to `internal/metrics/metrics.go`,
document the exact labels here, update `docs/monitoring.md`, and add a
PrometheusRule expression that uses the first-class metric instead of inferred
object state.
