# ADR-0010: Core And Preview Controller Tier

## Status
Superseded in part by [ADR-0018](0018-public-runtime-api-split.md)

ADR-0018 resets the API version to `kapro.io/v1alpha1`, renames `Backend` to
`Substrate`, removes the `GateExpression` public-preview CRD, and makes
`PromotionRun`/`Target` runtime CRDs. The controller-tier decision below is
historical context for why default installs stay conservative.

## Context

Kapro exposes a broad Kubernetes API surface for public preview. Some
controllers are required for the core promotion path; others support optional
automation, bootstrap, extension, or discovery workflows that need additional
operator configuration.

Starting every controller by default made first installs harder to reason
about:

- Preview controllers can depend on infrastructure that a normal first install
  has not configured yet.
- Users evaluating the core `Fleet` to `Promotion` to `PromotionRun` path saw
  optional surfaces before understanding the base model.
- A wildcard default made it too easy for new controllers to start in existing
  installs without an explicit operator decision.

The CRDs still ship together because all objects are part of the current
`kapro.io/v1alpha2` preview API, but controller startup should be conservative.

## Decision

The default operator install starts only the core public-preview controllers:

- `fleet`
- `plan`
- `promotion`
- `promotionrun`
- `cluster`

The `target` controller is an implicit dependency of `promotionrun`. Operators
do not list it in the default Helm values; the controller manager starts it
whenever `promotionrun` is selected.

All other controllers are preview/optional and require explicit opt-in through
the existing `controllers` setting:

- `approval`
- `backend`
- `gateexpression`
- `cluster-bootstrap`
- `clustertemplate`
- `plugin`
- `trigger`

Compatibility aliases for old controller keys may remain, but documentation,
examples, and Helm values use the canonical controller names.

The wildcard selection `controllers: ["*"]` remains available for development
and advanced installations, but it is not the default posture for public
preview.

## Rejected alternatives

### A. Start every controller by default

This maximizes feature visibility but makes the first install depend on preview
surfaces and increases the chance that a controller acts on a CRD before the
operator has configured the surrounding infrastructure.

### B. Split preview CRDs into a separate chart

This would reduce the visible API surface but create chart skew and upgrade
complexity during a pre-stable period. The current split is runtime behavior,
not CRD packaging.

### C. Use a single `enablePreview` boolean

A boolean is too coarse. Operators should be able to enable `trigger` without
also enabling plugin runtime dispatch, cluster bootstrap, or backend discovery.

## Consequences

**Easier:**
- A default install demonstrates the core promotion model with fewer moving
  parts.
- New preview controllers cannot accidentally start in existing installs just
  because a release added a new registered controller.
- Operators can opt into preview surfaces one at a time.

**Harder:**
- Documentation must clearly say that installed CRDs do not imply their
  controllers are running.
- Quickstarts that depend on preview controllers must include the matching
  Helm `controllers` values.

**Locks in:**
- Helm defaults list the five core controller keys, not `*`.
- `promotionrun` starts `target` implicitly.
- Public-surface tests fail if the default controller set drifts from this
  ADR or references unregistered controller keys.

## References

- [Install Guide](../getting-started/install.md)
- [Preview Controllers](../concepts/preview-controllers.md)
- [ADR-0001](0001-promotion-runtime-split.md)
- [ADR-0009](0009-promotionrun-target-status-collapse.md)
