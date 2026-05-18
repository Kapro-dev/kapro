# Decision API

The Decision API is Kapro's HTTP surface for **autonomous and human-in-the-loop
promotion decisions**. It is the only CNCF-ecosystem control plane today that
exposes a typed, authenticated, audited surface for an AI agent (or any external
caller) to read the full context behind a pending PromotionTarget gate and submit
a Decide / Override that lands as an `Approval` plus a `DecisionTrace` entry.

This page is the user-facing summary. The implementation is at
`internal/webhook/decision_api.go`. Hardening (auth, RBAC, bounded reads,
truncation) shipped in commit 616da97.

## Endpoints

All endpoints under `/api/v1/` require a bearer token; the operator validates it
via Kubernetes `TokenReview` and authorizes via `SubjectAccessReview`. The
caller's username from TokenReview becomes the audit identity recorded on every
DecisionTrace entry.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/v1/fleet` | Fleet-wide summary (cluster counts, promotion counts, current phase histogram). Paginated. |
| GET | `/api/v1/promotionruns/{name}/context` | Full PromotionRun context: plan DAG, target list, current phase per target, gate state. |
| GET | `/api/v1/promotionruns/{name}/targets/{key}/gate` | Per-target gate evaluation context: metric query results, soak progress, approval state, recent evidence. The minimal input an AI agent needs to make a decision. |
| GET | `/api/v1/clusters/{name}/health` | Aggregated health for one FleetCluster (delivery phase per app, heartbeat freshness, condition summary). |
| POST | `/api/v1/promotionruns/{name}/targets/{key}/decide` | Submit a decision (Approve / Reject / Defer) with confidence, reasoning, and factor references. Creates an Approval + a DecisionEntry on the target's DecisionTrace. |
| POST | `/api/v1/promotionruns/{name}/targets/{key}/override` | Human override of an autonomous decision. Recorded as a `HumanOverride` entry on DecisionTrace. |
| GET | `/healthz` | Public liveness probe. |

## Bounded reads

All list endpoints support `limit`, `labelSelector`, `phase` query parameters.
Defaults and ceilings:

- `defaultDecisionAPILimit = 100`
- `maxDecisionAPILimit = 500`
- Phase-filtered scans stop at `decisionAPIScanLimitMultiplier * limit` to
  prevent pathological scans against fleets where the requested phase is rare.
- Responses include `page.truncated=true` when the scan budget was exhausted.
  Clients must narrow the selector and retry — there is no
  cursor-based continuation today.

These bounds exist to keep the Decision API safe against accidental
denial-of-service from an over-eager agent loop.

## AgentPolicy: governing what an agent can do

A `kapro.io/v1alpha1 AgentPolicy` CR binds an AI agent ServiceAccount to a trust
boundary. Key controls:

- **mode**: `auto` (autonomous Approval creation), `recommend` (advisory only,
  human must confirm), `disabled` (Decide rejected).
- **scope**: which stages / cluster selectors / country risk tiers the agent is
  allowed to act in.
- **confidence**: per-stage and per-tier confidence floors (0-1.0).
- **escalation**: on low confidence → `reject` | `hold` | `escalate-to-human`.
- **rateLimits**: maxApprovalsPerHour, maxApprovalsPerDay, maxConcurrent,
  cooldown between consecutive Approves for the same plan.
- **blastRadius**: maxPercentOfFleet, maxPercentPerTier, maxAbsoluteClusters.
- **audit**: requireReasoning, requireMetricReferences, requireConfidenceScore,
  minReasoningLength.
- **timeWindows**: allow / deny decisions during specific hours/days/timezones.

This is what makes "give an AI the keys to promote your dev fleet" a
defensible posture rather than a hand-wave.

## DecisionTrace: the audit trail

Every Decide / Override creates an immutable entry on the target's
`DecisionTrace`. Fields:

- `decisionID` (stable identifier for the proposal)
- `decision` (the agent's recommendation: Approve / Reject / Defer)
- `effectiveDecision` (the recommendation after trust evaluation — may differ
  if AgentPolicy escalated or downgraded)
- `confidence` (0..1)
- `reasoning` (free text, length-checked against audit.minReasoningLength)
- `factors` (typed references to gate evidence, metric IDs, soak window)
- `conditions` (any policy conditions that fired)
- `supersedence` (link to the prior decision this overrides)
- `humanConfirmation` (when AgentPolicy.mode = recommend)
- `humanOverride` (when a human used the override endpoint)

`DecisionTrace.history[]` is append-only. `DecisionTrace.current` always points
at the entry the controller used to advance the FSM.

## What makes this unique in CNCF

No other project in the CNCF landscape — Flux, Argo CD, Argo Rollouts, Kargo,
Sveltos, OCM, Karmada — exposes a *typed, governed, audited* surface for
autonomous promotion decisions. Most projects assume a human at the
`kubectl apply` keyboard or expect an external CI system to gate via webhook.
The Decision API + AgentPolicy + DecisionTrace triad is intentionally
designed to make AI agents first-class participants in promotion governance
with the same audit guarantees you get from a human SRE.

This is the differentiator the Q2 roadmap focuses on productizing (published
OpenAPI, Go / TS SDKs, one-page "give an AI the keys" demo).

## See also

- [`docs/security.md`](security.md) — TokenReview / SubjectAccessReview wiring,
  TLS, and the production hardening recommendations.
- [`docs/extension-model.md`](extension-model.md) — how AgentPolicy composes
  with the rest of the extension surface (KAI/KGI/KPI plugins).
- `internal/webhook/decision_api.go` — implementation.
- `api/v1alpha1/agentpolicy_types.go` — CRD shape (post PR #PR1 split).
