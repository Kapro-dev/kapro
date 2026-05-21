# Kapro Architecture Decision Records

Numbered, dated, immutable records of the design decisions that shape
Kapro. Each ADR captures *why* a decision was made and *what was
rejected*, so future contributors can revisit the reasoning instead of
relitigating it.

| # | Title | Status |
|---|---|---|
| [0001](0001-promotion-runtime-split.md) | Promotion intent vs PromotionRun runtime split (Service/EndpointSlice model) | Accepted |
| [0002](0002-promotion-docker-lifecycle.md) | Docker-style Promotion lifecycle phases | Accepted |
| [0003](0003-cloudevents-publisher-posture.md) | CloudEvents publisher posture: emit, don't route | Accepted |
| [0004](0004-camelcase-field-harmonization.md) | Harmonize CRD JSON field names to strict Kubernetes camelCase | Accepted |
| [0005](0005-withdraw-target-namespace.md) | Withdraw kapro.io/promotion.target.* from the reserved CloudEvents vocabulary | Accepted |
| [0006](0006-external-gate-predicates.md) | External gate predicates — GateType (KEDA-shaped) | Proposed |
| [0007](0007-programmatic-sdk.md) | Kapro programmatic SDK — builder + subscriber + gate | Proposed |
| [0009](0009-promotionrun-target-status-collapse.md) | Target is the PromotionRun per-target state authority | Accepted |
| [0010](0010-core-and-preview-controller-tier.md) | Core and preview controller tier | Accepted |
| [0011](0011-conversion-webhook-scaffold.md) | Conversion webhook scaffold without legacy migration guarantee | Accepted |
| [0012](0012-competitive-positioning.md) | Competitive positioning | Accepted |

## Adding a new ADR

1. Copy the [template](#template) into `docs/adr/NNNN-<slug>.md` with the next number.
2. Write the decision in present tense ("we choose X").
3. Document the alternatives considered and why they were rejected — that is the most useful part for future readers.
4. Link the ADR from `docs/adr/README.md`.
5. ADRs are immutable: amend a decision by writing a new ADR that supersedes the old one. Do not edit the old one except to add a `Status: Superseded by NNNN` line at the top.

## Template

```markdown
# ADR-NNNN: <Title>

## Status
Accepted | Proposed | Superseded by <NNNN>

## Context
What was the problem, constraint, or pressure that forced a decision?

## Decision
The single sentence summarising what we chose.

## Rejected alternatives
Each alternative with *why* it was rejected. This is the load-bearing
section — future readers should be able to revisit your reasoning.

## Consequences
What this decision makes easier, what it makes harder, what it locks in.

## References
Links to PRs, commits, prior ADRs.
```
