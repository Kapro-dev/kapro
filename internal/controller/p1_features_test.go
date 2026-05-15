package controller_test

// p1_features_test.go — unit tests for the three P1 feature additions:
//
//   p1-halt-policy    TestReleaseReconciler_HaltPolicy_CancelsSiblingSync
//   p1-gate-template  TestReleaseReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics
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

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/controller"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
)

// ---- p1-halt-policy ---------------------------------------------------------
//
// The halt-policy envtest lives in e2e_test.go as TestE2E_HaltPolicy_CancelsSiblingTarget.
// It requires a real API server (envtest) because cancelPendingStageTargets uses
// field-indexed List + Update on cluster-scoped ReleaseTarget objects.

// ---- p1-gate-template -------------------------------------------------------

// alwaysPassGate is a mock gate.Gate that immediately returns Passed.
// Used to verify that GateTemplate evaluation is wired and reachable.
type alwaysPassGate struct{}

func (g *alwaysPassGate) Evaluate(_ context.Context, _ gate.Request) (gate.Result, error) {
	return gate.Result{
		Phase:   kaprov1alpha1.GatePhasePassed,
		Message: "mock: always passes",
	}, nil
}

// TestReleaseReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics is a
// regression test for the early-return bug in handleTargetMetricsCheck.
//
// The bug: when GatePolicySpec.Gate.Metrics was empty, handleTargetMetricsCheck
// returned immediately via the early-return guard, skipping GateTemplate
// evaluation entirely. A policy that configured only GateTemplates (and no
// Prometheus metrics) never had its templates run.
//
// The fix: metrics and templates are now evaluated under separate guards;
// the function only fast-paths when BOTH are empty.
func TestReleaseReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	const (
		releaseName  = "tmpl-rel"
		pipelineRef  = "initial"
		pipelineName = "tmpl-pipeline"
		stageName    = "deploy"
		envRefName   = "tmpl-env"
	)

	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: envRefName, Labels: map[string]string{"tier": "tmpl"}},
		Spec:       kaprov1alpha1.MemberClusterSpec{Delivery: kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"}},
	}
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipelineName},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{{
				Name:     stageName,
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "tmpl"}},
			}},
		},
	}

	gatePolicy := &kaprov1alpha1.GatePolicySpec{
		Mode: kaprov1alpha1.GateModeAuto,
		Gate: kaprov1alpha1.GateSpec{
			Templates: []kaprov1alpha1.GateTemplateSpec{
				{Name: "mock-gate-template", Type: "mock"},
			},
			// Metrics intentionally absent — was the bug trigger.
		},
	}

	// Pre-seed a ReleaseTarget with the env already in MetricsCheck so the
	// first reconcile exercises handleTargetMetricsCheck directly.
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       releaseName,
			Finalizers: []string{kaprov1alpha1.ReleaseFinalizer},
		},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "v1.0.0",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: pipelineRef, Pipeline: pipelineName},
			},
		},
		Status: kaprov1alpha1.ReleaseStatus{
			Phase:           kaprov1alpha1.ReleasePhaseProgressing,
			ResolvedVersion: "v1.0.0",
			PipelineProgress: []kaprov1alpha1.PipelineProgress{
				{Name: pipelineRef, Pipeline: pipelineName, Phase: "Progressing"},
			},
		},
	}
	rt := &kaprov1alpha1.ReleaseTarget{
		ObjectMeta: metav1.ObjectMeta{Name: controller.ReleaseTargetObjectNameForTest(kaprov1alpha1.TargetStatus{
			ReleaseRef:  releaseName,
			Target:      envRefName,
			PipelineRef: pipelineRef,
			Pipeline:    pipelineName,
			Stage:       stageName,
			Version:     "v1.0.0",
		})},
		Spec: kaprov1alpha1.ReleaseTargetSpec{
			ReleaseRef:  releaseName,
			Target:      envRefName,
			PipelineRef: pipelineRef,
			Pipeline:    pipelineName,
			Stage:       stageName,
			Version:     "v1.0.0",
			Gate:        gatePolicy,
			AppKey:      "default",
		},
		Status: kaprov1alpha1.ReleaseTargetStatus{TargetStatus: kaprov1alpha1.TargetStatus{
			ReleaseRef:  releaseName,
			Target:      envRefName,
			PipelineRef: pipelineRef,
			Pipeline:    pipelineName,
			Stage:       stageName,
			Version:     "v1.0.0",
			Phase:       kaprov1alpha1.TargetPhaseMetricsCheck,
			Gate:        gatePolicy,
			AppKey:      "default",
		}},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.ReleaseTarget{}).
		WithStatusSubresource(&kaprov1alpha1.Release{}).
		WithObjects(mc, pipeline, release, rt).
		Build()

	gateReg := gate.NewRegistry()
	gateReg.MustRegister("mock", &alwaysPassGate{})

	r := &controller.ReleaseTargetReconciler{
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

	// Re-read the ReleaseTarget to check FSM advanced.
	var updatedRT kaprov1alpha1.ReleaseTarget
	if err := c.Get(context.Background(), types.NamespacedName{Name: rt.Name}, &updatedRT); err != nil {
		t.Fatalf("Get ReleaseTarget: %v", err)
	}
	target := updatedRT

	// The env must have left MetricsCheck. If the bug is present it stays in
	// MetricsCheck because the GateTemplate is never evaluated.
	if target.Status.Phase == kaprov1alpha1.TargetPhaseMetricsCheck {
		t.Fatal("GateTemplate was not evaluated: target is still in MetricsCheck after reconcile. " +
			"Bug: handleTargetMetricsCheck returned early when Metrics[] is empty, skipping Templates.")
	}

	// With no approval required the env advances from MetricsCheck to Applying.
	if target.Status.Phase != kaprov1alpha1.TargetPhaseApplying {
		t.Errorf("expected target phase=Applying after template passed, got %s", target.Status.Phase)
	}

	// The gate run status must be recorded in the env.
	if len(target.Status.Gates) == 0 {
		t.Error("expected target.Gates to be populated after GateTemplate evaluation")
	} else if target.Status.Gates[0].Phase != kaprov1alpha1.GatePhasePassed {
		t.Errorf("expected gate status Passed, got %s", target.Status.Gates[0].Phase)
	}
}

