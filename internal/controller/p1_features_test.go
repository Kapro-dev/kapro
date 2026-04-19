package controller_test

// p1_features_test.go — unit tests for the three P1 feature additions:
//
//   p1-halt-policy    TestReleaseReconciler_HaltPolicy_CancelsSiblingSync
//   p1-gate-template  TestSyncReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics
//   p1-auto-rollback  already covered by TestSyncReconciler_OnFailureRollback_CreatesRollbackPromotion
//                     and TestSyncReconciler_TriggerRollback_Idempotent in sync_fsm_test.go
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/controller"
	"kapro.io/kapro/pkg/gate"
)

// ---- p1-halt-policy ---------------------------------------------------------

// TestReleaseReconciler_HaltPolicy_CancelsSiblingSync verifies that when one
// Sync in a stage fails and the stage uses the default failurePolicy (halt),
// all non-terminal sibling Syncs in that stage are immediately cancelled
// (transitioned to Failed) so they do not keep running.
//
// Before this fix, cancelPendingStageSyncs was absent — in-flight Syncs would
// continue consuming cluster resources after the stage decision was made.
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

	// Two environments that match the stage selector.
	env1 := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-env1", Labels: map[string]string{"tier": "halt"}},
		Spec:       kaprov1alpha1.EnvironmentSpec{Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"}},
	}
	env2 := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "halt-env2", Labels: map[string]string{"tier": "halt"}},
		Spec:       kaprov1alpha1.EnvironmentSpec{Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"}},
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
		},
	}

	// Sync for env1 — already Failed (the halt trigger).
	syncFailed := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{
			Name:       releaseName + "-" + pipelineRef + "-" + stageName + "-halt-env1",
			Namespace:  "default",
			Finalizers: []string{"kapro.io/sync-cleanup"},
			Labels: map[string]string{
				"kapro.io/release":      releaseName,
				"kapro.io/pipeline-ref": pipelineRef,
				"kapro.io/stage":        stageName,
				"kapro.io/environment":  env1.Name,
			},
		},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     releaseName,
			EnvironmentRef: env1.Name,
			Pipeline:       pipelineName,
			Stage:          stageName,
			Version:        "repo@sha256:abc",
		},
		Status: kaprov1alpha1.SyncStatus{Phase: kaprov1alpha1.SyncPhaseFailed},
	}

	// Sync for env2 — in-flight (Applying); must be cancelled by halt policy.
	syncApplying := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{
			Name:       releaseName + "-" + pipelineRef + "-" + stageName + "-halt-env2",
			Namespace:  "default",
			Finalizers: []string{"kapro.io/sync-cleanup"},
			Labels: map[string]string{
				"kapro.io/release":      releaseName,
				"kapro.io/pipeline-ref": pipelineRef,
				"kapro.io/stage":        stageName,
				"kapro.io/environment":  env2.Name,
			},
		},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     releaseName,
			EnvironmentRef: env2.Name,
			Pipeline:       pipelineName,
			Stage:          stageName,
			Version:        "repo@sha256:abc",
		},
		Status: kaprov1alpha1.SyncStatus{Phase: kaprov1alpha1.SyncPhaseApplying},
	}

	// Field indexer for IndexKeyRelease (required by handleProgressing and
	// clearActiveRelease which both list Syncs via MatchingFields).
	indexByRelease := func(obj client.Object) []string {
		if v, ok := obj.GetLabels()[controller.IndexKeyRelease]; ok {
			return []string{v}
		}
		return nil
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Sync{}, &kaprov1alpha1.Release{}).
		WithIndex(&kaprov1alpha1.Sync{}, controller.IndexKeyRelease, indexByRelease).
		WithObjects(env1, env2, pipeline, release, syncFailed, syncApplying).
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

	// 1. The in-flight sibling Sync must be Failed (not still Applying).
	var gotApplying kaprov1alpha1.Sync
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "default", Name: syncApplying.Name,
	}, &gotApplying); err != nil {
		t.Fatalf("Get sibling Sync: %v", err)
	}
	if gotApplying.Status.Phase != kaprov1alpha1.SyncPhaseFailed {
		t.Errorf("halt policy: expected sibling Sync to be Failed, got %q — "+
			"cancelPendingStageSyncs did not run", gotApplying.Status.Phase)
	}
	if gotApplying.Status.Message == "" {
		t.Error("halt policy: expected cancellation message on the cancelled Sync")
	}

	// 2. The Release itself must be Failed (halt stops the whole pipeline).
	var gotRelease kaprov1alpha1.Release
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "default", Name: releaseName,
	}, &gotRelease); err != nil {
		t.Fatalf("Get Release: %v", err)
	}
	if gotRelease.Status.Phase != kaprov1alpha1.ReleasePhaseFailed {
		t.Errorf("halt policy: expected Release to be Failed, got %q", gotRelease.Status.Phase)
	}

	// 3. The already-Failed Sync must be untouched (terminal states are never overwritten).
	var gotFailed kaprov1alpha1.Sync
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "default", Name: syncFailed.Name,
	}, &gotFailed); err != nil {
		t.Fatalf("Get trigger Sync: %v", err)
	}
	if gotFailed.Status.Phase != kaprov1alpha1.SyncPhaseFailed {
		t.Errorf("halt policy: trigger Sync phase changed unexpectedly to %q", gotFailed.Status.Phase)
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

// TestSyncReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics is a
// regression test for the early-return bug in handleMetricsCheck.
//
// The bug: when GatePolicy.spec.gate.metrics was empty, handleMetricsCheck
// returned immediately via the early-return guard, skipping GateTemplate
// evaluation entirely. A policy that configured only GateTemplates (and no
// Prometheus metrics) never had its templates run.
//
// The fix: metrics and templates are now evaluated under separate guards;
// the function only fast-paths when BOTH are empty.
func TestSyncReconciler_MetricsCheck_GateTemplatesEvaluatedWithoutMetrics(t *testing.T) {
	const tmplName = "mock-gate-template"

	// GateTemplate with type "mock" (resolved via GateRegistry, not built-in).
	tmpl := &kaprov1alpha1.GateTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: tmplName},
		Spec: kaprov1alpha1.GateTemplateSpec{
			// "mock" is not a built-in type; it's registered in the test registry below.
			// The kubebuilder enum validation (cel|job|webhook) is not enforced by
			// the fake client, so this is fine for unit testing.
			Type: "mock",
		},
	}

	// Policy: GateTemplates only — intentionally zero Prometheus metrics.
	// This is the exact scenario the bug prevented from working.
	policy := &kaprov1alpha1.GatePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl-only-policy"},
		Spec: kaprov1alpha1.GatePolicySpec{
			Mode: kaprov1alpha1.GateModeAuto,
			Gate: kaprov1alpha1.GateSpec{
				Templates: []kaprov1alpha1.GateTemplateRef{{Name: tmplName}},
				// Metrics field is intentionally absent — this triggered the bug.
			},
		},
	}

	// Sync already in MetricsCheck, referencing the templates-only policy.
	sync := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "tmpl-sync", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-tmpl",
			Version:        "v1.0.0",
			PolicyRef:      policy.Name,
		},
		Status: kaprov1alpha1.SyncStatus{Phase: kaprov1alpha1.SyncPhaseMetricsCheck},
	}

	// Use buildFSMClient so the finalizer is pre-injected; otherwise the first
	// reconcile burns a round-trip adding it and never reaches handleMetricsCheck.
	c := buildFSMClient(t, tmpl, policy, sync)

	// Wire a GateRegistry with our always-passing mock gate.
	reg := gate.NewRegistry()
	reg.MustRegister("mock", &alwaysPassGate{})

	r := &controller.SyncReconciler{
		Client:       c,
		Recorder:     record.NewFakeRecorder(100),
		GateRegistry: reg,
		// No MetricsGate wired — policy has no metrics, so this is never called.
		// No approval config — so after MetricsCheck the Sync goes directly to Applying.
	}

	// One reconcile: MetricsCheck with a passing GateTemplate → should advance.
	res := reconcilePromo(t, r, "default", "tmpl-sync")
	if !res.Requeue {
		t.Error("expected Requeue=true after phase transition out of MetricsCheck")
	}

	updated := getPromo(t, c, "default", "tmpl-sync")

	// The Sync must have left MetricsCheck.  If the bug is present it stays in
	// MetricsCheck because the GateTemplate is never evaluated.
	if updated.Status.Phase == kaprov1alpha1.SyncPhaseMetricsCheck {
		t.Fatal("GateTemplate was not evaluated: Sync is still in MetricsCheck after reconcile. " +
			"Bug: handleMetricsCheck returned early when Metrics[] is empty, skipping Templates.")
	}

	// With no approval required, the Sync advances directly from MetricsCheck
	// to Applying once all templates pass.
	if updated.Status.Phase != kaprov1alpha1.SyncPhaseApplying {
		t.Errorf("expected phase=Applying after template passed, got %s", updated.Status.Phase)
	}

	// The gate run status must be recorded in Sync.Status.Gates[].
	if len(updated.Status.Gates) == 0 {
		t.Error("expected Sync.Status.Gates to be populated after GateTemplate evaluation")
	} else if updated.Status.Gates[0].Phase != kaprov1alpha1.GatePhasePassed {
		t.Errorf("expected gate status Passed, got %s", updated.Status.Gates[0].Phase)
	}
}
