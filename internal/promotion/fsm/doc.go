// Package fsm provides a declarative state machine for the PromotionTarget
// rollout phases.
//
// It replaces a switch-on-phase dispatch in promotiontarget_controller.go
// with a registered table of (phase → handler) entries. The controller
// builds the table once at construction; Reconcile resolves the handler
// per tick via Machine.Step.
//
// The Machine is intentionally tiny: it owns dispatch, registration, and
// the "unknown phase = no-op" semantics that the imperative switch already
// implemented. Phase-handler logic (gate evaluation, actuator dispatch,
// status mutation, event emission) stays in the controller — the FSM does
// NOT replace those.
//
// Why "declarative" at all when the handlers still live elsewhere:
//   - the registered table is the single readable list of supported phases
//     (no more grep-the-switch);
//   - it's a stable hook for future static validation (e.g. "every phase in
//     the TargetPhase enum has a registered handler");
//   - it's a stable hook for observability (every dispatch is one call site,
//     not seven case branches).
//
// Generic on both the phase enum type and the environment type so the same
// primitive can host the PromotionRun FSM (D2) without copy-paste — the
// PromotionRunPhase enum is a distinct string type from TargetPhase and
// would otherwise require casts at every call site.
package fsm
