package controller

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// heartbeatTestScheme builds the scheme the reconciler needs for its watches.
// Separate from controllerTestScheme so we don't pull broader deps into this
// file's fakes.
func heartbeatTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("clientgoscheme: %v", err)
	}
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("kapro scheme: %v", err)
	}
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatalf("coordination scheme: %v", err)
	}
	return s
}

// newReconciler wires a reconciler with an injected clock so freshness checks
// are deterministic across CI runs.
func newReconciler(t *testing.T, now time.Time, objs ...client.Object) *FleetClusterHeartbeatReconciler {
	t.Helper()
	scheme := heartbeatTestScheme(t)
	return &FleetClusterHeartbeatReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(objs...).
			WithStatusSubresource(&kaprov1alpha1.FleetCluster{}).
			Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(32),
		Now:      func() time.Time { return now },
	}
}

// boostrapUsed returns a registered FleetCluster (status.bootstrap.used=true)
// so the reconciler doesn't short-circuit to ReasonNotRegistered.
func bootstrapUsedFleetCluster(name string, threshold int32) *kaprov1alpha1.FleetCluster {
	t := threshold
	return &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery:                    kaprov1alpha1.DeliverySpec{Mode: kaprov1alpha1.DeliveryModePull, BackendRef: "flux"},
			ConsecutiveFailureThreshold: &t,
		},
		Status: kaprov1alpha1.FleetClusterStatus{
			Bootstrap: &kaprov1alpha1.FleetClusterBootstrapStatus{Used: true},
		},
	}
}

func freshLease(name string, namespace string, observed time.Time) *coordinationv1.Lease {
	micro := metav1.NewMicroTime(observed)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      heartbeatLeaseName(name),
			Namespace: namespace,
		},
		Spec: coordinationv1.LeaseSpec{RenewTime: &micro},
	}
}

func readyCondition(t *testing.T, c client.Client, name string) *metav1.Condition {
	t.Helper()
	fc := &kaprov1alpha1.FleetCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, fc); err != nil {
		t.Fatalf("get FleetCluster: %v", err)
	}
	return apimeta.FindStatusCondition(fc.Status.Conditions, kaprov1alpha1.ConditionTypeReady)
}

func TestHeartbeat_FreshLease_ReadyTrue(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 3)
	lease := freshLease("cluster-a", defaultHeartbeatNamespace, now.Add(-10*time.Second))
	r := newReconciler(t, now, fc, lease)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kaprov1alpha1.ReasonHeartbeatFresh {
		t.Fatalf("expected Ready=True reason=HeartbeatFresh, got %+v", cond)
	}
}

func TestHeartbeat_MissingLease_AccumulatesUntilThreshold(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 3)
	r := newReconciler(t, now, fc)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}

	// Misses 1 and 2: Ready=Unknown reason=HeartbeatStale.
	for i := 1; i <= 2; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
		cond := readyCondition(t, r.Client, "cluster-a")
		if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != kaprov1alpha1.ReasonHeartbeatStale {
			t.Fatalf("miss %d: expected Ready=Unknown reason=HeartbeatStale, got %+v", i, cond)
		}
	}
	// Miss 3 hits the threshold: Ready=False reason=Unreachable.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile 3: %v", err)
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != kaprov1alpha1.ReasonUnreachable {
		t.Fatalf("miss 3: expected Ready=False reason=Unreachable, got %+v", cond)
	}
}

