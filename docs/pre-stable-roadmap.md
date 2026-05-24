# Pre-stable Roadmap

Kapro's roadmap stays in the `0.x.x` series until the public CRDs, Go SDK,
plugin contracts, conformance tests, upgrade behavior, and operational defaults
have proved stable across real release trains. The first version digit remains
`0` for roadmap work.

This page is a planning guide, not a compatibility promise. The binding record
for a release is still `CHANGELOG.md` plus the release notes for that tag.

GitHub milestones must use exact pre-stable semver names. Use names such as
`v0.2.4`, `v0.4.7`, or `v0.4.20`; do not use shorthand names such as `v0.6`
or broad train-start buckets such as `v0.10.0`.

The numbering strategy is `0.<capability-line>.<feature-increment>`. The second
digit groups a capability line; the third digit names the concrete feature
increment inside that line.

## Roadmap Lines

| Line | Theme | Practical ship criteria |
| --- | --- | --- |
| `0.2.x` | Programmable engine hardening | AdapterPolicy discovery is real, programmable gates are documented and tested, release-train policy is enforced, and retention metrics are available before opt-in GC. |
| `0.4.x` | Adoption and operator ergonomics | `pkg/kapro/server` can be assembled from smaller registrars, CLI adoption paths are observe-first by default, and brownfield users have clear rollback points. |
| `0.6.x` | Ecosystem and conformance | External adapter authors can run conformance locally, at least one substrate adapter proves the plugin contract outside the in-tree controller path, and examples compile in CI. |
| `0.8.x` | Operational scale and security | Upgrade, rollback, observability, tenancy, signing, provenance, and failure-mode tests are strong enough for production change-control review. |

Concrete milestones inside those lines still need all three digits, for example
`v0.4.7` or `v0.4.20`. Do not create a milestone until the feature increment is
specific enough to name that patch digit.

Patch increments are a planning budget, not a promise that every capability
line stops at `.10`. Once a line crosses roughly 10 increments, do an explicit
scope review: either continue the line with concrete milestones such as
`v0.4.20` or `v0.5.5`, or move the next work into a new capability line. Avoid
placeholder milestones such as `v0.10.0`, `v0.20.0`, or `v0.30.0` unless that
exact patch release has a real feature scope.

## Train Rules

- Keep user-facing work in narrow PRs that can be reviewed and merged
  independently.
- Prefer finishing a shipped preview surface over adding a new public CRD or
  SDK type.
- Do not widen public schemas without an immediate runtime path, documentation,
  and a migration story.
- Add or update tests in the same PR as behavior changes.
- Treat docs, examples, and conformance as part of the product, not as release
  cleanup.

## Non-goals

Kapro should not copy broad cluster-management platforms. The product center is
delivery promotion: deciding what should move, proving that it is safe to move,
and coordinating the handoff to existing delivery substrates.

The roadmap should therefore avoid generic cluster classification, inventory,
and policy-management features unless they directly improve promotion safety,
adoption, rollback, or integration authoring.
