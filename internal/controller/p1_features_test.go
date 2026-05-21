package controller_test

// p1_features_test.go — unit tests for the three P1 feature additions:
//
//   p1-halt-policy    TestPromotionRunReconciler_HaltPolicy_CancelsSiblingSync
//   p1-gate-template  TestPromotionRunReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics
//   p1-auto-rollback  covered end-to-end via e2e_test.go
//
// All tests use controller-runtime's fake client — no envtest required.

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/controller"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
)

// ---- p1-halt-policy ---------------------------------------------------------
//
// The halt-policy envtest lives in e2e_test.go as TestE2E_HaltPolicy_CancelsSiblingTarget.
// It requires a real API server (envtest) because cancelPendingStageTargets uses
// field-indexed List + Update on cluster-scoped PromotionTarget objects.

// ---- p1-gate-template -------------------------------------------------------

// alwaysPassGate is a mock gate.Gate that immediately returns Passed.
// Used to verify that GateTemplate evaluation is wired and reachable.
type alwaysPassGate struct{}

func (g *alwaysPassGate) Evaluate(_ context.Context, _ gate.Request) (gate.Result, error) {
	return gate.Result{
		Phase:   kaprov1alpha2.GatePhasePassed,
		Message: "mock: always passes",
	}, nil
}

// TestPromotionRunReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics is a
// regression test for the early-return bug in handleTargetMetricsCheck.
//
// The bug: when GatePolicySpec.Gate.Metrics was empty, handleTargetMetricsCheck
// returned immediately via the early-return guard, skipping GateTemplate
// evaluation entirely. A policy that configured only GateTemplates (and no
// Prometheus metrics) never had its templates run.
//
// The fix: metrics and templates are now evaluated under separate guards;
// the function only fast-paths when BOTH are empty.
func TestPromotionRunReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	const (
		promotionrunName  = "tmpl-rel"
		promotionplanRef  = "initial"
		promotionplanName = "tmpl-promotionplan"
		stageName         = "deploy"
		envRefName        = "tmpl-env"
	)

	mc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: envRefName, Labels: map[string]string{"tier": "tmpl"}},
		Spec:       kaprov1alpha2.ClusterSpec{Delivery: kaprov1alpha2.DeliverySpec{Mode: "pull", BackendRef: "flux"}},
	}
	promotionplan := &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: promotionplanName},
		Spec: kaprov1alpha2.PlanSpec{
			Stages: []kaprov1alpha2.Stage{{
				Name:     stageName,
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "tmpl"}},
			}},
		},
	}

	gatePolicy := &kaprov1alpha2.GatePolicySpec{
		Mode: kaprov1alpha2.GateModeAuto,
		Gate: kaprov1alpha2.GateSpec{
			Templates: []kaprov1alpha2.GateTemplateSpec{
				{Name: "mock-gate-template", Type: "mock"},
			},
			// Metrics intentionally absent — was the bug trigger.
		},
	}

	// Pre-seed a PromotionTarget with the env already in MetricsCheck so the
	// first reconcile exercises handleTargetMetricsCheck directly.
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:       promotionrunName,
			Finalizers: []string{kaprov1alpha2.PromotionRunFinalizer},
		},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "v1.0.0",
			PromotionPlans: []kaprov1alpha2.PlanRef{
				{Name: promotionplanRef, Plan: promotionplanName},
			},
		},
		Status: kaprov1alpha2.PromotionRunStatus{
			Phase:           kaprov1alpha2.PromotionRunPhaseProgressing,
			ResolvedVersion: "v1.0.0",
			PromotionPlanProgress: []kaprov1alpha2.PromotionPlanProgress{
				{Name: promotionplanRef, Plan: promotionplanName, Phase: "Progressing"},
			},
		},
	}
	rt := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: controller.PromotionTargetObjectNameForTest(kaprov1alpha2.TargetStatus{
			PromotionRunRef:  promotionrunName,
			Target:           envRefName,
			PromotionPlanRef: promotionplanRef,
			Plan:    promotionplanName,
			Stage:            stageName,
			Version:          "v1.0.0",
		})},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef:  promotionrunName,
			Target:           envRefName,
			PromotionPlanRef: promotionplanRef,
			Plan:    promotionplanName,
			Stage:            stageName,
			Version:          "v1.0.0",
			Gate:             gatePolicy,
			AppKey:           "default",
		},
		Status: kaprov1alpha2.TargetStatus{TargetStatus: kaprov1alpha2.TargetStatus{
			PromotionRunRef:  promotionrunName,
			Target:           envRefName,
			PromotionPlanRef: promotionplanRef,
			Plan:    promotionplanName,
			Stage:            stageName,
			Version:          "v1.0.0",
			Phase:            kaprov1alpha2.TargetPhaseMetricsCheck,
			Gate:             gatePolicy,
			AppKey:           "default",
		}},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.Target{}).
		WithStatusSubresource(&kaprov1alpha2.PromotionRun{}).
		WithObjects(mc, promotionplan, promotionrun, rt).
		Build()

	gateReg := gate.NewRegistry()
	gateReg.MustRegister("mock", &alwaysPassGate{})

	r := &controller.PromotionTargetReconciler{
		Client:           c,
		Recorder:         record.NewFakeRecorder(100),
		Scheme:           scheme,
		ActuatorRegistry: actuator.NewRegistry(),
		GateRegistry:     gateReg,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: rt.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	// Re-read the PromotionTarget to check FSM advanced.
	var updatedRT kaprov1alpha2.Target
	if err := c.Get(context.Background(), types.NamespacedName{Name: rt.Name}, &updatedRT); err != nil {
		t.Fatalf("Get PromotionTarget: %v", err)
	}
	target := updatedRT

	// The env must have left MetricsCheck. If the bug is present it stays in
	// MetricsCheck because the GateTemplate is never evaluated.
	if target.Status.Phase == kaprov1alpha2.TargetPhaseMetricsCheck {
		t.Fatal("GateTemplate was not evaluated: target is still in MetricsCheck after reconcile. " +
			"Bug: handleTargetMetricsCheck returned early when Metrics[] is empty, skipping Templates.")
	}

	// With no approval required the env advances from MetricsCheck to Applying.
	if target.Status.Phase != kaprov1alpha2.TargetPhaseApplying {
		t.Errorf("expected target phase=Applying after template passed, got %s", target.Status.Phase)
	}

	// The gate run status must be recorded in the env.
	if len(target.Status.Gates) == 0 {
		t.Error("expected target.Gates to be populated after GateTemplate evaluation")
	} else if target.Status.Gates[0].Phase != kaprov1alpha2.GatePhasePassed {
		t.Errorf("expected gate status Passed, got %s", target.Status.Gates[0].Phase)
	}
}

