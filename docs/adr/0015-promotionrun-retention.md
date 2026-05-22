# ADR-0015: PromotionRun Retention

## Status
Accepted

## Context

`PromotionRun` is the immutable execution record of one Promotion attempt. It is created by the Promotion controller, never updated by humans, and remains in the cluster after reaching a terminal phase (`Complete` / `Failed` / `Superseded`) so operators can audit what happened.

At realistic adoption volumes the object count grows quickly:

| Promotions / day | Attempts per promotion | PromotionRuns per year |
| ---------------- | ---------------------- | ---------------------- |
| 1                | 3                      | ~1,100                 |
| 10               | 5                      | ~18,000                |
| 50               | 5                      | ~91,000                |

Costs that scale with this count:

- etcd object storage and watch fan-out.
- Controller `List()` latency on PromotionRun (every Promotion reconcile lists its label-matched children).
- Apiserver memory for cached PromotionRun objects.
- `kubectl get promotionruns` becomes unusable in interactive consoles.

Without bounded retention the operator pays this cost indefinitely. Kapro already prunes status arrays (`Promotion.status.attempts[]` capped at `MaxPromotionAttempts = 20`, `status.lifecycleHandlerResults[]` at 50) but the underlying PromotionRun objects are never garbage-collected.

Kubernetes ownerReferences already cascade-delete child PromotionRuns when the parent Promotion is deleted. The remaining problem is **old terminal attempts under a still-living Promotion**.

## Decision

Add a Tier B (opt-in) controller, `promotionrun-gc`, that prunes terminal PromotionRun objects beyond a bounded retention window. The retention constants live in `api/v1alpha2/promotion_types.go` alongside `MaxPromotionAttempts` so all bounded-history caps are in one place:

```go
const (
    DefaultMaxRetainedPerPromotion = 50
    DefaultMinRetainedPerOutcome   = 10
)
```

Selection rules:

1. **Non-terminal attempts are never deleted.** Active runs are the live execution record; deletion would orphan child `Target` objects and break Promotion status aggregation.
2. **Each terminal outcome keeps at least `DefaultMinRetainedPerOutcome` members.** Successes filling the cap must not auto-prune the most recent Failures — that is usually exactly the run an operator is debugging.
3. **Total retained per Promotion (active + terminal) is capped at `DefaultMaxRetainedPerPromotion`.** Excess terminal attempts beyond that cap AND beyond the per-outcome floor are deleted oldest first.

The controller is intentionally Tier B — adopters running first installs expect `kubectl get promotionruns` to be the unmangled audit trail. Mature deployments opt in via the existing `--controllers` flag (ADR-0010):

```yaml
controllers:
  - fleet
  - plan
  - promotion
  - promotionrun
  - cluster
  - promotionrun-gc   # opt-in
```

## Consequences

- Production adopters bound etcd growth without writing a custom CronJob or external pruner.
- First-touch adopters keep the unbounded audit trail until they opt in; zero behaviour change in default installs.
- A new `Promotion.spec.retention` field becomes a clean future extension if per-Promotion tuning is requested — the controller already accepts per-instance overrides via `MaxRetainedPerPromotion` / `MinRetainedPerOutcome` fields on the reconciler struct.
- Each prune emits a `Normal` event `AttemptPruned` on the parent Promotion with phase + age + Promotion run name; the audit signal moves from the apiserver to the event stream.

## Rejected alternatives

- **A `CronJob` that runs `kubectl delete promotionruns ...`.** Requires writing the selection logic in shell, no per-outcome floor, no Promotion-event audit trail, and an extra workload to operate.
- **Time-based TTL (`spec.ttlSecondsAfterFinished`).** Mirrors Job garbage-collection but loses the "always keep the most recent N failures" semantic that adopters actually want for debugging.
- **Default-on Tier A.** Deletion is destructive; running by default surprises first-touch adopters who reasonably expect every PromotionRun they have ever created to still exist. Opt-in keeps the principle of least surprise.
- **Per-Promotion `spec.retention` field in v0.1.x.** Premature — defaults are sufficient for the volumes seen in v0.x preview. Add the field if adopters ask for tuning.

## Observability

- Each delete emits `Eventf(promotion, Normal, AttemptPruned, ...)` with the pruned run name, terminal phase, and age. Use this for audit and end-to-end accounting.
- Standard controller-runtime reconcile metrics (`controller_runtime_reconcile_total{controller=promotionrun-gc}`) cover reconcile rate and error rate.

## Future work

- `Promotion.spec.retention` field for per-Promotion overrides.
- A `kapro_promotionrun_pruned_total{phase}` counter so SLO doc readers can alert on prune storms.
- Optional `--retention-dry-run` flag that logs intended deletes without executing — useful for the first day of production opt-in.
