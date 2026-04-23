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
	"kapro.io/kapro/pkg/gate"
)

// ---- p1-halt-policy ---------------------------------------------------------

// TestReleaseReconciler_HaltPolicy_CancelsSiblingSync verifies that when one
// target in a stage fails and the stage uses the default failurePolicy (halt),
// all non-terminal sibling targets in that stage are immediately cancelled
// (transitioned to Failed) so they do not keep running.
//
// After the Sync CRD fold, targets are tracked inline in
// release.Status.Targets — no standalone Sync objects are created.
func TestReleaseReconciler_HaltPolicy_CancelsSiblingSync(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	const (
		releaseName  = "halt-rel"
		pipelineRef  = "initial"
		pipelineName = "halt-pipeline"
		stageName    = "deploy"
	)

	// Two MemberClusters that match the stage selector.
	target1 := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-env1", Labels: map[string]string{"tier": "halt"}},
		Spec:       kaprov1alpha1.MemberClusterSpec{Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"}},
	}
	target2 := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-env2", Labels: map[string]string{"tier": "halt"}},
		Spec:       kaprov1alpha1.MemberClusterSpec{Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"}},
	}
	pipeline := &kaprov1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipelineName},
		Spec: kaprov1alpha1.PipelineSpec{
			Stages: []kaprov1alpha1.Stage{{
				Name:     stageName,
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "halt"}},
				// OnFailure not set → defaults to halt
			}},
		},
	}

	// Pre-seed the Release with two inline target entries: one Failed (the halt
	// trigger) and one Applying (in-flight; must be cancelled by halt policy).
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       releaseName,
			Namespace:  "default",
			Finalizers: []string{"kapro.io/release-cleanup"},
		},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "some-art",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{
				{Name: pipelineRef, Pipeline: pipelineName},
			},
		},
		Status: kaprov1alpha1.ReleaseStatus{
			Phase:           kaprov1alpha1.ReleasePhaseProgressing,
			ResolvedVersion: "repo@sha256:abc",
			PipelineProgress: []kaprov1alpha1.PipelineProgress{
				{Name: pipelineRef, Pipeline: pipelineName, Phase: "Progressing"},
			},
			Targets: []kaprov1alpha1.TargetStatus{
				{
					ReleaseRef:  releaseName,
					Target:      "halt-env1",
					PipelineRef: pipelineRef,
					Pipeline:    pipelineName,
					Stage:       stageName,
					Version:     "repo@sha256:abc",
					Phase:       kaprov1alpha1.SyncPhaseFailed,
					Message:     "deploy failed: timeout",
				},
				{
					ReleaseRef:  releaseName,
					Target:      "halt-env2",
					PipelineRef: pipelineRef,
					Pipeline:    pipelineName,
					Stage:       stageName,
					Version:     "repo@sha256:abc",
					Phase:       kaprov1alpha1.SyncPhaseApplying,
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Release{}).
		WithObjects(target1, target2, pipeline, release).
		Build()

	r := &controller.ReleaseReconciler{
		Client:   c,
		Recorder: record.NewFakeRecorder(100),
		Scheme:   scheme,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: releaseName},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	var gotRelease kaprov1alpha1.Release
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "default", Name: releaseName,
	}, &gotRelease); err != nil {
		t.Fatalf("Get Release: %v", err)
	}

	// 1. The in-flight sibling env must be Failed (not still Applying).
	var applyingTarget *kaprov1alpha1.TargetStatus
	for i := range gotRelease.Status.Targets {
		if gotRelease.Status.Targets[i].Target == "halt-env2" {
			applyingTarget = &gotRelease.Status.Targets[i]
			break
		}
	}
	if applyingTarget == nil {
		t.Fatal("halt-env2 not found in release.Status.Targets")
	}
	if applyingTarget.Phase != kaprov1alpha1.SyncPhaseFailed {
		t.Errorf("halt policy: expected halt-env2 to be Failed, got %q — "+
			"cancelPendingStageEnvs did not run", applyingTarget.Phase)
	}
	if applyingTarget.Message == "" {
		t.Error("halt policy: expected cancellation message on the cancelled target")
	}

	// 2. The Release itself must be Failed (halt stops the whole pipeline).
	if gotRelease.Status.Phase != kaprov1alpha1.ReleasePhaseFailed {
		t.Errorf("halt policy: expected Release to be Failed, got %q", gotRelease.Status.Phase)
	}

	// 3. The already-Failed env must be untouched (terminal states are never overwritten).
	var failedTarget *kaprov1alpha1.TargetStatus
	for i := range gotRelease.Status.Targets {
		if gotRelease.Status.Targets[i].Target == "halt-env1" {
			failedTarget = &gotRelease.Status.Targets[i]
			break
		}
	}
	if failedTarget == nil {
		t.Fatal("halt-env1 not found in release.Status.Targets")
	}
	if failedTarget.Phase != kaprov1alpha1.SyncPhaseFailed {
		t.Errorf("halt policy: trigger target phase changed unexpectedly to %q", failedTarget.Phase)
	}
}

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
// regression test for the early-return bug in handleEnvMetricsCheck.
//
// The bug: when GatePolicySpec.Gate.Metrics was empty, handleEnvMetricsCheck
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
		Spec:       kaprov1alpha1.MemberClusterSpec{Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"}},
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

	// Pre-seed the Release with the env already in MetricsCheck so the
	// first reconcile exercises handleEnvMetricsCheck directly.
	release := &kaprov1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{
			Name:       releaseName,
			Namespace:  "default",
			Finalizers: []string{"kapro.io/release-cleanup"},
		},
		Spec: kaprov1alpha1.ReleaseSpec{
			Artifact: "some-art",
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
			Targets: []kaprov1alpha1.TargetStatus{
				{
					ReleaseRef:  releaseName,
					Target:      envRefName,
					PipelineRef: pipelineRef,
					Pipeline:    pipelineName,
					Stage:       stageName,
					Version:     "v1.0.0",
					Phase:       kaprov1alpha1.SyncPhaseMetricsCheck,
					Gate:        gatePolicy,
					AppKey:      "some-art",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Release{}).
		WithObjects(mc, pipeline, release).
		Build()

	gateReg := gate.NewRegistry()
	gateReg.MustRegister("mock", &alwaysPassGate{})

	r := &controller.ReleaseReconciler{
		Client:       c,
		Recorder:     record.NewFakeRecorder(100),
		Scheme:       scheme,
		GateRegistry: gateReg,
		// No MetricsGate wired — policy has no metrics, so it is never called.
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: releaseName},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}

	var gotRelease kaprov1alpha1.Release
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "default", Name: releaseName,
	}, &gotRelease); err != nil {
		t.Fatalf("Get Release: %v", err)
	}

	if len(gotRelease.Status.Targets) == 0 {
		t.Fatal("no target entries in updated Release status")
	}
	target := gotRelease.Status.Targets[0]

	// The env must have left MetricsCheck. If the bug is present it stays in
	// MetricsCheck because the GateTemplate is never evaluated.
	if target.Phase == kaprov1alpha1.SyncPhaseMetricsCheck {
		t.Fatal("GateTemplate was not evaluated: target is still in MetricsCheck after reconcile. " +
			"Bug: handleEnvMetricsCheck returned early when Metrics[] is empty, skipping Templates.")
	}

	// With no approval required the env advances from MetricsCheck to Applying.
	if target.Phase != kaprov1alpha1.SyncPhaseApplying {
		t.Errorf("expected target phase=Applying after template passed, got %s", target.Phase)
	}

	// The gate run status must be recorded in the env.
	if len(target.Gates) == 0 {
		t.Error("expected target.Gates to be populated after GateTemplate evaluation")
	} else if target.Gates[0].Phase != kaprov1alpha1.GatePhasePassed {
		t.Errorf("expected gate status Passed, got %s", target.Gates[0].Phase)
	}
}

// TestReleaseReconciler_ReleasesForNewMatchingCluster verifies that a newly
// registered cluster still wakes an in-progress Release even before that cluster
// has an inline TargetStatus entry in Release.status.targets.
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
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
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
			Artifact: "bundle",
			Pipelines: []kaprov1alpha1.ReleasePipelineRef{{
				Name:     "main",
				Pipeline: pipelineName,
			}},
		},
		Status: kaprov1alpha1.ReleaseStatus{
			Phase: kaprov1alpha1.ReleasePhaseProgressing,
			// No status.targets entry yet for the new cluster.
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
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
