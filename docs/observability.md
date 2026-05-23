# Observability

Kapro treats metrics, audit traces, and distributed tracing as separate but
linked surfaces:

- Prometheus metrics answer "how often and how long?"
- `DecisionTrace` records answer "why did the controller decide this?"
- OpenTelemetry spans answer "which execution path produced this result?"

## OpenTelemetry Spans

Kapro emits spans through the global OpenTelemetry provider. Installations that
do not configure a provider keep the default no-op behavior.

All `kapro.actuator.*` spans include the standard actuator identity attributes:
`kapro.actuator.name`, `kapro.actuator.contract_version`,
`kapro.actuator.driver`, `kapro.actuator.adapter`, and
`kapro.actuator.runtime`.

| Span name | Package | Attributes |
| --- | --- | --- |
| `kapro.predicate.evaluate` | `pkg/kapro/gate` | `kapro.predicate.name`, `kapro.fleet`, `kapro.promotion`, `kapro.promotionrun`, `kapro.plan`, `kapro.stage`, `kapro.target`, `kapro.version`, `kapro.predicate.phase`, `kapro.predicate.reason` |
| `kapro.decisiontrace.emit` | `internal/decisiontrace` | `kapro.promotionrun`, `kapro.plan`, `kapro.stage`, `kapro.target`, `kapro.decisiontrace.event_type`, `kapro.decisiontrace.source`, `kapro.decisiontrace.phase`, `kapro.decisiontrace.reason` |
| `kapro.actuator.apply` | `pkg/kapro/actuator` | `kapro.actuator.name`, `kapro.actuator.contract_version`, `kapro.actuator.driver`, `kapro.actuator.adapter`, `kapro.actuator.runtime`, `kapro.cluster`, `kapro.app_key`, `kapro.version`, `kapro.previous_version` |
| `kapro.actuator.observe` | `pkg/kapro/actuator` | `kapro.actuator.name`, `kapro.cluster`, `kapro.app_key`, `kapro.version`, `kapro.actuator.converged` |
| `kapro.actuator.rollback` | `pkg/kapro/actuator` | `kapro.actuator.name`, `kapro.cluster`, `kapro.app_key`, `kapro.previous_version` |
| `kapro.actuator.apply_delta` | `pkg/kapro/actuator` | `kapro.actuator.name`, `kapro.cluster`, `kapro.actuator.desired_versions`, `kapro.actuator.applied` |
| `kapro.actuator.observe_all` | `pkg/kapro/actuator` | `kapro.actuator.name`, `kapro.cluster`, `kapro.actuator.desired_versions`, `kapro.actuator.converged` |
| `kapro.actuator.backend_objects` | `pkg/kapro/actuator` | `kapro.actuator.name`, `kapro.cluster`, `kapro.actuator.desired_versions`, `kapro.actuator.backend_objects` |
| `kapro.spoke.delivery.tick` | `cmd/kapro-cluster-controller` | `kapro.cluster`, `kapro.desired_version_count`, `kapro.delivery.backend_ref`, `kapro.cluster.suspended`, `kapro.spoke.delivery.status_write` |
| `kapro.spoke.delivery.reconcile` | `cmd/kapro-cluster-controller` | `kapro.cluster`, `kapro.app_key`, `kapro.version`, `kapro.delivery.backend_ref`, `kapro.delivery.backend`, `kapro.delivery.driver`, `kapro.delivery.phase`, `kapro.delivery.result`, `kapro.delivery.format`, `kapro.delivery.observed_digest`, `kapro.delivery.applied_objects` |
| gRPC client spans | plugin transport | Standard `otelgrpc` client attributes for plugin probe and runtime calls. |

DecisionTrace emission spans are marked error when validation, object creation,
or signing fails. Gate predicate spans are marked error when evaluation returns
an error or a failed gate result.

## Plugin Trace Propagation

The in-operator plugin transport installs `otelgrpc.NewClientHandler()` on every
plugin dial option returned by `internal/plugin/transport.DialOptions`. That
means plugin probe and runtime calls can carry trace context to external plugin
servers when both sides use OpenTelemetry-aware gRPC handlers.

Plugin authors should register the matching server handler:

```go
grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
```

This keeps plugin spans connected to controller reconcile, gate, and
DecisionTrace spans in a single trace tree.

## Boundaries

Current tracing covers SDK gate predicates, hub-side actuator calls,
DecisionTrace emission, spoke delivery reconciliation, and operator plugin gRPC
clients. CSR bootstrap and deeper per-object apply spans should gain
first-class coverage before Kapro is treated as production complete for
cross-process debugging.
