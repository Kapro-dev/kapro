package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/planner"
)

func TestStageDependencySatisfied_AnyUnlocksFromOneConvergedTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &PromotionRunReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			fleetClusterForStage("cluster-a", "canary"),
			fleetClusterForStage("cluster-b", "canary"),
		).Build(),
	}
	promotionrun := &kaprov1alpha2.PromotionRun{}
	targets := []kaprov1alpha2.TargetExecutionState{
		{
			Target:     "cluster-a",
			PlanRef:    "main",
			Stage:      "canary",
			Phase:      kaprov1alpha2.TargetPhaseConverged,
			FinishedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		},
		{
			Target:  "cluster-b",
			PlanRef: "main",
			Stage:   "canary",
			Phase:   kaprov1alpha2.TargetPhaseHealthCheck,
		},
	}
	promotionplan := promotionplanWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), promotionrun, targets, "main", promotionplan, kaprov1alpha2.StageDependency{
		Stage:            "canary",
		Strategy:         kaprov1alpha2.StageDependencyAny,
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

func TestPromotionRunDesiredVersions_ExplicitDefaultOverridesSpecVersion(t *testing.T) {
	promotionrun := &kaprov1alpha2.PromotionRun{
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "fallback",
			Versions: map[string]string{
				"default": "explicit",
				"api":     "api-v2",
			},
		},
	}

	desired := promotionrunDesiredVersionsFromSpec(promotionrun)
	if got := desired["default"]; got != "explicit" {
		t.Fatalf("default version = %q, want explicit", got)
	}
	if got := promotionrunPrimaryVersion(promotionrun, desired); got != "explicit" {
		t.Fatalf("primary version = %q, want explicit", got)
	}
}

func TestHandleProgressingFailsWhenPromotionPlanGenerationChanges(t *testing.T) {
	scheme := controllerTestScheme(t)
	promotionplan := &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "progressive", Generation: 2},
		Spec: kaprov1alpha2.PlanSpec{Stages: []kaprov1alpha2.Stage{{
			Name:     "canary",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"stage": "canary"}},
		}}},
	}
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Generation: 1},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "repo@sha256:abc",
			Plans:   []kaprov1alpha2.PlanRef{{Name: "main", Plan: "progressive"}},
		},
		Status: kaprov1alpha2.PromotionRunStatus{
			Phase:           kaprov1alpha2.PromotionRunPhaseProgressing,
			ResolvedVersion: "repo@sha256:abc",
			PlanProgress: []kaprov1alpha2.PlanProgress{{
				Name:               "main",
				Plan:               "progressive",
				ObservedGeneration: 1,
				Phase:              "Progressing",
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.PromotionRun{}).
		WithIndex(&kaprov1alpha2.Target{}, IndexKeyPromotionTargetPromotionRun, func(obj client.Object) []string {
			return PromotionTargetPromotionRunExtractor(obj)
		}).
		WithObjects(promotionplan, promotionrun).
		Build()
	recorder := record.NewFakeRecorder(10)
	r := &PromotionRunReconciler{
		Client:   c,
		Recorder: recorder,
	}

	if _, err := r.handleProgressing(context.Background(), promotionrun.DeepCopy()); err != nil {
		t.Fatalf("handleProgressing returned error: %v", err)
	}

	var updated kaprov1alpha2.PromotionRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "rel-1"}, &updated); err != nil {
		t.Fatalf("get PromotionRun: %v", err)
	}
	if updated.Status.Phase != kaprov1alpha2.PromotionRunPhaseFailed {
		t.Fatalf("phase = %s, want Failed", updated.Status.Phase)
	}
	ready := apimeta.FindStatusCondition(updated.Status.Conditions, "Ready")
	if ready == nil || ready.Reason != "PromotionPlanChanged" {
		t.Fatalf("Ready condition = %#v, want reason PromotionPlanChanged", ready)
	}
	stalled := apimeta.FindStatusCondition(updated.Status.Conditions, kaprov1alpha2.ConditionTypeStalled)
	if stalled == nil || stalled.Reason != "PromotionPlanChanged" {
		t.Fatalf("Stalled condition = %#v, want reason PromotionPlanChanged", stalled)
	}
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "PromotionPlanChanged") {
			t.Fatalf("event = %q, want PromotionPlanChanged", event)
		}
	case <-time.After(time.Second):
		t.Fatal("expected PromotionPlanChanged event")
	}
}

