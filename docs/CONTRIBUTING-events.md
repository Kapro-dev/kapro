# Contributing to `pkg/events` (Kapro CloudEvents Vocabulary)

`pkg/events.EventType` constants are part of Kapro's **public API**.
Subscribers (Argo Events, Flux Notification Controller, third-party
integrations) depend on the literal strings. This doc is the
self-review checklist every change to the vocabulary or its emitters
must pass before commit.

## When to use this checklist

Any of the following:

- Adding a new `EventType` constant in `pkg/events/types.go`.
- Changing the shape of `pkg/events.Event` or `pkg/events.EventData`.
- Wiring a new emit point in any controller.
- Changing what `data.*` fields an event carries.

## Checklist

### 1. Vocabulary

- [ ] Constant follows reverse-DNS naming: `kapro.io/promotion.<scope>[.<verb>]`.
- [ ] Constant's Go doc comment names the trigger transition precisely
      (controller verb + observable state change).
- [ ] Added to `events.AllEventTypes()` in declaration order.
- [ ] Added to the vocabulary canary in `pkg/events/types_test.go`
      (`TestVocabularyStable` literal-string assertion + `lookupByName`
      switch + `TestAllEventTypesIncludesAllConstants` count check).
- [ ] Added to `docs/cloudevents.md` in the matching scope table.
      The drift canary (`TestEventTypesDocumentedInCloudEventsMd`)
      will fail the build if you skip this.

### 2. Envelope (`pkg/events.Event` / `EventData`)

- [ ] Every new field has a Go doc comment that explains *which event
      scopes set it* (whole-Promotion / attempt / wave / stage / gate
      / target).
- [ ] `data.phase` semantic preserved: `Promotion.status.phase` for
      whole-Promotion / attempt events; `PromotionRun.status.phase`
      for run-scoped events. **Never overload scoped phase
      (wave/stage/gate local state) into `phase`.**
- [ ] `data.reason` uses canonical lowercase tokens when subscribers
      need to branch on it. The human sentence goes in `data.message`.
- [ ] New fields are listed in `docs/cloudevents.md` under "`data`
      field schema" with their semantic.

### 3. Emitter

- [ ] Emitter is in `internal/lifecycle.Dispatcher` (sink + optional
      per-Promotion fan-out) or via `StageEventPublisher`. Nothing
      outside `internal/lifecycle` should construct
      `events.Event{...}` directly.
- [ ] `events.Render` is the only renderer used (no parallel JSON
      construction). Both per-Promotion webhook and operator sink
      paths share this.
- [ ] Transition guard: emission is dedup'd against a `previousPhase`
      helper or equivalent. Each transition fires exactly once even
      under reconcile re-entry.
- [ ] When emitting from `PromotionRunReconciler` or
      `PromotionTargetReconciler`, populate `data.kaproRef` and
      `data.promotionUID` from the `kapro.io/kapro` and
      `kapro.io/promotion-uid` labels stamped by `PromotionController`.
- [ ] Empty `PromotionName` guard: emitter returns early when the run
      lacks the `kapro.io/promotion` label (detached test fixtures or
      break-glass-authored runs).

### 4. Tests

- [ ] Unit test asserting the new field renders into the envelope
      (extend `TestStageWaveGateFieldsRenderInData` or write a
      similar one).
- [ ] Controller integration test asserting the new emit point fires
      exactly once per transition (use `fakeLifecycleDispatcher` /
      `fakeStageEventPublisher` to capture).
- [ ] If touching the dispatcher concurrency surface (inflight map,
      goroutine fan-out), run `go test -race ./internal/lifecycle/...`
      and confirm clean.

### 5. Documentation

- [ ] `docs/cloudevents.md` event-type table updated.
- [ ] `docs/cloudevents.md` `data` field schema updated if a new field
      lands.
- [ ] CHANGELOG entry under `Unreleased` describes the new event +
      the field surface change.
- [ ] If the change supersedes a prior design decision, write a new
      ADR (don't edit old ones — see `docs/adr/README.md`).

### 6. Final pass

- [ ] `go test ./...` clean.
- [ ] `go test -race ./internal/lifecycle/... ./pkg/events/...` clean.
- [ ] `gofmt -d` clean.
- [ ] `go vet ./...` clean.
- [ ] Compared every emitter against the documented contract field-by-
      field. (This is the step I missed in PRs #80 and #81 — don't
      skip it.)

## Failure modes the checklist exists to prevent

These are bugs we shipped and then had to fix in follow-up PRs. The
checklist exists to catch them at the emitter, before commit:

- PR #80: `data.phase` mixed CloudEvents type with Promotion phase
  across the sink path — broke dashboards grouping by phase.
- PR #80: `kapro_lifecycle_hook_duration_seconds` silently dropped
  failure samples — incomplete observability.
- PR #80: `KAPRO_EVENTS_SINK_TIMEOUT` was documented per-attempt but
  enforced per-handler — misled operators tuning the value.
- PR #80: Per-Promotion webhook and sink emitted different envelopes
  despite docs claiming they were identical.
- PR #81: `kaproRefFromRun` read `PromotionPlans[0].PromotionPlan`
  (a PromotionPlan CR name like `<kapro>-promotionplan`) instead of
  the Kapro fleet name.
- PR #81: `data.phase` set to wave/stage/gate phase on all the new
  emitters — broke the documented "data.phase = Promotion phase"
  contract.
- PR #81: `wave.completed.reason` was a human sentence; docs claimed
  canonical lowercase tokens.
- PR #82: kapro/uid labels were stamped only on newly-created runs —
  in-flight attempts crossing the upgrade boundary kept emitting
  empty `data.kaproRef` / `data.promotionUID`.

Every one was a docs↔code drift catchable by step 6.

## Skipping the checklist

Don't. The reviewer (Copilot) is the second line of defence; the
checklist is the first. We have empirical evidence (8+12+1 review
comments across PRs #80/#81/#82) that skipping is expensive.
