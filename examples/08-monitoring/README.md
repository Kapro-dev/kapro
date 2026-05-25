# Kapro Monitoring Examples

These examples show one way to wire Kapro into a Prometheus Operator and
Grafana stack.

| File | Purpose |
| --- | --- |
| `kapro-alerts.yaml` | Generic Prometheus alert rules for direct import or adaptation. |
| `kapro-operations-dashboard.json` | Compact Grafana dashboard for the core Kapro metrics endpoint. |
| `prometheus-rules.yaml` | PrometheusRule example for Kapro alerts. |
| `grafana-dashboard.json` | Grafana dashboard that uses Kapro metrics and kube-state-metrics CRD state metrics. |
| `kapro-lifecycle-dashboard.json` | Grafana dashboard for Promotion lifecycle hooks (CloudEvents sink + per-Promotion webhooks/events) and Cluster reachability. Pair with the operations dashboard for full coverage. |
| `kube-state-metrics-crd-config.yaml` | CustomResourceStateMetrics example for `PromotionRun`, `Trigger`, and `Plugin` status. |

The full dashboard assumes both the CRD state metrics from
`kube-state-metrics-crd-config.yaml` and the recording rules from
`prometheus-rules.yaml` are installed. Adjust namespaces, labels, rule
selectors, and dashboard datasource UIDs for your observability stack.

Runbooks for the alerts are documented in `docs/operations.md`; the metric
inventory and alert mapping are documented in `docs/monitoring.md`; SLI
definitions and recommended thresholds are documented in `docs/operator-slos.md`.
