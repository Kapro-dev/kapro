package fsm

import (
	"context"
	"errors"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// testEnv is a stand-in for the real PromotionTargetReconciler env passed
// at the call site. It just lets handlers record which one fired.
type testEnv struct {
	called string
}

func TestMachine_RegisterAndStep(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	if err := m.Register(kaprov1alpha2.TargetPhaseVerification, func(_ context.Context, e *testEnv) (ctrl.Result, error) {
		e.called = "verification"
		// Requeue: true is deprecated in controller-runtime; use a
		// non-zero RequeueAfter as the equivalent "ask for a follow-up
		// reconcile" signal.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	env := &testEnv{}
	res, err := m.Step(context.Background(), kaprov1alpha2.TargetPhaseVerification, env)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if env.called != "verification" {
		t.Fatalf("called = %q, want verification", env.called)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("RequeueAfter = 0, want non-zero")
	}
}

func TestMachine_UnknownPhaseIsNoop(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	env := &testEnv{}
	res, err := m.Step(context.Background(), kaprov1alpha2.TargetPhaseApplying, env)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Fatalf("Result = %+v, want zero", res)
	}
	if env.called != "" {
		t.Fatal("handler should not have been invoked for unregistered phase")
	}
}

func TestMachine_InitialHandler(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	if err := m.RegisterInitial(func(_ context.Context, e *testEnv) (ctrl.Result, error) {
		e.called = "initial"
		return ctrl.Result{}, nil
	}); err != nil {
		t.Fatalf("RegisterInitial: %v", err)
	}
	env := &testEnv{}
	if _, err := m.Step(context.Background(), "", env); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if env.called != "initial" {
		t.Fatalf("called = %q, want initial", env.called)
	}
}

func TestMachine_EmptyInitialIsNoop(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	res, err := m.Step(context.Background(), "", &testEnv{})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Fatalf("Result = %+v, want zero", res)
	}
}

func TestMachine_DuplicateRegisterFails(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	h := func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil }
	if err := m.Register(kaprov1alpha2.TargetPhaseVerification, h); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := m.Register(kaprov1alpha2.TargetPhaseVerification, h); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestMachine_RegisterEmptyPhaseFails(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	h := func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil }
	if err := m.Register("", h); err == nil {
		t.Fatal("expected Register(\"\") to fail with guidance to use RegisterInitial")
	}
}

func TestMachine_RegisterNilHandlerFails(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	if err := m.Register(kaprov1alpha2.TargetPhaseVerification, nil); err == nil {
		t.Fatal("expected nil handler registration to fail")
	}
	if err := m.RegisterInitial(nil); err == nil {
		t.Fatal("expected nil initial handler to fail")
	}
}

func TestMachine_PhasesListsRegistered(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	_ = m.Register(kaprov1alpha2.TargetPhaseVerification, func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil })
	_ = m.Register(kaprov1alpha2.TargetPhaseSoaking, func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil })
	phases := m.Phases()
	if len(phases) != 2 {
		t.Fatalf("Phases = %v, want 2 entries", phases)
	}
}

func TestMachine_HandlerErrorPropagates(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	want := errors.New("boom")
	_ = m.Register(kaprov1alpha2.TargetPhaseApplying, func(_ context.Context, _ *testEnv) (ctrl.Result, error) {
		return ctrl.Result{}, want
	})
	_, err := m.Step(context.Background(), kaprov1alpha2.TargetPhaseApplying, &testEnv{})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestMachine_ValidateTransition_DeclaredAllowed(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	_ = m.RegisterTransition(Transition[kaprov1alpha2.TargetPhase, *testEnv]{
		Phase:       kaprov1alpha2.TargetPhasePending,
		AllowedNext: []kaprov1alpha2.TargetPhase{kaprov1alpha2.TargetPhaseVerification},
		Handler:     func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil },
	})
	if err := m.ValidateTransition(kaprov1alpha2.TargetPhasePending, kaprov1alpha2.TargetPhaseVerification); err != nil {
		t.Fatalf("declared transition rejected: %v", err)
	}
	if err := m.ValidateTransition(kaprov1alpha2.TargetPhasePending, kaprov1alpha2.TargetPhaseApplying); err == nil {
		t.Fatal("undeclared transition Pending → Applying should fail")
	}
}

func TestMachine_ValidateTransition_TerminalAlwaysAllowed(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	_ = m.RegisterTransition(Transition[kaprov1alpha2.TargetPhase, *testEnv]{
		Phase:       kaprov1alpha2.TargetPhasePending,
		AllowedNext: []kaprov1alpha2.TargetPhase{kaprov1alpha2.TargetPhaseVerification},
		Handler:     func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil },
	})
	_ = m.RegisterTerminal(kaprov1alpha2.TargetPhaseFailed, kaprov1alpha2.TargetPhaseSkipped)
	if err := m.ValidateTransition(kaprov1alpha2.TargetPhasePending, kaprov1alpha2.TargetPhaseFailed); err != nil {
		t.Fatalf("transition to terminal should always be allowed: %v", err)
	}
}

func TestMachine_RegisterTerminal_RejectsRegisteredPhase(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	_ = m.Register(kaprov1alpha2.TargetPhaseApplying, func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil })
	if err := m.RegisterTerminal(kaprov1alpha2.TargetPhaseApplying); err == nil {
		t.Fatal("expected RegisterTerminal on a Register'd phase to fail")
	}
}

func TestMachine_ValidateGraph_FlagsMissingPhases(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	_ = m.Register(kaprov1alpha2.TargetPhasePending, func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil })
	_ = m.RegisterTerminal(kaprov1alpha2.TargetPhaseFailed)
	required := []kaprov1alpha2.TargetPhase{
		kaprov1alpha2.TargetPhasePending,
		kaprov1alpha2.TargetPhaseVerification, // missing
		kaprov1alpha2.TargetPhaseFailed,
	}
	missing := m.ValidateGraph(required)
	if len(missing) != 1 || missing[0] != kaprov1alpha2.TargetPhaseVerification {
		t.Fatalf("missing = %v, want [Verification]", missing)
	}
}

func TestMachine_NoMetadataIsUnrestricted(t *testing.T) {
	m := New[kaprov1alpha2.TargetPhase, *testEnv]()
	_ = m.Register(kaprov1alpha2.TargetPhasePending, func(_ context.Context, _ *testEnv) (ctrl.Result, error) { return ctrl.Result{}, nil })
	if err := m.ValidateTransition(kaprov1alpha2.TargetPhasePending, kaprov1alpha2.TargetPhaseApplying); err != nil {
		t.Fatalf("unrestricted transition should be allowed: %v", err)
	}
}
