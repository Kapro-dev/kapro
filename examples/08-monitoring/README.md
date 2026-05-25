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

Apply the Kubernetes monitoring assets from the repository root:

```bash
kubectl apply -f examples/08-monitoring/prometheus-rules.yaml
kubectl apply -f examples/08-monitoring/kube-state-metrics-crd-config.yaml
```

Import the Grafana JSON dashboards through your Grafana UI or provisioning
system.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/08-monitoring/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/08-monitoring/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/08-monitoring --ignore-not-found
```
