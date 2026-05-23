# Observability

Kapro treats metrics, audit traces, and distributed tracing as separate but
linked surfaces:

- Prometheus metrics answer "how often and how long?"
- `DecisionTrace` records answer "why did the controller decide this?"
- OpenTelemetry spans answer "which execution path produced this result?"

## OpenTelemetry Spans

Kapro emits spans through the global OpenTelemetry provider. Installations that
do not configure a provider keep the default no-op behavior.

| Span name | Package | Attributes |
| --- | --- | --- |
| `kapro.predicate.evaluate` | `pkg/kapro/gate` | `kapro.predicate.name`, `kapro.fleet`, `kapro.promotion`, `kapro.promotionrun`, `kapro.plan`, `kapro.stage`, `kapro.target`, `kapro.version`, `kapro.predicate.phase`, `kapro.predicate.reason` |
| `kapro.decisiontrace.emit` | `internal/decisiontrace` | `kapro.promotionrun`, `kapro.plan`, `kapro.stage`, `kapro.target`, `kapro.decisiontrace.event_type`, `kapro.decisiontrace.source`, `kapro.decisiontrace.phase`, `kapro.decisiontrace.reason` |
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

Current tracing covers SDK gate predicates, DecisionTrace emission, and operator
plugin gRPC clients. Spoke delivery loops, CSR bootstrap, and every actuator
operation should gain first-class spans before Kapro is treated as production
complete for cross-process debugging.
