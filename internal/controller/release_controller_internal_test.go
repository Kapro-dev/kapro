package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/planner"
)

func TestStageDependencySatisfied_AnyUnlocksFromOneConvergedTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
			memberClusterForStage("cluster-b", "canary"),
		).Build(),
	}
	release := &kaprov1alpha1.Release{
		Status: kaprov1alpha1.ReleaseStatus{
			Targets: []kaprov1alpha1.TargetStatus{
				{
					Target:      "cluster-a",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseConverged,
					FinishedAt:  time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				},
				{
					Target:      "cluster-b",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseHealthCheck,
				},
			},
		},
	}
	pipeline := pipelineWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), release, "main", pipeline, kaprov1alpha1.StageDependency{
		Stage:            "canary",
		Strategy:         kaprov1alpha1.StageDependencyAny,
		RequiredSoakTime: &metav1.Duration{Duration: time.Hour},
	})
	if err != nil {
		t.Fatalf("stageDependencySatisfied returned error: %v", err)
	}
	if !satisfied {
		t.Fatalf("expected any dependency to be satisfied, wait=%s", wait)
	}
	if wait != 0 {
		t.Fatalf("expected no remaining wait, got %s", wait)
	}
}

func TestNotifyReleaseEvent_UsesPipelineStageNotifications(t *testing.T) {
	scheme := controllerTestScheme(t)
	notifier := &recordingNotifier{}
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "progressive"},
		Spec: kaprov1alpha1.PipelineSpec{Stages: []kaprov1alpha1.Stage{{
			Name:     "canary",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
			Gate: &kaprov1alpha1.GatePolicySpec{
				Notifications: []kaprov1alpha1.NotificationSpec{{
					Type:   "webhook",
					Events: []string{notification.EventReleaseStarted},
				}},
			},
		}}},
	}
	r := &ReleaseReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pipeline).Build(),
		Notifier: notifier,
	}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version:   "repo@sha256:abc",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{{Name: "main", Pipeline: "progressive"}},
		},
		Status: kaprov1alpha1.ReleaseStatus{
			Phase:           kaprov1alpha1.ReleasePhaseProgressing,
			ResolvedVersion: "repo@sha256:abc",
		},
	}

	r.notifyReleaseEvent(context.Background(), release, notification.EventReleaseStarted, "started")

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 release notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Type != notification.EventReleaseStarted {
		t.Fatalf("expected release started event, got %q", notifier.events[0].Type)
	}
	if len(notifier.policies) != 1 || len(notifier.policies[0].Channels) != 1 {
		t.Fatalf("expected release policy to collect one channel, got %#v", notifier.policies)
	}
}

func TestNotifyStageEvent_UsesStageNotificationPolicy(t *testing.T) {
	scheme := controllerTestScheme(t)
	notifier := &recordingNotifier{}
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "progressive"},
		Spec: kaprov1alpha1.PipelineSpec{Stages: []kaprov1alpha1.Stage{{
			Name:     "canary",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
			Gate: &kaprov1alpha1.GatePolicySpec{
				Notifications: []kaprov1alpha1.NotificationSpec{{
					Type:   "webhook",
					Events: []string{notification.EventStageCompleted},
				}},
			},
		}}},
	}
	r := &ReleaseReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(pipeline).Build(),
		Notifier: notifier,
	}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version:   "repo@sha256:abc",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{{Name: "main", Pipeline: "progressive"}},
		},
		Status: kaprov1alpha1.ReleaseStatus{ResolvedVersion: "repo@sha256:abc"},
	}

	r.notifyStageEvent(context.Background(), release, "main", "canary", notification.EventStageCompleted, "complete")

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 stage notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Type != notification.EventStageCompleted {
		t.Fatalf("expected stage completed event, got %q", notifier.events[0].Type)
	}
	if notifier.events[0].Pipeline != "main" || notifier.events[0].Stage != "canary" {
		t.Fatalf("stage event context not populated: %#v", notifier.events[0])
	}
	if len(notifier.policies) != 1 || len(notifier.policies[0].Channels) != 1 {
		t.Fatalf("expected stage policy to provide one channel, got %#v", notifier.policies)
	}
}

func TestStageDependencySatisfied_AllRequiresEveryTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
			memberClusterForStage("cluster-b", "canary"),
		).Build(),
	}
	release := &kaprov1alpha1.Release{
		Status: kaprov1alpha1.ReleaseStatus{
			Targets: []kaprov1alpha1.TargetStatus{
				{
					Target:      "cluster-a",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseConverged,
					FinishedAt:  time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				},
				{
					Target:      "cluster-b",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseApplying,
				},
			},
		},
	}
	pipeline := pipelineWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), release, "main", pipeline, kaprov1alpha1.StageDependency{
		Stage:    "canary",
		Strategy: kaprov1alpha1.StageDependencyAll,
	})
	if err != nil {
		t.Fatalf("stageDependencySatisfied returned error: %v", err)
	}
	if satisfied {
		t.Fatal("expected all dependency to wait for every target")
	}
	if wait != 0 {
		t.Fatalf("expected no timer wait while target is still running, got %s", wait)
	}
}

func TestStageDependencySatisfied_ReturnsRemainingSoakTime(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
		).Build(),
	}
	release := &kaprov1alpha1.Release{
		Status: kaprov1alpha1.ReleaseStatus{
			Targets: []kaprov1alpha1.TargetStatus{
				{
					Target:      "cluster-a",
					PipelineRef: "main",
					Stage:       "canary",
					Phase:       kaprov1alpha1.TargetPhaseConverged,
					FinishedAt:  time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}
	pipeline := pipelineWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), release, "main", pipeline, kaprov1alpha1.StageDependency{
		Stage:            "canary",
		RequiredSoakTime: &metav1.Duration{Duration: time.Hour},
	})
	if err != nil {
		t.Fatalf("stageDependencySatisfied returned error: %v", err)
	}
	if satisfied {
		t.Fatal("expected dependency to wait for soak time")
	}
	if wait <= 0 {
		t.Fatalf("expected positive remaining soak wait, got %s", wait)
	}
	if wait > time.Hour {
		t.Fatalf("expected wait below one hour, got %s", wait)
	}
}

func TestListTargetsForStageUsesReleasePlanner(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
			memberClusterForStage("cluster-b", "canary"),
		).Build(),
		Planner: planner.NewFramework(testPlannerFilter{NameValue: "cluster-b"}),
	}
	release := &kaprov1alpha1.Release{}
	pipeline := pipelineWithCanaryStage()

	targets, err := r.listTargetsForStage(context.Background(), "main", pipeline, pipeline.Spec.Stages[0], release)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Name != "cluster-b" {
		t.Fatalf("targets = %#v", targets)
	}
}

func memberClusterForStage(name, stage string) *kaprov1alpha1.MemberCluster {
	return &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"stage": stage},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Mode: "pull", Backend: "flux"},
		},
	}
}

type testPlannerFilter struct {
	NameValue string
}

func (t testPlannerFilter) Name() string { return "test-filter" }

func (t testPlannerFilter) Filter(_ context.Context, _ *planner.CycleState, _ planner.Request, target kaprov1alpha1.MemberCluster) *planner.Status {
	if target.Name != t.NameValue {
		return planner.NewStatus(planner.Skip, "filtered by test")
	}
	return nil
}

func pipelineWithCanaryStage() *kaprov1alpha1.Pipeline {
	return &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "pipeline"},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{
				{
					Name:     "canary",
					Selector: metav1.LabelSelector{MatchLabels: map[string]string{"stage": "canary"}},
				},
			},
		},
	}
}
