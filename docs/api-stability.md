# API Stability and Upgrade Policy

Kapro uses explicit API maturity levels for CRDs, Go extension packages,
lifecycle events, and language-neutral plugin contracts. The maturity level
describes compatibility expectations for users and plugin authors; it does not
change Kubernetes API version strings by itself.

The current release line is pre-stable; `v0.3.7` is the current public preview
release, and Kapro releases stay in the `0.x.x` series until the project
explicitly graduates its public contracts. `v0.1.0` was the first public
release for the full promotion-domain API, not a promise that all
`kapro.io/v1alpha2` fields are stable. `CHANGELOG.md` and the release notes are
the binding upgrade record for each tag.

Pre-stable milestones use exact `v0.x.y` names, for example `v0.2.4`,
`v0.4.7`, or `v0.4.20`, once the feature increment is concrete enough to pick
the third digit. The project uses `0.<capability-line>.<feature-increment>`:
the second digit groups the capability line and the third digit names the
actual shipped increment. `1.0.0` is reserved for a future stability
graduation, not for ordinary roadmap planning. See the [pre-stable release train](release-train.md).

## Maturity Levels

| Level | Meaning | Compatibility |
|---|---|---|
| Alpha | Experimental surface used to validate the model. | May change or be removed between minor releases. Migration notes are provided when a surface is used by examples or documented workflows. |
| Preview | Implemented and intended for early adopters. | Compatible within a minor release. Breaking changes require release notes, a migration path, and at least one minor release of overlap when practical. |
| Stable | Production contract. | Backward-compatible within the major version. Breaking changes require a new API version or a documented major-version migration. |

Maturity is assigned per surface, not per repository. A Preview proto can live
beside Alpha CRDs, and a Stable Go package can exist while other packages remain
Preview. The table below is the source of truth for the current contract level.

## Current Surface Classification

| Surface | Path | Level |
|---|---|---|
| Core promotion CRDs | `api/v1alpha2` `Fleet`, `Promotion`, `PromotionRun`, `Target`, `Plan`, `Source`, `Unit`, `Cluster`, `Backend`, `Approval` | Alpha |
| Trigger CRD | `api/v1alpha2` `Trigger` | Preview |
| Plugin CRD | `api/v1alpha2` `Plugin` | Preview |
| GateExpression CRD | `api/v1alpha2` `GateExpression` | Preview; full algebra enabled in `v0.2.x` |
| DecisionTrace CRD | `api/v1alpha2` `DecisionTrace` | Preview |
| Agent decision APIs | `api/v1alpha2` `Policy`, `Target.status.decisionTrace`, Decision API HTTP routes | Preview |
| Fleet auto-import CRD | `api/v1alpha2` `ClusterTemplate` | Preview; only implemented sources are runtime features |
| In-process actuator interface | `pkg/actuator` | Preview |
| In-process gate predicate interface | `pkg/kapro/gate` (`pkg/gate` compatibility alias) | Preview |
| In-process planner interface | `pkg/planner` | Preview |
| KAI actuator plugin contract | `spec/kai/v1alpha1` | Preview |
| KGI gate plugin contract | `spec/kgi/v1alpha1` | Preview |
| KPI planner plugin contract | `spec/kpi/v1alpha1` | Preview |
| Conformance harnesses | `conformance/actuator`, `conformance/gate`, `conformance/planner`, `conformance/provider`, `cmd/kapro-conformance` | Preview |
| Lifecycle event schema | `docs/events.md` | Preview |

All `v1alpha2` APIs remain below stable maturity until Kapro publishes a
stable API version. A surface can be Preview while the Kubernetes version is
still `v1alpha2`; the table above is the source of truth for compatibility
expectations.

No public surface is Stable in the `v0.1.x` line.

## Stable Core Versus Preview

