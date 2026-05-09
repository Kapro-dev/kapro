package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestShouldPatchMemberClusterStatus_SkipsFreshHeartbeatOnly(t *testing.T) {
	now := time.Now().UTC()
	oldStatus := kaprov1alpha1.MemberClusterStatus{
		Phase:              kaprov1alpha1.ClusterPhaseConverged,
		ObservedGeneration: 1,
		CurrentVersions:    map[string]string{"default": "repo@sha256:abc"},
		DeliverySystem:     "flux",
		Health: kaprov1alpha1.ClusterHealth{
			AllWorkloadsReady: true,
		},
		LastHeartbeat: now.Format(time.RFC3339),
		Conditions: []metav1.Condition{{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "Converged",
			Message: "cluster converged",
		}},
	}
	newStatus := oldStatus
	newStatus.LastHeartbeat = now.Add(30 * time.Second).Format(time.RFC3339)

	if shouldPatchMemberClusterStatus(oldStatus, newStatus) {
		t.Fatal("expected fresh heartbeat-only update to be skipped")
	}
}

func TestShouldPatchMemberClusterStatus_PatchesSemanticChange(t *testing.T) {
	oldStatus := kaprov1alpha1.MemberClusterStatus{
		Phase:              kaprov1alpha1.ClusterPhaseApplying,
		ObservedGeneration: 1,
		LastHeartbeat:      time.Now().UTC().Format(time.RFC3339),
	}
	newStatus := oldStatus
	newStatus.Phase = kaprov1alpha1.ClusterPhaseConverged

	if !shouldPatchMemberClusterStatus(oldStatus, newStatus) {
		t.Fatal("expected semantic status change to patch")
	}
}

func TestSpecTracker_DetectsChanges(t *testing.T) {
	tracker := &specTracker{}

	// First call always reports change (no cached state).
	mc := &kaprov1alpha1.MemberCluster{}
	mc.ResourceVersion = "100"
	mc.Spec.DesiredVersion = "v1.0.0"
	if !tracker.changed(mc) {
		t.Fatal("first call should always report change")
	}

	// Same resourceVersion — no change.
	if tracker.changed(mc) {
		t.Fatal("same resourceVersion should report no change")
	}

	// New resourceVersion but same spec — no change.
	mc.ResourceVersion = "101"
	if tracker.changed(mc) {
		t.Fatal("new resourceVersion with same spec should report no change")
	}

	// New resourceVersion with different desiredVersion — change.
	mc.ResourceVersion = "102"
	mc.Spec.DesiredVersion = "v2.0.0"
	if !tracker.changed(mc) {
		t.Fatal("different desiredVersion should report change")
	}

	// New resourceVersion with different desiredVersions map — change.
	mc.ResourceVersion = "103"
	mc.Spec.DesiredVersions = map[string]string{"app-a": "v3.0.0"}
	if !tracker.changed(mc) {
		t.Fatal("different desiredVersions map should report change")
	}
}

func TestDebouncedReconciler_CoalescesSignals(t *testing.T) {
	debouncer := newDebouncedReconciler(100 * time.Millisecond)

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go debouncer.Run(ctx, func(_ context.Context) {
		calls.Add(1)
	})

	// Send 5 signals rapidly — they should coalesce into 1 execution.
	for i := 0; i < 5; i++ {
		debouncer.Signal()
	}

	// Wait for debounce interval + processing time.
	time.Sleep(300 * time.Millisecond)

	got := calls.Load()
	if got != 1 {
		t.Fatalf("expected 1 coalesced reconcile, got %d", got)
	}

	// Send another signal — should trigger a second execution.
	debouncer.Signal()
	time.Sleep(300 * time.Millisecond)

	got = calls.Load()
	if got != 2 {
		t.Fatalf("expected 2 reconciles after second signal, got %d", got)
	}
}

func TestDebouncedReconciler_StopsOnContextCancel(t *testing.T) {
	debouncer := newDebouncedReconciler(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		debouncer.Run(ctx, func(_ context.Context) {})
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run exited cleanly.
	case <-time.After(time.Second):
		t.Fatal("debouncedReconciler.Run did not exit after context cancel")
	}
}
