// Package fsm provides a declarative state machine primitive for Kapro
// rollout phases — first wired into the PromotionTarget FSM (D3) and
// designed to host the PromotionRun FSM unchanged (D2).
//
// # Why this exists
//
// The imperative switch-on-phase dispatch hides the phase graph inside
// handler bodies. With this package the graph is data: every transition
// is registered with explicit AllowedNext metadata; Graph() returns the
// adjacency list verbatim; ValidateTransition flags any handler that
// wanders off the declared graph; ValidateGraph asserts every enum
// constant has been wired up. None of that prevents bugs — it surfaces
// them at unit-test time or in a Prometheus counter at runtime, instead
// of leaving them to be debugged from a half-broken status field.
//
// # Contract surface
//
//   - Handler[Env]                 : one tick of a phase. Reads + mutates
//     Env, returns ctrl.Result + error.
//     Phase mutation is a side effect on
//     Env (the existing controllers call
//     r.transitionTo from inside handlers).
//   - Transition[Phase, Env]       : {Phase, AllowedNext, Handler}. Used
//     by RegisterTransition.
//   - Machine[Phase, Env]          : the dispatch table. Generic on both
//     Phase (any comparable enum, e.g.
//     TargetPhase or PromotionRunPhase)
//     and Env.
//   - New[Phase, Env]()            : empty Machine. Construct one at
//     controller setup.
//   - Register(phase, h)           : phase → handler, no AllowedNext.
//     Equivalent to RegisterTransition
//     with empty AllowedNext.
//   - RegisterTransition(t)        : phase → handler + AllowedNext.
//   - RegisterInitial(h, next...)  : handler for the zero-value phase
//     (a brand-new object). Separate from
//     Register so the special case is
//     explicit.
//   - RegisterTerminal(phases...)  : declare phases that are terminal
//     (Reconcile filters them before
//     Step is called). Used by Graph()
//     and ValidateGraph; transitions TO
//     a terminal phase are always allowed
//     by ValidateTransition.
//   - Step(ctx, phase, env)        : dispatch one tick. Unknown
//     (unregistered) phases match the
//     legacy switch default: zero result,
//     no error, no work.
//   - Phases()                     : list of registered (non-initial,
//     non-terminal) phases.
//   - Graph()                      : adjacency map {phase → AllowedNext}
//     for tests, docs, visualization.
//     Terminal phases appear as keys with
//     nil values; initial appears under
//     the zero-value key.
//   - ValidateTransition(from, to) : nil iff the transition is allowed
//     by the registered graph (terminal,
//     no-op, no-metadata, or explicitly
//     listed). Otherwise an error
//     describing the mismatch.
//   - ValidateGraph(required)      : returns the list of required phases
//     that have no handler and aren't
//     terminal — call from a unit test
//     to catch "added a phase, forgot to
//     register it" drift.
//
// # Where the boundaries are
//
// The Machine does NOT:
//   - encode side-effect ordering (handlers do their own gate eval,
//     actuator dispatch, event emission, status writes);
//   - own the in-memory phase mutation (caller's transitionTo does);
//   - enforce transitions at runtime — ValidateTransition is for
//     observability (Warning Event + Prometheus counter); the actual
//     mutation proceeds either way. Crashing the reconciler on a
//     graph-doc drift in production would be strictly worse than letting
//     it through with a loud alert; the graph is documentation and a
//     violation means the docs are stale, not that the rollout is unsafe.
//
// # Adopting it from a new controller
//
// One pattern (the one PromotionTarget uses):
//
//   - Build the Machine lazily (sync.Once on the reconciler) with
//     handlers as method-receiver closures so they capture the
//     reconciler's stable state (Client, Recorder, registries).
//   - Per-Reconcile values (the current object pointer, in-memory
//     mutable state) flow through Env, not closures.
//   - Reconcile filters terminal phases BEFORE calling Step so the
//     no-handler path is genuinely a no-op.
package fsm
