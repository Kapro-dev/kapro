# SLO Gate Plugin

This example implements the Kapro Gate Interface for SLO-style promotion
checks. It runs as a gRPC server and returns a gate phase for one promotion target.

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
go run ./examples/05-plugins/03-slo-gate --listen :9090
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

The standalone manifest is `examples/05-plugins/03-slo-gate/registration.yaml`.

```yaml
apiVersion: kapro.io/v1alpha1
kind: Plugin
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
KAPRO_CONTROLLERS=fleet,plan,promotion,promotionrun,cluster,plugin
```

The operator hot-loads ready `Plugin` objects with a fresh
`observedGeneration` when the plugin gateway is enabled and the `plugin`
controller is running. Apply the `Plugin`, wait for the readiness probe to mark
it ready, then reference it from a gate template. Later readiness changes are
loaded without restarting the operator.

## Verify

```bash
go test ./examples/05-plugins/03-slo-gate
```

The test suite runs the shared KGI conformance harness and provider-specific
tests.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/05-plugins/03-slo-gate/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/05-plugins/03-slo-gate/run.sh test
examples/05-plugins/03-slo-gate/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

```bash
kubectl delete -f examples/05-plugins/03-slo-gate --ignore-not-found
```