func TestHeartbeat_Recovery_FlipsImmediately(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 3)
	// Pre-seed status as Unreachable with 3 misses to simulate a cluster that's been down.
	transition := metav1.NewTime(now.Add(-time.Hour))
	fc.Status.Conditions = []metav1.Condition{{
		Type: kaprov1alpha1.ConditionTypeReady, Status: metav1.ConditionFalse,
		Reason: kaprov1alpha1.ReasonUnreachable, LastTransitionTime: transition,
	}}
	fc.Status.Heartbeat = &kaprov1alpha1.FleetClusterHeartbeatStatus{
		ConsecutiveMisses: 5, Reason: kaprov1alpha1.ReasonUnreachable,
	}
	lease := freshLease("cluster-a", defaultHeartbeatNamespace, now.Add(-5*time.Second))
	r := newReconciler(t, now, fc, lease)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kaprov1alpha1.ReasonHeartbeatFresh {
		t.Fatalf("expected immediate flip to Ready=True, got %+v", cond)
	}
	// Misses reset.
	fcGot := &kaprov1alpha1.FleetCluster{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: "cluster-a"}, fcGot)
	if fcGot.Status.Heartbeat == nil || fcGot.Status.Heartbeat.ConsecutiveMisses != 0 {
		t.Fatalf("expected misses reset to 0, got %+v", fcGot.Status.Heartbeat)
	}
}

func TestHeartbeat_Suspended_NoMissAccumulation(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 3)
	fc.Spec.Suspend = true
	r := newReconciler(t, now, fc) // no Lease — would be a miss if not suspended
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}

	// Reconcile 5 times. Misses should stay 0; reason stays Suspended.
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != kaprov1alpha1.ReasonSuspended {
		t.Fatalf("expected Ready=Unknown reason=Suspended, got %+v", cond)
	}
	fcGot := &kaprov1alpha1.FleetCluster{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: "cluster-a"}, fcGot)
	if fcGot.Status.Heartbeat == nil || fcGot.Status.Heartbeat.ConsecutiveMisses != 0 {
		t.Fatalf("suspended cluster should not accumulate misses, got %+v", fcGot.Status.Heartbeat)
	}
}

func TestHeartbeat_PushMode_AlwaysReady(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 3)
	fc.Spec.Delivery.Mode = kaprov1alpha1.DeliveryModePush
	r := newReconciler(t, now, fc) // no Lease

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kaprov1alpha1.ReasonPushModeNoHeartbeat {
		t.Fatalf("expected Ready=True reason=PushModeNoHeartbeat, got %+v", cond)
	}
}

// TestHeartbeat_NotYetRegistered exercises the bootstrap-aware NotRegistered
// path: spec.bootstrap is set, status.bootstrap.used is false → the CSR
// bootstrap workflow hasn't completed yet.
func TestHeartbeat_NotYetRegistered(t *testing.T) {
	now := time.Now()
	tokenTTL := "1h"
	fc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery:  kaprov1alpha1.DeliverySpec{Mode: kaprov1alpha1.DeliveryModePull, BackendRef: "flux"},
			Bootstrap: &kaprov1alpha1.FleetClusterBootstrapSpec{TTL: tokenTTL},
		},
		// No Status.Bootstrap.Used — bootstrap workflow not yet complete.
	}
	r := newReconciler(t, now, fc)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != kaprov1alpha1.ReasonNotRegistered {
		t.Fatalf("expected Ready=Unknown reason=NotRegistered, got %+v", cond)
	}
}

// TestHeartbeat_LegacyClusterNoBootstrap_LeaseEstablishesReady covers the
// path that broke before the spec.bootstrap gate: a pull-mode FleetCluster
// created without the CSR bootstrap workflow (e.g. via legacy `kapro spoke
// add`, manual kubectl apply, or FleetClusterTemplate auto-import) MUST
// reach Ready=True purely on the strength of a fresh heartbeat Lease.
// Otherwise requireFreshHeartbeat would defer their promotions indefinitely.
func TestHeartbeat_LegacyClusterNoBootstrap_LeaseEstablishesReady(t *testing.T) {
	now := time.Now()
	fc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-cluster"},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery: kaprov1alpha1.DeliverySpec{Mode: kaprov1alpha1.DeliveryModePull, BackendRef: "flux"},
			// spec.Bootstrap intentionally nil — legacy / non-bootstrap path.
		},
	}
	lease := freshLease("legacy-cluster", defaultHeartbeatNamespace, now.Add(-5*time.Second))
	r := newReconciler(t, now, fc, lease)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "legacy-cluster"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cond := readyCondition(t, r.Client, "legacy-cluster")
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != kaprov1alpha1.ReasonHeartbeatFresh {
		t.Fatalf("legacy cluster with fresh Lease should reach Ready=True/HeartbeatFresh; got %+v", cond)
	}
}

