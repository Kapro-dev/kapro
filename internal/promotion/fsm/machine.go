package fsm

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
)

// Handler is invoked for one tick of a phase. It receives the
// caller-supplied Env (the controller, the reconciler, whatever has the
// state and side-effect surface) and returns a controller-runtime Result
// plus an error. Phase mutation is done as a side effect through Env;
// the Machine does not enforce a next-phase return value because the
// existing controllers transition imperatively via Env's own methods.
type Handler[Env any] func(ctx context.Context, env Env) (ctrl.Result, error)

// Transition is a phase binding: the handler that runs when Step is
// called with this phase, plus the set of phases the handler is allowed
// to transition INTO. AllowedNext is descriptive metadata used by
// ValidateTransition for runtime checks and by Graph for documentation.
// An empty AllowedNext means "no restriction" (the legacy Register
// semantics), useful for terminal phases or for migration purposes.
type Transition[Phase comparable, Env any] struct {
	Phase       Phase
	AllowedNext []Phase
	Handler     Handler[Env]
}

// Machine is a per-phase dispatch table generic over the phase enum
// (kaprov1alpha1.TargetPhase, kaprov1alpha1.PromotionRunPhase, or any
// other comparable phase type) and the per-call environment.
//
// Construct one at controller setup, Register every supported phase, then
// call Step from Reconcile. Unknown phases match the previous imperative
// switch's default branch: zero result, no error, no work.
type Machine[Phase comparable, Env any] struct {
	transitions map[Phase]Handler[Env]
	allowedNext map[Phase]map[Phase]struct{}
	terminalSet map[Phase]struct{}
	// initialHandler runs when Step is called with the zero-value phase
	// (a freshly-created object hits this on first Reconcile). Registered
	// via RegisterInitial; treated separately from the regular map so a
	// zero-value lookup is explicit rather than a missing key.
	initialHandler     Handler[Env]
	initialAllowedNext map[Phase]struct{}
}

// New returns an empty Machine. Use Register / RegisterInitial to populate.
func New[Phase comparable, Env any]() *Machine[Phase, Env] {
	return &Machine[Phase, Env]{
		transitions: make(map[Phase]Handler[Env]),
		allowedNext: make(map[Phase]map[Phase]struct{}),
		terminalSet: make(map[Phase]struct{}),
	}
}

// Register binds a handler to a phase. Returns an error if a handler was
// already registered for the same phase — duplicate registration is almost
// always a programmer bug (two controllers / two callers stomping each
// other), so surface it loudly at construction.
//
// This is the simple form: no AllowedNext metadata. Use RegisterTransition
// when you want runtime transition validation.
func (m *Machine[Phase, Env]) Register(phase Phase, h Handler[Env]) error {
	return m.RegisterTransition(Transition[Phase, Env]{Phase: phase, Handler: h})
}

// RegisterTransition binds a handler to a phase with optional AllowedNext
// metadata. The metadata is used by ValidateTransition to flag accidental
// transitions to undeclared phases — useful for catching bugs in handler
// internals that wander off the documented FSM graph.
func (m *Machine[Phase, Env]) RegisterTransition(t Transition[Phase, Env]) error {
	var zero Phase
	if t.Phase == zero {
		return fmt.Errorf("fsm: use RegisterInitial for the zero-value phase, not Register")
	}
	if t.Handler == nil {
		return fmt.Errorf("fsm: handler for phase %v is nil", t.Phase)
	}
	if _, exists := m.transitions[t.Phase]; exists {
		return fmt.Errorf("fsm: handler for phase %v already registered", t.Phase)
	}
	m.transitions[t.Phase] = t.Handler
	if len(t.AllowedNext) > 0 {
		set := make(map[Phase]struct{}, len(t.AllowedNext))
		for _, p := range t.AllowedNext {
			set[p] = struct{}{}
		}
		m.allowedNext[t.Phase] = set
	}
	return nil
}

// RegisterTerminal records that a phase is a terminal state with no
// handler — Reconcile is expected to filter it before reaching Step.
// Used by ValidateGraph and ValidateTransition (any transition TO a
// terminal phase is always allowed). Registering both as a terminal and
// via Register is an error.
func (m *Machine[Phase, Env]) RegisterTerminal(phases ...Phase) error {
	var zero Phase
	for _, p := range phases {
		if p == zero {
			return fmt.Errorf("fsm: terminal phase cannot be the zero value")
		}
		if _, exists := m.transitions[p]; exists {
			return fmt.Errorf("fsm: phase %v is registered with a handler, cannot also be terminal", p)
		}
		m.terminalSet[p] = struct{}{}
	}
	return nil
}

