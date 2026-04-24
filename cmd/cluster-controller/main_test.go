package main

import (
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