func TestHandleFailedSummarizesChildTargets(t *testing.T) {
	scheme := controllerTestScheme(t)
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Generation: 1},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "repo@sha256:abc",
			Plans:   []kaprov1alpha2.PlanRef{{Name: "main", Plan: "progressive"}},
		},
		Status: kaprov1alpha2.PromotionRunStatus{
			Phase:           kaprov1alpha2.PromotionRunPhaseFailed,
			ResolvedVersion: "repo@sha256:abc",
			StartedAt:       time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339),
			CompletedAt:     time.Now().UTC().Format(time.RFC3339),
		},
	}
	objects := []client.Object{
		promotionrun,
		targetForSummary("rel-1-a", "rel-1", "cluster-a", kaprov1alpha2.TargetPhaseConverged),
		targetForSummary("rel-1-b", "rel-1", "cluster-b", kaprov1alpha2.TargetPhaseFailed),
		targetForSummary("rel-1-c", "rel-1", "cluster-c", kaprov1alpha2.TargetPhaseApplying),
		targetForSummary("other-a", "other-run", "cluster-z", kaprov1alpha2.TargetPhaseFailed),
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.PromotionRun{}, &kaprov1alpha2.Target{}).
		WithIndex(&kaprov1alpha2.Target{}, IndexKeyPromotionTargetPromotionRun, func(obj client.Object) []string {
			return PromotionTargetPromotionRunExtractor(obj)
		}).
		WithObjects(objects...).
		Build()
	r := &PromotionRunReconciler{Client: c, Scheme: scheme}

	if _, err := r.handleFailed(context.Background(), promotionrun.DeepCopy()); err != nil {
		t.Fatalf("handleFailed returned error: %v", err)
	}

	var updated kaprov1alpha2.PromotionRun
	if err := c.Get(context.Background(), client.ObjectKey{Name: "rel-1"}, &updated); err != nil {
		t.Fatalf("get PromotionRun: %v", err)
	}
	if updated.Status.Summary == nil {
		t.Fatal("summary is nil, want aggregate counts")
	}
	if got := *updated.Status.Summary; got.TotalTargets != 3 || got.SyncedTargets != 1 || got.FailedTargets != 1 || got.PendingTargets != 1 {
		t.Fatalf("summary = %#v, want total=3 synced=1 failed=1 pending=1", got)
	}
	if got := updated.Status.Report; got.TotalTargets != 3 || got.SyncedTargets != 1 || got.FailedTargets != 1 || got.PendingTargets != 1 {
		t.Fatalf("report = %#v, want total=3 synced=1 failed=1 pending=1", got)
	}
}

func TestPromotionRunSummaryConvergedAtFromCompleteReport(t *testing.T) {
	completedAt := time.Now().UTC().Format(time.RFC3339)
	summary := promotionRunSummaryFromReport(kaprov1alpha2.PromotionRunReportSummary{
		Phase:         kaprov1alpha2.PromotionRunPhaseComplete,
		CompletedAt:   completedAt,
		TotalTargets:  2,
		SyncedTargets: 2,
	})
	if summary.ConvergedAt != completedAt {
		t.Fatalf("convergedAt = %q, want %q", summary.ConvergedAt, completedAt)
	}
}

