# ADR-0019: DeliveryUnit Authoring Layer

## Status
Accepted

## Context

Kapro's pre-preview API still had two competing aggregate roots. `Fleet` carried
source, substrate, cluster, and plan intent, while `Promotion` created runtime
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
- default `fleet` and `plan` values for generated promotions;
- stable labels, especially `kapro.io/unit`.

The DeliveryUnit controller derives:

- `Source` from `DeliveryUnit.spec.source`;
- `Trigger` from `DeliveryUnit.spec.triggers[]`.

`Source` and `Trigger` remain Kubernetes-visible CRDs because users and
operators need to inspect them, but they are controller-derived by default.
They are comparable to `EndpointSlice` or `ReplicaSet`: real API objects with
status and operational value, not primary Git-authored intent.

`Fleet` moves toward target-set semantics. It describes the clusters and
substrate defaults for those clusters. It must not be the primary owner of
source mapping intent.

The user-authored Fleet and Cluster substrate binding is named
`spec.delivery`. A Fleet or Cluster selects how work is
applied or synced; it does not itself represent a delivery action. Runtime
progress may still use `status.delivery` because that status records concrete
delivery execution.

`Promotion` remains an explicit user-authored action. Changing
`DeliveryUnit.spec.source` or a future `DeliveryUnit.spec.version` must not
silently deploy. A rollout still starts from a `Promotion` that references
`unit`, `fleet`, and `plan` or explicit plan overrides.

Runtime objects stay unchanged in principle:

- `PromotionRun` is one execution attempt stamped from a Promotion;
- `Target` is one per-cluster/stage execution record;
- `DecisionTrace` is audit evidence.

The public-preview authorship boundary is:

| Category | Kinds | Authorship |
| --- | --- | --- |
| User-authored intent | `DeliveryUnit`, `Fleet`, `Cluster`, `ClusterTemplate`, `Plan`, `Policy`, `Plugin`, `SubstrateClass`, `Substrate`, `SubstrateDiscoveryPolicy`, typed substrate config CRDs | Users, platform teams, CLI generators, or GitOps write spec; Kapro writes status |
| User-authored action | `Promotion`, `Approval` | Humans, CI, or CLI create explicit action records; Kapro writes status |
| Controller-derived | `Source`, `Trigger` | Kapro writes spec and status from `DeliveryUnit`; users inspect them but do not author them in the public-preview path |
| Runtime | `PromotionRun`, `Target`, `DecisionTrace` | Kapro writes spec and status; users observe them like EndpointSlices or ReplicaSets |

That leaves 12 core `kapro.io` CRDs in the authored surface, 2 derived
`kapro.io` CRDs for inspection, and 3 `runtime.kapro.io` CRDs that are not a
user interface. Typed substrate config CRDs are authored only by platform teams
when a substrate needs typed settings.

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

- New promotion repo scaffolds generate `deliveryunits/<name>.yaml`, not
  `sources/<name>.yaml`.
- Existing Argo and Flux import paths generate DeliveryUnits with embedded
  source mappings.
- `kapro source apply` accepts DeliveryUnit YAML and continues accepting legacy
  Source YAML for local mapping files.
- Promotion and PromotionRun carry `unit`; runtime Target objects get
  the canonical `kapro.io/unit` label.
- `Fleet` still serves older inline source and plan fields as compatibility
  inputs during the 0.6.x hard-migration window. They are not emitted by
  new-repo or import generators, are not the public-preview authoring path, and
  should be removed before v1.0 once equivalent DeliveryUnit/Plan coverage exists
  in conformance and quickstart tests.
- Static GitOps YAML must not fake ownerReferences. Controller-derived `Source`
  and `Trigger` owner references are set only by the DeliveryUnit controller.

## References

- ADR-0001: Promotion intent vs PromotionRun runtime split
- ADR-0009: Target is the PromotionRun per-target state authority
- ADR-0017: Promotion control plane for any delivery substrate
- ADR-0018: Public and runtime API split
