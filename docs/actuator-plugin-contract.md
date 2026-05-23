# Actuator Plugin Contract

Kapro has one actuator contract with two transports:

- in-process substrates implement `kapro.io/kapro/pkg/kapro/actuator`;
- out-of-process plugins implement the gRPC KAI service in
  `kapro.io/kapro/spec/kai/v1alpha1`.

Use in-process registration when you own the operator binary and want direct Go
composition. Use gRPC plugins when the substrate should ship, scale, upgrade,
or hold credentials independently from the operator.

## Version

KAI v1alpha1 plugins return `contract_version: v1alpha1` from
`GetCapabilities`. Kapro rejects actuator plugins with an empty or unsupported
contract version before they enter the runtime registry.

Plugin implementation versions are separate. `plugin_version` is recorded for
operators but is not used for compatibility decisions.

## Capabilities

KAI v1alpha1 publishes canonical capability names in
`spec/kai/v1alpha1`:

| Capability | Meaning |
|---|---|
| `apply` | Plugin can apply one artifact version to one target. |
| `convergence` | Plugin can report convergence for an applied version. |
| `observe` | Alias-style capability accepted for convergence reporting. |
| `rollback` | Plugin can directly roll a target back to a previous version. |
| `delta` | Plugin can apply multi-artifact desired-version deltas directly. |
| `backendobjects` | Plugin can report backend-native object status. |
| `dry-run` | Plugin can validate without persisting backend changes. |

Capability names may be plain or vendor-qualified. For example, both `apply`
and `argocd.application.targetRevision.apply` satisfy the base apply
capability.

The base actuator conformance suite requires `apply`, convergence/observe, and
`rollback` because it exercises `Apply`, `IsConverged`, and `Rollback`.

## Request Ownership

Kapro owns PromotionRun ordering, retries, rollback intent, target status, and
failure policy. The actuator owns backend mutation and backend readiness checks.

KAI methods must be idempotent for the same request. They must also respect
request context cancellation so controller shutdowns and timed-out reconciles do
not leave plugin calls hanging.

## In-Process Mapping

The in-process SDK mirrors the same shape:

```go
type Actuator interface {
    Apply(context.Context, actuator.ApplyRequest) error
    IsConverged(context.Context, *v1alpha2.Cluster, string, string) (bool, error)
    Rollback(context.Context, *v1alpha2.Cluster, string, string) error
    ApplyDelta(context.Context, actuator.DeltaApplyRequest) (int, error)
    IsAllConverged(context.Context, *v1alpha2.Cluster, map[string]string) (bool, error)
}
```

Register in-process substrates with explicit `actuator.Capabilities` through
`server.RegisterActuator` or `Registry.RegisterRegistration`. Legacy
registrations without capability bits still work, but new substrates should
declare support bits so controllers can avoid unsupported calls.

## Conformance

Run the live gRPC conformance suite before publishing an external actuator:

```bash
go run ./cmd/kapro-conformance actuator --endpoint localhost:9090
```

Use `--param key=value` for backend-specific test resources. The suite calls
`Apply` twice, `IsConverged` twice, and `Rollback` twice, so point it at
isolated resources that tolerate idempotency checks.
