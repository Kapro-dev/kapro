# Conformance Packages

Kapro ships conformance packages for external plugin authors. They verify the
minimum behavior required by the KAI, KGI, and KPI contracts before a plugin is
registered in a hub.

| Contract | Package | Service client | Contract focus |
|---|---|---|---|
| KAI actuator | `kapro.io/kapro/conformance/actuator` | `spec/kai/v1alpha1.ActuatorServiceClient` | Apply one version, report convergence, and accept rollback |
| KGI gate | `kapro.io/kapro/conformance/gate` | `spec/kgi/v1alpha1.GateServiceClient` | Decide whether one target may advance |
| KPI planner | `kapro.io/kapro/conformance/planner` | `spec/kpi/v1alpha1.PlannerServiceClient` | Filter, order, include, skip, or defer candidate targets |

The harnesses are Go packages, but the plugin under test can be written in any
language that serves the published gRPC contract. The test only needs a gRPC
client connection to a live plugin process.

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

## Author Workflow

Plugin authors should use this workflow before publishing a
`PluginRegistration`:

1. Generate or update client and server stubs from the matching proto.
2. Implement `GetCapabilities` and return `contractVersion: v1alpha1`.
3. Start the plugin server in a test fixture with the same TLS, auth, timeout,
   and backend configuration shape used in production.
4. Run the matching conformance harness against the live gRPC endpoint.
5. Add backend-specific tests for permissions, retryable failures, terminal
   failures, and cleanup.
6. Publish the plugin image, the `PluginRegistration` manifest, and operational
   defaults together.

Do not mock the plugin implementation under test. It is fine to use a fake
backend for the default scenario, but the gRPC server and request handling path
should be the real plugin binary or real service implementation.

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

Scenario inputs should avoid wall-clock dependent decisions unless the test
controls the clock. If a backend uses eventual consistency, the scenario should
wait outside the conformance call and then invoke the harness against a known
state. The harness expects a single request to have a deterministic result.

Plugin-specific tests should cover:

- authentication failures;
- backend timeout and retry behavior;
- unavailable or missing target resources;
- rollback from the current version to the previous version;
- concurrent requests for different targets;
- request cancellation while the backend operation is in flight;
- cleanup of any resources created by the test.

The conformance packages are the minimum bar. Passing them means the plugin
obeys the published Kapro contract; it does not prove backend-specific
correctness, capacity, or production readiness. A plugin that passes the
matching suite and documents its runtime assumptions can be described as
Kapro-compatible; see `docs/plugin-compatibility.md`.

KAI and KGI contracts still require `GetCapabilities` and context cancellation
support. Until those checks are added to the base harnesses, plugin repositories
should cover them with plugin-specific tests. KPI conformance already checks
capabilities and cancelled contexts.

## CI Expectations

Run conformance in the plugin repository on every pull request that changes:

- proto-generated code or transport adapters;
- request validation;
- backend client code;
- idempotency, convergence, planning, or gate decision logic;
- default parameters used by `PluginRegistration`.

For a release candidate, run conformance against the same container image and
configuration that will be published. Store the Kapro version, plugin image
digest, backend version, and test command in release notes so operators can
reproduce the result.

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

## Future Certified Plugin Story

The future certified plugin story builds on the same harnesses. A certified
plugin candidate is expected to provide:

- passing conformance output for each supported contract;
- a compatibility matrix for Kapro versions, contract versions, plugin image
  digests, and backend versions;
- a minimal `PluginRegistration` manifest and a production-oriented manifest
  with TLS and timeout settings;
- documentation for required backend permissions;
- documented idempotency, timeout, retry, and rollback behavior;
- known scale limits, including safe parallelism and backend quota assumptions.

Certification, when introduced, will be an interoperability signal. It will not
make Kapro core maintainers responsible for third-party backend outages,
permissions, or product-specific behavior.

## Limitations

The conformance packages intentionally do not test:

- backend-specific correctness beyond the request and response contract;
- throughput, latency, or quota behavior under load;
- TLS certificate rotation;
- multi-version plugin upgrade behavior;
- whether a plugin is appropriate for a particular regulated environment.

Operators should still review plugin source, permissions, manifests, and
runtime limits before allowing a plugin to control production releases.
