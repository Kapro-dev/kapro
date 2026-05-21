# Changelog

This changelog tracks user-visible API, behavior, packaging, and upgrade
changes for Kapro releases. Kapro is still pre-stable: all Kubernetes APIs are
served as `kapro.io/v1alpha2`, and release notes are the binding compatibility
record for each tag.

## Unreleased

### ⚠️ Breaking — `kapro.io/v1alpha2` migration (clean break; conversion scaffold only)

All CRDs moved from `kapro.io/v1alpha1` to `kapro.io/v1alpha2`. ADR-0011 adds
the `/convert` handler scaffold, but the shipped CRDs do not enable conversion
strategy, no v1alpha1 served version remains, and no automatic legacy
conversion path is published for this migration. This remains a clean break
appropriate for pre-stable software with no production users yet.

**Kind renames** (the new short forms are the canonical names going forward):

| v1alpha1                    | v1alpha2          |
| --------------------------- | ----------------- |
| `Kapro`                     | `Fleet`           |
| `FleetCluster`              | `Cluster`         |
| `FleetClusterTemplate`      | `ClusterTemplate` |
| `AgentPolicy`               | `Policy`          |
| `PromotionSource`           | `Source`          |
| `PromotionTrigger`          | `Trigger`         |
| `PromotionPlan`             | `Plan`            |
| `PromotionTarget`           | `Target`          |
| `BackendProfile`            | `Backend`         |
| `PluginRegistration`        | `Plugin`          |
| `PromotionUnit`             | `Unit`            |

**Field renames** in `Promotion`, `Trigger`, and stamped `PromotionRun`:

- `spec.kaproRef` → `spec.fleetRef`
- `spec.promotionPlan` (inline plan on `Fleet`) → `spec.plan`
- `spec.promotionPlans[].promotionPlanRef` → `spec.plans[].planRef`

CloudEvents payload field `data.kaproRef` is renamed to `data.fleetRef`.

**Upgrade path**: there is no automatic operator migration for legacy v1alpha1
objects. Delete legacy v1alpha1 objects and old prototype CRDs before applying
the new `kapro.io/v1alpha2` CRDs, then re-author manifests with the new Kinds
and field names.

### Changed — default controller set narrowed

The Helm chart and operator fallback now start the ADR-0010 core controllers by
default: `fleet`, `plan`, `promotion`, `promotionrun`, and `cluster`. The
`target` controller starts implicitly with `promotionrun`. Preview controllers
such as `backend`, `approval`, `trigger`, `plugin`, `cluster-bootstrap`, and
`clustertemplate` must be listed explicitly in `controllers` when needed.
Built-in `flux`, `argo`, and `oci` Backend specs remain admissible without the
backend controller; external/plugin backends still require Ready status.

Older controller keys such as `kapro`, `promotion-target`, `backend-profile`,
and `promotion-trigger` are still accepted as compatibility aliases, but new
manifests and Helm values should use the canonical keys.

### Added — `kapro lint`

Static analysis for Kapro YAML manifests. Runs without a cluster
connection so it is safe in CI pipelines and pre-commit hooks.

```
kapro lint examples/quickstart/*.yaml
kapro lint --strict promotion.yaml plan.yaml
cat promotion.yaml | kapro lint -
kapro lint -o json promotion.yaml
```

Checks:

- **Fleet** — exactly one of `spec.source` / `spec.sourceRef`,
  `spec.delivery.backendRef` set, `spec.clusters` non-empty.
- **Promotion** — `spec.fleetRef` set, `spec.version` or
  `spec.versions` set, `spec.timeout` set (advisory), no duplicate
  scope targets, non-empty Plan refs.
- **Plan** — unique stage names, every `dependsOn[].stage`
  references a real stage, no self-dependencies, no cycles in the
  DAG, manual gates declare `approval.required: true` (silent
  auto-advance is an ERROR), manual gates with `required: true` list
  at least one approver (WARN), metric gates have a preset or
  threshold.

