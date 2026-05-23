# Plugin Authoring

Kapro plugins implement one narrow extension contract:

- KAI: actuator plugins apply a version and report convergence.
- KGI: gate plugins decide whether a target may advance.
- KPI: planner plugins filter and order targets before binding.

Built-in actuators, gates, and planners remain the default execution path.
External plugin runtime dispatch is an API preview and is enabled explicitly
with `KAPRO_ENABLE_PLUGIN_GATEWAY=true`. When enabled, the operator loads ready
actuator, gate, and planner `Plugin` objects after readiness probes succeed.
Later readiness changes hot-load updated plugins and unload plugins that become
stale, incompatible, or deleted.

Notifications are not a plugin contract. The runtime notification path is
inline gate configuration; Kapro does not expose separate public notification
provider/policy CRDs in the KISS API.

## Ecosystem Labels

Kapro uses two plugin ecosystem labels:

| Label | Meaning | Status |
|---|---|---|
| Kapro-compatible plugin | Implements one supported KAI, KGI, or KPI contract, reports the matching `contract_version`, passes the relevant base conformance harness, and documents runtime assumptions. | Available now |
| Certified Kapro plugin | Reserved for a later certification process. Do not use this label yet. | Reserved |

Third-party authors can claim Kapro-compatible when the published contract and
conformance requirements are met. Do not describe a plugin as certified until a
certification process exists.

## Contracts

| Contract | Proto | Go package |
|---|---|---|
| KAI actuator | `spec/kai/v1alpha1/actuator.proto` | `kapro.io/kapro/spec/kai/v1alpha1` |
| KGI gate | `spec/kgi/v1alpha1/gate.proto` | `kapro.io/kapro/spec/kgi/v1alpha1` |
| KPI planner | `spec/kpi/v1alpha1/planner.proto` | `kapro.io/kapro/spec/kpi/v1alpha1` |

Compatibility is based on the `contract_version` returned by
`GetCapabilities`, not the plugin implementation version.

For trusted in-process actuator substrates, use the stable SDK package
`kapro.io/kapro/pkg/kapro/actuator`. The legacy
`kapro.io/kapro/pkg/actuator` import path remains as a v0.2.x compatibility
bridge, but new plugins and custom operator binaries should not depend on it.
Register in-process substrates with `server.RegisterActuator(...)` or append to
`server.DefaultActuatorRegistrars()` before calling `server.New`.

## Compatibility Matrix

| Plugin type | Contract | Supported versions | Conformance package | Example |
|---|---|---|---|---|
| `actuator` | KAI | `v1alpha1` | `conformance/actuator` | `examples/plugins/argocd-actuator` |
| `gate` | KGI | `v1alpha1` | `conformance/gate` | `examples/plugins/slo-gate` |
| `planner` | KPI | `v1alpha1` | `conformance/planner` | `examples/plugins/capacity-planner` |

The supported versions are also defined in
`pkg/plugincompat/compatibility.go`.

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

The actuator contract has a dedicated transport and capability guide at
`docs/actuator-plugin-contract.md`.

## Conformance CLI

External authors can run the same base harnesses against a live gRPC plugin
with `kapro-conformance`:

```bash
go run ./cmd/kapro-conformance actuator --endpoint localhost:9090
go run ./cmd/kapro-conformance gate --endpoint localhost:9090
go run ./cmd/kapro-conformance planner --endpoint localhost:9090
```

Build the binary when testing from a checkout:

```bash
go build ./cmd/kapro-conformance
```

Use repeated `--param key=value` flags for plugin-specific scenario parameters.
For the Argo CD actuator example:

```bash
go run ./cmd/kapro-conformance actuator \
  --endpoint localhost:9090 \
  --param argocdNamespace=argocd \
  --param application=checkout
```

The actuator conformance scenario calls `Apply` twice, `IsConverged` twice, and
`Rollback` twice. Use isolated backend resources because the plugin may perform
real backend mutations while proving idempotency.

## Plugin Manifests

Plugins are declared with `Plugin`.

```yaml
apiVersion: kapro.io/v1alpha2
kind: Plugin
metadata:
  name: argocd-actuator
spec:
  type: actuator
  name: argo/pull
  protocol: grpc
  endpoint: dns:///argocd-actuator.kapro-system.svc:9090
  timeout: 10s
```

