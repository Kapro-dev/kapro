# Pre-stable Release Train

Kapro is pre-stable software. Until the project explicitly graduates its public
contracts, releases stay in the `0.x.x` series.

The first version digit remains `0` for roadmap work. GitHub milestones must
name exact feature releases with all three SemVer digits chosen:

| Milestone shape | Purpose |
| --- | --- |
| `v0.2.4` | A concrete patch-level release for a known feature increment. |
| `v0.4.7` | A later pre-stable feature release; the third digit is still meaningful. |
| `v0.4.20` | A larger pre-stable feature increment without changing the first digit. |

Roadmap discussions may refer to broader `0.x` lines, but active GitHub
milestones should not be broad buckets like `v0.10.0` or shorthand names like
`v0.6`. Create the milestone only when the feature increment is concrete enough
to pick the third digit.

## Numbering Strategy

Until stability graduation, Kapro uses both remaining SemVer digits
intentionally:

- `0.<capability-line>.<feature-increment>` is the required shape.
- `<capability-line>` groups a coherent capability line, risk level, and adoption story.
  For example, `0.2.y` is programmable-engine and adapter hardening, while
  `0.4.y` can hold later adoption and operator-ergonomics work.
- `<feature-increment>` names the concrete feature release in that line. It is
  not only for bug fixes while Kapro is pre-stable. Examples: `v0.2.4`,
  `v0.4.7`, `v0.4.20`.
- Patch-only fixes can still use the next increment in the same line, but the
  milestone title must name the actual shipped scope.

This keeps the project honest: versions communicate which capability line a
change belongs to and which concrete increment shipped it, without pretending
the public API has reached a stable first digit.

## Why Not 1.0 Yet

`1.0.0` is not a planning bucket. It is a graduation signal. Kapro should stay
below `1.0.0` until these conditions are true:

- the core promotion CRDs have clear Stable or late-Preview compatibility
  expectations;
- upgrade, rollback, and CRD migration behavior is proven across real minor
  trains;
- public Go SDK packages have stopped shifting shape between trains;
- adapter and plugin conformance are strong enough for external authors to
  self-verify integrations;
- operational defaults are boring in real clusters, including retention,
  observability, recovery, and failure-mode behavior;
- documentation and examples match the supported path rather than aspirational
  architecture.

Until then, Kapro can still ship serious, production-usable releases under
`0.x.x`. The version number should reflect honest contract maturity, not feature
ambition.

## Train Discipline

Each minor train should have a narrow theme and a short ship list:

- define the user-visible outcome before opening implementation issues;
- keep independent improvements in separate PRs;
- include `CHANGELOG.md` entries for every user-visible change;
- run the normal CI, docs, lint, smoke, and conformance checks before merge;
- avoid widening public CRDs or SDK types unless the field or method has an
  immediate user path and an upgrade story.

## Train Budget

The third digit is meaningful during pre-stable development, so Kapro can ship
feature increments such as `v0.4.20` or `v0.5.6` without changing the first
digit. Those releases must still be concrete increments, not broad planning
buckets.

As a default budget, a capability line should trigger a planning review after
roughly 10 patch-level increments. The review asks whether the line is still
finishing the same adoption story or whether the next coherent capability line
should begin. It is not a hard limit: continuing to `v0.5.6` is acceptable
when the individual milestones are specific, independently reviewable, and the
release notes explain why the work still belongs to its current capability line.

Do not create broad train-start placeholders such as `v0.10.0`, `v0.20.0`, or
`v0.30.0` just to reserve space. Pick an exact `v0.x.y` milestone only when the
feature increment is concrete enough to describe, implement, test, and release.

When a train introduces an Alpha or Preview surface, the release notes must say
which parts are supported now and which parts are future work. Avoid naming a
release after an aspiration that the shipped code does not yet satisfy.
