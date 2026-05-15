# SLO Gate Plugin

This example implements the Kapro Gate Interface for SLO-style promotion
checks. It runs as a gRPC server and returns a gate phase for one release target.

## Behavior

The plugin compares one observed value with one threshold:

```text
value <operator> threshold
```

Supported operators are `lt`, `lte`, `gt`, `gte`, and `eq`. Symbol aliases
`<`, `<=`, `>`, `>=`, and `==` are also accepted.

The plugin supports two providers:

| Provider | Purpose |
|---|---|
| `static` | Reads the value from `parameters.value`. Useful for tests and simple webhooks. |
| `prometheus` | Executes a Prometheus instant query and reads the first returned value. |

Invalid gate configuration returns `INCONCLUSIVE`. Empty Prometheus results
return `RUNNING` so Kapro can retry according to the gate policy.

## Run

```bash
go run ./examples/plugins/slo-gate --listen :9090
```

## Parameters

| Name | Required | Purpose |
|---|---:|---|
| `provider` | No | `static` or `prometheus`. Defaults to `static`. |
| `metric` | No | Metric label used in response messages. |
| `threshold` | Yes | Numeric threshold. |
| `operator` | No | Comparison operator. Defaults to `lte`. |
| `value` | For `static` | Static numeric value. |
| `prometheusURL` | For `prometheus` | Base Prometheus URL. |
| `query` | For `prometheus` | Prometheus instant query. |

## Registration

```yaml
apiVersion: kapro.io/v1alpha1
kind: PluginRegistration
metadata:
  name: slo-gate
spec:
  type: gate
  name: slo
  protocol: grpc
  endpoint: dns:///slo-gate.kapro-system.svc:9090
  timeout: 5s
  parameters:
    provider: prometheus
    prometheusURL: http://prometheus.monitoring.svc:9090
    query: sum(rate(http_requests_total{status=~"5.."}[5m]))
    metric: error_rate
    threshold: "0.05"
    operator: lte
```

Enable runtime plugin loading in the Kapro operator with:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

## Verify

```bash
go test ./examples/plugins/slo-gate
```

The test suite runs the shared KGI conformance harness and provider-specific
tests.
