# Event Vocabulary Checklist

Use this checklist when changing `pkg/events` constants, envelope fields, or
emitters.

## Vocabulary

- Constant follows reverse-DNS naming: `kapro.io/promotion.<scope>[.<verb>]`.
- Constant is added to `events.AllEventTypes()` in declaration order.
- `docs/events.md` lists the literal string in the matching event table.
- `pkg/events/drift_canary_test.go` passes.

## Envelope

- Every new data field has a Go doc comment.
- `data.phase` keeps its documented meaning:
  - Promotion phase for whole-Promotion and attempt events;
  - PromotionRun phase for wave, stage, gate, and target events.
- Machine-readable causes go in `data.reason`.
- Human text goes in `data.message`.
- `docs/events.md` documents every new field.

## Emitter

- Use `events.Render`; do not hand-build parallel JSON envelopes.
- Deduplicate transition events with previous-phase guards.
- Populate `fleetRef` and `promotionUID` when emitting from PromotionRun or
  Target paths.
- Skip emission for detached runs that lack a Promotion label.

## Tests

- Add or update unit tests for new envelope fields.
- Add controller tests for new emit points.
- Run `go test ./...`.
- Run `go test -race ./internal/lifecycle/... ./pkg/events/...` when touching
  dispatcher concurrency.

## Final Review

Compare every emitter against `docs/events.md` field-by-field before opening a
PR. The drift canary catches missing type strings; it does not prove every data
field is semantically correct.
