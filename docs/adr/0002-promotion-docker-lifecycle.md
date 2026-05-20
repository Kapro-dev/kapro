# ADR-0002: Docker-style Promotion lifecycle phases

## Status
Accepted

## Context

After [ADR-0001](0001-promotion-runtime-split.md) made `Promotion` the
durable user-facing intent, its `status.phase` enum needed to be
designed deliberately. The previous ad-hoc set
(`Pending|Running|Promoted|Failed|Suspended`) was:
- Inconsistent with any Kubernetes precedent.
- Missing transitions users actually care about (paused / resumed,
  rollback, terminating).
- Awkward to talk about ("Promoted" reads like an adjective; users
  said "succeeded").

We needed a phase model that operators recognise instantly and that
maps cleanly to event subscribers.

## Decision

Adopt the Docker container lifecycle as the phase vocabulary for
`Promotion.status.phase`. Eight phases:

| Phase | Docker analogue | Meaning |
|---|---|---|
| `Pending` | created | Created, not yet stamped |
| `Progressing` | running | Active PromotionRun rolling out |
| `Paused` | paused | `spec.suspended=true` |
| `Restarting` | restarting | New attempt stamped after a prior terminal |
| `Succeeded` | exited 0 | Latest attempt converged |
| `Failed` | exited >0 | Latest attempt failed |
| `RollingBack` | (custom) | Reserved for `spec.rollbackTo`; not yet emitted |
| `Terminating` | removing | `deletionTimestamp` set; GC draining children |

Plus four `status.conditions`: `Ready`, `Progressing`, `Suspended`,
`RollbackAvailable`. K8s `Event` is emitted on every transition;
`Failed` is `Warning`, all others `Normal`.

## Rejected alternatives

### A. Keep the ad-hoc enum (Pending/Running/Promoted/Failed/Suspended)
Five phases, no precedent, awkward names, missing transitions.

### B. Argo CD's `Synced|OutOfSync|...` model
Argo's model is about per-cluster reconcile state, not fleet promotion
intent. We orchestrate Argo; we don't replicate its phase model.

### C. Tekton's `PipelineRun` phases
Tekton has `Pending|Running|Succeeded|Failed|Cancelled`. Closer but
misses our needs: no paused/resumed semantic, no restarting between
attempts, no terminating phase. Also: Tekton is per-pipeline-run; our
Promotion is multi-attempt by design.

### D. Pure state-machine vocabulary (`S1|S2|S3`)
Unreadable. Operators want a vocabulary they already know.

## Consequences

**Easier:**
- Every operator who has used Docker recognises the model immediately.
- Pairs cleanly with the CloudEvents vocabulary (`kapro.io/promotion.*`)
  — see [ADR-0003](0003-cloudevents-publisher-posture.md).
- Subscribers can branch on phase without lookup tables.

**Harder:**
- `Restarting` is a transient phase; subscribers that aren't
  prepared for it may double-count `Progressing` transitions.
  Documented as expected behaviour in
  `docs/events.md`.
- `RollingBack` is reserved but not yet emitted (pending `spec.rollbackTo`
  design). Some confusion possible if subscribers pre-register for it.

**Locks in:**
- Phase names are part of the v1alpha1 contract — they appear as
  literal strings in CloudEvents `data.phase`, the CRD enum, and
  printcolumns. Renaming requires a major version bump.

## References

- PR #77: Restore Promotion as durable intent; demote PromotionRun to runtime
- PR #79: Promotion lifecycle hooks: webhook + event handlers on phase transitions
- `api/v1alpha1/promotion_types.go` — `PromotionPhase` constants
- `internal/controller/promotion_controller.go` — `computePromotionPhase`,
  `emitPhaseTransitionEvent`
