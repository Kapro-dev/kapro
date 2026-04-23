package controller_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/internal/controller"
)

// ---- helpers ----------------------------------------------------------------

// mockActuator satisfies actuator.Actuator and returns pre-configured results.
type mockActuator struct {
	applyErr     error
	converged    bool
	convergedErr error
}

func (m *mockActuator) Apply(_ context.Context, _ actuator.ApplyRequest) error {
	return m.applyErr
}
func (m *mockActuator) IsConverged(_ context.Context, _ *kaprov1alpha1.MemberCluster, _, _ string) (bool, error) {
	return m.converged, m.convergedErr
}
func (m *mockActuator) Rollback(_ context.Context, _ *kaprov1alpha1.MemberCluster, _ string) error {
	return m.applyErr
}

func fsmScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func buildFSMClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	// Pre-inject the promotion finalizer so tests don't burn a reconcile round-trip
	// adding it. Tests care about phase transitions, not finalizer mechanics.
	for _, o := range objs {
		if p, ok := o.(*kaprov1alpha1.Sync); ok {
			if !controllerutil.ContainsFinalizer(p, "kapro.io/sync-cleanup") {
				controllerutil.AddFinalizer(p, "kapro.io/sync-cleanup")
			}
		}
	}
	return fake.NewClientBuilder().
		WithScheme(fsmScheme(t)).
		WithStatusSubresource(&kaprov1alpha1.Sync{}, &kaprov1alpha1.MemberCluster{}).
		WithObjects(objs...).
		Build()
}

func newReconciler(c client.Client, act actuator.Actuator) *controller.SyncReconciler {
	reg := actuator.NewRegistry()
	if act != nil {
		if err := reg.Register("flux", act); err != nil {
			panic(err)
		}
	}
	return &controller.SyncReconciler{
		Client:           c,
		Recorder:         record.NewFakeRecorder(100),
		ActuatorRegistry: reg,
	}
}

func reconcilePromo(t *testing.T, r *controller.SyncReconciler, namespace, name string) ctrl.Result { //nolint:unparam // namespace is "default" in all current tests but kept for future flexibility
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: name},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	return res
}

func getPromo(t *testing.T, c client.Client, namespace, name string) kaprov1alpha1.Sync { //nolint:unparam // namespace is "default" in all current tests but kept for future flexibility
	t.Helper()
	var p kaprov1alpha1.Sync
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, &p); err != nil {
		t.Fatalf("Get Sync %s/%s: %v", namespace, name, err)
	}
	return p
}

// ---- tests ------------------------------------------------------------------

// TestSyncReconciler_EmptyPhase_TransitionsToPending verifies that a
// brand-new Promotion (no phase) gets transitioned to Pending on the first
// reconcile.
func TestSyncReconciler_EmptyPhase_TransitionsToPending(t *testing.T) {
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "new-promo", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-dev",
			Version:        "v1.0.0",
		},
	}
	c := buildFSMClient(t, promo)
	r := newReconciler(c, nil)

	res := reconcilePromo(t, r, "default", "new-promo")

	if !res.Requeue {
		t.Error("expected Requeue=true after phase transition")
	}
	updated := getPromo(t, c, "default", "new-promo")
	if updated.Status.Phase != kaprov1alpha1.SyncPhasePending {
		t.Errorf("expected phase=Pending, got %s", updated.Status.Phase)
	}
}

// TestSyncReconciler_Pending_NoRegistration_Requeues verifies that when
// no MemberCluster is found for the environment, the reconciler requeues
// without advancing the phase.
func TestSyncReconciler_Pending_NoRegistration_Requeues(t *testing.T) {
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-noreg", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-missing",
			Version:        "v1.0.0",
		},
		Status: kaprov1alpha1.SyncStatus{
			Phase: kaprov1alpha1.SyncPhasePending,
		},
	}
	c := buildFSMClient(t, promo)
	r := newReconciler(c, nil)

	res := reconcilePromo(t, r, "default", "promo-noreg")

	if res.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 when no MemberCluster found")
	}
	updated := getPromo(t, c, "default", "promo-noreg")
	if updated.Status.Phase != kaprov1alpha1.SyncPhasePending {
		t.Errorf("expected phase to stay Pending, got %s", updated.Status.Phase)
	}
}