Severity: `ERROR` fails (exit 1), `WARN` is advisory (exit 0).
`--strict` upgrades warnings to errors. `-o json` emits the issue
list as a stable JSON array. Other Kapro kinds
(Cluster, Backend, PromotionRun, …) are skipped silently
so `kapro lint **/*.yaml` does not flag manifests the linter has no
rules for yet.

### Added — `kapro diag` CLI

New `kapro diag <promotion>` command renders a single-screen narrative of a
Promotion's current state: phase + age, conditions, attempt history, active
run targets, blocked-on hints (gates, approvals, suspension), recent
Kubernetes Events, and suggested next-action commands.

Use it as the default "what is this Promotion doing right now?" entry point
before reaching for `kubectl describe`. Supports `-o json` for scripting and
`--events N` to bound the events table.

```
kapro diag checkout-v1.2.3
kapro diag checkout-v1.2.3 -o json
kapro diag checkout-v1.2.3 --events 25
```

### Added — `kapro suspend` / `kapro resume`

Shortcut subcommands that flip `Promotion.spec.suspended` so operators do
not have to hand-craft a `kubectl patch` payload. Idempotent: a no-op
when the Promotion is already in the desired state.

```
kapro suspend checkout-v1.2.3
kapro resume  checkout-v1.2.3
```

### Documented — shell completion (cobra default)

Cobra already ships a `kapro completion {bash,zsh,fish,powershell}`
subcommand; it is now called out in `--help`. Example:

```
kapro completion zsh > "${fpath[1]}/_kapro"
```

### Added — reference `Plan` library (`examples/plans/`)

Six copy-paste-ready Plans covering the most common shapes:

1. `01-canary-then-full.yaml` — one canary, then everything else.
2. `02-blue-green.yaml` — single-cluster blue/green with manual cutover.
3. `03-multi-region-staggered.yaml` — EU → US → APAC with cross-region soak.
4. `04-region-failover.yaml` — primary + passive standby (failover stays safe).
5. `05-ring-deployment.yaml` — concentric rings with increasing parallelism.
6. `06-metrics-gated.yaml` — canary holds error_rate + p99_latency below thresholds.

A unit test (`examples/plans/plans_validate_test.go`) parses each YAML
into `kapro.io/v1alpha2.Plan` and checks DAG references, so
schema drift between the docs and the CRD source-of-truth fails the
build.

### Added — Grafana lifecycle + fleet-health dashboard

`examples/monitoring/kapro-lifecycle-dashboard.json` is a focused
companion to the existing operations dashboard. It visualises the gap
the older dashboards did not cover:

- **Promotion lifecycle hooks** — invocation rate by kind
  (`Webhook` / `Event` / `Sink`), failure rate, dispatch p50/p95/p99
  latency, and breakdown by Promotion phase. Uses
  `kapro_lifecycle_hook_invocations_total` and
  `kapro_lifecycle_hook_duration_seconds`.
- **CloudEvents sink** — throughput stat and per-phase breakdown so
  subscribers can see exactly which Promotion phases are emitting.
- **FleetCluster health** — heartbeat misses per cluster, unreachable
  vs recovered transition rates. Uses
  `kapro_fleetcluster_heartbeat_misses` and the
  `kapro_fleetcluster_{unreachable,recovered}_transitions_total`
  pair.
- **FSM drift signal** —
  `kapro_fsm_unexpected_transitions_total{from,to}` non-zero rates as
  an early warning that the documented FSM has drifted from handler code.

Import alongside `kapro-operations-dashboard.json`.
## Unreleased

### Fixed — README quickstart now works on a fresh cluster, no prereqs

Five fixes that, together, make `helm upgrade --install kapro ...`
followed by `kubectl apply -f examples/quickstart/*.yaml` reach
`Promotion: Succeeded` on a vanilla `kind` cluster with nothing
pre-installed.

