package controller

import (
	"testing"

	"k8s.io/client-go/tools/record"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// TestPromotionTargetFSM_GraphCoversAllPhases asserts that every
// TargetPhase constant in the API has either a registered handler or
// a terminal entry in the FSM. Catches "added a new phase, forgot to
// wire it into buildFSM" bugs at unit-test time rather than in
// production with a silent no-op from Machine.Step on the unknown phase.
//
// If you add a new TargetPhase, add it to allTargetPhases below AND
// to buildFSM in promotiontarget_controller.go.
func TestPromotionTargetFSM_GraphCoversAllPhases(t *testing.T) {
	allTargetPhases := []kaprov1alpha1.TargetPhase{
		kaprov1alpha1.TargetPhasePending,
		kaprov1alpha1.TargetPhaseVerification,
		kaprov1alpha1.TargetPhaseHealthCheck,
		kaprov1alpha1.TargetPhaseSoaking,
		kaprov1alpha1.TargetPhaseMetricsCheck,
		kaprov1alpha1.TargetPhaseWaitingApproval,
		kaprov1alpha1.TargetPhaseApplying,
		kaprov1alpha1.TargetPhaseConverged,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	}

	r := &PromotionTargetReconciler{
		Recorder: record.NewFakeRecorder(8),
	}
	r.ensureFSM()
	missing := r.fsmMachine.ValidateGraph(allTargetPhases)
	if len(missing) != 0 {
		t.Fatalf("FSM is missing handlers / terminal entries for phases: %v", missing)
	}
}

// TestPromotionTargetFSM_GraphAdjacencyMatchesDocs asserts the declared
// AllowedNext sets match the comment-block "graph" in buildFSM. Keeping
// these in sync is the entire point of the declarative table.
func TestPromotionTargetFSM_GraphAdjacencyMatchesDocs(t *testing.T) {
	r := &PromotionTargetReconciler{Recorder: record.NewFakeRecorder(8)}
	r.ensureFSM()
	graph := r.fsmMachine.Graph()

	expectAllowed := func(from kaprov1alpha1.TargetPhase, wantNext ...kaprov1alpha1.TargetPhase) {
		t.Helper()
		got := graph[from]
		gotSet := make(map[kaprov1alpha1.TargetPhase]struct{}, len(got))
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
		kaprov1alpha1.TargetPhasePending,
	)
	expectAllowed(kaprov1alpha1.TargetPhasePending,
		kaprov1alpha1.TargetPhaseVerification,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	expectAllowed(kaprov1alpha1.TargetPhaseVerification,
		kaprov1alpha1.TargetPhaseHealthCheck,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	expectAllowed(kaprov1alpha1.TargetPhaseHealthCheck,
		kaprov1alpha1.TargetPhaseSoaking,
		kaprov1alpha1.TargetPhaseMetricsCheck,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	expectAllowed(kaprov1alpha1.TargetPhaseSoaking,
		kaprov1alpha1.TargetPhaseMetricsCheck,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	expectAllowed(kaprov1alpha1.TargetPhaseMetricsCheck,
		kaprov1alpha1.TargetPhaseWaitingApproval,
		kaprov1alpha1.TargetPhaseApplying,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	expectAllowed(kaprov1alpha1.TargetPhaseWaitingApproval,
		kaprov1alpha1.TargetPhaseApplying,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	expectAllowed(kaprov1alpha1.TargetPhaseApplying,
		kaprov1alpha1.TargetPhaseConverged,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	for _, p := range []kaprov1alpha1.TargetPhase{
		kaprov1alpha1.TargetPhaseConverged,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	} {
		if _, ok := graph[p]; !ok {
			t.Errorf("terminal phase %s missing from graph", p)
		}
	}
}

// TestEventTypeForPhase_CoversAllRegisteredPhases asserts that every phase
// registered in the FSM (non-initial, non-terminal) has a stable, named
// notification event type — i.e. eventTypeForPhase does NOT fall through to
// the generic "kapro.promotionrun.target.unknown" sentinel. Catches the
// "added a phase, registered a handler, forgot to map a notification event"
// drift case at unit-test time.
//
// Initial ("") and terminal phases (Converged, Failed, Skipped) have their
// own dedicated entries in eventTypeForPhase and are listed explicitly here
// so we verify them too — terminal phases drive
// EventTargetConverged/Failed/Skipped which downstream notifiers depend on.
func TestEventTypeForPhase_CoversAllRegisteredPhases(t *testing.T) {
	r := &PromotionTargetReconciler{Recorder: record.NewFakeRecorder(8)}
	r.ensureFSM()
	phases := r.fsmMachine.Phases()
	phases = append(phases,
		kaprov1alpha1.TargetPhaseConverged,
		kaprov1alpha1.TargetPhaseFailed,
		kaprov1alpha1.TargetPhaseSkipped,
	)
	const fallback = "kapro.promotionrun.target.unknown"
	for _, phase := range phases {
		evt := eventTypeForPhase(phase)
		if evt == "" {
			t.Errorf("phase %s: empty event type", phase)
		}
		if evt == fallback {
			t.Errorf("phase %s: falls through to %q — add an explicit case in eventTypeForPhase", phase, fallback)
		}
	}
}
