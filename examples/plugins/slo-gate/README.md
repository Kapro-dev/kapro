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
| `prometheus` | Executes a Prometheus instant query and reads a scalar or exactly one vector result. |

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

The standalone manifest is `examples/plugins/slo-gate-registration.yaml`.

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
    query: sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))
    metric: error_rate
    threshold: "0.05"
    operator: lte
```

The registration makes the plugin available to the runtime plugin registry as
`slo`. Reference it from a gate template with `type: plugin`:

```yaml
gate:
  templates:
    - name: error-rate
      type: plugin
      plugin:
        name: slo
      args:
        - name: query
          value: sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))
        - name: threshold
          value: "0.05"
        - name: operator
          value: lte
```

Enable runtime plugin loading in the Kapro operator with:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

The operator only registers `PluginRegistration` objects that are already
`status.ready=true` with a fresh `observedGeneration` when the operator starts.
Apply the `PluginRegistration`, wait for the readiness probe to mark it ready,
then start or restart the Kapro operator. Registrations are not hot-loaded after
startup.

## Verify

```bash
go test ./examples/plugins/slo-gate
```

The test suite runs the shared KGI conformance harness and provider-specific
tests.