func TestTargetStatusFromPromotionTargetSpecIdentityWins(t *testing.T) {
	rt := &kaprov1alpha2.Target{
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: "run-from-spec",
			Target:          "cluster-from-spec",
			PlanRef:         "plan-ref-from-spec",
			Plan:            "plan-from-spec",
			Stage:           "stage-from-spec",
			Version:         "version-from-spec",
			AppKey:          "app-from-spec",
			DesiredVersions: map[string]string{"api": "v2"},
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				PromotionRunRef: "stale-run",
				Target:          "stale-cluster",
				PlanRef:         "stale-plan-ref",
				Plan:            "stale-plan",
				Stage:           "stale-stage",
				Version:         "stale-version",
				AppKey:          "stale-app",
				DesiredVersions: map[string]string{"api": "stale"},
				Phase:           kaprov1alpha2.TargetPhaseFailed,
				Message:         "gate timeout",
			},
		},
	}

	got := targetStatusFromPromotionTarget(rt)
	if got.PromotionRunRef != "run-from-spec" ||
		got.Target != "cluster-from-spec" ||
		got.PlanRef != "plan-ref-from-spec" ||
		got.Plan != "plan-from-spec" ||
		got.Stage != "stage-from-spec" ||
		got.Version != "version-from-spec" ||
		got.AppKey != "app-from-spec" {
		t.Fatalf("identity fields came from stale status, got %#v", got)
	}
	if got.Phase != kaprov1alpha2.TargetPhaseFailed || got.Message != "gate timeout" {
		t.Fatalf("execution fields = phase %q message %q, want status-owned execution state", got.Phase, got.Message)
	}
	if got.DesiredVersions["api"] != "v2" {
		t.Fatalf("desiredVersions = %#v, want spec-owned values", got.DesiredVersions)
	}
	got.DesiredVersions["api"] = "mutated"
	if rt.Spec.DesiredVersions["api"] != "v2" {
		t.Fatalf("targetStatusFromPromotionTarget returned aliased desiredVersions map")
	}
}

func TestNotifyPromotionRunEvent_UsesPromotionPlanStageNotifications(t *testing.T) {
	scheme := controllerTestScheme(t)
	notifier := &recordingNotifier{}
	promotionplan := &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "progressive"},
		Spec: kaprov1alpha2.PlanSpec{Stages: []kaprov1alpha2.Stage{{
			Name:     "canary",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
			Gate: &kaprov1alpha2.GatePolicySpec{
				Notifications: []kaprov1alpha2.NotificationSpec{{
					Type:   "webhook",
					Events: []string{notification.EventPromotionRunStarted},
				}},
			},
		}}},
	}
	r := &PromotionRunReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(promotionplan).Build(),
		Notifier: notifier,
	}
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "repo@sha256:abc",
			Plans:   []kaprov1alpha2.PlanRef{{Name: "main", Plan: "progressive"}},
		},
		Status: kaprov1alpha2.PromotionRunStatus{
			Phase:           kaprov1alpha2.PromotionRunPhaseProgressing,
			ResolvedVersion: "repo@sha256:abc",
		},
	}

	r.notifyPromotionRunEvent(context.Background(), promotionrun, notification.EventPromotionRunStarted, "started")

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 promotionrun notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Type != notification.EventPromotionRunStarted {
		t.Fatalf("expected promotionrun started event, got %q", notifier.events[0].Type)
	}
	if len(notifier.policies) != 1 || len(notifier.policies[0].Channels) != 1 {
		t.Fatalf("expected promotionrun policy to collect one channel, got %#v", notifier.policies)
	}
}

