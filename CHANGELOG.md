# Changelog

This changelog tracks user-visible API, behavior, packaging, and upgrade
changes for Kapro releases. Kapro is still pre-stable: all Kubernetes APIs are
served as `kapro.io/v1alpha1`, and release notes are the binding compatibility
record for each tag.

## Unreleased

### Added (Promotion lifecycle hooks)

- `Promotion.spec.lifecycle.handlers[]` declares user-defined handlers
  fired asynchronously on coarse Promotion phase transitions. Two handler
  kinds in this release:
  - **`webhook`** — POSTs a CloudEvents v1.0 JSON envelope to an HTTPS
    URL. Supports per-handler `timeout` (default 30s, max 5m),
    `maxRetries` (default 3) with linear backoff on transient failures
    (network errors, 5xx, 408, 429), and an optional `authHeader` whose
    value is sourced from a Kubernetes Secret in the operator's
    namespace.
  - **`event`** — records an additional Kubernetes Event on the
    Promotion with templated `{{.Phase}} / {{.PreviousPhase}} /
    {{.Version}} / {{.Name}} / {{.AttemptName}}` substitution.
- Handlers nominate phases via `on: [Pending, Progressing, Paused,
  Restarting, Succeeded, Failed, Terminating]` and fire when the
  controller transitions into one of those phases.
- **Fire-and-forget**: handler failures never change the Promotion phase
  or block reconcile. Outcomes are recorded in
  `Promotion.status.lifecycleHandlerResults[]` (bounded, newest first)
  and surfaced as `LifecycleHookFired` (Normal) /
  `LifecycleHookFailed` (Warning) Kubernetes Events.
- **At-least-once + idempotent**: the dispatcher dedupes invocations
  keyed by `(handler, phase, attempt)`. A controller restart mid-fire
  may re-invoke a handler; receivers should treat the CloudEvents `id`
  and the `(handler, phase, attemptName)` fields in `data` as the
  idempotency key.
- **SSRF guard** on outbound webhook calls (rejects loopback, private,
  link-local, metadata addresses). Opt-out via
  `KAPRO_LIFECYCLE_INSECURE_WEBHOOKS=1` for in-cluster sinks and local
  development.
- **Prometheus metrics**:
  - `kapro_lifecycle_hook_invocations_total{kind, phase, result}`
  - `kapro_lifecycle_hook_duration_seconds{kind, phase}` (histogram)

### Added

- Restored the `Promotion` CRD as the durable user-facing rollout intent,
  partially reversing #75. The PromotionController materializes each
  Promotion into a `PromotionRun` attempt and mirrors run status back into
  `Promotion.status`. The model mirrors `Deployment → ReplicaSet → Pod` and
  Docker Swarm `Service → Task → Container`: intent is durable, attempts are
  ephemeral.
- `Promotion.spec.kaproRef` references the parent Kapro fleet; the
  PromotionController inherits the rollout plan from `Kapro.spec.promotionplan`
  when `Promotion.spec.promotionPlans` is unset.
- `Promotion.status.activeAttemptRef` (current non-terminal `PromotionRun`)
  and `Promotion.status.attempts[]` (bounded last-20 history, newest-first).
- `PromotionRun.status.phase` gained terminal `Superseded`. When a new
  attempt is stamped under the same Promotion, the previous non-terminal
  run is patched to `Superseded` with reason `SupersededByNewPromotionAttempt`.
- Admission webhook for `PromotionRun`: create/update is denied unless the
  requester is the Kapro controller service account (configurable via env
  `KAPRO_PROMOTIONRUN_WRITERS`). `system:masters` is always allowed as
  break-glass.
- Recommended RBAC role `kapro-promotion-engineer` (renamed from
  `kapro-promotionrun-engineer`): users get Promotion CRUD; PromotionRun
  is read-only.

### Changed

- `kapro promote <kapro> --version <v>` now creates a `Promotion` (not a
  `PromotionRun`). The CLI surface is unchanged for the common case.
- `kapro promotionrun create` is now documented as advanced/debug usage that
  bypasses the intent layer.
- `PromotionController` stamps a NEW `PromotionRun` whenever the deterministic
  hash of `Promotion.spec` changes; existing runs are never rolling-updated
  for new desired versions. Spec hash covers kaproRef, version, versions,
  plans, scope, and timeout (suspended deliberately excluded).

### Changed (cont.) — PromotionTrigger now emits Promotion

- Renamed `PromotionTrigger.spec.promotionrunTemplate` to
  `spec.promotionTemplate`. The template adds a required `kaproRef` field
  pointing at the parent Kapro fleet. Existing trigger manifests must be
  rewritten (alpha API, no migration tooling).
- Renamed status `activePromotionRuns` (slice) to `managedPromotion`
  (single name) + `activePromotionRunCount`. Added
  `status.recentArtifacts[]` (bounded last 20) so tag movement is recorded
  even when the dedup path skips a Promotion update.
- `PromotionTrigger` now creates or updates a single managed Promotion per
  trigger; the PromotionController stamps PromotionRun attempts under it.
  Dedup: skips when the managed Promotion's `spec.version` already matches
  the observed digest AND the trigger template hash is unchanged.
- A tag flip A → B → A now produces three Promotion updates only when the
  active digest differs from A at the moment of the flip; redundant
  same-digest observations are coalesced into the recent-artifact history.
- Renamed RBAC role `kapro-promotion-trigger-admin` keeps its existing
  grants; triggers no longer need direct PromotionRun write access.