// RegisterInitial binds the handler invoked when Step is called with the
// zero-value phase (i.e. a brand-new object that hasn't transitioned yet).
// Kept separate from Register so the special case is explicit.
//
// allowedNext optionally declares which phases the initial handler may
// transition into; same semantics as RegisterTransition's AllowedNext.
func (m *Machine[Phase, Env]) RegisterInitial(h Handler[Env], allowedNext ...Phase) error {
	if h == nil {
		return fmt.Errorf("fsm: initial handler is nil")
	}
	if m.initialHandler != nil {
		return fmt.Errorf("fsm: initial handler already registered")
	}
	m.initialHandler = h
	if len(allowedNext) > 0 {
		set := make(map[Phase]struct{}, len(allowedNext))
		for _, p := range allowedNext {
			set[p] = struct{}{}
		}
		m.initialAllowedNext = set
	}
	return nil
}

// ValidateTransition returns nil when a transition from `from` to `to`
// is consistent with the registered AllowedNext metadata, otherwise an
// error describing the mismatch. If `from` has no AllowedNext entry,
// returns nil — the absence of metadata means "no restriction" so
// gradual adoption of the declarative graph is non-breaking.
//
// Transitions TO a terminal phase are always allowed.
// Transitions FROM `to == from` (no-op) are always allowed.
func (m *Machine[Phase, Env]) ValidateTransition(from, to Phase) error {
	if from == to {
		return nil
	}
	if _, ok := m.terminalSet[to]; ok {
		return nil
	}
	var allowed map[Phase]struct{}
	var zero Phase
	if from == zero {
		allowed = m.initialAllowedNext
	} else {
		allowed = m.allowedNext[from]
	}
	if allowed == nil {
		return nil // no metadata = no restriction (gradual adoption)
	}
	if _, ok := allowed[to]; !ok {
		return fmt.Errorf("fsm: transition %v → %v is not in declared AllowedNext", from, to)
	}
	return nil
}

// Graph returns a snapshot of the registered AllowedNext metadata as a
// readable adjacency map. Useful for tests, docs, and visualization.
// Terminal phases appear as keys with nil slice values.
func (m *Machine[Phase, Env]) Graph() map[Phase][]Phase {
	out := make(map[Phase][]Phase, len(m.allowedNext)+len(m.terminalSet)+1)
	for phase, set := range m.allowedNext {
		nexts := make([]Phase, 0, len(set))
		for n := range set {
			nexts = append(nexts, n)
		}
		out[phase] = nexts
	}
	for phase := range m.terminalSet {
		out[phase] = nil
	}
	if m.initialAllowedNext != nil {
		var zero Phase
		nexts := make([]Phase, 0, len(m.initialAllowedNext))
		for n := range m.initialAllowedNext {
			nexts = append(nexts, n)
		}
		out[zero] = nexts
	}
	return out
}

// ValidateGraph asserts every required phase has either a handler
// (Register / RegisterTransition / RegisterInitial) or is declared
// terminal. Returns the list of phases that are missing — useful in a
// test to catch "added a phase, forgot to register it" bugs.
func (m *Machine[Phase, Env]) ValidateGraph(required []Phase) []Phase {
	var missing []Phase
	for _, p := range required {
		if _, ok := m.transitions[p]; ok {
			continue
		}
		if _, ok := m.terminalSet[p]; ok {
			continue
		}
		missing = append(missing, p)
	}
	return missing
}

// Phases returns the registered non-initial phases in no particular order.
// Useful for tests and for future static validation that every phase
// constant has a handler.
func (m *Machine[Phase, Env]) Phases() []Phase {
	out := make([]Phase, 0, len(m.transitions))
	for p := range m.transitions {
		out = append(out, p)
	}
	return out
}

// Step dispatches one phase tick. Unknown (unregistered) phases return
// (ctrl.Result{}, nil) — the same no-op behaviour as the legacy switch's
// default branch, so terminal phases that the caller filters out earlier
// don't need a handler registered here.
func (m *Machine[Phase, Env]) Step(ctx context.Context, phase Phase, env Env) (ctrl.Result, error) {
	var zero Phase
	if phase == zero {
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