func TestResolveStageGate_ExpandsMetricPreset(t *testing.T) {
	promotionplan := &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "progressive"},
		Spec: kaprov1alpha2.PlanSpec{
			MetricPresets: map[string]kaprov1alpha2.MetricGate{
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
	stage := kaprov1alpha2.Stage{
		Name: "canary",
		Gate: &kaprov1alpha2.GatePolicySpec{
			Gate: kaprov1alpha2.GateSpec{
				Metrics: []kaprov1alpha2.MetricGate{{
					Preset:   "error-rate",
					Window:   "10m",
					Interval: "1m",
				}},
			},
		},
	}

	gatePolicy, err := resolveStageGate(promotionplan, stage)
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
	gatePolicy, err := resolveStageGate(&kaprov1alpha2.Plan{
		Spec: kaprov1alpha2.PlanSpec{
			MetricPresets: map[string]kaprov1alpha2.MetricGate{
				"error-rate": {
					Provider:  "prometheus",
					Query:     "rate(errors[5m])",
					Threshold: float64Ptr(0.01),
				},
			},
		},
	}, kaprov1alpha2.Stage{
		Name: "canary",
		Gate: &kaprov1alpha2.GatePolicySpec{
			Gate: kaprov1alpha2.GateSpec{
				Metrics: []kaprov1alpha2.MetricGate{{
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
	_, err := resolveStageGate(&kaprov1alpha2.Plan{}, kaprov1alpha2.Stage{
		Name: "canary",
		Gate: &kaprov1alpha2.GatePolicySpec{
			Gate: kaprov1alpha2.GateSpec{
				Metrics: []kaprov1alpha2.MetricGate{{Preset: "missing"}},
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
	promotionplan := &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "progressive"},
		Spec: kaprov1alpha2.PlanSpec{Stages: []kaprov1alpha2.Stage{{
			Name:     "canary",
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "canary"}},
			Gate: &kaprov1alpha2.GatePolicySpec{
				Notifications: []kaprov1alpha2.NotificationSpec{{
					Type:   "webhook",
					Events: []string{notification.EventStageCompleted},
				}},
			},
		}}},
	}
	r := &PromotionRunReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(promotionplan).Build(),
		Notifier: notifier,
	}
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1"},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "repo@sha256:abc",
			Plans:   []kaprov1alpha2.PlanRef{{Name: "main", Plan: "progressive"}},
		},
		Status: kaprov1alpha2.PromotionRunStatus{ResolvedVersion: "repo@sha256:abc"},
	}

	r.notifyStageEvent(context.Background(), promotionrun, "main", "canary", notification.EventStageCompleted, "complete")

	if len(notifier.events) != 1 {
		t.Fatalf("expected 1 stage notification, got %d", len(notifier.events))
	}
	if notifier.events[0].Type != notification.EventStageCompleted {
		t.Fatalf("expected stage completed event, got %q", notifier.events[0].Type)
	}
	if notifier.events[0].Plan != "main" || notifier.events[0].Stage != "canary" {
		t.Fatalf("stage event context not populated: %#v", notifier.events[0])
	}
	if len(notifier.policies) != 1 || len(notifier.policies[0].Channels) != 1 {
		t.Fatalf("expected stage policy to provide one channel, got %#v", notifier.policies)
	}
}

func TestStageDependencySatisfied_AllRequiresEveryTarget(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &PromotionRunReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			fleetClusterForStage("cluster-a", "canary"),
			fleetClusterForStage("cluster-b", "canary"),
		).Build(),
	}
	promotionrun := &kaprov1alpha2.PromotionRun{}
	targets := []kaprov1alpha2.TargetExecutionState{
		{
			Target:     "cluster-a",
			PlanRef:    "main",
			Stage:      "canary",
			Phase:      kaprov1alpha2.TargetPhaseConverged,
			FinishedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		},
		{
			Target:  "cluster-b",
			PlanRef: "main",
			Stage:   "canary",
			Phase:   kaprov1alpha2.TargetPhaseApplying,
		},
	}
	promotionplan := promotionplanWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), promotionrun, targets, "main", promotionplan, kaprov1alpha2.StageDependency{
		Stage:    "canary",
		Strategy: kaprov1alpha2.StageDependencyAll,
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
	r := &PromotionRunReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			fleetClusterForStage("cluster-a", "canary"),
		).Build(),
	}
	promotionrun := &kaprov1alpha2.PromotionRun{}
	targets := []kaprov1alpha2.TargetExecutionState{
		{
			Target:     "cluster-a",
			PlanRef:    "main",
			Stage:      "canary",
			Phase:      kaprov1alpha2.TargetPhaseConverged,
			FinishedAt: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}
	promotionplan := promotionplanWithCanaryStage()

	satisfied, wait, err := r.stageDependencySatisfied(context.Background(), promotionrun, targets, "main", promotionplan, kaprov1alpha2.StageDependency{
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

func TestListTargetsForStageUsesPromotionRunPlanner(t *testing.T) {
	scheme := controllerTestScheme(t)
	r := &PromotionRunReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			fleetClusterForStage("cluster-a", "canary"),
			fleetClusterForStage("cluster-b", "canary"),
		).Build(),
		Planner: planner.NewFramework(testPlannerFilter{NameValue: "cluster-b"}),
	}
	promotionrun := &kaprov1alpha2.PromotionRun{}
	promotionplan := promotionplanWithCanaryStage()

	targets, err := r.listTargetsForStage(context.Background(), "main", promotionplan, promotionplan.Spec.Stages[0], promotionrun)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Name != "cluster-b" {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestReconcilePromotionPlanStagesHonorsStageMaxParallel(t *testing.T) {
	scheme := controllerTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		fleetClusterForStage("cluster-a", "canary"),
		fleetClusterForStage("cluster-b", "canary"),
		fleetClusterForStage("cluster-c", "canary"),
	).WithIndex(&kaprov1alpha2.Target{}, IndexKeyPromotionTargetPromotionRun, func(obj client.Object) []string {
		return PromotionTargetPromotionRunExtractor(obj)
	}).Build()
	r := &PromotionRunReconciler{
		Client: c,
		Scheme: scheme,
	}
	promotionplan := promotionplanWithCanaryStage()
	promotionplan.Spec.Stages[0].Strategy = &kaprov1alpha2.StageStrategySpec{MaxParallel: 1}
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "promotionrun-a"},
		Spec:       kaprov1alpha2.PromotionRunSpec{Version: "1.2.3"},
		Status:     kaprov1alpha2.PromotionRunStatus{ResolvedVersion: "1.2.3"},
	}

	var targets []kaprov1alpha2.TargetExecutionState
	progress, allComplete, anyFailed, _, _, err := r.reconcilePromotionPlanStages(context.Background(), promotionrun, &targets, "main", promotionplan)
	if err != nil {
		t.Fatal(err)
	}
	if allComplete || anyFailed {
		t.Fatalf("allComplete=%v anyFailed=%v, want progressing without failure", allComplete, anyFailed)
	}
	if len(targets) != 1 {
		t.Fatalf("bound targets = %#v, want 1", targets)
	}
	if err := r.persistPromotionTargets(context.Background(), promotionrun, targets); err != nil {
		t.Fatal(err)
	}
	var persisted kaprov1alpha2.TargetList
	if err := c.List(context.Background(), &persisted, client.MatchingFields{IndexKeyPromotionTargetPromotionRun: promotionrun.Name}); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Items) != 1 {
		t.Fatalf("persisted targets = %d, want 1", len(persisted.Items))
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

	targets[0].Phase = kaprov1alpha2.TargetPhaseConverged
	progress, allComplete, anyFailed, _, _, err = r.reconcilePromotionPlanStages(context.Background(), promotionrun, &targets, "main", promotionplan)
	if err != nil {
		t.Fatal(err)
	}
	if allComplete || anyFailed {
		t.Fatalf("allComplete=%v anyFailed=%v, want second target progressing", allComplete, anyFailed)
	}
	if len(targets) != 2 {
		t.Fatalf("bound targets after second reconcile = %#v, want 2", targets)
	}
	if progress[0].Deferred != 1 {
		t.Fatalf("deferred after one convergence = %d, want 1", progress[0].Deferred)
	}
}

func targetForSummary(name, runName, clusterName string, phase kaprov1alpha2.TargetPhase) *kaprov1alpha2.Target {
	return &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: runName,
			Target:          clusterName,
			PlanRef:         "main",
			Plan:            "progressive",
			Stage:           "canary",
			Version:         "repo@sha256:abc",
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				PromotionRunRef: runName,
				Target:          clusterName,
				PlanRef:         "main",
				Plan:            "progressive",
				Stage:           "canary",
				Version:         "repo@sha256:abc",
				Phase:           phase,
			},
		},
	}
}

func fleetClusterForStage(name, stage string) *kaprov1alpha2.Cluster {
	return &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"stage": stage},
		},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
	}
}

type testPlannerFilter struct {
	NameValue string
}

func (t testPlannerFilter) Name() string { return "test-filter" }

func (t testPlannerFilter) Filter(_ context.Context, _ *planner.CycleState, _ planner.Request, target kaprov1alpha2.Cluster) *planner.Status {
	if target.Name != t.NameValue {
		return planner.NewStatus(planner.Skip, "filtered by test")
	}
	return nil
}

func promotionplanWithCanaryStage() *kaprov1alpha2.Plan {
	return &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: "promotionplan"},
		Spec: kaprov1alpha2.PlanSpec{
			Stages: []kaprov1alpha2.Stage{
				{
					Name:     "canary",
					Selector: metav1.LabelSelector{MatchLabels: map[string]string{"stage": "canary"}},
				},
			},
		},
	}
}
