# Adapter Capabilities

Kapro adapters and actuators publish capability metadata so controllers can
choose a supported path before invoking a substrate operation. Unsupported
operations should be explicit metadata, not routine runtime errors.

## Adapter Bits

`pkg/kapro/adapter.Capabilities` is returned by every public SDK adapter:

| Bit | Meaning |
|---|---|
| `SupportsApply` | The adapter can move a target toward a requested version. |
| `SupportsObserve` | The adapter can report convergence without changing substrate state. |
| `SupportsRollback` | The adapter has a direct rollback operation. |
| `SupportsDiscover` | The adapter can discover substrate-native objects for existing GitOps adoption. |
| `SupportsDryRun` | The adapter can validate an operation without persisting changes. |
| `SupportsSubstrateIO` | The adapter can surface substrate-native object status for Target status. |

Reference Argo CD and Flux adapters currently advertise discovery support only.
OCI advertises discovery unsupported because OCI delivery has no substrate-native
Kubernetes objects to discover.

## Actuator Bits

`pkg/kapro/actuator.Capabilities` is stored with each runtime actuator
registration:

| Bit | Meaning |
|---|---|
| `SupportsApply` | The actuator can apply one artifact version. |
| `SupportsDelta` | The actuator can apply a multi-artifact desired-version map directly. |
| `SupportsObserve` / `SupportsConvergence` | The actuator can poll target convergence after apply. |
| `SupportsRollback` | The actuator can perform direct rollback before the rollback target re-applies prior versions. |
| `SupportsTwoPhase` | The actuator implements the optional `TwoPhaseStaging` SDK extension for prepare, commit, and discard. |
| `SupportsSubstrateObjects` | The actuator can report substrate-native objects into target status. |
| `SupportsDryRun` | The actuator can validate without making substrate changes. |

Registrations created through the legacy `Register` and `Upsert` helpers have
no support bits. Controllers treat that as pre-capability full support so older
in-process binaries keep their existing behavior. New substrates should use
`RegisterRegistration` or `UpsertRegistration` and set explicit bits.

## Controller Behavior

The target controller now branches on actuator capabilities:

- no `SupportsApply`: the target fails before any substrate call;
- no `SupportsDelta`: multi-artifact delivery falls back to one `Apply` call
  per changed app key;
- no observe/convergence support: the controller trusts a successful apply and
  marks the target converged;
- substrate object status is collected only when `SupportsSubstrateObjects` is set;
- unsupported direct rollback is skipped and the generated rollback target
  re-applies the previous desired versions.

Plugin actuators derive these bits from the probed plugin status capabilities
and register them with the actuator registry. `SupportsTwoPhase` is currently a
Go SDK extension bit; gRPC actuator plugins should not advertise it until the
KAI wire contract grows explicit staged-apply RPCs. OCI pull delivery already
performs two-phase server-side apply on the spoke and reports diagnostics in
`Cluster.status.delivery[app].staging`, but the hub-side pull actuator does not
implement `Prepare`, `Commit`, or `Discard`.