### Migration from v0.5.0-rc.0 (#75)

- Existing `PromotionRun` manifests still work; they execute directly without
  a Promotion parent.
- New users should author a `Promotion` (or use `kapro promote`) instead of
  a `PromotionRun` so the CI re-stamp, audit, and cancellation semantics
  are first-class.

### Added (Docker-style Promotion lifecycle)

- `Promotion.status.phase` is now a Docker-container-shaped state machine:
  `Pending` (created), `Progressing` (running), `Paused` (suspended),
  `Restarting` (new attempt after a prior terminal one), `Succeeded`
  (exited 0), `Failed` (exited >0), `RollingBack` (reserved for
  `spec.rollbackTo`), `Terminating` (removing). The old enum
  (`Pending/Running/Promoted/Failed/Suspended`) is replaced.
- `Promotion.status.conditions` now publishes `Ready`, `Progressing`,
  `Suspended`, and `RollbackAvailable`. `RollbackAvailable=True` once any
  prior attempt reached `Succeeded` — observability today, wired to a
  rollback feature in a follow-up.
- The controller emits a Kubernetes `Event` on every coarse phase
  transition (e.g. `Pending -> Progressing -> Succeeded -> Restarting`),
  marking `Failed` as `Warning` and everything else as `Normal`.
- `kubectl delete promotion <name>` now publishes `phase=Terminating`
  before owner-reference GC drains child `PromotionRun` objects.

### Fixed

- `Promotion.spec.suspended` now propagates to the freshly-stamped
  `PromotionRun.spec.suspended` at t=0. Previously, a Promotion whose
  spec went from suspended to unsuspended on a new generation could
  briefly stamp a non-suspended run; this is now sealed by writing the
  parent's current suspend state into `buildRunSpec`.
- `PromotionController` now references the materialized inline
  `PromotionPlan` by its generated name (`<kapro>-promotionplan`) via the
  shared `InlinePromotionPlanName` helper, instead of using the bare
  Kapro name. This unblocks `PromotionRun` plan resolution when a
  Promotion does not specify `spec.promotionPlans`.

### Changed

- Re-centered the public API on `Kapro`, `Promotion`, `PromotionPlan`, and
  `PromotionTarget`; `PromotionRun` is now controller-authored runtime state
  and admission gates direct human writes. Advanced reusable objects remain
  available where they add real value.
- `kapro init` now generates inline `Kapro.spec.source` for the default
  greenfield path instead of teaching a separate `PromotionSource` first.
- `kapro source package` now reads inline source units from
  `Kapro.spec.source` with `--kapro <name>` when pull-mode packaging.
- Removed obsolete namespace flags from public CLI paths because
  `PromotionRun`, `PromotionTarget`, `Approval`, and rollback are
  cluster-scoped.

### Performance

- `PromotionController` now indexes `Promotion.spec.kaproRef` and uses
  `MatchingFields` to enqueue only the Promotions referencing a changed
  Kapro fleet, instead of listing every Promotion and filtering in
  memory.
- Terminal coarse phases (`Succeeded`, `Failed`, `Paused`, `Terminating`)
  no longer trigger periodic 15s requeues; the controller relies on
  child `PromotionRun` watches and spec edits to wake up. Active phases
  retain the 15s cadence as a belt-and-braces for missed watch events.

### Removed

- Removed the `PromotionPolicy`, `NotificationProvider`, and
  `NotificationPolicy` CRDs from generated manifests, Helm CRDs, bootstrap
  CRDs, controller registration, and examples. `Promotion` itself was
  removed in an earlier draft of this release and has since been restored
  as the durable user-facing intent (see the "Added" section above).

### Migration

- Wrap any direct `PromotionRun` manifests in a `Promotion` (or use
  `kapro promote`) so the CI re-stamp, audit, and supersession semantics
  are first-class. Existing `PromotionRun` manifests continue to apply
  for the duration of the alpha but are now considered an advanced path;
  default RBAC grants users `get/list/watch` only.
- Move reusable guardrails into inline `PromotionPlan` stage gates
  (`GatePolicySpec`, including CEL gates). Cluster-wide admission, freeze
  windows, and org-level policy are now deferred to external policy
  engines (e.g. Kyverno, Gatekeeper) — there is no longer an in-tree
  `PromotionPolicy` CRD or runtime freeze-window enforcement.
- Keep notification routing inline on gates and stages. Centralized
  provider reuse via `NotificationProvider` is removed; teams that
  previously shared a provider across many policies must duplicate the
  inline routing or front it with an external notifier.
- Helm upgrades do not delete CRDs that are already installed in a
  cluster. If an existing alpha hub installed the removed CRDs, delete
  the stale `promotionpolicies`, `notificationproviders`, and
  `notificationpolicies` CRDs manually after migrating stored objects.
  Existing `promotions` objects are preserved by the restoration.

## v0.4.0-alpha.0 - 2026-05-17

`v0.4.0-alpha.0` is the first alpha release for the current Kapro promotion
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
  conformance, operations, monitoring, and API stability docs.

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
- Removed community-positioning, alpha/GA positioning, duplicate security-model,
  and release-notes guide pages from the documentation set. The public index
  now links only the operator, concept, backend, security, extension, and
  release history docs that users need for onboarding.

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

Release verification for `v0.4.0-alpha.0` completed on 2026-05-17 with:

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

No verification waivers were recorded.

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