// TestSyncReconciler_Pending_StaleHeartbeat_Requeues verifies that a
// stale cluster heartbeat keeps the promotion in Pending.
func TestSyncReconciler_Pending_StaleHeartbeat_Requeues(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "env-stale"},
		Status: kaprov1alpha1.MemberClusterStatus{
			LastHeartbeat: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-stale", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-stale",
			Version:        "v1.0.0",
		},
		Status: kaprov1alpha1.SyncStatus{Phase: kaprov1alpha1.SyncPhasePending},
	}
	c := buildFSMClient(t, mc, promo)
	r := newReconciler(c, nil)

	res := reconcilePromo(t, r, "default", "promo-stale")

	if res.RequeueAfter == 0 {
		t.Error("expected RequeueAfter > 0 for stale heartbeat")
	}
	updated := getPromo(t, c, "default", "promo-stale")
	if updated.Status.Phase != kaprov1alpha1.SyncPhasePending {
		t.Errorf("expected phase to stay Pending for stale heartbeat, got %s", updated.Status.Phase)
	}
}

// TestSyncReconciler_Pending_FreshHeartbeat_TransitionsToHealthCheck
// verifies that a Promotion with a fresh heartbeat advances from Pending to
// HealthCheck.
func TestSyncReconciler_Pending_FreshHeartbeat_TransitionsToHealthCheck(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "env-fresh"},
		Status: kaprov1alpha1.MemberClusterStatus{
			LastHeartbeat: time.Now().UTC().Format(time.RFC3339),
		},
	}
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-fresh", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-fresh",
			Version:        "v1.0.0",
		},
		Status: kaprov1alpha1.SyncStatus{Phase: kaprov1alpha1.SyncPhasePending},
	}
	c := buildFSMClient(t, mc, promo)
	r := newReconciler(c, nil)

	// First reconcile: Pending → Verification (VerificationGate is nil — skip immediately)
	reconcilePromo(t, r, "default", "promo-fresh")
	// Second reconcile: Verification → HealthCheck
	res := reconcilePromo(t, r, "default", "promo-fresh")

	if !res.Requeue {
		t.Error("expected Requeue=true after transition to HealthCheck")
	}
	updated := getPromo(t, c, "default", "promo-fresh")
	if updated.Status.Phase != kaprov1alpha1.SyncPhaseHealthCheck {
		t.Errorf("expected phase=HealthCheck, got %s", updated.Status.Phase)
	}
}

// TestSyncReconciler_Applying_ClusterConverged_SetsConvergedPhase verifies
// that when the cluster converges at the desired version, the Promotion moves to
// Converged.
func TestSyncReconciler_Applying_ClusterConverged_SetsConvergedPhase(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "env-conv"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
		},
		Status: kaprov1alpha1.MemberClusterStatus{
			Phase:           kaprov1alpha1.ClusterPhaseConverged,
			CurrentVersions: map[string]string{"rel-1": "v2.0.0"},
			LastHeartbeat:   time.Now().UTC().Format(time.RFC3339),
		},
	}
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-conv", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-conv",
			Version:        "v2.0.0",
		},
		Status: kaprov1alpha1.SyncStatus{
			Phase:           kaprov1alpha1.SyncPhaseApplying,
			PreviousVersion: "v1.0.0",
		},
	}
	c := buildFSMClient(t, mc, promo)
	r := newReconciler(c, &mockActuator{})

	reconcilePromo(t, r, "default", "promo-conv")

	updated := getPromo(t, c, "default", "promo-conv")
	if updated.Status.Phase != kaprov1alpha1.SyncPhaseConverged {
		t.Errorf("expected phase=Converged, got %s", updated.Status.Phase)
	}
	if updated.Status.FinishedAt == "" {
		t.Error("expected FinishedAt to be set on convergence")
	}
}

// TestSyncReconciler_OnFailureRollback_CreatesRollbackPromotion verifies
// that when a cluster reports Failed and the policy is onFailure=rollback, a
// new rollback Promotion is created targeting the previous version.
func TestSyncReconciler_OnFailureRollback_CreatesRollbackPromotion(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "env-rollback"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
		},
		Status: kaprov1alpha1.MemberClusterStatus{
			Phase: kaprov1alpha1.ClusterPhaseFailed,
		},
	}
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-fail", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-rollback",
			Version:        "v2.0.0",
			Gate: &kaprov1alpha1.GatePolicySpec{
				Mode:      kaprov1alpha1.GateModeAuto,
				OnFailure: "rollback",
			},
		},
		Status: kaprov1alpha1.SyncStatus{
			Phase:           kaprov1alpha1.SyncPhaseApplying,
			PreviousVersion: "v1.0.0",
		},
	}
	c := buildFSMClient(t, mc, promo)
	r := newReconciler(c, &mockActuator{})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "promo-fail"},
	})
	if err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	// Original promotion must be Failed.
	updated := getPromo(t, c, "default", "promo-fail")
	if updated.Status.Phase != kaprov1alpha1.SyncPhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}

	// A rollback Promotion must have been created.
	var rollback kaprov1alpha1.Sync
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "default", Name: "promo-fail-rollback",
	}, &rollback); err != nil {
		t.Fatalf("expected rollback Promotion to be created: %v", err)
	}
	if rollback.Spec.Version != "v1.0.0" {
		t.Errorf("expected rollback Version=v1.0.0, got %s", rollback.Spec.Version)
	}
	if rollback.Spec.EnvironmentRef != "env-rollback" {
		t.Errorf("expected rollback EnvironmentRef=env-rollback, got %s", rollback.Spec.EnvironmentRef)
	}
}

