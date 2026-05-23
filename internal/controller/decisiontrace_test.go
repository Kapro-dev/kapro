package controller

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/decisiontrace"
)

func TestPromotionRunSuspendedEmitsDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "run-a",
			Finalizers: []string{promotionrunFinalizer},
		},
		Spec: kaprov1alpha2.PromotionRunSpec{Suspended: true},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.PromotionRun{}).
		WithObjects(run).
		Build()
	r := &PromotionRunReconciler{
		Client:               c,
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var traces kaprov1alpha2.DecisionTraceList
	if err := c.List(context.Background(), &traces); err != nil {
		t.Fatalf("List traces: %v", err)
	}
	if len(traces.Items) != 1 {
		t.Fatalf("trace count = %d, want 1", len(traces.Items))
	}
	trace := traces.Items[0]
	if trace.Spec.EventType != kaprov1alpha2.DecisionTraceEventSuspend ||
		trace.Spec.PromotionRun != "run-a" ||
		trace.Spec.Source != "promotionrun-controller" {
		t.Fatalf("trace spec = %#v", trace.Spec)
	}
}

func TestPromotionRunSuspendedTraceFailureDoesNotBlockReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "run-a",
			Finalizers: []string{promotionrunFinalizer},
		},
		Spec: kaprov1alpha2.PromotionRunSpec{Suspended: true},
	}
	boom := errors.New("trace sink down")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.PromotionRun{}).
		WithObjects(run).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*kaprov1alpha2.DecisionTrace); ok {
					return boom
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()
	r := &PromotionRunReconciler{
		Client:               c,
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: run.Name}}); err != nil {
		t.Fatalf("Reconcile should ignore trace create failure, got %v", err)
	}
}

func TestTargetDeliveryStatusEmitsDecisionTraces(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.DecisionTrace{}).
		Build()
	r := &TargetReconciler{
		Client:               c,
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}
	attempted := metav1.NewTime(time.Date(2026, 5, 23, 19, 55, 0, 0, time.UTC))
	applied := metav1.NewTime(time.Date(2026, 5, 23, 19, 56, 0, 0, time.UTC))
	promotionrun := &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "run-a"}}
	target := &kaprov1alpha2.TargetExecutionState{
		PromotionRunRef: "run-a",
		PlanRef:         "plan-a",
		Stage:           "prod",
		Target:          "cluster-a",
	}
	cluster := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Status: kaprov1alpha2.ClusterStatus{
			Delivery: map[string]kaprov1alpha2.ClusterDeliveryStatus{
				"api": {
					Phase:           kaprov1alpha2.DeliveryPhaseFailed,
					DesiredVersion:  "v2",
					LastAttemptedAt: &attempted,
					LastError:       "dry-run rejected configmap",
					ObservedDigest:  "sha256:abc",
					Format:          "raw-yaml",
					AppliedObjects:  0,
					Staging: &kaprov1alpha2.DeliveryStagingStatus{
						Type:                 kaprov1alpha2.DeliveryStagingTwoPhase,
						FailurePolicy:        kaprov1alpha2.DeliveryStagingFailureAbort,
						StagedObjects:        3,
						StagingFailedObjects: 1,
						FailurePhase:         kaprov1alpha2.DeliveryPhaseStaging,
					},
				},
				"worker": {
					Phase:           kaprov1alpha2.DeliveryPhaseConverged,
					DesiredVersion:  "v2",
					LastAttemptedAt: &attempted,
					LastAppliedAt:   &applied,
					ObservedDigest:  "sha256:def",
					Format:          "raw-yaml",
					AppliedObjects:  2,
				},
			},
		},
	}

	r.emitDeliveryDecisionTraces(context.Background(), promotionrun, target, cluster, map[string]string{
		"api":    "v2",
		"worker": "v2",
	})

	var traces kaprov1alpha2.DecisionTraceList
	if err := c.List(context.Background(), &traces); err != nil {
		t.Fatalf("List traces: %v", err)
	}
	if len(traces.Items) != 2 {
		t.Fatalf("trace count = %d, want 2", len(traces.Items))
	}
	sort.Slice(traces.Items, func(i, j int) bool {
		return traces.Items[i].Spec.Message < traces.Items[j].Spec.Message
	})
	apiTrace := traces.Items[0]
	if apiTrace.Spec.EventType != kaprov1alpha2.DecisionTraceEventDelivery {
		t.Fatalf("eventType = %q, want Delivery", apiTrace.Spec.EventType)
	}
	if apiTrace.Spec.Reason != "DeliveryFailed" || apiTrace.Spec.Phase != string(kaprov1alpha2.DeliveryPhaseFailed) {
		t.Fatalf("trace phase/reason = %q/%q, want Failed/DeliveryFailed", apiTrace.Spec.Phase, apiTrace.Spec.Reason)
	}
	if len(apiTrace.Spec.Evidence) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(apiTrace.Spec.Evidence))
	}
	detail := apiTrace.Spec.Evidence[0].Detail
	if detail["appKey"] != "api" ||
		detail["stagingFailurePhase"] != string(kaprov1alpha2.DeliveryPhaseStaging) ||
		detail["stagingFailedObjects"] != "1" ||
		detail["observedDigest"] != "sha256:abc" {
		t.Fatalf("api trace evidence = %#v", detail)
	}
}

func TestTargetTransitionToEmitsDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.DecisionTrace{}).
		Build()
	r := &TargetReconciler{
		Client:               c,
		Recorder:             record.NewFakeRecorder(10),
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}
	promotionrun := &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "run-a"}}
	target := &kaprov1alpha2.TargetExecutionState{
		PromotionRunRef: "run-a",
		PlanRef:         "plan-a",
		Stage:           "canary",
		Target:          "cluster-a",
		Phase:           kaprov1alpha2.TargetPhasePending,
		Version:         "v2",
		AppKey:          "api",
	}

	r.transitionTo(context.Background(), promotionrun, target, kaprov1alpha2.TargetPhaseVerification)

	trace := singleDecisionTrace(t, c)
	if trace.Spec.EventType != kaprov1alpha2.DecisionTraceEventStage ||
		trace.Spec.Source != "target-controller" ||
		trace.Spec.Reason != "TargetPhaseTransition" ||
		trace.Spec.Phase != string(kaprov1alpha2.TargetPhaseVerification) {
		t.Fatalf("trace spec = %#v", trace.Spec)
	}
	detail := trace.Spec.Evidence[0].Detail
	if detail["fromPhase"] != string(kaprov1alpha2.TargetPhasePending) ||
		detail["toPhase"] != string(kaprov1alpha2.TargetPhaseVerification) ||
		detail["version"] != "v2" ||
		detail["appKey"] != "api" {
		t.Fatalf("trace evidence = %#v", detail)
	}
}

func TestTargetFailTargetEmitsDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.DecisionTrace{}).
		Build()
	r := &TargetReconciler{
		Client:               c,
		Recorder:             record.NewFakeRecorder(10),
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}
	promotionrun := &kaprov1alpha2.PromotionRun{ObjectMeta: metav1.ObjectMeta{Name: "run-a"}}
	target := &kaprov1alpha2.TargetExecutionState{
		PromotionRunRef: "run-a",
		PlanRef:         "plan-a",
		Stage:           "canary",
		Target:          "cluster-a",
		Phase:           kaprov1alpha2.TargetPhaseMetricsCheck,
		Version:         "v2",
		Gate:            &kaprov1alpha2.GatePolicySpec{OnFailure: "continue"},
	}

	r.failTarget(context.Background(), promotionrun, target, "metric gate failed")

	trace := singleDecisionTrace(t, c)
	if target.Phase != kaprov1alpha2.TargetPhaseSkipped {
		t.Fatalf("target phase = %q, want Skipped", target.Phase)
	}
	if trace.Spec.Reason != "TargetSkippedOnFailureContinue" ||
		trace.Spec.Phase != string(kaprov1alpha2.TargetPhaseSkipped) ||
		trace.Spec.Message != "metric gate failed" {
		t.Fatalf("trace spec = %#v", trace.Spec)
	}
	if got := trace.Spec.Evidence[0].Detail["onFailure"]; got != "continue" {
		t.Fatalf("onFailure evidence = %q, want continue", got)
	}
}

