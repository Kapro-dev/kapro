package fsm

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Handler is invoked for one tick of a phase. It receives the
// caller-supplied Env (the controller, the reconciler, whatever has the
// state and side-effect surface) and returns a controller-runtime Result
// plus an error. Phase mutation is done as a side effect through Env;
// the Machine does not enforce a next-phase return value because the
// existing controllers transition imperatively via Env's own methods.
type Handler[Env any] func(ctx context.Context, env Env) (ctrl.Result, error)

// Machine is a per-phase dispatch table over kaprov1alpha1.TargetPhase
// (or any other phase enum if D2 reuses this for PromotionRun later).
//
// Construct one at controller setup, Register every supported phase, then
// call Step from Reconcile. Unknown phases match the previous imperative
// switch's default branch: zero result, no error, no work.
type Machine[Env any] struct {
	transitions map[kaprov1alpha1.TargetPhase]Handler[Env]
	// initialHandler runs when Step is called with the zero-value phase
	// (a freshly-created PromotionTarget hits this on first Reconcile).
	// Registered via RegisterInitial; treated separately from the regular
	// map so a Phase=="" lookup is explicit rather than a missing key.
	initialHandler Handler[Env]
}

// New returns an empty Machine. Use Register / RegisterInitial to populate.
func New[Env any]() *Machine[Env] {
	return &Machine[Env]{
		transitions: make(map[kaprov1alpha1.TargetPhase]Handler[Env]),
	}
}

// Register binds a handler to a phase. Returns an error if a handler was
// already registered for the same phase — duplicate registration is almost
// always a programmer bug (two controllers / two callers stomping each
// other), so surface it loudly at construction.
func (m *Machine[Env]) Register(phase kaprov1alpha1.TargetPhase, h Handler[Env]) error {
	if phase == "" {
		return fmt.Errorf("fsm: use RegisterInitial for the zero-value phase, not Register")
	}
	if h == nil {
		return fmt.Errorf("fsm: handler for phase %q is nil", phase)
	}
	if _, exists := m.transitions[phase]; exists {
		return fmt.Errorf("fsm: handler for phase %q already registered", phase)
	}
	m.transitions[phase] = h
	return nil
}

// RegisterInitial binds the handler invoked when Step is called with an
// empty phase (i.e. a brand-new PromotionTarget that hasn't transitioned
// yet). Kept separate from Register so the special case is explicit.
func (m *Machine[Env]) RegisterInitial(h Handler[Env]) error {
	if h == nil {
		return fmt.Errorf("fsm: initial handler is nil")
	}
	if m.initialHandler != nil {
		return fmt.Errorf("fsm: initial handler already registered")
	}
	m.initialHandler = h
	return nil
}

// Phases returns the registered non-initial phases in no particular order.
// Useful for tests and for future static validation that every TargetPhase
// constant has a handler.
func (m *Machine[Env]) Phases() []kaprov1alpha1.TargetPhase {
	out := make([]kaprov1alpha1.TargetPhase, 0, len(m.transitions))
	for p := range m.transitions {
		out = append(out, p)
	}
	return out
}

// Step dispatches one phase tick. Unknown (unregistered) phases return
// (ctrl.Result{}, nil) — the same no-op behaviour as the legacy switch's
// default branch, so terminal phases (Converged / Failed / Skipped) that
// the caller filters out earlier don't need a handler registered here.
func (m *Machine[Env]) Step(ctx context.Context, phase kaprov1alpha1.TargetPhase, env Env) (ctrl.Result, error) {
	if phase == "" {
		if m.initialHandler == nil {
			return ctrl.Result{}, nil
		}
		return m.initialHandler(ctx, env)
	}
	h, ok := m.transitions[phase]
	if !ok {
		return ctrl.Result{}, nil
	}
	return h(ctx, env)
}
