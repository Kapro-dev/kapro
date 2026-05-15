package controller

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/notification"
)

type recordingNotifier struct {
	events   []notification.Event
	policies []notification.NotificationPolicy
}

func (n *recordingNotifier) Notify(_ context.Context, event notification.Event, policy notification.NotificationPolicy) {
	n.events = append(n.events, event)
	n.policies = append(n.policies, policy)
}

func TestReleaseTargetPredicates_RejectedStatusChangeEnqueues(t *testing.T) {
	p := releaseTargetPredicates()
	oldObj := &kaprov1alpha1.ReleaseTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rel-wave-prod-cluster-a",
			Generation: 1,
		},
		Status: kaprov1alpha1.ReleaseTargetStatus{
			TargetStatus: kaprov1alpha1.TargetStatus{
				Phase: kaprov1alpha1.TargetPhaseWaitingApproval,
			},
		},
	}
	newObj := oldObj.DeepCopy()
	newObj.Status.Rejected = true
	newObj.Status.RejectedBy = "alice"

	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected rejected status change to enqueue reconcile")
	}
}

func TestReleaseTargetMemberClusterPredicates_HeartbeatOnlyIgnored(t *testing.T) {
	p := releaseTargetMemberClusterPredicates()
	oldObj := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status: kaprov1alpha1.MemberClusterStatus{
			Phase:         kaprov1alpha1.ClusterPhaseConverged,
			LastHeartbeat: "2025-01-01T00:00:00Z",
		},
	}
	newObj := oldObj.DeepCopy()
	newObj.Status.LastHeartbeat = "2025-01-01T00:00:30Z"

	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected heartbeat-only MemberCluster update to be ignored")
	}
}

func TestHeartbeatLeasePredicates_IgnoreFreshRenewal(t *testing.T) {
	p := heartbeatLeasePredicates()
	oldRenew := metav1.NewMicroTime(time.Now().Add(-30 * time.Second).UTC())
	newRenew := metav1.NewMicroTime(time.Now().UTC())
	oldObj := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: heartbeatLeaseName("cluster-a")},
		Spec:       coordinationv1.LeaseSpec{RenewTime: &oldRenew},
	}
	newObj := oldObj.DeepCopy()
	newObj.Spec.RenewTime = &newRenew

	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected fresh-to-fresh heartbeat renewal to be ignored")
	}
}

func TestHeartbeatLeasePredicates_EnqueueOnFreshnessBoundary(t *testing.T) {
	p := heartbeatLeasePredicates()
	oldRenew := metav1.NewMicroTime(time.Now().Add(-heartbeatFreshTimeout - time.Second).UTC())
	newRenew := metav1.NewMicroTime(time.Now().UTC())
	oldObj := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: heartbeatLeaseName("cluster-a")},
		Spec:       coordinationv1.LeaseSpec{RenewTime: &oldRenew},
	}
	newObj := oldObj.DeepCopy()
	newObj.Spec.RenewTime = &newRenew

	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Fatal("expected stale-to-fresh heartbeat renewal to enqueue")
	}
}

func TestUpdateReleaseTargetStatusContract_SetsObservedGenerationAndConditions(t *testing.T) {
	r := &ReleaseTargetReconciler{}
	rt := &kaprov1alpha1.ReleaseTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rel-wave-prod-cluster-a",
			Generation: 3,
		},
		Status: kaprov1alpha1.ReleaseTargetStatus{
			TargetStatus: kaprov1alpha1.TargetStatus{
				Phase:   kaprov1alpha1.TargetPhaseConverged,
				Message: "done",
			},
		},
	}

	r.updateReleaseTargetStatusContract(rt)

	if rt.Status.ObservedGeneration != 3 {
		t.Fatalf("expected ObservedGeneration=3, got %d", rt.Status.ObservedGeneration)
	}
	ready := false
	for _, cond := range rt.Status.Conditions {
		if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		t.Fatal("expected Ready=True condition on converged target")
	}
}

func TestNotifyPersistedTransitions_OnlyOnPersistedPhaseChange(t *testing.T) {
	notifier := &recordingNotifier{}
	r := &ReleaseTargetReconciler{Notifier: notifier}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
	}
	prev := &kaprov1alpha1.TargetStatus{
		Target:  "cluster-a",
		Version: "repo@sha256:abc",
		Phase:   kaprov1alpha1.TargetPhasePending,
	}
	curr := prev.DeepCopy()
	curr.Phase = kaprov1alpha1.TargetPhaseHealthCheck

	r.notifyPersistedTransitions(context.Background(), release, prev, curr)

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 persisted phase notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Phase != string(kaprov1alpha1.TargetPhaseHealthCheck) {
		t.Fatalf("expected HealthCheck notification, got %q", notifier.events[0].Phase)
	}
}

func TestNotifyPersistedTransitions_ApprovalOnlyAfterPersistedStamp(t *testing.T) {
	notifier := &recordingNotifier{}
	r := &ReleaseTargetReconciler{Notifier: notifier}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
	}
	prev := &kaprov1alpha1.TargetStatus{
		Target:  "cluster-a",
		Version: "repo@sha256:abc",
		Phase:   kaprov1alpha1.TargetPhaseWaitingApproval,
	}
	curr := prev.DeepCopy()
	curr.ApprovalSentAt = "2025-01-01T00:00:00Z"

	r.notifyPersistedTransitions(context.Background(), release, prev, curr)

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 approval notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Phase != string(kaprov1alpha1.TargetPhaseWaitingApproval) {
		t.Fatalf("expected WaitingApproval notification, got %q", notifier.events[0].Phase)
	}
}

func TestNotifyGateEvent_SendsSemanticGateType(t *testing.T) {
	notifier := &recordingNotifier{}
	r := &ReleaseTargetReconciler{Notifier: notifier}
	release := &kaprov1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "rel-1"}}
	target := &kaprov1alpha1.TargetStatus{
		Target:      "cluster-a",
		Version:     "repo@sha256:abc",
		PipelineRef: "main",
		Stage:       "canary",
		Phase:       kaprov1alpha1.TargetPhaseMetricsCheck,
		Gate: &kaprov1alpha1.GatePolicySpec{
			Notifications: []kaprov1alpha1.NotificationSpec{{Type: "webhook", Events: []string{notification.EventGatePassed}}},
		},
	}

	r.notifyGateEvent(context.Background(), release, target, notification.EventGatePassed, "metrics", "passed", false)

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 gate notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Type != notification.EventGatePassed {
		t.Fatalf("expected gate passed event, got %q", notifier.events[0].Type)
	}
	if notifier.events[0].Pipeline != "main" || notifier.events[0].Stage != "canary" || notifier.events[0].Target != "cluster-a" {
		t.Fatalf("gate event context not populated: %#v", notifier.events[0])
	}
	if len(notifier.policies) != 1 || len(notifier.policies[0].Channels) != 1 {
		t.Fatalf("expected gate policy to provide one notification channel, got %#v", notifier.policies)
	}
}
