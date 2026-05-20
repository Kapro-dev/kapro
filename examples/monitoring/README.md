# Kapro Monitoring Examples

These examples show one way to wire Kapro into a Prometheus Operator and
Grafana stack.

| File | Purpose |
| --- | --- |
| `kapro-alerts.yaml` | Generic Prometheus alert rules for direct import or adaptation. |
| `kapro-operations-dashboard.json` | Compact Grafana dashboard for the core Kapro metrics endpoint. |
| `prometheus-rules.yaml` | PrometheusRule example for Kapro alerts. |
| `grafana-dashboard.json` | Grafana dashboard that uses Kapro metrics and kube-state-metrics CRD state metrics. |
| `kapro-lifecycle-dashboard.json` | Grafana dashboard for Promotion lifecycle hooks (CloudEvents sink + per-Promotion webhooks/events) and FleetCluster reachability. Pair with the operations dashboard for full coverage. |
| `kube-state-metrics-crd-config.yaml` | CustomResourceStateMetrics example for `PromotionRun`, `PromotionTrigger`, and `PluginRegistration` status. |

The PrometheusRule and full dashboard assume the CRD state metrics from
`kube-state-metrics-crd-config.yaml` are installed. Adjust namespaces, labels,
rule selectors, and dashboard datasource UIDs for your observability stack.

Runbooks for the alerts are documented in `docs/operations.md`; the metric
inventory and alert mapping are documented in `docs/monitoring.md`.