The core runtime path is the promotion execution model: `Promotion`,
`PromotionRun`, `Target`, `Plan`, `Cluster`, `Backend`,
`Source`, and `Approval`. These APIs are still versioned
`v1alpha2`, but they are the durable product center and are exercised by the
operator runtime.

Preview APIs are intentionally separated from that core. Inline gate
notifications remain the active runtime path; there is no public notification
provider/policy CRD in the KISS API. `DecisionTrace` records the durable
controller audit stream. `Policy`, `Target.status.decisionTrace`, and the
Decision API are opt-in surfaces for machine assistance, not required for
deterministic promotion execution.
`ClusterTemplate` auto-import sources are only runtime features when the
source is implemented by the controller; unsupported sources must be documented
as future work rather than claimed as supported behavior.

## Compatibility Rules

Kapro treats these changes as schema-compatible for Preview and Stable CRD
surfaces:

- adding optional CRD fields with safe defaults;
- adding status fields, conditions, reasons, or events;
- widening validation so previously valid objects remain valid;
- adding printer columns, categories, labels, annotations, or defaults that do
  not change reconcile behavior for existing objects.

Kapro treats these changes as contract-compatible for Preview and Stable plugin,
Go, and event surfaces:

- adding enum values when existing consumers are required to ignore unknown
  values;
- adding proto fields with new field numbers;
- adding Go interface helpers that do not change existing method signatures;
- adding lifecycle event payload fields while preserving documented event type
  names and existing field meanings.

Kapro treats these changes as breaking unless an API version, overlap period, or
major-version migration covers them:

- removing or renaming CRD fields, proto fields, packages, methods, or enum
  values;
- changing the semantic meaning of an existing field;
- changing a default in a way that alters an existing rollout, gate, approval,
  or rollback workflow;
- tightening validation so an object accepted by the previous release is
  rejected by the new release;
- changing generated object names or labels that operators are expected to
  select on;
- changing documented lifecycle event type names or the meaning of documented
  payload fields;
- changing plugin request or response requirements in a way that makes an
  existing conformant plugin fail.

Operational defaults, log text, metrics names, and example manifests can change
before Stable unless this document explicitly lists them as a contract. When an
example is changed, the release notes should still explain how an existing user
updates their manifests.

## Deprecation Policy

Deprecation follows these rules:

- A deprecated field, enum value, package API, or proto field is marked in
  documentation and release notes.
- Deprecation notes identify the first release that includes the replacement,
  the earliest release where removal is allowed, and the user-visible action.
- Preview surfaces keep deprecated behavior for at least one minor release when
  the old and new behavior can coexist safely.
- Stable surfaces keep deprecated behavior for at least two minor releases, or
  until the next major API version when coexistence would compromise safety.
- Proto fields are not reused after removal. Removed fields remain reserved in
  the `.proto` file.
- CRD fields are not silently repurposed. A semantic change requires a new
  field, a conversion path, or a new API version.
- Removal notes state the replacement field or workflow and the first release
  where removal is allowed.

Alpha surfaces may change faster, but changes that affect committed examples,
published manifests, generated CRDs, stored status expectations, or conformance
tests still include migration notes.

## Schema Compatibility Expectations

Kapro includes the ADR-0011 `/convert` handler scaffold, but `v0.1.x` does not
enable CRD conversion strategy or rely on it for any automatic upgrade path.
Operators should therefore assume that the storage schema in a tagged release
must be readable by that same operator version and by any downgrade version
named in release notes.

There is no automatic legacy conversion for unreleased prototype objects. The
ADR-0011 scaffold is infrastructure for future served-version transitions, not a
compatibility promise for old prototype schemas. Users testing old branches
should recreate objects from the generated examples instead of relying on
controller-side migration code. The first tagged release that documents a CRD as
Preview must include explicit migration notes before removing or renaming that
surface.

For concrete cleanup and manifest rewrite steps, see the
[v1alpha1 to v1alpha2 migration guide](migration-v1alpha1-to-v1alpha2.md).

