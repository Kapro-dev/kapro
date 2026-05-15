# Plugin Authoring

Kapro plugins implement one narrow extension contract:

- KAI: actuator plugins apply a version and report convergence.
- KGI: gate plugins decide whether a target may advance.
- KPI: planner plugins filter and order targets before binding.

Built-in actuators, gates, and planners remain the default execution path.
External plugin runtime registration is an API preview and is enabled explicitly
with `KAPRO_ENABLE_PLUGIN_GATEWAY=true`. When enabled, the operator registers
ready actuator, gate, and planner `PluginRegistration` objects once at startup.
Dynamic hot reload is future work.

## Contracts

| Contract | Proto | Go package |
|---|---|---|
| KAI actuator | `spec/kai/v1alpha1/actuator.proto` | `kapro.io/kapro/spec/kai/v1alpha1` |
| KGI gate | `spec/kgi/v1alpha1/gate.proto` | `kapro.io/kapro/spec/kgi/v1alpha1` |
| KPI planner | `spec/kpi/v1alpha1/planner.proto` | `kapro.io/kapro/spec/kpi/v1alpha1` |

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

`PluginRegistration` is an API preview. Runtime use is startup-time only and
requires `KAPRO_ENABLE_PLUGIN_GATEWAY=true`. Actuator, gate, and planner
registrations with `status.ready=true` and fresh `status.observedGeneration` are
loaded into runtime registries. The operator builds the built-in planner stack
first, then appends external planner plugins in startup registration order.

TLS is configured with a namespaced Secret reference because
`PluginRegistration` is cluster-scoped:

```yaml
spec:
  tlsSecretRef:
    namespace: kapro-system
    name: argocd-actuator-tls
  parameters:
    tlsServerName: argocd-actuator.kapro-system.svc
```

The Secret may contain `ca.crt` for server verification and optionally
`tls.crt` plus `tls.key` for client certificate authentication.

## Actuator Requirements

An actuator plugin must:

- implement `GetCapabilities` and return `contractVersion: v1alpha1`;
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

A complete external actuator example is available in
`examples/plugins/argocd-actuator`. It implements KAI for Argo CD Applications
by patching `spec.source.targetRevision` and checking Argo CD sync and health
status for convergence.

## Gate Requirements

A gate plugin must:

- implement `GetCapabilities` and return `contractVersion: v1alpha1`;
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

A gate plugin implementation example is available in
`examples/plugins/slo-gate`. It implements KGI for SLO checks using static
values or Prometheus instant queries. Reference a runtime gate plugin from a
gate template with `type: plugin` and `plugin.name` set to
`PluginRegistration.spec.name`.

## Planner Requirements

A planner plugin must:

- implement `GetCapabilities` and return `contractVersion: v1alpha1`;
- return one decision per target it wants to include, skip, or defer;
- keep responses deterministic for the same request;
- not create or mutate `ReleaseTarget` objects;
- respect request context cancellation;
- leave binding, retries, and failure policy decisions to Kapro.

Register planner plugins with `spec.type: planner`. Kapro probes ready planner
registrations and records their status. With `KAPRO_ENABLE_PLUGIN_GATEWAY=true`,
ready planner registrations are called during stage planning. KPI `INCLUDE`
keeps a target eligible and contributes its score, `SKIP` records a skip reason,
and `DEFER` records a defer reason for the current planning cycle. Omitted
targets remain eligible with score `0`. Unsupported decisions, duplicate
targets, and unknown target names fail the planning cycle closed.

Planner plugins cannot bind releases directly. Kapro still applies
`Stage.spec.strategy.maxParallel`, creates `ReleaseTarget` objects, owns status,
and drives retries and failure policy.

Run the base planner conformance harness from your plugin tests:

```go
func TestKPIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kpiv1alpha1.NewPlannerServiceClient(conn)
    plannerconformance.Run(t, client, plannerconformance.DefaultScenario())
}
```

A planner plugin implementation example is available in
`examples/plugins/capacity-planner`. It implements KPI for capacity-aware
filtering, ordering, and deferring rollout targets.

## Package Imports

```go
import (
    actuatorconformance "kapro.io/kapro/conformance/actuator"
    gateconformance "kapro.io/kapro/conformance/gate"
    plannerconformance "kapro.io/kapro/conformance/planner"
    kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
    kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
    kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"
)
```