// TestPromotionRunReconciler_PromotionRunsForNewMatchingCluster verifies that a newly
// registered cluster still wakes an in-progress PromotionRun even before that cluster
// has a PromotionTarget child object.
//
// This protects the watch-mapper fallback path used when the active-cluster
// index has no hit yet for the new cluster.
func TestPromotionRunReconciler_PromotionRunsForNewMatchingCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	const (
		promotionrunName  = "new-cluster-rel"
		promotionplanName = "new-cluster-promotionplan"
	)

	mc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "new-cluster",
			Labels: map[string]string{"tier": "prod", "region": "eu"},
		},
		Spec: kaprov1alpha2.ClusterSpec{
			Delivery: kaprov1alpha2.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
	}

	promotionplan := &kaprov1alpha2.Plan{
		ObjectMeta: metav1.ObjectMeta{Name: promotionplanName},
		Spec: kaprov1alpha2.PlanSpec{
			Stages: []kaprov1alpha2.Stage{{
				Name:     "prod",
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			}},
		},
	}

	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: promotionrunName, Namespace: "default"},
		Spec: kaprov1alpha2.PromotionRunSpec{
			Version: "registry.example.com/bundle@sha256:cccc",
			PromotionPlans: []kaprov1alpha2.PlanRef{{
				Name:          "main",
				Plan: promotionplanName,
			}},
		},
		Status: kaprov1alpha2.PromotionRunStatus{
			Phase: kaprov1alpha2.PromotionRunPhaseProgressing,
			// No PromotionTarget exists yet for the new cluster.
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha2.Target{}).
		WithObjects(mc, promotionplan, promotionrun).
		Build()

	r := &controller.PromotionRunReconciler{Client: c}

	reqs := r.ProgressingPromotionRunsForNewClusterForTest(context.Background(), mc)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 reconcile request for new matching cluster, got %d", len(reqs))
	}
	if reqs[0].Name != promotionrunName || reqs[0].Namespace != "default" {
		t.Fatalf("unexpected request target: %#v", reqs[0])
	}
}
