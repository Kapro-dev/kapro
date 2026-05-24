package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// TestFleetClusterReadyConditionChangedPredicate covers the watch-filter that
// requeues Kapro reconciles only when a FleetCluster's Ready condition changes
// — the trigger that drives Phase=Unreachable computation. Without this filter
// FleetReconciler would feedback-loop on its own Phase/CurrentVersions writes.
func TestFleetClusterReadyConditionChangedPredicate(t *testing.T) {
	pred := fleetClusterReadyConditionChangedPredicate{}

	base := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c"},
		Status: kaprov1alpha1.ClusterStatus{
			Conditions: []metav1.Condition{{
				Type:   kaprov1alpha1.ConditionTypeReady,
				Status: metav1.ConditionTrue,
				Reason: kaprov1alpha1.ReasonHeartbeatFresh,
			}},
		},
	}

	// Create/Delete/Generic must NOT enqueue — Kapro doesn't own creation
	// of FleetClusters via this watch path (cleanupRemovedClusters handles
	// deletion via Kapro's primary reconcile).
	if pred.Create(event.CreateEvent{Object: base}) {
		t.Error("Create should not trigger")
	}
	if pred.Delete(event.DeleteEvent{Object: base}) {
		t.Error("Delete should not trigger")
	}
	if pred.Generic(event.GenericEvent{Object: base}) {
		t.Error("Generic should not trigger")
	}

	// Same conditions → no requeue (avoid feedback loop on our own status writes).
	same := base.DeepCopy()
	if pred.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: same}) {
		t.Error("no-change update should NOT trigger")
	}

	// Phase/CurrentVersions change but conditions[Ready] unchanged → no requeue.
	versionsOnly := base.DeepCopy()
	versionsOnly.Status.Phase = kaprov1alpha1.ClusterPhaseConverged
	versionsOnly.Status.CurrentVersions = map[string]string{"default": "v1"}
	if pred.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: versionsOnly}) {
		t.Error("Phase/CurrentVersions change without Ready change should NOT trigger")
	}

	// Ready transitions to False/Unreachable → MUST trigger.
	flipped := base.DeepCopy()
	flipped.Status.Conditions[0].Status = metav1.ConditionFalse
	flipped.Status.Conditions[0].Reason = kaprov1alpha1.ReasonUnreachable
	if !pred.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: flipped}) {
		t.Error("Ready True→False/Unreachable should trigger")
	}

	// Ready reason flips while status stays the same (Stale→Unreachable both Unknown) → trigger.
	staleBase := base.DeepCopy()
	staleBase.Status.Conditions[0].Status = metav1.ConditionUnknown
	staleBase.Status.Conditions[0].Reason = kaprov1alpha1.ReasonHeartbeatStale
	staleToOther := staleBase.DeepCopy()
	staleToOther.Status.Conditions[0].Reason = kaprov1alpha1.ReasonSuspended
	if !pred.Update(event.UpdateEvent{ObjectOld: staleBase, ObjectNew: staleToOther}) {
		t.Error("Ready reason flip (Stale→Suspended) should trigger")
	}

	// Adding Ready when previously absent → trigger.
	noCond := &kaprov1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c"}}
	if !pred.Update(event.UpdateEvent{ObjectOld: noCond, ObjectNew: base}) {
		t.Error("adding Ready condition should trigger")
	}

	// Removing Ready (rare, but defensive) → trigger.
	if !pred.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: noCond}) {
		t.Error("removing Ready condition should trigger")
	}

	// Both nil → no trigger.
	emptyOld := &kaprov1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c"}}
	emptyNew := emptyOld.DeepCopy()
	if pred.Update(event.UpdateEvent{ObjectOld: emptyOld, ObjectNew: emptyNew}) {
		t.Error("both-nil Ready should not trigger")
	}
}
