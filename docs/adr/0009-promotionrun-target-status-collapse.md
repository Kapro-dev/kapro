# ADR-0009: Target Is The PromotionRun Per-Target State Authority

## Status
Accepted

## Context

Earlier Kapro API shapes duplicated per-target rollout state in two places:
child `Target` objects and a persisted `PromotionRun.status.targets` list.
That duplication made the runtime contract harder to operate:

- Writers had to keep two status surfaces in sync.
- Large fleets could bloat the parent `PromotionRun` status with one entry per
  cluster/stage execution.
- Readers had to know whether `PromotionRun.status.targets` or child `Target`
  objects were authoritative.

This contradicted the runtime split from
[ADR-0001](0001-promotion-runtime-split.md): `PromotionRun` is the execution
attempt record, while `Target` is the per-target execution record.

## Decision

Child `Target` objects are the sole authoritative per-target runtime state.
`PromotionRun.status.targets` is not part of the public API surface.

`PromotionRun.status.summary` remains as a compact aggregate for `kubectl`,
dashboards, and automation:

```go
type PromotionRunSummary struct {
	TotalTargets   int32  `json:"totalTargets"`
	SyncedTargets  int32  `json:"syncedTargets"`
	FailedTargets  int32  `json:"failedTargets"`
	PendingTargets int32  `json:"pendingTargets"`
	ConvergedAt    string `json:"convergedAt,omitempty"`
}
```

`PromotionRun.status.report` may expose bounded counters, timestamps, artifact
metadata, and pending approval names, but it must not duplicate per-target
state. Detailed target inspection is always done by listing `Target` objects,
usually with:

```sh
kubectl get targets -l kapro.io/promotionrun=<promotionrun-name>
```

PromotionRun print columns use `status.summary.*` fields. CLI, API, and
gateway views that need target detail list child `Target` objects instead of
reading embedded target state from the parent run.

## Rejected Alternatives

### A. Keep both surfaces and mark `status.targets` deprecated

This preserves the drift bug. A deprecated field still has to be populated
correctly because users and tools will keep reading it.

### B. Drop all target aggregate data from PromotionRun

This removes duplication but makes `kubectl get promotionruns` much less useful
for first-line operations. Aggregate counts are cheap, bounded, and match
common Kubernetes status patterns.

### C. Store an opaque digest of child Target statuses

A digest would prove something changed but would not help operators answer the
basic questions: how many targets exist, how many converged, and how many
failed.

## Consequences

**Easier:**
- One source of truth for per-target runtime details.
- Parent `PromotionRun` status stays bounded as fleet size grows.
- CLI and gateway behavior is aligned with Kubernetes ownership: list child
  resources for detail, read parent status for summary.

**Harder:**
- Consumers that previously scraped embedded target state must switch to
  `Target` list calls.
- Controller tests need fixtures for both parent summary aggregation and child
  `Target` detail rendering.

**Locks in:**
- `PromotionRun.status.summary` is the public aggregate status surface.
- `Target.status` is the public per-target detail surface.
- Public-surface tests must fail if generated CRDs reintroduce
  `PromotionRun.status.targets`.

## References

- [ADR-0001](0001-promotion-runtime-split.md)
- [Migration guide: v1alpha1 to v1alpha2](../migration-v1alpha1-to-v1alpha2.md)
