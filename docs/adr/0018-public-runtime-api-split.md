# ADR-0018: Public And Runtime API Split

## Status
Accepted

## Context

Kapro's first public preview needs a GitOps-friendly API boundary. Users should
commit durable desired state to Git, while Kapro-owned execution records should
remain observable runtime state. Keeping both surfaces in one API group makes it
too easy for GitOps reconcilers, humans, or backup tooling to treat runtime
objects as desired state.

The project is still pre-public-preview. There are no production users who need
live conversion webhooks for the old shape, so this is the right window for a
big-bang cleanup.

## Decision

Kapro splits the API into two groups:

- `kapro.io/v1alpha1` for user-authored desired state.
- `runtime.kapro.io/v1alpha1` for Kapro-owned runtime records.

The Go package paths are Kapro-qualified to avoid colliding with Kubernetes'
`runtime` package:

- `api/kapro/v1alpha1`
- `api/kaproruntime/v1alpha1`

The public group keeps the meaningful authoring CRDs:

- `SubstrateDiscoveryPolicy`
- `Approval`
- `Cluster`
- `ClusterTemplate`
- `Fleet`
- `Plan`
- `Plugin`
- `Policy`
- `Promotion`
- `Source`
- `Substrate`
- `SubstrateClass`
- `Trigger`

The runtime group owns:

- `DecisionTrace`
- `PromotionRun`
- `Target`

`PromotionRun` and `Target` are real runtime CRDs, not embedded-only structs.
The public package may define shared spec/status structs used by those runtime
CRDs, but it must not register public `PromotionRun` or `Target` roots.

`Backend` is renamed to `Substrate`, and delivery references become
`spec.substrate.ref`. The built-in Argo user-facing substrate name is
`argo`, matching `flux`; `argocd` remains a compatibility input where useful
for CLI profile normalization.

`GateExpression` and `FleetDriftReport` are removed from the first public
preview CRD set. They can return later only if they have a clear lifecycle and
contract. Runtime ideas such as `SubstrateDispatch`, `PromotionQueue`,
`PromotionTopology`, and `PromotionShard` stay internal Go concepts rather than
CRDs until users need to author or operate them directly.

## Rejected Alternatives

### Keep Runtime Objects In `kapro.io`

This keeps imports simpler but weakens the operational boundary. A GitOps
controller can accidentally apply `PromotionRun` or `Target` manifests as if
they were desired state, and RBAC cannot communicate authorship as clearly as
an API group split.

### Create CRDs For Queue, Topology, Dispatch, And Shard Concepts Now

These concepts are useful controller implementation patterns, but they do not
yet have independent user-authored lifecycle. Exposing them now would add API
surface without a stable contract. Kapro keeps them as internal structs first.

### Ship Conversion Webhooks For The Old Alpha Shape

There are no public-preview or production users depending on the old shape.
Maintaining conversion webhooks before the public contract is stable would
slow down the cleanup and leave migration code that nobody needs.

## Consequences

- GitOps systems can be configured to apply `kapro.io` desired state and ignore
  `runtime.kapro.io`.
- Human-write RBAC can exclude the runtime group by default.
- Backup tools can preserve public desired state and treat runtime records as
  regenerable or retention-managed state.
- Runtime schema evolution is easier because user manifests do not reference
  runtime objects directly.
- Existing local alpha manifests must be re-applied with the new group, kind,
  and field names. This is acceptable before public preview.
- `Plan` remains a public template with no controller or finalizer. Deleting a
  `Plan` does not garbage-collect runtime records; pending or in-flight
  `PromotionRun` objects that reference it fail with `PromotionPlanNotFound`.

## References

- ADR-0001: Promotion intent vs PromotionRun runtime split
- ADR-0009: Target is the PromotionRun per-target state authority
- ADR-0016: Substrate class and typed config contract
- ADR-0017: Promotion control plane for any delivery substrate
