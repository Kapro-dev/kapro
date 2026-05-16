# API Stability and Upgrade Policy

Kapro uses explicit API maturity levels for CRDs, Go extension packages,
lifecycle events, and language-neutral plugin contracts. The maturity level
describes compatibility expectations for users and plugin authors; it does not
change Kubernetes API version strings by itself.

The current release line is pre-stable. `v0.1.0-alpha` is the first planned
version anchor, not a promise that all `kapro.io/v1alpha1` fields are stable.
Release notes are the binding upgrade record for each tag.

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
| Core promotion CRDs | `api/v1alpha1` `PromotionSource`, `Pipeline`, `Release`, `ReleaseTarget`, `MemberCluster`, `Approval`, `AgentPolicy` | Alpha |
| ReleaseTrigger CRD | `api/v1alpha1` `ReleaseTrigger` | Preview |
| PluginRegistration CRD | `api/v1alpha1` `PluginRegistration` | Preview |
| Notification provider/policy CRDs | `api/v1alpha1` `NotificationProvider`, `NotificationPolicy` | Preview |
| In-process actuator interface | `pkg/actuator` | Preview |
| In-process gate interface | `pkg/gate` | Preview |
| In-process planner interface | `pkg/planner` | Preview |
| KAI actuator plugin contract | `spec/kai/v1alpha1` | Preview |
| KGI gate plugin contract | `spec/kgi/v1alpha1` | Preview |
| KPI planner plugin contract | `spec/kpi/v1alpha1` | Preview |
| Conformance harnesses | `conformance/actuator`, `conformance/gate`, `conformance/planner` | Preview |
| Lifecycle event schema | `docs/events.md` | Preview |

All `v1alpha1` APIs remain below stable maturity until Kapro publishes a
stable API version. A surface can be Preview while the Kubernetes version is
still `v1alpha1`; the table above is the source of truth for compatibility
expectations.

No public surface is Stable in `v0.1.0-alpha`.

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

Kapro does not publish conversion webhooks in `v0.1.0-alpha`. Operators should
therefore assume that the storage schema in a tagged release must be readable by
that same operator version and by any downgrade version named in release notes.

There is no automatic legacy conversion for pre-release objects such as the
removed `KaproBundle` experiment. The project had no supported public install
before the `PromotionSource` architecture; users testing unreleased branches
should recreate those objects from the generated examples instead of relying on
controller-side migration code. The first tagged release that documents a CRD as
Preview must include explicit migration notes before removing or renaming that
surface.

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

Every release should also update `CHANGELOG.md` using the structure in
`docs/release-notes.md`.

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
- Keep `Release` and `ReleaseTarget` objects immutable from automation while an
  operator upgrade is in progress; create a new `Release` for rollback.
- Upgrade plugin servers before enabling a Kapro version that requires a newer
  KAI, KGI, or KPI contract.
- Run the relevant conformance harness for each external plugin before
  upgrading production hubs.
- Confirm `PluginRegistration.status.ready=true` and fresh
  `status.observedGeneration` before restarting an operator that loads startup
  plugin registrations.

Within a supported minor upgrade, existing in-flight releases continue from
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
6. Watch `Release`, `ReleaseTarget`, `PluginRegistration`, and controller
   workqueue metrics until queues drain and observed generations catch up.
7. Resume automation that creates new `Release` objects.

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
3. Publish tested `PluginRegistration` manifests and operational defaults.
4. Document backend permissions, idempotency behavior, timeout behavior, and
   failure modes.
5. Publish a compatibility matrix listing Kapro versions, contract versions,
   backend versions, and conformance evidence.

Certification, when introduced, will identify plugins that meet Kapro's
published contract and documentation requirements. It will not transfer support
ownership of third-party backends to the Kapro core maintainers.