- **Helm chart now ships a self-signed webhook serving cert by
  default** (`webhook.certManager.enabled: false`). The cert is
  generated at first install via `genCA` / `genSignedCert` and
  reused across `helm upgrade` via `lookup`, so the webhook is not
  briefly broken on every release. cert-manager remains supported
  (`--set webhook.certManager.enabled=true`) for installs that
  prefer its certificate lifecycle.
- **Chart auto-injects the operator service account into
  `KAPRO_PROMOTIONRUN_WRITERS`**. The admission webhook restricts
  `PromotionRun` writes to a service-account allowlist; previously
  the chart did not set this and the operator was rejected when
  trying to create its own `PromotionRun` for the controller's own
  Promotion reconcile. Extra writers can be appended via
  `.Values.promotionRunWriters`.
- **`KAPRO_HUB_API_URL` is no longer required** for the operator to
  start. The `FleetClusterBootstrap` controller self-disables when
  the URL is empty (typical for single-cluster, hub-only, or
  quickstart installs) instead of crashing the whole operator on
  startup.
- **Tenancy ownership label propagates from `Kapro` → child
  `PromotionPlan`** in the inline-plan path, and from `Promotion` →
  child `PromotionRun`. Without this, the controller's own writes
  were denied by its own webhook for missing `kapro.io/team`.
- **Quickstart manifests carry `kapro.io/team: platform`** so a
  fresh apply hits the tenancy webhook with a valid value.

End-to-end verified on `kind-kind`: install → apply quickstart →
`Promotion.status.phase: Succeeded` and `PromotionRun.status.phase:
Complete` reached in under 15 seconds.

## v0.1.0 - 2026-05-19

`v0.1.0` is the first public Kapro release. It supersedes the earlier alpha
tag line and publishes the durable `Promotion` intent plus controller-owned
`PromotionRun` execution model as the public pre-stable baseline.

### BREAKING — CRD field names harmonised to strict Kubernetes camelCase

Fourteen JSON field names across `Kapro`, `Approval`, `PromotionRun`,
`PromotionTarget`, and `AuditEntry` were renamed to match the
Kubernetes camelCase convention. The Go field names are unchanged —
this is a YAML/JSON-on-the-wire change only. Existing alpha or release
candidate manifests must be rewritten before installing `v0.1.0`.

| Path | Was | Becomes |
|---|---|---|
| `Kapro.spec.promotionplan` | `promotionplan` | `promotionPlan` |
| `Approval.spec.promotionrun` | `promotionrun` | `promotionRun` |
| `PromotionRun.spec.promotionplans` | `promotionplans` | `promotionPlans` |
| `PromotionRun.spec.promotionPlans[].promotionplan` | `promotionplan` | `promotionPlan` |
| `PromotionRun.status.promotionplanProgress` | `promotionplanProgress` | `promotionPlanProgress` |
| `PromotionRun.status.promotionPlanProgress[].promotionplan` | `promotionplan` | `promotionPlan` |
| `PromotionRun.status.targets[].promotionrunRef` | `promotionrunRef` | `promotionRunRef` |
| `PromotionRun.status.targets[].promotionplanRef` | `promotionplanRef` | `promotionPlanRef` |
| `PromotionRun.status.targets[].promotionplan` | `promotionplan` | `promotionPlan` |
| `PromotionRun.status.auditTrail[].promotionrun` | `promotionrun` | `promotionRun` |
| `PromotionRun.status.auditTrail[].promotionrunDerivedFrom` | `promotionrunDerivedFrom` | `promotionRunDerivedFrom` |
| `PromotionTarget.spec.promotionrunRef` | `promotionrunRef` | `promotionRunRef` |
| `PromotionTarget.spec.promotionplanRef` | `promotionplanRef` | `promotionPlanRef` |
| `PromotionTarget.spec.promotionplan` | `promotionplan` | `promotionPlan` |

