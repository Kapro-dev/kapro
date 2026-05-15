# Changelog

This changelog tracks user-visible API, behavior, and release-process changes.
Kapro is currently preparing `v0.1.0-alpha`; entries below that heading are the
release-note structure for the first tagged pre-stable release.

Release notes must call out CRD schema changes, plugin contract changes,
deprecations, removals, upgrade steps, and compatibility expectations. See
`docs/release-notes.md` and `docs/api-stability.md`.

## Unreleased

### Added

- Added API stability and release hygiene documentation for pre-stable releases.
- Added the `v0.1.0-alpha` release-note structure and follow-up checklist.

### Changed

- Clarified alpha, preview, and stable API surface expectations for CRDs,
  extension packages, plugin contracts, and lifecycle event schemas.

### Deprecated

- None.

### Removed

- None.

### Migration

- None.

## v0.1.0-alpha — Pending

First alpha milestone for Kapro. This release is intended to provide a concrete
version anchor for early adopters and contributors while keeping the `v1alpha1`
API below stable maturity.

### Scope

- Publish installable CRDs and the operator chart for local and controlled
  development environments.
- Document the core promotion workflow: `KaproBundle`, `Pipeline`, `Release`,
  `ReleaseTarget`, `MemberCluster`, and `Approval`.
- Publish preview extension contracts for in-process actuators, gates, planners,
  and the KAI/KGI/KPI gRPC plugin APIs.
- Publish preview policies for `ReleaseTrigger`, `PluginRegistration`,
  notification provider/policy APIs, and lifecycle event payloads.
- Publish conformance package entry points for plugin authors.

### Compatibility

- All CRDs remain `kapro.io/v1alpha1` and below stable maturity.
- Alpha CRD fields may change before `v0.2.0`; documented examples and shipped
  manifests should receive migration notes when they change.
- Preview plugin contracts must remain compatible within this release line
  unless a release note marks a breaking alpha change explicitly.
- Stored status is the recovery source for in-flight releases; do not rely on
  controller memory or log output as an API.

### Upgrade Notes

- Apply CRD updates before rolling the operator.
- Run KAI, KGI, or KPI conformance packages before enabling external plugin
  images with a new Kapro build.
- Read `docs/api-stability.md` before upgrading a hub with in-flight releases
  or enabled plugin registrations.

### Known Gaps Before v0.2.0

- No stable API version is published yet.
- Plugin gateway runtime dispatch is preview and startup-time only for actuator
  and gate registrations.
- Planner plugin runtime dispatch remains future work.
- API conversion webhooks are not yet part of the release process.
- Markdown and release-note checks are not yet enforced in CI.

### v0.2.0 Follow-up Checklist

- [ ] Decide which core CRD fields move from Alpha to Preview.
- [ ] Add a CRD schema compatibility check to CI or document the manual command.
- [ ] Reserve removed proto field numbers before any KAI/KGI/KPI contract
      cleanup.
- [ ] Add release-note validation to the pull request checklist or CI.
- [ ] Add explicit migration notes for every changed shipped example.
- [ ] Reconcile `CHANGELOG.md` against the tag contents before cutting
      `v0.2.0`.

---

## Historical Draft Entries

The entries below predate the `v0.1.0-alpha` release hygiene structure. Keep
them for context, but reconcile them against the tagged contents before using
them as GitHub release notes.

## v0.3.0 — Spoke-Local Flux + CLI + GCP-Native

Production-ready spoke-local delivery mode with complete CLI and GCP integration.
All operations use Go SDK — no gcloud, helm, flux, or kubectl dependencies.

### Spoke-Local Flux (deliveryMode: spoke)

- **OCI bundle pull model** — spoke's own Flux controllers pull and reconcile bundles from GAR
- **Per-wave directories** — bundle structured as wave-00/, wave-01/, wave-02/ to avoid Kustomization resource conflicts
- **Wave Kustomization DAG** — each wave has dependsOn to previous wave, visible in k9s on every spoke
- **HelmReleases without kubeConfig** — spoke's helm-controller reconciles locally (not hub pushing remotely)
- **Spoke actuator** — patches OCIRepository tag on spoke, reads Flux status directly for convergence
- **Both modes coexist** — `deliveryMode: push` (v0.2 behavior) and `deliveryMode: spoke` (new) on same hub

### CLI (`kapro`)

