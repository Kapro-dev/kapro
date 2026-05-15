# Kapro Monitoring Assets

This directory contains generic monitoring assets that can be imported or
adapted without assuming a specific Prometheus Operator installation.

| File | Purpose |
| --- | --- |
| `prometheus/kapro-alerts.yaml` | Prometheus alert groups for Kapro controller, release, gate, plugin, trigger, and rollout duration signals. |
| `grafana/kapro-operations-dashboard.json` | Compact Grafana dashboard for the Kapro operations metric surface. |

For installable Prometheus Operator and kube-state-metrics examples, see
`examples/monitoring/`. For metric names, alert coverage, and runbook mapping,
see `docs/monitoring.md` and `docs/operations.md`.