// TestHeartbeat_LegacyClusterNoBootstrap_MissingLeaseCountsAsMiss confirms the
// symmetric case: a legacy cluster with no Lease still goes through the normal
// miss-counter path (Stale → Unreachable) rather than being held in
// NotRegistered limbo forever.
func TestHeartbeat_LegacyClusterNoBootstrap_MissingLeaseCountsAsMiss(t *testing.T) {
	now := time.Now()
	threshold := int32(1)
	fc := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-cluster"},
		Spec: kaprov1alpha1.FleetClusterSpec{
			Delivery:                    kaprov1alpha1.DeliverySpec{Mode: kaprov1alpha1.DeliveryModePull, BackendRef: "flux"},
			ConsecutiveFailureThreshold: &threshold,
		},
	}
	r := newReconciler(t, now, fc)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "legacy-cluster"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cond := readyCondition(t, r.Client, "legacy-cluster")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != kaprov1alpha1.ReasonUnreachable {
		t.Fatalf("legacy cluster with threshold=1 + no Lease should hit Unreachable, not NotRegistered; got %+v", cond)
	}
}

// TestHeartbeat_StaleLeaseCountedAsMiss verifies that a Lease that exists but
// was last renewed before the freshness window is treated as a miss (the
// reconciler doesn't trust a stale renewal time).
func TestHeartbeat_StaleLeaseCountedAsMiss(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 1) // threshold=1 so we hit Unreachable in one tick
	stale := freshLease("cluster-a", defaultHeartbeatNamespace, now.Add(-30*time.Minute))
	r := newReconciler(t, now, fc, stale)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != kaprov1alpha1.ReasonUnreachable {
		t.Fatalf("expected Ready=False reason=Unreachable, got %+v", cond)
	}
}

// TestHeartbeat_DefaultThresholdWhenSpecNil exercises the default-of-3 fallback
// when spec.consecutiveFailureThreshold is nil. Belt-and-braces against an
// older API revision that lacks the field.
func TestHeartbeat_DefaultThresholdWhenSpecNil(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 0)
	fc.Spec.ConsecutiveFailureThreshold = nil
	r := newReconciler(t, now, fc)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}

	// Reconcile twice — should still be below default threshold of 3.
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	cond := readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != kaprov1alpha1.ReasonHeartbeatStale {
		t.Fatalf("after 2 misses with default threshold=3, expected Stale; got %+v", cond)
	}
	// Third miss hits the default threshold.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile 3: %v", err)
	}
	cond = readyCondition(t, r.Client, "cluster-a")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != kaprov1alpha1.ReasonUnreachable {
		t.Fatalf("expected Ready=False reason=Unreachable after 3 misses, got %+v", cond)
	}
}

func TestLeaseToFleetCluster_MapsHeartbeatPrefix(t *testing.T) {
	mapFn := leaseToFleetCluster(defaultHeartbeatNamespace)
	cases := []struct {
		ns, name string
		want     string // empty means no enqueue
	}{
		{defaultHeartbeatNamespace, "kapro-heartbeat-de-prod", "de-prod"},
		{defaultHeartbeatNamespace, "kapro-heartbeat-cluster-a", "cluster-a"},
		{"other-ns", "kapro-heartbeat-foo", ""},             // wrong namespace
		{defaultHeartbeatNamespace, "some-other-lease", ""}, // wrong prefix
		{defaultHeartbeatNamespace, "kapro-heartbeat-", ""}, // empty cluster name
	}
	for _, c := range cases {
		lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: c.ns, Name: c.name}}
		got := mapFn(context.Background(), lease)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s/%s: expected no enqueue, got %v", c.ns, c.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Name != c.want {
			t.Errorf("%s/%s: expected enqueue of %s, got %v", c.ns, c.name, c.want, got)
		}
	}
}

