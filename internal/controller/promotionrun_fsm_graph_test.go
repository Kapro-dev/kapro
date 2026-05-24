package controller

import (
	"strings"
	"testing"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	"k8s.io/client-go/tools/record"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// TestPromotionRunFSM_GraphCoversAllPhases asserts every
// PromotionRunPhase constant has either a registered handler or a
// terminal entry. Companion to D3's TestPromotionTargetFSM_GraphCoversAllPhases.
func TestPromotionRunFSM_GraphCoversAllPhases(t *testing.T) {
	allRunPhases := []kaprov1alpha1.PromotionRunPhase{
		kaprov1alpha1.PromotionRunPhasePending,
		kaprov1alpha1.PromotionRunPhaseProgressing,
		kaprov1alpha1.PromotionRunPhaseComplete,
		kaprov1alpha1.PromotionRunPhaseFailed,
	}

	r := &PromotionRunReconciler{Recorder: record.NewFakeRecorder(8)}
	r.ensureRunFSM()
	missing := r.runFsmMachine.ValidateGraph(allRunPhases)
	if len(missing) != 0 {
		t.Fatalf("PromotionRun FSM is missing handlers / terminal entries for phases: %v", missing)
	}
}

// TestPromotionRunFSM_GraphAdjacencyMatchesDocs locks the declared
// AllowedNext sets against the comment-block "graph" in buildRunFSM.
func TestPromotionRunFSM_GraphAdjacencyMatchesDocs(t *testing.T) {
	r := &PromotionRunReconciler{Recorder: record.NewFakeRecorder(8)}
	r.ensureRunFSM()
	graph := r.runFsmMachine.Graph()

	expectAllowed := func(from kaprov1alpha1.PromotionRunPhase, wantNext ...kaprov1alpha1.PromotionRunPhase) {
		t.Helper()
		got := graph[from]
		gotSet := make(map[kaprov1alpha1.PromotionRunPhase]struct{}, len(got))
		for _, p := range got {
			gotSet[p] = struct{}{}
		}
		if len(got) != len(wantNext) {
			t.Errorf("%s → %v, want %v", from, got, wantNext)
			return
		}
		for _, p := range wantNext {
			if _, ok := gotSet[p]; !ok {
				t.Errorf("%s → missing expected next %s (got %v)", from, p, got)
			}
		}
	}

	expectAllowed("",
		kaprov1alpha1.PromotionRunPhaseProgressing,
	)
	expectAllowed(kaprov1alpha1.PromotionRunPhasePending,
		kaprov1alpha1.PromotionRunPhaseProgressing,
		kaprov1alpha1.PromotionRunPhaseFailed,
	)
	expectAllowed(kaprov1alpha1.PromotionRunPhaseProgressing,
		kaprov1alpha1.PromotionRunPhaseComplete,
		kaprov1alpha1.PromotionRunPhaseFailed,
	)
	// Failed is sticky (no AllowedNext entries — same-phase no-op only).
	if nexts := graph[kaprov1alpha1.PromotionRunPhaseFailed]; len(nexts) != 0 {
		t.Errorf("Failed → %v, want empty (Failed is sticky)", nexts)
	}
	// Complete is the one true terminal — appears in the graph with nil nexts.
	if _, ok := graph[kaprov1alpha1.PromotionRunPhaseComplete]; !ok {
		t.Error("terminal phase Complete missing from graph")
	}
}

// TestPromotionRunFSM_SetRunPhaseValidates asserts setRunPhase emits a
// Warning + bumps the metric on undeclared transitions (and still
// performs the mutation, since validation is observability-only).
//
// Picks Progressing → Pending as the undeclared transition: it's a
// backwards move that no real code path should take, and Pending is not
// terminal (terminal targets are always allowed by ValidateTransition,
// which is why Pending → Complete would NOT fire the warning here even
// though Complete isn't in AllowedNext).
func TestPromotionRunFSM_SetRunPhaseValidates(t *testing.T) {
	rec := record.NewFakeRecorder(8)
	r := &PromotionRunReconciler{Recorder: rec}
	pr := &kaproruntimev1alpha1.PromotionRun{}
	pr.Status.Phase = kaprov1alpha1.PromotionRunPhaseProgressing

	r.setRunPhase(pr, kaprov1alpha1.PromotionRunPhasePending)
	if pr.Status.Phase != kaprov1alpha1.PromotionRunPhasePending {
		t.Fatalf("phase = %q, want Pending (validation must not block mutation)", pr.Status.Phase)
	}
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "FSMUnexpectedTransition") {
			t.Fatalf("event = %q, want it to contain FSMUnexpectedTransition", ev)
		}
	default:
		t.Fatal("expected a Warning event for undeclared transition, got none")
	}
}

// TestPromotionRunFSM_SetRunPhaseDeclaredSilent asserts a declared
// transition does NOT fire the Warning event.
func TestPromotionRunFSM_SetRunPhaseDeclaredSilent(t *testing.T) {
	rec := record.NewFakeRecorder(8)
	r := &PromotionRunReconciler{Recorder: rec}
	pr := &kaproruntimev1alpha1.PromotionRun{}
	pr.Status.Phase = kaprov1alpha1.PromotionRunPhasePending

	r.setRunPhase(pr, kaprov1alpha1.PromotionRunPhaseProgressing)
	if pr.Status.Phase != kaprov1alpha1.PromotionRunPhaseProgressing {
		t.Fatalf("phase = %q, want Progressing", pr.Status.Phase)
	}
	select {
	case ev := <-rec.Events:
		t.Fatalf("declared transition fired event %q, want silence", ev)
	default:
	}
}
