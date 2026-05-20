# ADR-0005: Withdraw `kapro.io/promotion.target.*` from the reserved CloudEvents vocabulary

## Status
Accepted

## Context

[ADR-0003](0003-cloudevents-publisher-posture.md) established that
Kapro emits CloudEvents and explicitly does not ship a notification
routing layer (no Slack/Teams/PagerDuty backends in-tree). The
vocabulary published in PR #80 included three reserved namespaces for
future expansion:

- `kapro.io/promotion.wave.*` — wave (PromotionPlan DAG node) level
- `kapro.io/promotion.stage.*` — stage level
- `kapro.io/promotion.target.*` — per-cluster (per FleetCluster) level

PR #81 emitted the first two. The third was held in reserve. While
designing the per-cluster events we hit three problems:

1. **Cardinality explosion.** A 47-cluster fleet × multiple promotions
   per week × 5+ transitions per cluster per promotion ≈ thousands of
   events per week per fleet. Most subscribers would filter them out;
   the signal-to-noise is poor.

2. **Direct duplication of Flux Notification Controller and Argo CD
   Notifications.** Per-cluster reconcile state is exactly what those
   projects emit, with the full backend ecosystem (Slack, Teams,
   PagerDuty, email, webhook, gRPC). Kapro re-emitting the same thing
   at a higher abstraction layer is the duplication anti-pattern
   ADR-0003 set out to avoid.

3. **The fleet-narrative work is already done.** `stage.completed`
   already tells subscribers "all targets in this stage converged."
   `stage.gate.{waiting,passed,failed}` already carries the per-target
   detail in the `target` field where it matters most (gate
   evaluation is the one transition that genuinely IS per-target).
   Subscribers wanting "cluster X converged on v1.2.3" can subscribe
   to Flux/Argo's per-Kustomization / per-Application events directly.

## Decision

The `kapro.io/promotion.target.*` namespace is **withdrawn** — removed
from the reserved list in `pkg/events/types.go` and from the
documented vocabulary in `docs/events.md`. Kapro will not emit
per-cluster events.

The single exception is the gate-scope: `kapro.io/promotion.stage.gate.*`
events DO populate `data.target` because gate evaluation is the one
transition that is inherently per-target. That's already shipped and
stays.

## Rejected alternatives

### A. Keep the namespace reserved indefinitely
Lets the question linger; new contributors will ask "when will the
target events ship?" The honest answer is "never," so document it
honestly.

### B. Ship a small per-cluster event set anyway (e.g. just `target.failed` for paging)
The kind of subscribers who want paging on a failed cluster apply
should subscribe to Flux Notification Controller's
`Kustomization.status.conditions[Ready]=False` Alert — which is
designed exactly for this and is operational today across the CNCF
ecosystem. Kapro adding a parallel surface is duplication.

### C. Emit aggregated per-wave counts instead
`stage.completed` already counts target convergence; adding a
separate `target.aggregate` event would be redundant.

## Consequences

**Easier:**
- The vocabulary stays small and well-scoped: 18 EventTypes total
  across whole-Promotion, attempt, wave, stage, and gate scopes.
- No event-rate concerns for large fleets — subscribers see one event
  per stage transition, not one per cluster per stage.
- Clean positioning story: "Kapro emits fleet-level events; per-cluster
  events are Flux/Argo's job." Easy to repeat in talks and READMEs.

**Harder:**
- Users who want "did cluster X converge?" must either watch
  `PromotionTarget.status.phase` directly via the Kubernetes API or
  subscribe to Flux/Argo events for that cluster. Documented in the
  integrations cookbook (cross-link from docs/events.md added as part
  of this PR).

**Locks in:**
- The 18-EventType vocabulary as the v1alpha1 surface. Any new event
  scope (e.g. an audit channel) requires a fresh ADR — the
  `target.*` withdrawal sets the precedent that we are explicit about
  what we will NOT emit.

## References

- [ADR-0003](0003-cloudevents-publisher-posture.md) — CloudEvents
  publisher posture (emit, don't route).
- PR #80 — original vocabulary + sink.
- PR #81 — wave + stage + gate events.
- `pkg/events/types.go` — taxonomy section in the package doc.
- `docs/events.md` — user-facing vocabulary.
