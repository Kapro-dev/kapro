# Pre-stable Release Train

Kapro is pre-stable software. Until the project explicitly graduates its public
contracts, releases stay in the `0.x.x` series.

The first version digit remains `0` for roadmap work. Larger product phases use
the second digit:

| Train | Purpose |
| --- | --- |
| `0.2.x` | Current programmable engine, adapter, archive, and adoption hardening line. |
| `0.10.x` | Next adoption and operator ergonomics line when `0.2.x` is complete. |
| `0.20.x` | Broader ecosystem and conformance line after the SDK contracts prove useful. |
| `0.30.x` | Larger operational scale, security, and multi-substrate line. |

Patch releases in a train, such as `0.2.1` or `0.10.3`, are for bug fixes,
documentation, compatibility hardening, and narrow polish. Minor train releases,
such as `0.10.0`, `0.20.0`, and `0.30.0`, are for coherent product increments
that may add Preview surfaces or change Alpha surfaces with migration notes.

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

When a train introduces an Alpha or Preview surface, the release notes must say
which parts are supported now and which parts are future work. Avoid naming a
release after an aspiration that the shipped code does not yet satisfy.
