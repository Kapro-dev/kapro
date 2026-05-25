# ADR-0019: DeliveryUnit Authoring Layer

## Status
Accepted

## Context

Kapro's pre-preview API still had two competing aggregate roots. `Fleet` carried
source, delivery, cluster, and plan intent, while `Promotion` created runtime
attempts from that mixed shape. Existing GitOps adoption also generated
top-level `Source` manifests, even though source mappings are app/workload
intent rather than a standalone lifecycle most users should manage directly.

Before public preview, we can make one breaking structural migration without
supporting conversion webhooks or compatibility aliases. This is the right
window to align the API with Kubernetes' Service/EndpointSlice and
Deployment/ReplicaSet pattern: users author the durable top-level object, and
controllers derive lower-level machinery.

## Decision

Kapro introduces `DeliveryUnit` as the canonical user-authored app/workload
delivery definition.

`DeliveryUnit` owns:

- source mapping intent in `spec.source`;
- optional trigger intent in `spec.triggers`;
- default `fleetRef` and `planRef` for generated promotions;
- stable labels, especially `kapro.io/unit`.

The DeliveryUnit controller derives:

- `Source` from `DeliveryUnit.spec.source`;
- `Trigger` from `DeliveryUnit.spec.triggers[]`.

`Source` and `Trigger` remain Kubernetes-visible CRDs because users and
operators need to inspect them, but they are controller-derived by default.
They are comparable to `EndpointSlice` or `ReplicaSet`: real API objects with
status and operational value, not primary Git-authored intent.

`Fleet` moves toward target-set semantics. It describes the clusters and
delivery defaults for those clusters. It must not be the primary owner of
source mapping intent.

`Promotion` remains an explicit user-authored action. Changing
`DeliveryUnit.spec.source` or a future `DeliveryUnit.spec.version` must not
silently deploy. A rollout still starts from a `Promotion` that references a
`deliveryUnitRef`, `fleetRef`, and `planRef` or explicit plan overrides.

Runtime objects stay unchanged in principle:

- `PromotionRun` is one execution attempt stamped from a Promotion;
- `Target` is one per-cluster/stage execution record;
- `DecisionTrace` is audit evidence.

## Rejected Alternatives

### Keep `Source` As The User-Facing Root

This makes adoption output small, but it leaks an implementation detail into the
main authoring model. Users need an app/workload object that can hold source,
trigger, default fleet, and default plan together.

### Move Version Onto `DeliveryUnit` And Let It Deploy Automatically

This is attractive for simple demos, but it hides the operational action
boundary. Kapro needs a reviewable rollout event with audit, approval, retry,
and runtime history, so `Promotion` stays explicit.

### Collapse `PromotionRun` Or `Target`

`PromotionRun` and `Target` have independent runtime lifecycles. Collapsing
them would make retries, per-target status, audit, and retention harder.

### Make Source And Trigger Pure Internal Structs

They are derived by default, but keeping them as CRDs preserves Kubernetes-native
inspection, RBAC, status, and controller composition. The key rule is authorship,
not visibility.

## Consequences

- Greenfield scaffolds generate `deliveryunits/<name>.yaml`, not
  `sources/<name>.yaml`.
- Existing Argo and Flux import paths generate DeliveryUnits with embedded
  source mappings.
- `kapro source apply` accepts DeliveryUnit YAML and continues accepting legacy
  Source YAML for local mapping files.
- Promotion and PromotionRun carry `deliveryUnitRef`; runtime Target objects get
  the canonical `kapro.io/unit` label.
- `Fleet` still serves older inline source and plan fields as compatibility
  inputs during the 0.6.x hard-migration window. They are not emitted by
  greenfield or import generators, are not the public-preview authoring path,
  and should be removed before v1.0 once equivalent DeliveryUnit/Plan coverage
  exists in conformance and quickstart tests.
- Static GitOps YAML must not fake ownerReferences. Controller-derived `Source`
  and `Trigger` owner references are set only by the DeliveryUnit controller.

## References

- ADR-0001: Promotion intent vs PromotionRun runtime split
- ADR-0009: Target is the PromotionRun per-target state authority
- ADR-0017: Promotion control plane for any delivery substrate
- ADR-0018: Public and runtime API split
