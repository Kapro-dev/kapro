# Changelog

This changelog tracks user-visible API, behavior, packaging, and upgrade
changes for Kapro releases. Kapro is still pre-stable: all Kubernetes APIs are
served as `kapro.io/v1alpha1`, and release notes are the binding compatibility
record for each tag.

## Unreleased

### Added

- None.

### Changed

- None.

### Deprecated

- None.

### Removed

- None.

### Migration

- None.

## v0.4.0-alpha.0 - Pending

`v0.4.0-alpha.0` is the first release candidate for the current Kapro promotion
domain architecture. It is intended for controlled alpha adopters who can run
the documented verification suite and accept `v1alpha1` API movement.

### Added

- Added the full promotion-domain API surface around `Promotion`,
  `PromotionRun`, `PromotionTarget`, `PromotionPlan`, `PromotionPolicy`,
  `PromotionSource`, `PromotionUnit`, `FleetCluster`, `BackendProfile`,
  `PromotionTrigger`, `PluginRegistration`, `NotificationProvider`, and
  `NotificationPolicy`.
- Added runtime enforcement for `PromotionPolicy` CEL checks and freeze windows
  before `PromotionRun` creation, including audit-mode Events and
  `onFailure: continue` handling.
- Added Argo CD brownfield onboarding for existing Applications, multi-source
  Applications, app-of-apps children, ApplicationSet Git generator inputs, and
  cluster Secrets.
- Added Flux brownfield onboarding for `GitRepository`, `OCIRepository`,
  `Bucket`, `Kustomization`, and `HelmRelease` version fields.
- Added `PromotionSource` and `PromotionUnit` mappings so Kapro can promote
  backend-native version fields without requiring one packaging format.
- Added Git-native source write support for discovered JSON, YAML, Kustomize
  image, Argo source, and Flux source fields.
- Added `BackendProfile` discovery and observe/adopt policy for greenfield and
  brownfield backends.
- Added hot-loaded plugin runtime registration for KAI actuators, KGI gates, and
  KPI planner plugins when `KAPRO_ENABLE_PLUGIN_GATEWAY=true`.
- Added KPI planner runtime dispatch so external planner plugins can filter,
  defer, and score targets while Kapro retains binding and state ownership.
- Added PromotionTrigger OCI source observation with digest pinning, tag
  filtering, cooldown, max-active limits, and signature policy safeguards.
- Added lifecycle event and notification documentation, including CloudEvents
  webhook payload guidance.
- Added install, Kind demo, Argo E2E, Flux Git-native E2E, live Flux E2E,
  conformance, operations, monitoring, API stability, and GA readiness docs.

### Changed

- Reframed Kapro as an agent-ready promotion controller for Kubernetes fleets:
  intent plus promotion plan, policy checks, backend apply, health evidence, and
  rollback decision support.
- Replaced the old packaging-centric docs with the `PromotionSource` /
  `PromotionUnit` architecture.
- Renamed the public domain language away from release/member terminology and
  toward `FleetCluster`, `Promotion`, `PromotionRun`, and `PromotionTarget`.
- Changed Promotion policies from a fail-closed reserved field into an enforced
  alpha runtime for CEL and freeze-window checks.
- Changed plugin registration from startup-only discovery into hot-loaded
  runtime registration after readiness probes succeed.
- Changed backend discovery to refresh from backend objects when opted in with
  `KAPRO_ENABLE_BACKEND_OBJECT_WATCHES=true`; Argo CD cluster Secrets are
  watched without the optional backend-object watch flag.
- Changed release documentation to use `v0.4.0-alpha.0` as the candidate tag and
  to require explicit verification evidence before tagging.

### Deprecated

- Deprecated any unreleased manifests or docs that still refer to the removed
  packaging prototype. Use `PromotionSource` plus `PromotionUnit` mappings instead.
- Deprecated release/member-era names from unreleased branches. Use
  `FleetCluster`, `Promotion`, `PromotionTrigger`, `PromotionRun`, and
  `PromotionTarget`.

### Removed

- Removed the public packaging prototype workflow from the release-facing
  documentation set.
- Removed the standalone evolution plan page. Completed milestones are recorded
  here; future work is tracked in `docs/ROADMAP.md`.
- Removed the obsolete first-alpha release runbook from the documentation index.

### Migration

- Apply the `v0.4.0-alpha.0` CRDs and RBAC before rolling the operator.
- Replace any local pre-0.4 packaging test manifests with `PromotionSource`,
  `PromotionUnit`, `BackendProfile`, `PromotionPlan`, and `PromotionRun`
  manifests from `examples/hub-config/`.
- Replace old release/member names from pre-release branches with the current
  promotion-domain kinds.
- Start Argo and Flux brownfield onboarding in observe mode, review generated
  `PromotionSource` mappings, then switch selected objects to adopt/write mode.
- Run KAI, KGI, and KPI conformance before enabling any external plugin through
  `PluginRegistration`.
- Keep artifact signature policy on `PromotionTrigger` for this release;
  `PromotionPolicy.spec.verification` remains a preview field and is not the
  enforcement path yet.

### Compatibility

- CRD schema: pre-stable `kapro.io/v1alpha1`; compatible only within the
  documented `v0.4.0-alpha.0` operator and CRD set.
- Plugin contracts: KAI, KGI, and KPI are preview contracts. Run conformance
  before using external plugins with this release.
- Lifecycle events: event type names and documented payload fields are preview
  integration contracts for this release line.
- Downgrade: do not downgrade a hub with stored `v0.4.0-alpha.0` objects to an
  older unreleased operator unless the release notes for that operator explicitly
  name the stored schema as compatible.

### Verification

Before tagging `v0.4.0-alpha.0`, run:

```bash
go test ./...
make build
make lint
make validate-yaml-json
make check-markdown-links
scripts/verify-install.sh render
scripts/verify-install.sh kind-demo
scripts/verify-install.sh argo-e2e
scripts/verify-install.sh flux-git-e2e
scripts/verify-install.sh flux-e2e
```

If an environment cannot run Docker, Kind, Argo CD, Flux, or external network
dependencies, record the waiver in the release notes before tagging.

### Known Gaps

- No stable Kubernetes API version is published yet.
- Conversion webhooks are not part of this release.
- `NotificationProvider` and `NotificationPolicy` are API previews; inline gate
  notifications remain the active runtime path.
- `PromotionPolicy.spec.verification` is present but not the enforcement path;
  use PromotionTrigger signature verification for artifact policy.
- Production soak across many independent operators and repository styles is not
  published yet.
- The documented security model has not had an independent audit.