func TestPromotionRunUpsertTargetEmitsBindDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.DecisionTrace{}).
		Build()
	r := &PromotionRunReconciler{
		Client:               c,
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-a"},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Versions: map[string]string{"api": "v2", "worker": "v3"},
		},
	}
	plan := &kaprov1alpha2.Plan{ObjectMeta: metav1.ObjectMeta{Name: "plan-cr"}}
	stage := kaprov1alpha2.Stage{Name: "canary"}
	cluster := kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}}
	var targets []kaprov1alpha2.TargetExecutionState

	if _, err := r.upsertTarget(context.Background(), &targets, promotionrun, "plan-a", plan, stage, cluster, nil); err != nil {
		t.Fatalf("upsertTarget: %v", err)
	}

	trace := singleDecisionTrace(t, c)
	if trace.Spec.EventType != kaprov1alpha2.DecisionTraceEventBatchProgress ||
		trace.Spec.Source != "promotionrun-controller" ||
		trace.Spec.Reason != "TargetBound" ||
		trace.Spec.Phase != "Bind" {
		t.Fatalf("trace spec = %#v", trace.Spec)
	}
	detail := trace.Spec.Evidence[0].Detail
	if detail["target"] != "cluster-a" || detail["stage"] != "canary" || detail["desiredVersionCount"] != "2" {
		t.Fatalf("trace evidence = %#v", detail)
	}
}

func TestCancelPromotionRunTargetsEmitsDecisionTrace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "run-a-plan-a-canary-cluster-a"},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: "run-a",
			Target:          "cluster-a",
			PlanRef:         "plan-a",
			Plan:            "plan-cr",
			Stage:           "canary",
			Version:         "v2",
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetExecutionState: kaprov1alpha2.TargetExecutionState{
				Phase: kaprov1alpha2.TargetPhaseApplying,
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.DecisionTrace{}).
		WithIndex(&kaprov1alpha2.Target{}, IndexKeyPromotionTargetPromotionRun, func(obj client.Object) []string {
			return PromotionTargetPromotionRunExtractor(obj)
		}).
		WithObjects(target).
		Build()
	r := &PromotionRunReconciler{
		Client:               c,
		DecisionTraceEmitter: decisiontrace.Emitter{Client: c},
	}

	if err := r.cancelPromotionRunTargets(context.Background(), "run-a", "promotionrun exceeded timeout"); err != nil {
		t.Fatalf("cancelPromotionRunTargets: %v", err)
	}

	trace := singleDecisionTrace(t, c)
	if trace.Spec.Reason != "PromotionRunTimeoutCancelled" ||
		trace.Spec.Phase != string(kaprov1alpha2.TargetPhaseFailed) ||
		trace.Spec.Target != "cluster-a" {
		t.Fatalf("trace spec = %#v", trace.Spec)
	}
	if got := trace.Spec.Evidence[0].Detail["fromPhase"]; got != string(kaprov1alpha2.TargetPhaseApplying) {
		t.Fatalf("fromPhase evidence = %q, want Applying", got)
	}
}

func singleDecisionTrace(t *testing.T, c client.Client) kaprov1alpha2.DecisionTrace {
	t.Helper()
	var traces kaprov1alpha2.DecisionTraceList
	if err := c.List(context.Background(), &traces); err != nil {
		t.Fatalf("List traces: %v", err)
	}
	if len(traces.Items) != 1 {
		t.Fatalf("trace count = %d, want 1: %#v", len(traces.Items), traces.Items)
	}
	return traces.Items[0]
}
