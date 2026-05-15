# Plugin Conformance

Kapro provides Go conformance harnesses for external plugin authors. The goal is
to make KAI, KGI, and KPI behavior testable without requiring a full Kapro
controller or cluster.

## Packages

| Contract | Harness package | Service |
|---|---|---|
| KAI actuator | `kapro.io/kapro/conformance/actuator` | `ActuatorService` |
| KGI gate | `kapro.io/kapro/conformance/gate` | `GateService` |
| KPI planner | `kapro.io/kapro/conformance/planner` | `PlannerService` |

Each harness accepts a generated gRPC client and a scenario. The default
scenario checks the contract version, deterministic behavior, valid responses,
request immutability, and context cancellation where applicable.

## Actuator Example

```go
func TestKAIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kaiv1alpha1.NewActuatorServiceClient(conn)
    actuatorconformance.Run(t, client, actuatorconformance.DefaultScenario())
}
```

The actuator harness expects:

- `GetCapabilities` returns `contractVersion: v1alpha1`;
- `Apply` is idempotent;
- `Rollback` is idempotent;
- `IsConverged` returns a normalized convergence result;
- canceled contexts are respected.

## Gate Example

```go
func TestKGIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kgiv1alpha1.NewGateServiceClient(conn)
    gateconformance.Run(t, client, gateconformance.DefaultScenario())
}
```

The gate harness expects:

- `GetCapabilities` returns `contractVersion: v1alpha1`;
- `Evaluate` returns a valid gate phase;
- the request is not mutated;
- canceled contexts are respected.

## Planner Example

```go
func TestKPIConformance(t *testing.T) {
    conn := dialPlugin(t)
    client := kpiv1alpha1.NewPlannerServiceClient(conn)
    plannerconformance.Run(t, client, plannerconformance.DefaultScenario())
}
```

The planner harness expects:

- `GetCapabilities` returns `contractVersion: v1alpha1`;
- empty target lists return empty plans;
- the same request produces the same response;
- returned targets exist in the request and are not duplicated;
- each target has a valid include, skip, or defer decision;
- canceled contexts are respected.

## Running From a Plugin Repo

Add Kapro as a Go module dependency, import the relevant conformance package,
and run normal Go tests:

```bash
go test ./...
```

Plugin authors should run conformance in CI for every change that touches the
plugin server, request mapping, backend client, or capability response.

## Certified Plugin Story

The current harness is a base compatibility check, not a certification program.
A future certified-plugin process should add:

- published test results for a specific plugin version;
- signed plugin images;
- documented least-privilege RBAC;
- supported Kapro and KAI/KGI/KPI contract versions;
- operational metrics and dashboards for the plugin itself;
- security review of credential handling and webhook behavior.

Until certification exists, platform teams should treat conformance as the
minimum bar before allowing a plugin to register in production.