// TestReleaseReconciler_ReleasesForNewMatchingCluster verifies that a newly
// registered cluster still wakes an in-progress Release even before that cluster
// has a ReleaseTarget child object.
//
// This protects the watch-mapper fallback path used when the active-cluster
// index has no hit yet for the new cluster.
func TestReleaseReconciler_ReleasesForNewMatchingCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	const (
		releaseName  = "new-cluster-rel"
		pipelineName = "new-cluster-pipeline"
	)

	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "new-cluster",
			Labels: map[string]string{"tier": "prod", "region": "eu"},
		},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{Mode: "pull", BackendRef: "flux"},
		},
	}

	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipelineName},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{{
				Name:     "prod",
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}},
			}},
		},
	}

	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: releaseName, Namespace: "default"},
		Spec: kaprov1alpha1.ReleaseSpec{
			Version: "registry.example.com/bundle@sha256:cccc",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{{
				Name:     "main",
				Pipeline: pipelineName,
			}},
		},
		Status: kaprov1alpha1.ReleaseStatus{
			Phase: kaprov1alpha1.ReleasePhaseProgressing,
			// No ReleaseTarget exists yet for the new cluster.
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.ReleaseTarget{}).
		WithObjects(mc, pipeline, release).
		Build()

	r := &controller.ReleaseReconciler{Client: c}

	reqs := r.ProgressingReleasesForNewClusterForTest(context.Background(), mc)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 reconcile request for new matching cluster, got %d", len(reqs))
	}
	if reqs[0].Name != releaseName || reqs[0].Namespace != "default" {
		t.Fatalf("unexpected request target: %#v", reqs[0])
	}
}