// TestSpecPredicate_StatusOnlyUpdatesIgnored ensures we don't feedback-loop
// on our own status patches. Only generation changes or status.bootstrap.used
// transitions should trigger reconcile.
func TestSpecPredicate_StatusOnlyUpdatesIgnored(t *testing.T) {
	pred := fleetClusterSpecOrBootstrapPredicate{}
	oldFC := &kaprov1alpha1.FleetCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Generation: 1},
		Status: kaprov1alpha1.FleetClusterStatus{
			Bootstrap: &kaprov1alpha1.FleetClusterBootstrapStatus{Used: true},
			Conditions: []metav1.Condition{{
				Type: kaprov1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
			}},
		},
	}
	newFC := oldFC.DeepCopy()
	newFC.Status.Conditions[0].Status = metav1.ConditionFalse // status-only change
	if pred.Update(event.UpdateEvent{ObjectOld: oldFC, ObjectNew: newFC}) {
		t.Fatal("expected status-only update to be filtered out")
	}
	// Bootstrap.Used flip MUST trigger reconcile.
	newFC2 := oldFC.DeepCopy()
	newFC2.Status.Bootstrap.Used = false
	if !pred.Update(event.UpdateEvent{ObjectOld: oldFC, ObjectNew: newFC2}) {
		t.Fatal("expected Bootstrap.Used flip to trigger reconcile")
	}
	// Generation change MUST trigger reconcile (spec edit, e.g. Suspend toggle).
	newFC3 := oldFC.DeepCopy()
	newFC3.Generation = 2
	if !pred.Update(event.UpdateEvent{ObjectOld: oldFC, ObjectNew: newFC3}) {
		t.Fatal("expected generation change to trigger reconcile")
	}
}

// TestApplyDesired_LeaseObservedAtMatchesLeaseSpec exercises the read path so
// dashboards that surface status.heartbeat.leaseObservedAt see the actual
// Lease renewal time, not the reconciler's clock.
func TestApplyDesired_LeaseObservedAtMatchesLeaseSpec(t *testing.T) {
	now := time.Now()
	leaseRenewed := now.Add(-15 * time.Second)
	fc := bootstrapUsedFleetCluster("cluster-a", 3)
	lease := freshLease("cluster-a", defaultHeartbeatNamespace, leaseRenewed)
	r := newReconciler(t, now, fc, lease)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	fcGot := &kaprov1alpha1.FleetCluster{}
	_ = r.Get(context.Background(), types.NamespacedName{Name: "cluster-a"}, fcGot)
	if fcGot.Status.Heartbeat == nil || fcGot.Status.Heartbeat.LeaseObservedAt == nil {
		t.Fatalf("expected LeaseObservedAt to be set, got %+v", fcGot.Status.Heartbeat)
	}
	// metav1.Time precision is seconds — compare with second-level tolerance.
	delta := fcGot.Status.Heartbeat.LeaseObservedAt.Time.Sub(leaseRenewed).Abs()
	if delta > time.Second {
		t.Fatalf("LeaseObservedAt drifted from Lease.renewTime by %s", delta)
	}
}

// TestReconcile_EmitsEventOnUnreachableTransition exercises the event hookup.
// Uses fake recorder's channel to assert the event payload.
func TestReconcile_EmitsEventOnUnreachableTransition(t *testing.T) {
	now := time.Now()
	fc := bootstrapUsedFleetCluster("cluster-a", 1) // immediate Unreachable on first miss
	r := newReconciler(t, now, fc)
	rec := r.Recorder.(*record.FakeRecorder)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster-a"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	select {
	case evt := <-rec.Events:
		if evt == "" {
			t.Fatal("expected non-empty event")
		}
		if !contains(evt, eventReasonClusterUnreachable) {
			t.Fatalf("expected event %q in %q", eventReasonClusterUnreachable, evt)
		}
		if !contains(evt, corev1.EventTypeWarning) {
			t.Fatalf("expected Warning event type in %q", evt)
		}
	default:
		t.Fatal("expected event to be recorded")
	}
}
