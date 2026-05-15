# Conformance Packages

Kapro ships conformance packages for external plugin authors. They verify the
minimum behavior required by the KAI, KGI, and KPI contracts before a plugin is
registered in a hub.

| Contract | Package | Service client |
|---|---|---|
| KAI actuator | `kapro.io/kapro/conformance/actuator` | `spec/kai/v1alpha1.ActuatorServiceClient` |
| KGI gate | `kapro.io/kapro/conformance/gate` | `spec/kgi/v1alpha1.GateServiceClient` |
| KPI planner | `kapro.io/kapro/conformance/planner` | `spec/kpi/v1alpha1.PlannerServiceClient` |

## Required Use

Every external plugin test suite should include the matching conformance run:

```go
func TestKAIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kaiv1alpha1.NewActuatorServiceClient(conn)
    actuatorconformance.Run(t, client, actuatorconformance.DefaultScenario())
}
```

```go
func TestKGIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kgiv1alpha1.NewGateServiceClient(conn)
    gateconformance.Run(t, client, gateconformance.DefaultScenario())
}
```

```go
func TestKPIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kpiv1alpha1.NewPlannerServiceClient(conn)
    plannerconformance.Run(t, client, plannerconformance.DefaultScenario())
}
```

Use `DefaultScenario()` for the base contract check. Add backend-specific
scenarios for real resources, authentication, timeout behavior, and failure
paths.

## What The Harnesses Check

KAI actuator conformance verifies:

- `Apply` accepts the same request more than once.
- `Rollback` accepts the same request more than once.
- `IsConverged` returns deterministic responses for identical observed state.

KGI gate conformance verifies:

- `Evaluate` returns a valid phase.
- `Evaluate` does not mutate the request object.
- The plugin reports errors through gRPC instead of inventing unsupported
  phases.

KPI planner conformance verifies:

- `GetCapabilities` returns `contractVersion: v1alpha1`.
- Empty target lists are accepted.
- Planning is deterministic for the same request.
- Planning does not mutate the request object.
- Returned targets are drawn from the request and have valid decisions.
- Cancelled contexts are honored.

## Scenario Rules

Conformance scenarios must be deterministic. Use immutable versions, stable
target names, and isolated backend resources. Do not point conformance tests at
shared production systems.

Plugin-specific tests should cover:

- authentication failures;
- backend timeout and retry behavior;
- unavailable or missing target resources;
- rollback from the current version to the previous version;
- cleanup of any resources created by the test.

The conformance packages are the minimum bar. Passing them means the plugin
obeys the published Kapro contract; it does not prove backend-specific
correctness, capacity, or production readiness.

## Registration Gate

Before a plugin is used by a hub:

1. Run the matching conformance harness in the plugin repository.
2. Build and publish the plugin image.
3. Apply a `PluginRegistration` with `spec.type` set to `actuator`, `gate`, or
   `planner`.
4. Wait for the registration controller to set `status.ready=true` and
   `status.observedGeneration` equal to `metadata.generation`.
5. Restart the operator with `KAPRO_ENABLE_PLUGIN_GATEWAY=true` for startup-time
   actuator and gate registration.

Planner registrations are probed and reported in status. Runtime planner
dispatch remains future work.
