# Kapro Monitoring Examples

These examples show one way to wire Kapro into a Prometheus Operator and
Grafana stack.

| File | Purpose |
| --- | --- |
| `prometheus-rules.yaml` | PrometheusRule example for Kapro alerts. |
| `grafana-dashboard.json` | Grafana dashboard that uses Kapro metrics and kube-state-metrics CRD state metrics. |
| `kube-state-metrics-crd-config.yaml` | CustomResourceStateMetrics example for `Release`, `ReleaseTrigger`, and `PluginRegistration` status. |

The PrometheusRule and dashboard assume the CRD state metrics from
`kube-state-metrics-crd-config.yaml` are installed. Adjust namespaces, labels,
rule selectors, and dashboard datasource UIDs for your observability stack.

Runbooks for the alerts are documented in `docs/operations.md`; the metric
inventory and alert mapping are documented in `docs/monitoring.md`.