`Plugin` is an API preview. Runtime use requires
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`. Actuator, gate, and planner
plugins with `status.ready=true` and fresh `status.observedGeneration` are
loaded into runtime registries.

Only platform administrators should create or update `Plugin`
objects. A plugin endpoint can influence deployment execution or gate decisions,
so plugin ownership is part of the platform trust boundary. Production plugins
should run behind TLS, use least-privilege Kubernetes RBAC for their backend, and
store client certificates or CA data in platform-owned Secrets. See
`docs/rbac-tenancy.md` for the RBAC and tenancy model.

When a plugin omits `contract_version` or reports an unsupported version,
`status.ready` is false, `Ready=False`, `Compatible=False`, and the condition
message lists the supported contract versions.

TLS is configured with a namespaced Secret reference because
`Plugin` is cluster-scoped:

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

KAI conformance enforces context cancellation for `Apply`, so plugin tests
should run against isolated resources that can tolerate failed conformance
attempts.

An in-process actuator substrate should publish matching
`actuator.Capabilities` metadata for `Backend.spec.driver`,
`Backend.spec.adapter`, `Backend.spec.runtime`, and supported delivery modes.
Built-in Argo CD, Flux, OCI, and pull-mode bridges are exposed through the
server registrar functions, so external authors do not need to import
`internal/...` packages to compose the reference operator behavior.

Run the base actuator conformance harness from your plugin tests:

```go
func TestKAIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kaiv1alpha1.NewActuatorServiceClient(conn)
    actuatorconformance.Run(t, client, actuatorconformance.DefaultScenario())
}
```

A complete external actuator example is available in
`examples/plugins/argocd-actuator`, with a sample `Plugin` manifest at
`examples/plugins/argocd-actuator-registration.yaml`. It implements KAI for
Argo CD Applications by patching `spec.source.targetRevision` and checking
Argo CD sync and health status for convergence. Argo CD is the first external
substrate proof for the actuator plugin axis.
The example uses only public Kapro packages: `spec/kai/v1alpha1`,
`pkg/plugincompat`, and the test-only `conformance/actuator` harness.
`examples/plugins/argocd-applicationset-actuator`, with a sample `Plugin`
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
`examples/plugins/slo-gate`, with a sample `Plugin` manifest at
`examples/plugins/slo-gate-registration.yaml`. It implements KGI for SLO checks
using static values or Prometheus instant queries. Reference a runtime gate
plugin from a gate template with `type: plugin` and `plugin.name` set to
`Plugin.spec.name`.

## Planner Requirements

A planner plugin must:

- implement `GetCapabilities` and return `contractVersion: v1alpha1`;
- return one decision per target it wants to include, skip, or defer;
- keep responses deterministic for the same request;
- not create or mutate `Target` objects;
- respect request context cancellation;
- leave binding, retries, and failure policy decisions to Kapro.

Register planner plugins with `spec.type: planner`. Kapro probes ready planner
plugins and records their status. Built-in planning remains the runtime
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
`examples/plugins/capacity-planner`, with a sample `Plugin` manifest at
`examples/plugins/capacity-planner-registration.yaml`. It implements KPI for
capacity-aware filtering, ordering, and deferring promotion targets.

## Example Catalog

| Example | Contract | `Plugin` manifest | Runtime status |
|---|---|---|---|
| `examples/plugins/argocd-actuator` | KAI actuator | `examples/plugins/argocd-actuator-registration.yaml` | Hot-loaded dispatch preview |
| `examples/plugins/argocd-applicationset-actuator` | KAI actuator | `examples/plugins/argocd-applicationset-actuator-registration.yaml` | Hot-loaded dispatch preview |
| `examples/plugins/slo-gate` | KGI gate | `examples/plugins/slo-gate-registration.yaml` | Hot-loaded dispatch preview |
| `examples/plugins/capacity-planner` | KPI planner | `examples/plugins/capacity-planner-registration.yaml` | Hot-loaded planner dispatch preview |

## Conformance Rules

Run the matching conformance harness in the plugin repository before publishing
a `Plugin`, either through Go tests or with `cmd/kapro-conformance`. Use
deterministic inputs, immutable versions, stable target names, and isolated
backend resources. Do not point conformance tests at shared production systems.

The harnesses check the base contract:

- KAI: idempotent `Apply`, idempotent `Rollback`, deterministic convergence.
- KGI: valid phases, no request mutation, gRPC errors for unsupported work.
- KPI: capabilities, empty target handling, deterministic planning, valid
  target decisions, no request mutation, and context cancellation.

Add plugin-specific tests for authentication, backend permissions, timeouts,
retryable and terminal failures, cleanup, rollback, and concurrency. Passing
the conformance package means the plugin obeys the Kapro contract; it does not
prove backend-specific production readiness.

When a new contract version is added, update `pkg/plugincompat`, this matrix,
and the matching conformance harness in the same change.

## Package Imports

```go
import (
    actuatorconformance "kapro.io/kapro/conformance/actuator"
    gateconformance "kapro.io/kapro/conformance/gate"
    plannerconformance "kapro.io/kapro/conformance/planner"
    actuator "kapro.io/kapro/pkg/kapro/actuator"
    kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
    kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
    kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"
)
```