Two printcolumn JSONPaths on `PromotionTarget` updated accordingly.

A new drift canary at `api/v1alpha1/camelcase_canary_test.go`
(`TestJSONTagsAreCamelCase`) fails the build if a future contributor
reintroduces lowercase-two-word JSON tags or snake_case anywhere in
the API. Decision rationale and rejected alternatives (compatibility
shim, defer to v1beta1) are captured in
[ADR-0004](docs/adr/0004-camelcase-field-harmonization.md).

Examples (`examples/quickstart/`, `examples/kind-demo/`,
`examples/brownfield/`, `examples/promotion-trigger/`,
`examples/monitoring/`, and `examples/rbac/`), the CLI scaffold
(`cmd/kapro/scaffold.go`), and migration/security/ADR docs
(`docs/flux-migration.md`, `docs/argo-migration.md`,
`docs/security.md`, and `docs/adr/`) all use the new keys.

No `kapro migrate v0.1-fields` CLI subcommand ships in this release;
the rename list is short enough for `sed` and existing manifests can
be rewritten with a one-liner.

### Added (quality A-pass — docs / tests / process)

- **Three Architecture Decision Records** capture the design decisions
  made across PRs #77–#82 so future contributors don't relitigate them:
  - [ADR-0001](docs/adr/0001-promotion-runtime-split.md) — `Promotion`
    intent vs `PromotionRun` runtime split (Service/EndpointSlice model)
  - [ADR-0002](docs/adr/0002-promotion-docker-lifecycle.md) —
    Docker-style Promotion lifecycle phases
  - [ADR-0003](docs/adr/0003-cloudevents-publisher-posture.md) —
    CloudEvents publisher posture (emit, don't route)
- **`.github/CONTRIBUTING_EVENTS.md`** — a 6-step self-review checklist
  every change to the `pkg/events` vocabulary or its emitters must pass
  before commit. Closes the docs↔code drift gap that cost 21 review
  comments across PRs #80/#81/#82.
- **`TestEventTypesDocumentedInEventsMd`** — drift canary: every
  `EventType` constant exported from `pkg/events` must appear verbatim
  in `docs/events.md`. Build fails on doc/code drift.
- **`TestRenderSucceedsForEveryEventType`** — sweep test: every
  constant in `AllEventTypes()` round-trips through `Render` without
  panic. New constants get exercised automatically.
- All critical packages (`internal/lifecycle`, `pkg/events`) verified
  race-clean with `go test -race`.

### Added (Wave / Stage / Gate CloudEvents — the fleet narrative)

- Seven new EventTypes published to the operator-level CloudEvents sink,
  filling the previously-reserved `kapro.io/promotion.wave.*`,
  `.stage.*`, and `.stage.gate.*` namespaces:
  - `kapro.io/promotion.wave.entered` — PromotionPlan DAG node started
  - `kapro.io/promotion.wave.completed` — PromotionPlan DAG node terminal
  - `kapro.io/promotion.stage.entered` — Stage Pending→Progressing
  - `kapro.io/promotion.stage.completed` — all targets in a Stage Converged
  - `kapro.io/promotion.stage.gate.waiting` — gate evaluation started
  - `kapro.io/promotion.stage.gate.passed` — gate returned Passed
  - `kapro.io/promotion.stage.gate.failed` — gate returned Failed (terminal)
- `pkg/events.Event` and `EventData` carry new `Wave`, `Stage`, `Gate`,
  `Target` fields. CloudEvents `data` includes them as top-level keys
  so subscribers can filter by fleet topology without parsing the type.
- New `StageEventPublisher` interface in `internal/controller` and
  matching `Dispatcher.PublishWaveEvent` / `PublishStageEvent` /
  `PublishGateEvent` methods on `internal/lifecycle.Dispatcher`. The
  per-Promotion handler path is intentionally NOT wired for these
  events — they go to the operator-level sink only. The v1alpha1
  handler `on: [Phase]` filter remains stable; an `onTypes:` extension
  is reserved for a future minor release.
- Transition guards (`previousPromotionPlanPhase`, `previousStagePhase`)
  ensure each event fires exactly once per phase edge, even across
  reconcile loops.
- Updated `docs/events.md` with the new vocabulary, data-field
  schema, and a complete worked example (`stage.gate.passed`).

### Fixed (PR #80 review feedback)

- **Unified per-Promotion webhook payload with `pkg/events.Render`**. The per-Promotion `spec.lifecycle.handlers[].webhook` path used to produce a separate envelope shape with a lowercased-phase type string. Both per-Promotion and operator-level sink deliveries now share the same CloudEvents v1.0 envelope, matching the contract docs.
- **Sink metric labels use the Promotion phase**, not the CloudEvents type. `kapro_lifecycle_hook_*{phase=...}` is uniform across `kind={Webhook,Event,Sink}` so dashboards can group/sum by phase without conditionals.
- **`kapro_lifecycle_hook_duration_seconds` observes on failure too**, not only success. End-to-end latency for slow/failing sinks is now visible.
- **`KAPRO_EVENTS_SINK_TIMEOUT` is total per-event**, not per-attempt. The dispatcher wraps the entire Publish call in `context.WithTimeout` and derives per-attempt sub-budgets from the remaining time. Backoff sleeps now respect the overall deadline.
- **Wired the previously-documented attempt and resume events**:
  - `kapro.io/promotion.attempt.stamped` is published once per new PromotionRun stamped by the controller.
  - `kapro.io/promotion.attempt.superseded` is published once per superseded run.
  - `kapro.io/promotion.resumed` is published as a synthetic event on every transition out of `Paused`, before the new-phase event.
- Metric help text + comments updated to reflect `kind="Sink"`.

### Added (CloudEvents vocabulary + operator-level sink — the CNCF positioning layer)

- New `pkg/events` Go package exporting the **stable Kapro CloudEvents
  vocabulary**: `kapro.io/promotion.{created,progressing,paused,resumed,restarting,succeeded,failed,rollingBack,terminating}`,
  `kapro.io/promotion.attempt.{stamped,superseded}`, and reserved
  `kapro.io/promotion.{wave,stage,target}.*` namespaces.
  Constants are part of the public API; once published they will not be
  renamed or removed within `v1alpha1`.
- Every phase transition is now also published as a **CloudEvents v1.0**
  structured-mode JSON envelope (`application/cloudevents+json`) to the
  operator-level sink when configured. This is the canonical subscription
  point — Argo Events, Flux Notification Controller, kube-event-exporter,
  Knative, AWS EventBridge, Google Eventarc, Azure Event Grid, and any
  CloudEvents-aware system can subscribe.
- Operator sink config via env vars on the `kapro-operator` Deployment:
  `KAPRO_EVENTS_SINK_URL`, `KAPRO_EVENTS_SINK_AUTH_HEADER_NAME` (default
  `Authorization`), `KAPRO_EVENTS_SINK_AUTH_HEADER_VALUE` (best sourced
  via `valueFrom.secretKeyRef`), `KAPRO_EVENTS_SINK_TIMEOUT` (10s
  default), `KAPRO_EVENTS_SINK_MAX_RETRIES` (3 default).
- Sink delivery is fire-and-forget — a sink failure does not block
  per-Promotion `spec.lifecycle.handlers[]` and does not block reconcile.
  Sink outcomes are surfaced via Kubernetes Events (`EventSinkDelivered`,
  `EventSinkFailed`) and the `kapro_lifecycle_hook_*` Prometheus metrics
  (with `kind="Sink"`).
- Documentation:
  - `docs/events.md` — the versioned vocabulary contract and subscriber
    cookbook.
  - `docs/extension-model.md` — event emission as an extension boundary.
  - `.github/CONTRIBUTING_EVENTS.md` — contributor checklist for event
    vocabulary changes.

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
- `Promotion.spec.fleetRef` references the parent Fleet; the
  PromotionController inherits the rollout plan from `Fleet.spec.plan`
  when `Promotion.spec.plans` is unset.
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

- `kapro promote <fleet> --version <v>` now creates a `Promotion` (not a
  `PromotionRun`). The CLI surface is unchanged for the common case.
- `kapro promotionrun create` is now documented as advanced/debug usage that
  bypasses the intent layer.
- `PromotionController` stamps a NEW `PromotionRun` whenever the deterministic
  hash of `Promotion.spec` changes; existing runs are never rolling-updated
  for new desired versions. Spec hash covers fleetRef, version, versions,
  plans, scope, and timeout (suspended deliberately excluded).

### Changed (cont.) — PromotionTrigger now emits Promotion

- Renamed `PromotionTrigger.spec.promotionrunTemplate` to
  `spec.promotionTemplate`. The template adds a required `fleetRef` field
  pointing at the parent Fleet. Existing trigger manifests must be
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

### Migration from earlier alpha/RC builds (#75)

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
  Plan by its generated name (`<fleet>-promotionplan`) via the
  shared `InlinePromotionPlanName` helper, instead of using the bare
  Fleet name. This unblocks `PromotionRun` plan resolution when a
  Promotion does not specify `spec.plans`.

### Changed

- Re-centered the public API on `Kapro`, `Promotion`, `PromotionPlan`, and
  `PromotionTarget`; `PromotionRun` is now controller-authored runtime state
  and admission gates direct human writes. Advanced reusable objects remain
  available where they add real value.
- `kapro init` now generates inline `Fleet.spec.source` for the default
  greenfield path instead of teaching a separate `Source` first.
- `kapro source package` now reads inline source units from
  `Fleet.spec.source` with `--fleet <name>` when pull-mode packaging.
- Removed obsolete namespace flags from public CLI paths because
  `PromotionRun`, `Target`, `Approval`, and rollback are
  cluster-scoped.

### Performance

- `PromotionController` now indexes `Promotion.spec.fleetRef` and uses
  `MatchingFields` to enqueue only the Promotions referencing a changed
  Fleet, instead of listing every Promotion and filtering in
  memory.
- Terminal coarse phases (`Succeeded`, `Failed`, `Paused`, `Terminating`)
  no longer trigger periodic 15s requeues; the controller relies on
  child `PromotionRun` watches and spec edits to wake up. Active phases
  retain the 15s cadence as a belt-and-braces for missed watch events.

### Removed

- Removed the `PromotionPolicy`, `NotificationProvider`, and
  `NotificationPolicy` CRDs from generated manifests, Helm CRDs, bootstrap
  CRDs, controller registration, and examples. `Promotion` is the durable
  user-facing intent in the public `v0.1.0` baseline.

### Migration

- Wrap any direct `PromotionRun` manifests in a `Promotion` (or use
  `kapro promote`) so the CI re-stamp, audit, and supersession semantics
  are first-class. Existing `PromotionRun` manifests continue to apply
  for the duration of the alpha but are now considered an advanced path;
  recommended human-user RBAC grants `promotionruns` `get/list/watch` only,
  while the admission webhook enforces controller-owned writes.
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
  here; future work should be tracked in GitHub issues and ADRs.
- Removed the obsolete first-alpha release runbook from the documentation index.
- Removed community-positioning, alpha/GA positioning, duplicate security-model,
  and release-notes guide pages from the documentation set. The public index
  now links only the operator, concept, backend, security, extension, and
  release history docs that users need for onboarding.

### Migration

- Apply the `v0.4.0-alpha.0` CRDs and RBAC before rolling the operator.
- Replace any local pre-0.4 packaging test manifests with the current
  quickstart or Kind demo examples.
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