CRD schema changes should follow these rules:

- Prefer additive spec fields with explicit safe defaults.
- Keep status changes additive and preserve the meaning of existing conditions,
  reasons, phases, and counters.
- Do not repurpose fields, enum values, labels, annotations, or generated names.
- Document any validation tightening as a migration even when the affected input
  was never intended to be valid.
- Document downgrade compatibility whenever stored spec or status shape changes.
- Include example-manifest updates in the same pull request as schema changes
  when the example exercises the changed field.

## Change Process

Changes to Preview or Stable surfaces should include:

1. Updated API, proto, or Go documentation.
2. Updated examples or conformance scenarios when behavior changes.
3. Release notes with `Added`, `Changed`, `Deprecated`, `Removed`, or
   `Migration` entries.
4. Upgrade instructions for existing hubs when an operator action is required.
5. A compatibility note explaining why the change is backward-compatible, or
   why it is intentionally breaking.

Every release should also update `CHANGELOG.md`.

For proto contracts, new fields use new field numbers and removed fields are
reserved. For CRDs, new durable concepts should prefer additive fields or a new
API version over reinterpreting existing fields. For Go extension packages,
prefer a new interface or adapter helper over changing a method signature used
by plugin authors.

## Upgrade Policy

Kapro upgrades are designed around Kubernetes controller safety:

- Upgrade one hub control plane at a time. Do not run different Kapro versions
  against the same shard unless the release notes explicitly allow it.
- Apply CRD updates before rolling operator pods.
- Keep leader election enabled for multi-replica deployments.
- Keep `PromotionRun` and `Target` objects immutable from automation
  while an operator upgrade is in progress; update `Promotion` intent or create
  a new `Promotion` for rollback.
- Upgrade plugin servers before enabling a Kapro version that requires a newer
  KAI, KGI, or KPI contract.
- Run the relevant conformance harness for each external plugin before
  upgrading production hubs.
- Confirm `Plugin.status.ready=true` and fresh
  `status.observedGeneration` before relying on runtime plugin dispatch. When
  `KAPRO_ENABLE_PLUGIN_GATEWAY=true`, ready plugins are hot-loaded and stale or
  incompatible plugins are unloaded.

Within a supported minor upgrade, existing in-flight PromotionRuns continue from
Kubernetes status. Controllers may requeue work after restart, but gate
progress, target phase, approval state, and audit trail are persisted in CRDs.

Recommended upgrade order:

1. Read the release notes and migration notes for CRD, plugin, and operational
   changes.
2. Back up Kapro CRDs and any namespace-scoped Secrets used for notifications,
   approvals, and plugin TLS.
3. Apply CRDs and RBAC.
4. Upgrade external plugin servers and run their conformance suites.
5. Roll one hub operator deployment or shard at a time.
6. Watch `PromotionRun`, `Target`, `Plugin`, and controller
   workqueue metrics until queues drain and observed generations catch up.
7. Resume automation that creates or updates `Promotion` objects.

Rollback is safest when the previous operator version still understands the
stored CRD schema. If a CRD schema changed, roll back only to a version named as
compatible in the release notes. Do not downgrade plugin servers below the
contract version required by the running operator.

## Plugin Certification Path

Kapro defines a future certified plugin path for external KAI, KGI, and KPI
implementations. Certification is non-binding and is not required to run a
plugin. The path is:

1. Implement the published proto contract.
2. Pass the matching conformance harness.
3. Publish tested `Plugin` manifests and operational defaults.
4. Document backend permissions, idempotency behavior, timeout behavior, and
   failure modes.
5. Publish a compatibility matrix listing Kapro versions, contract versions,
   backend versions, and conformance evidence.

Certification, when introduced, will identify plugins that meet Kapro's
published contract and documentation requirements. It will not transfer support
ownership of third-party backends to the Kapro core maintainers.
