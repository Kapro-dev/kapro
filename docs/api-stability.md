# API Stability and Upgrade Policy

Kapro uses explicit API maturity levels for CRDs, Go extension packages, and
language-neutral plugin contracts. The maturity level describes compatibility
expectations for users and plugin authors; it does not change Kubernetes API
version strings by itself.

## Maturity Levels

| Level | Meaning | Compatibility |
|---|---|---|
| Alpha | Experimental surface used to validate the model. | May change or be removed between minor releases. Migration notes are provided when a surface is used by examples or documented workflows. |
| Preview | Implemented and intended for early adopters. | Compatible within a minor release. Breaking changes require release notes, a migration path, and at least one minor release of overlap when practical. |
| Stable | Production contract. | Backward-compatible within the major version. Breaking changes require a new API version or a documented major-version migration. |

## Current Surface Classification

| Surface | Path | Level |
|---|---|---|
| Core promotion CRDs | `api/v1alpha1` `KaproApp`, `Pipeline`, `Release`, `ReleaseTarget`, `MemberCluster`, `Approval`, `AgentPolicy` | Alpha |
| ReleaseTrigger CRD | `api/v1alpha1` `ReleaseTrigger` | Preview |
| PluginRegistration CRD | `api/v1alpha1` `PluginRegistration` | Preview |
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

## Deprecation Policy

Deprecation follows these rules:

- A deprecated field, enum value, package API, or proto field is marked in
  documentation and release notes.
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
published manifests, or conformance tests still include migration notes.

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

Within a supported minor upgrade, existing in-flight releases continue from
Kubernetes status. Controllers may requeue work after restart, but gate
progress, target phase, approval state, and audit trail are persisted in CRDs.

## Plugin Certification Path

Kapro defines a future certified plugin path for external KAI, KGI, and KPI
implementations. Certification is non-binding and is not required to run a
plugin. The path is:

1. Implement the published proto contract.
2. Pass the matching conformance harness.
3. Publish tested `PluginRegistration` manifests and operational defaults.
4. Document backend permissions, idempotency behavior, timeout behavior, and
   failure modes.

Certification, when introduced, will identify plugins that meet Kapro's
published contract and documentation requirements. It will not transfer support
ownership of third-party backends to the Kapro core maintainers.
