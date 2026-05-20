# ADR-0004: Harmonize CRD JSON field names to strict Kubernetes camelCase

## Status
Accepted

## Context

The Kapro v1alpha1 CRDs grew incrementally and accumulated naming drift
on the wire (JSON tag) layer. The same logical concept ("PromotionPlan
reference") appeared as three different JSON spellings in different
places:

- `promotionplan` (lowercase one-word) — `PromotionPlanRef.PromotionPlan`,
  `PromotionPlanProgress.PromotionPlan`, `TargetStatus.PromotionPlan`,
  `PromotionTargetSpec.PromotionPlan`, `KaproSpec.PromotionPlan`
- `promotionplans` (lowercase plural) — `PromotionRunSpec.PromotionPlans`
- `promotionPlans` (camelCase, plural) — `PromotionSpec.PromotionPlans`,
  `PromotionTriggerTemplate.PromotionPlans`

The drift extended further: `promotionrun` (lowercase) on
`ApprovalSpec.PromotionRun`, `promotionrunDerivedFrom` on
`AuditEntry`, `promotionrunRef`/`promotionplanRef` on `TargetStatus`
and `PromotionTargetSpec`, and `promotionplanProgress` on
`PromotionRunStatus`. Fourteen JSON tags total violated the
Kubernetes camelCase convention.

Kapro just cut `v0.1.0` as the first public release. This is the last
moment to normalise the public field shape cheaply — every additional
adopter raises the cost of a future rename.

## Decision

Adopt strict Kubernetes camelCase for every JSON tag in
`api/v1alpha1/*.go`. Treat the rename as a documented breaking change
in the next patch release (v0.1.1 per the saved policy that bumps the
third version component for features).

The fourteen renamed tags are:

| Type / field | Was | Becomes |
|---|---|---|
| `KaproSpec.PromotionPlan` | `promotionplan` | `promotionPlan` |
| `ApprovalSpec.PromotionRun` | `promotionrun` | `promotionRun` |
| `PromotionPlanRef.PromotionPlan` | `promotionplan` | `promotionPlan` |
| `PromotionPlanProgress.PromotionPlan` | `promotionplan` | `promotionPlan` |
| `PromotionRunSpec.PromotionPlans` | `promotionplans` | `promotionPlans` |
| `PromotionRunStatus.PromotionPlanProgress` | `promotionplanProgress` | `promotionPlanProgress` |
| `TargetStatus.PromotionRunRef` | `promotionrunRef` | `promotionRunRef` |
| `TargetStatus.PromotionPlanRef` | `promotionplanRef` | `promotionPlanRef` |
| `TargetStatus.PromotionPlan` | `promotionplan` | `promotionPlan` |
| `PromotionTargetSpec.PromotionRunRef` | `promotionrunRef` | `promotionRunRef` |
| `PromotionTargetSpec.PromotionPlanRef` | `promotionplanRef` | `promotionPlanRef` |
| `PromotionTargetSpec.PromotionPlan` | `promotionplan` | `promotionPlan` |
| `AuditEntry.PromotionRun` | `promotionrun` | `promotionRun` |
| `AuditEntry.PromotionRunDerivedFrom` | `promotionrunDerivedFrom` | `promotionRunDerivedFrom` |

Two printcolumn JSONPaths on `PromotionTarget` updated to match.

A new drift canary (`api/v1alpha1/camelcase_canary_test.go`,
`TestJSONTagsAreCamelCase`) walks every struct tag in `api/v1alpha1`
and fails the build if a future contributor reintroduces a
lowercase-two-word JSON tag (`promotionplan`, `promotionrun`, etc.) or
snake_case anywhere in the API.

## Rejected alternatives

### A. Ship a dual-unmarshal compatibility shim
Implement a custom `UnmarshalJSON` on each affected type that accepts
both the old and new JSON keys, deprecate the old keys with a warning,
and remove them in a later release.

- Costs a meaningful amount of code on every affected type.
- Subverts kubebuilder/openapi schema generation — the CRD itself
  would still only describe one shape, so admission validators get
  out of sync with the unmarshal behaviour.
- Encourages contributors to extend the duality instead of fixing it.
- v1alpha1 explicitly permits breaking changes; paying the cost of
  compatibility shimming here would be over-engineering.

### B. Keep the drift; rename only at v1beta1
Doing nothing now means every reader of the CRD docs from today
through v1beta1 has to live with the inconsistency, and every new
example/blog/talk perpetuates it. The fix gets more expensive with
every adopter.

### C. Use kubectl's auto field-name conversion to "fix" it client-side
kubectl does not transparently convert `promotionplan` to
`promotionPlan` — the JSON tag is the wire shape, period. Not an
option.

### D. Adopt snake_case to match other ecosystems (Helm, OCI)
Kubernetes API convention is camelCase. Diverging from it would
force every Kapro CRD to look unlike every other operator's CRD.
Pure regression.

## Consequences

**Easier:**
- Every reader of a Kapro manifest sees one consistent style.
- Subscribers writing tooling against the JSON shape (CloudEvents
  consumers, kube-state-metrics configs, Backstage plugins) don't
  hit case-mismatch surprises.
- New contributors recognise the convention immediately.

**Harder:**
- Existing alpha or release-candidate manifests need a one-time rewrite. The keys
  affected are a small, mechanical set (`sed` is sufficient). A
  `kapro migrate v0.1-fields` CLI subcommand is reserved for a
  follow-up if user demand emerges.
- This is a breaking change. Adopters between v0.1.0 and v0.1.1
  must update their manifests before upgrading.

**Locks in:**
- JSON tags in `api/v1alpha1` are now part of the public API contract.
- The `TestJSONTagsAreCamelCase` canary enforces the convention going
  forward; new constants/fields that violate it will fail CI.

## References

- PR vinn/crd-camelcase-harmonization (this change)
- `api/v1alpha1/camelcase_canary_test.go` — the drift canary
- `.github/CONTRIBUTING_EVENTS.md` — style discipline that informs this
  decision (the events checklist and the CRD checklist are the same
  shape: catch drift at commit time, not in review)
