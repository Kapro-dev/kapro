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

func TestResolveStageGate_ExpandsMetricPreset(t *testing.T) {
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "progressive"},
		Spec: kaprov1alpha1.PipelineSpec{
			MetricPresets: map[string]kaprov1alpha1.MetricGate{
				"error-rate": {
					Provider:  "prometheus",
					Query:     `sum(rate(errors[{{.Window}}])) / sum(rate(requests[{{.Window}}]))`,
					Window:    "5m",
					Interval:  "30s",
					Endpoint:  "http://prometheus.monitoring.svc:9090",
					Threshold: float64Ptr(0.01),
				},
			},
		},
	}
	stage := kaprov1alpha1.Stage{
		Name: "canary",
		Gate: &kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{{
					Preset:   "error-rate",
					Window:   "10m",
					Interval: "1m",
				}},
			},
		},
	}

	gatePolicy, err := resolveStageGate(pipeline, stage)
	if err != nil {
		t.Fatalf("resolveStageGate returned error: %v", err)
	}
	metric := gatePolicy.Gate.Metrics[0]
	if metric.Provider != "prometheus" || metric.Query == "" || metric.Endpoint == "" {
		t.Fatalf("preset fields were not expanded: %#v", metric)
	}
	if metric.Window != "10m" || metric.Interval != "1m" {
		t.Fatalf("inline overrides not applied: %#v", metric)
	}
	if metric.Threshold == nil || *metric.Threshold != 0.01 {
		t.Fatalf("threshold=%v, want 0.01", metric.Threshold)
	}
}

func TestResolveStageGate_CanOverridePresetThresholdToZero(t *testing.T) {
	gatePolicy, err := resolveStageGate(&kaprov1alpha1.Pipeline{
		Spec: kaprov1alpha1.PipelineSpec{
			MetricPresets: map[string]kaprov1alpha1.MetricGate{
				"error-rate": {
					Provider:  "prometheus",
					Query:     "rate(errors[5m])",
					Threshold: float64Ptr(0.01),
				},
			},
		},
	}, kaprov1alpha1.Stage{
		Name: "canary",
		Gate: &kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{{
					Preset:    "error-rate",
					Threshold: float64Ptr(0),
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("resolveStageGate returned error: %v", err)
	}
	metric := gatePolicy.Gate.Metrics[0]
	if metric.Threshold == nil || *metric.Threshold != 0 {
		t.Fatalf("threshold=%v, want explicit 0", metric.Threshold)
	}
}

func TestResolveStageGate_UnknownMetricPreset(t *testing.T) {
	_, err := resolveStageGate(&kaprov1alpha1.Pipeline{}, kaprov1alpha1.Stage{
		Name: "canary",
		Gate: &kaprov1alpha1.GatePolicySpec{
			Gate: kaprov1alpha1.GateSpec{
				Metrics: []kaprov1alpha1.MetricGate{{Preset: "missing"}},
			},
		},
	})
	if err == nil {
		t.Fatal("expected unknown preset error")
	}
}

func float64Ptr(v float64) *float64 {
	return &v
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

func TestReconcilePipelineStagesHonorsStageMaxParallel(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &ReleaseReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			memberClusterForStage("cluster-a", "canary"),
			memberClusterForStage("cluster-b", "canary"),
			memberClusterForStage("cluster-c", "canary"),
		).Build(),
	}
	pipeline := pipelineWithCanaryStage()
	pipeline.Spec.Stages[0].Strategy = &kaprov1alpha1.StageStrategySpec{MaxParallel: 1}
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: "release-a"},
		Spec:       kaprov1alpha1.ReleaseSpec{Version: "1.2.3"},
		Status:     kaprov1alpha1.ReleaseStatus{ResolvedVersion: "1.2.3"},
	}

	progress, allComplete, anyFailed, _, _, err := r.reconcilePipelineStages(context.Background(), release, "main", pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if allComplete || anyFailed {
		t.Fatalf("allComplete=%v anyFailed=%v, want progressing without failure", allComplete, anyFailed)
	}
	if len(release.Status.Targets) != 1 {
		t.Fatalf("bound targets = %#v, want 1", release.Status.Targets)
	}
	if len(progress) != 1 {
		t.Fatalf("progress = %#v", progress)
	}
	stageProgress := progress[0]
	if stageProgress.Total != 3 || stageProgress.Deferred != 2 || stageProgress.Phase != "Progressing" {
		t.Fatalf("stage progress = %#v", stageProgress)
	}
	if len(stageProgress.PlannerResults) != 2 {
		t.Fatalf("planner results = %#v, want 2 deferred targets", stageProgress.PlannerResults)
	}
	for _, result := range stageProgress.PlannerResults {
		if result.Plugin != "stage-strategy" || result.Reason != "MaxParallel" {
			t.Fatalf("unexpected planner result: %#v", result)
		}
	}

	release.Status.Targets[0].Phase = kaprov1alpha1.TargetPhaseConverged
	progress, allComplete, anyFailed, _, _, err = r.reconcilePipelineStages(context.Background(), release, "main", pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if allComplete || anyFailed {
		t.Fatalf("allComplete=%v anyFailed=%v, want second target progressing", allComplete, anyFailed)
	}
	if len(release.Status.Targets) != 2 {
		t.Fatalf("bound targets after second reconcile = %#v, want 2", release.Status.Targets)
	}
	if progress[0].Deferred != 1 {
		t.Fatalf("deferred after one convergence = %d, want 1", progress[0].Deferred)
	}
}

func memberClusterForStage(name, stage string) *kaprov1alpha1.MemberCluster {
	return &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"stage": stage},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"},
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
