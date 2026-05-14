# Plugin Authoring

Kapro plugins implement one narrow extension contract:

- KAI: actuator plugins apply a version and report convergence.
- KGI: gate plugins decide whether a target may advance.

The controller runtime still uses in-process registries today. The proto
contracts and conformance harnesses are provided now so plugin authors can build
against the stable API shape before `PluginGateway` runtime dispatch is wired.

## Contracts

| Contract | Proto | Go package |
|---|---|---|
| KAI actuator | `spec/kai/v1alpha1/actuator.proto` | `kapro.io/kapro/spec/kai/v1alpha1` |
| KGI gate | `spec/kgi/v1alpha1/gate.proto` | `kapro.io/kapro/spec/kgi/v1alpha1` |

Generate stubs with:

```bash
make proto
```

Verify committed stubs are current with:

```bash
make check-proto
```

## Registration

Plugins are declared with `PluginRegistration`.

```yaml
apiVersion: kapro.io/v1alpha1
kind: PluginRegistration
metadata:
  name: argocd-actuator
spec:
  type: actuator
  name: argo/pull
  protocol: grpc
  endpoint: dns:///argocd-actuator.kapro-system.svc:9090
  timeout: 10s
```

`PluginRegistration` is an API preview. It records the future runtime contract
for `PluginGateway`; it does not change the current in-process execution path.

## Actuator Requirements

An actuator plugin must:

- make `Apply` idempotent for the same version and target;
- make `Rollback` idempotent for the same previous version and target;
- return deterministic `IsConverged` results for the same backend state;
- respect request context cancellation;
- avoid storing release state outside the backend it controls.

Run the base actuator conformance harness from your plugin tests:

```go
func TestKAIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kaiv1alpha1.NewActuatorServiceClient(conn)
    actuatorconformance.Run(t, client, actuatorconformance.DefaultScenario())
}
```

## Gate Requirements

A gate plugin must:

- return one valid phase: `PASSED`, `FAILED`, `RUNNING`, or `INCONCLUSIVE`;
- not mutate the request object;
- respect request context cancellation;
- keep long-running state outside the plugin process or make it reconstructable;
- leave retry timing and failure policy decisions to Kapro.

Run the base gate conformance harness from your plugin tests:

```go
func TestKGIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kgiv1alpha1.NewGateServiceClient(conn)
    gateconformance.Run(t, client, gateconformance.DefaultScenario())
}
```

## Package Imports

```go
import (
    actuatorconformance "kapro.io/kapro/conformance/actuator"
    gateconformance "kapro.io/kapro/conformance/gate"
    kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
    kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
)
```