- **`kapro hub init`** — bootstraps hub cluster: flux-operator, FluxInstance, CRDs, Fleet registration. Interactive project/cluster selection when no flags provided.
- **`kapro hub registry list/create/add`** — centralized GAR registry management. Saved to `~/.kapro/config.yaml`.
- **`kapro spoke add`** — adds spoke cluster: auto-installs flux-operator + FluxInstance + Fleet registration + IAM bindings. Single command, zero manual steps.
- **`kapro fleet list/sync`** — Fleet membership management. `sync` auto-discovers and registers all Fleet clusters.
- **`kapro bundle generate --push`** — reads KaproBundle from hub, generates per-wave bundle, validates, pushes via ORAS Go SDK. Used by CI pipelines.
- **`kapro status`** — live fleet dashboard with colored phases (Converged/Converging/Failed), per-cluster version, health, heartbeat.
- **`~/.kapro/config.yaml`** — persistent CLI context. `hub init` writes it, all commands read project/registry from it.
- **Spinner UX** — braille animation with colored status symbols: ✔ (success), ✗ (failure), ⚠ (warning), ℹ (info).

### GCP-Native (zero shell dependencies)

- **ORAS Go SDK** for OCI bundle push (replaces `flux push artifact` shell exec)
- **Fleet Hub API** for cluster discovery and membership registration
- **Container API** for cluster endpoint resolution and location auto-detection
- **IAM + CRM APIs** for cross-project spoke access and service account bindings
- **Artifact Registry API** for GAR repository creation and listing
- **Workload Identity** for authentication — zero credentials on GKE, gcloud fallback for local dev

### Production Scale (150 clusters)

- **Parallel spoke bootstrap** — bounded concurrency (10 at a time) via semaphore channel
- **Spoke client cache** — sync.Map with 5min TTL, health probe before reuse (expired tokens auto-invalidate)
- **Version-change detection** — skip bootstrap if MemberCluster already converged at target version
- **Error isolation** — failing spoke doesn't block other spokes in the reconcile loop
- **Embedded CRDs** — FluxInstance, ResourceSet, and 8 Kapro CRDs via go:embed (no external file deps)

### KaproBundle Validation

- No empty component names, versions, or registries
- No duplicate component or registry names
- DependsOn references must exist
- Dependencies must be in same or earlier wave
- Registry URLs must match declared type (oci:// for OCI type)

### Release Flow (e2e verified on GKE)

- Release CR → Progressing → ReleaseTarget created per cluster
- FSM: Verification ✔ → HealthCheck ✔ → Applying
- SpokeFluxActuator.Apply() → patches OCIRepository tag on spoke
- Spoke pulls new bundle → wave DAG reconverges
- MemberCluster.status.version updated from OCIRepository tag

### Breaking Changes

- `kapro cluster bootstrap` removed — use `kapro spoke add`
- `kapro gcp *` commands removed — use `kapro fleet list/sync`
- `kapro world` / `kapro fleet` removed — use `kapro status`
- `kapro promote` removed — use Release CR
- CLI commands renamed: `cluster add` → `spoke add`

---

## v0.2.0 — Push Model Complete

Flux Operator actuator, Fleet API integration, and GCP SDK.

### Flux Operator Actuator

- **ResourceSet-based delivery** — patches ResourceSet inputs on hub, Flux Operator renders per-cluster HelmReleases with kubeConfig
- **KaproBundle component spec v2** — registries, waves, dependsOn, values, valuesFrom, timeout, retries, CRDs, suspend
- **ResourceSet generator** — builds HelmRepositories + HelmReleases from KaproBundle spec
- **Convergence check** — scans ResourceSet inventory for HelmRelease Ready status

### Fleet API + GCP SDK

- **GCPFleetProvider** — auto-discovers clusters from Fleet memberships via Go SDK
- **GCPBasicProvider** — GKE DNS endpoint + Workload Identity, zero gcloud dependency
- **Auto-generate kubeconfig secrets** from Fleet API + WI tokens
- **Token caching** — shared OAuth2 token source, auto-refresh

### Fleet Observability

- **MemberCluster status sync** — reads HelmRelease status, writes version + health to MemberCluster
- **k9s columns** — version, phase, convergence, healthy visible in `kubectl get memberclusters`

---

## v0.1.0 — Initial Release

First release of Kapro: CRDs, FSM, gates, approval webhook.

### CRDs (7)

- Artifact, Pipeline, Release, ReleaseTarget, MemberCluster, Approval, Source

### Controllers (5)

- ReleaseReconciler (two-level DAG), ReleaseTargetReconciler (10-state FSM), SourceReconciler, ApprovalReconciler, CSRApprovalReconciler

### Gates (8)

- Soak, Approval, Metrics, HealthCheck, CEL, Job, Webhook, Verification

### CLI

- cluster bootstrap/join, get releases/targets, approve/reject, rollback, release create, world

### Scalability

- Controller sharding, lease-based heartbeat, field-indexed lookups, conditional poll
