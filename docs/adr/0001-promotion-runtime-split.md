# ADR-0001: Promotion intent vs PromotionRun runtime split (Service/EndpointSlice model)

## Status
Accepted

## Context

Kapro's first promotion API surface (v0.4.0-alpha.0) had both `Promotion`
and `PromotionRun` as user-creatable CRDs. The intent was that
`Promotion` captured durable intent and `PromotionRun` captured an
execution attempt — but both were user-facing, with no enforced
contract about which one a human should write. Two consequences:

- New users faced two front doors with no signpost.
- CI re-stamping ("promote the same app with a new version") had no
  canonical pattern — operators improvised.

A simpler proposal floated: delete `Promotion` and make `PromotionRun`
the single user-facing object. That ships fewer CRDs but loses the
durable-intent / immutable-attempt distinction that distinguishes Kapro
from per-cluster reconcile tools like Flux/Argo CD.

## Decision

`Promotion` is the durable user-authored intent object. `PromotionRun`
is a controller-authored, immutable execution attempt. Humans, CI, and
`PromotionTrigger` write `Promotion`. The Promotion controller is the
sole writer of `PromotionRun` (enforced by a validating admission
webhook plus default RBAC that gives users `get/list/watch` only on
`PromotionRun`). Each spec change on a Promotion stamps a new
PromotionRun; the prior non-terminal run is marked `Superseded`.

This matches the Service/EndpointSlice pattern in Kubernetes itself:
Service is user-facing, EndpointSlice is controller-managed. Same
shape as Deployment/ReplicaSet and Job/Pod.

## Rejected alternatives

### A. Delete `Promotion`, make `PromotionRun` the single user object
Loses durable intent. CI re-stamping becomes "create a new PromotionRun
each time", with no stable name for the intent — auditability suffers,
naming churn forces label/annotation conventions to fill the gap.
Also: Argo CD has only `Application` (intent), Flux has only
`Kustomization` (intent), Argo Rollouts has `Rollout` (intent) plus
`AnalysisRun` (attempt). Every well-scaled GitOps primitive has the
intent/attempt split. Collapsing it would be regressing.

### B. Keep both objects user-creatable
The previous state. Two front doors, no contract, real user confusion.

### C. Route everything through `PromotionTrigger`
Considered briefly. PromotionTrigger is an automated user; making it
the single ingress conflates "external-signal observer" with
"durable-intent owner." Two roles, one object, no Kubernetes analogue.
Also breaks idempotency — direct `Promotion` writes are
`kubectl apply` semantics; through-Trigger adds "did the trigger fire?"
state.

## Consequences

**Easier:**
- One front door for users (`Promotion`).
- CI re-stamping is `kubectl apply` semantics on the same `Promotion`.
- Audit trail is the bounded `Promotion.status.attempts[]` history.
- Per-attempt rollout state stays observable via the immutable
  `PromotionRun` objects (controller-only writes mean the attempt
  record is forensically reliable).

**Harder:**
- Adds an admission webhook (one more thing to operate).
- Direct-`PromotionRun` users from earlier alpha need to migrate to
  authoring `Promotion` (documented in CHANGELOG).
- One extra CRD to learn.

**Locks in:**
- The `PromotionRun.spec` immutability contract.
- The `Superseded` terminal phase semantic.
- Default RBAC for human users: `promotions:CRUD`, `promotionruns:RO`.

## References

- PR #77: Restore Promotion as durable intent; demote PromotionRun to runtime
- PR #78: Address PR #77 review comments (PromotionPlan name, kaproRef index, terminal-phase requeue, CHANGELOG consolidation)
- [ADR-0002](0002-promotion-docker-lifecycle.md) builds on this split by giving Promotion a Docker-shaped lifecycle.