// TestSyncReconciler_TriggerRollback_Idempotent verifies that calling
// reconcile twice when a rollback is triggered does not create a second rollback
// Promotion.
func TestSyncReconciler_TriggerRollback_Idempotent(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "env-idem"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
		},
		Status: kaprov1alpha1.MemberClusterStatus{
			Phase: kaprov1alpha1.ClusterPhaseFailed,
		},
	}
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-idem", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-idem",
			Version:        "v2.0.0",
			Gate: &kaprov1alpha1.GatePolicySpec{
				Mode:      kaprov1alpha1.GateModeAuto,
				OnFailure: "rollback",
			},
		},
		Status: kaprov1alpha1.SyncStatus{
			Phase:           kaprov1alpha1.SyncPhaseApplying,
			PreviousVersion: "v1.0.0",
		},
	}
	c := buildFSMClient(t, mc, promo)
	r := newReconciler(c, &mockActuator{})

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "promo-idem"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first Reconcile error: %v", err)
	}

	// Second reconcile on the now-Failed promotion — must not create a duplicate.
	// (Phase is Failed, so reconciler returns immediately with no action.)
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second Reconcile error: %v", err)
	}

	// Count rollback Promotions.
	var list kaprov1alpha1.SyncList
	if err := c.List(context.Background(), &list, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	rollbackCount := 0
	for _, p := range list.Items {
		if p.Labels["kapro.io/rollback-for"] == "promo-idem" {
			rollbackCount++
		}
	}
	if rollbackCount != 1 {
		t.Errorf("expected exactly 1 rollback Promotion, got %d", rollbackCount)
	}
}

// TestSyncReconciler_FailPromotion_SetsFailedPhase_NoRollback verifies
// that when OnFailure is NOT "rollback", failPromotion just sets Failed phase
// without creating a rollback Promotion.
func TestSyncReconciler_FailPromotion_SetsFailedPhase_NoRollback(t *testing.T) {
	mc := &kaprov1alpha1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "env-halt"},
		Spec: kaprov1alpha1.MemberClusterSpec{
			Actuator: kaprov1alpha1.ActuatorSpec{Type: "flux"},
		},
		Status: kaprov1alpha1.MemberClusterStatus{
			Phase: kaprov1alpha1.ClusterPhaseFailed,
		},
	}
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-halt", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-halt",
			Version:        "v2.0.0",
			Gate: &kaprov1alpha1.GatePolicySpec{
				Mode:      kaprov1alpha1.GateModeAuto,
				OnFailure: "halt",
			},
		},
		Status: kaprov1alpha1.SyncStatus{
			Phase:           kaprov1alpha1.SyncPhaseApplying,
			PreviousVersion: "v1.0.0",
		},
	}
	c := buildFSMClient(t, mc, promo)
	r := newReconciler(c, &mockActuator{})

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "promo-halt"},
	}); err != nil {
		t.Fatalf("Reconcile error: %v", err)
	}

	updated := getPromo(t, c, "default", "promo-halt")
	if updated.Status.Phase != kaprov1alpha1.SyncPhaseFailed {
		t.Errorf("expected phase=Failed, got %s", updated.Status.Phase)
	}

	// No rollback Promotion should have been created.
	var rollback kaprov1alpha1.Sync
	err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "default", Name: "promo-halt-rollback",
	}, &rollback)
	if err == nil {
		t.Error("expected no rollback Promotion when OnFailure=halt")
	}
}

// TestSyncReconciler_Converged_IsNoop verifies that reconciling a
// Promotion already in Converged phase is a no-op (no further transitions).
func TestSyncReconciler_Converged_IsNoop(t *testing.T) {
	promo := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{Name: "promo-done", Namespace: "default"},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     "rel-1",
			EnvironmentRef: "env-done",
			Version:        "v1.0.0",
		},
		Status: kaprov1alpha1.SyncStatus{
			Phase: kaprov1alpha1.SyncPhaseConverged,
		},
	}
	c := buildFSMClient(t, promo)
	r := newReconciler(c, nil)

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "promo-done"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Error("expected no requeue for a terminal phase")
	}
	updated := getPromo(t, c, "default", "promo-done")
	if updated.Status.Phase != kaprov1alpha1.SyncPhaseConverged {
		t.Errorf("expected phase to remain Converged, got %s", updated.Status.Phase)
	}
}
