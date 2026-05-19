# Plugin Authoring

Kapro plugins implement one narrow extension contract:

- KAI: actuator plugins apply a version and report convergence.
- KGI: gate plugins decide whether a target may advance.
- KPI: planner plugins filter and order targets before binding.

Built-in actuators, gates, and planners remain the default execution path.
External plugin runtime registration is an API preview and is enabled explicitly
with `KAPRO_ENABLE_PLUGIN_GATEWAY=true`. When enabled, the operator registers
ready actuator, gate, and planner `PluginRegistration` objects after readiness
probes succeed. Later readiness changes hot-load updated registrations and
unload registrations that become stale, incompatible, or deleted.

Notifications are not a plugin contract. The runtime notification path is
inline gate configuration; Kapro does not expose separate public notification
provider/policy CRDs in the KISS API.

## Ecosystem Labels

Kapro uses two plugin ecosystem labels:

| Label | Meaning | Status |
|---|---|---|
| Kapro-compatible plugin | Implements one supported KAI, KGI, or KPI contract, reports the matching `contract_version`, passes the relevant base conformance harness, and documents runtime assumptions. | Available now |
| Certified Kapro plugin | Meets the compatible-plugin bar plus future project certification requirements such as provenance, support windows, upgrade testing, and operational limits. | Future work |

Third-party authors can claim Kapro-compatible when the published contract and
conformance requirements are met. Do not describe a plugin as certified until a
certification process exists. See `docs/plugin-compatibility.md` for the
current matrix and future certification story.

## Contracts

| Contract | Proto | Go package |
|---|---|---|
| KAI actuator | `spec/kai/v1alpha1/actuator.proto` | `kapro.io/kapro/spec/kai/v1alpha1` |
| KGI gate | `spec/kgi/v1alpha1/gate.proto` | `kapro.io/kapro/spec/kgi/v1alpha1` |
| KPI planner | `spec/kpi/v1alpha1/planner.proto` | `kapro.io/kapro/spec/kpi/v1alpha1` |

Compatibility is based on the `contract_version` returned by
`GetCapabilities`, not the plugin implementation version. See
`docs/plugin-compatibility.md` for the supported version matrix and probe
status policy.

Generate stubs with:

```bash
make proto
```

Verify committed stubs are current with:

```bash
make check-proto
```

See `docs/api-stability.md` for the compatibility policy that applies to these
contracts.

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

`PluginRegistration` is an API preview. Runtime use requires
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`. Actuator, gate, and planner
registrations with `status.ready=true` and fresh `status.observedGeneration` are
loaded into runtime registries.

Only platform administrators should create or update `PluginRegistration`
objects. A plugin endpoint can influence deployment execution or gate decisions,
so registration is part of the platform trust boundary. Production plugins
should run behind TLS, use least-privilege Kubernetes RBAC for their backend, and
store client certificates or CA data in platform-owned Secrets. See
`docs/rbac-tenancy.md` for the RBAC and tenancy model.

When a plugin omits `contract_version` or reports an unsupported version,
`status.ready` is false, `Ready=False`, `Compatible=False`, and the condition
message lists the supported contract versions.

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
- avoid storing PromotionRun state outside the backend it controls.

Run the base actuator conformance harness from your plugin tests:

```go
func TestKAIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kaiv1alpha1.NewActuatorServiceClient(conn)
    actuatorconformance.Run(t, client, actuatorconformance.DefaultScenario())
}
```

See `docs/conformance.md` for scenario rules and registration checks.

A complete external actuator example is available in
`examples/plugins/argocd-actuator`, with a sample registration manifest at
`examples/plugins/argocd-actuator-registration.yaml`. It implements KAI for
Argo CD Applications by patching `spec.source.targetRevision` and checking
Argo CD sync and health status for convergence.
`examples/plugins/argocd-applicationset-actuator`, with a sample registration
manifest at `examples/plugins/argocd-applicationset-actuator-registration.yaml`,
implements the ApplicationSet-based `argo/push` variant by patching
`spec.template.spec.source.targetRevision` and checking a generated
Application's sync and health status.

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
`examples/plugins/slo-gate`, with a sample registration manifest at
`examples/plugins/slo-gate-registration.yaml`. It implements KGI for SLO checks
using static values or Prometheus instant queries. Reference a runtime gate
plugin from a gate template with `type: plugin` and `plugin.name` set to
`PluginRegistration.spec.name`.

## Planner Requirements

A planner plugin must:

- implement `GetCapabilities` and return `contractVersion: v1alpha1`;
- return one decision per target it wants to include, skip, or defer;
- keep responses deterministic for the same request;
- not create or mutate `PromotionTarget` objects;
- respect request context cancellation;
- leave binding, retries, and failure policy decisions to Kapro.

Register planner plugins with `spec.type: planner`. Kapro probes ready planner
registrations and records their status. Built-in planning remains the runtime
dispatch path until external planner dispatch is implemented.

Run the base planner conformance harness from your plugin tests:

```go
func TestKPIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kpiv1alpha1.NewPlannerServiceClient(conn)
    plannerconformance.Run(t, client, plannerconformance.DefaultScenario())
}
```

A planner plugin implementation example is available in
`examples/plugins/capacity-planner`, with a sample registration manifest at
`examples/plugins/capacity-planner-registration.yaml`. It implements KPI for
capacity-aware filtering, ordering, and deferring promotion targets.

## Example Catalog

| Example | Contract | Registration manifest | Runtime status |
|---|---|---|---|
| `examples/plugins/argocd-actuator` | KAI actuator | `examples/plugins/argocd-actuator-registration.yaml` | Hot-loaded dispatch preview |
| `examples/plugins/argocd-applicationset-actuator` | KAI actuator | `examples/plugins/argocd-applicationset-actuator-registration.yaml` | Hot-loaded dispatch preview |
| `examples/plugins/slo-gate` | KGI gate | `examples/plugins/slo-gate-registration.yaml` | Hot-loaded dispatch preview |
| `examples/plugins/capacity-planner` | KPI planner | `examples/plugins/capacity-planner-registration.yaml` | Hot-loaded planner dispatch preview |

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
