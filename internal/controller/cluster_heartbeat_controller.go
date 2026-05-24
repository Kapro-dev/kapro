// Package controller — FleetCluster heartbeat reconciler.
//
// Single writer of FleetCluster.status.heartbeat AND
// FleetCluster.status.conditions[Ready]. Does NOT write
// FleetCluster.status.phase (kapro_controller is the sole Phase writer; it
// reads conditions[Ready] and surfaces Phase=Unreachable when this reconciler
// has set Ready=False reason=Unreachable).
//
// Inputs:
//
//   - FleetCluster (primary watch). spec.consecutiveFailureThreshold is the
//     hysteresis knob (default 3); spec.suspend short-circuits to Unknown;
//     spec.delivery.mode=push short-circuits to Ready=True (no spoke agent,
//     no Lease — heartbeat is N/A).
//   - coordination.k8s.io/v1 Lease in HeartbeatNamespace (secondary watch,
//     mapped back to the owning FleetCluster by name prefix). Spoke renews
//     this Lease on its heartbeat interval (default 30s).
//
// Reconcile state machine (per cluster, per reconcile):
//
//	┌─────────┐   suspend       ┌────────────────┐
//	│  Spec   │────────────────▶│ Ready=Unknown  │
//	│ inputs  │                 │ reason=Suspended│
//	└────┬────┘                 └────────────────┘
//	     │ push mode             ┌────────────────────────┐
//	     ├──────────────────────▶│ Ready=True             │
//	     │                       │ reason=PushModeNoHB    │
//	     │                       └────────────────────────┘
//	     │ not yet registered    ┌────────────────────────┐
//	     ├──────────────────────▶│ Ready=Unknown          │
//	     │                       │ reason=NotRegistered   │
//	     │                       └────────────────────────┘
//	     │
//	     ▼
//	┌──────────────┐ fresh   ┌─────────────────────────┐
//	│ Read Lease   │────────▶│ Ready=True              │
//	│              │         │ reason=HeartbeatFresh   │
//	└──────┬───────┘         │ misses ← 0              │
//	       │ stale           └─────────────────────────┘
//	       ▼
//	┌──────────────────────────┐
//	│ misses++                 │
//	│ if misses ≥ threshold:   │
//	│   Ready=False            │
//	│   reason=Unreachable     │
//	│ else:                    │
//	│   Ready=Unknown          │
//	│   reason=HeartbeatStale  │
//	└──────────────────────────┘
//
// Recovery: any fresh observation snaps misses to 0 and flips Ready=True
// immediately. There is no recovery hysteresis — first fresh tick wins. This
// is intentional: the threshold provides hysteresis on failure (a single
// missed Lease renewal does not flap Ready), but a recovering cluster should
// be allowed to receive promotions as fast as we can observe it.
//
// Cadence: reconcile re-queues at HeartbeatFreshTimeout / 2 by default so a
// freshly-renewed Lease cannot age past the threshold between Lease watch
// events.
package controller

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/metrics"
)

const (
	// defaultConsecutiveFailureThreshold matches the CRD default; the
	// admission default kicks in when the spec field is set, but a status
	// reconcile against an older API revision could see nil. Treat nil as 3.
	defaultConsecutiveFailureThreshold int32 = 3

	// heartbeatReconcileRequeue is the requeue cadence when nothing has
	// changed. Half the freshness window so a Lease can't age into stale
	// territory between watch events.
	heartbeatReconcileRequeue = heartbeatFreshTimeout / 2

	// eventReasonHeartbeatStale fires when misses increment but threshold
	// hasn't been reached yet (Ready=Unknown).
	eventReasonHeartbeatStale = "HeartbeatStale"
	// eventReasonHeartbeatRecovered fires when a stale cluster goes fresh
	// without crossing the Unreachable threshold.
	eventReasonHeartbeatRecovered = "HeartbeatRecovered"
	// eventReasonClusterUnreachable fires when Ready transitions to False
	// (misses crossed the threshold).
	eventReasonClusterUnreachable = "ClusterUnreachable"
	// eventReasonClusterRecovered fires when Ready transitions from False
	// back to True. Mirrors eventReasonHeartbeatRecovered but at the
	// Unreachable→Ready edge specifically.
	eventReasonClusterRecovered = "ClusterRecovered"
)

// ClusterHeartbeatReconciler watches FleetClusters and their associated
// coordination Leases to maintain the Ready condition + heartbeat status
// substruct.
type ClusterHeartbeatReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// HeartbeatNamespace is where the spoke writes its Lease. Defaults to
	// kapro-system if empty.
	HeartbeatNamespace string

	// Now lets tests inject a fake clock. nil means time.Now.
	Now func() time.Time
}

// Reconcile implements reconcile.Reconciler.
func (r *ClusterHeartbeatReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("fleetcluster", req.Name)

	fc := &kaprov1alpha1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, fc); err != nil {
		if apierrors.IsNotFound(err) {
			// FleetCluster gone — drop any orphan metric series.
			metrics.FleetClusterHeartbeatMisses.DeleteLabelValues(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get FleetCluster %q: %w", req.Name, err)
	}
	if !fc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	now := r.now()
	desired := r.computeDesiredReady(ctx, fc, now)
	if err := r.applyDesired(ctx, fc, desired, now); err != nil {
		logger.Error(err, "apply heartbeat status")
		return ctrl.Result{RequeueAfter: heartbeatReconcileRequeue}, err
	}
	return ctrl.Result{RequeueAfter: heartbeatReconcileRequeue}, nil
}

// desiredReady is the bag of derived values one reconcile pass produces. It
// is computed first, then applied transactionally to the FleetCluster status.
// Keeping compute and apply separate makes the state machine unit-testable
// without a Kubernetes client.
type desiredReady struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
	// Misses is the new consecutive miss count to persist.
	Misses int32
	// LeaseObservedAt is the timestamp the reconciler extracted from the
	// Lease. Nil when no Lease exists (or when push mode skipped the check).
	LeaseObservedAt *metav1.Time
}

func (r *ClusterHeartbeatReconciler) computeDesiredReady(ctx context.Context, fc *kaprov1alpha1.Cluster, now time.Time) desiredReady {
	// Suspend wins. Operators have asked us to stop reasoning about this
	// cluster's reachability.
	if fc.Spec.Suspend {
		return desiredReady{
			Status:  metav1.ConditionUnknown,
			Reason:  kaprov1alpha1.ReasonSuspended,
			Message: "FleetCluster is suspended; heartbeat tracking disabled",
		}
	}

	// Push mode has no spoke agent and no Lease. Reachability is whatever
	// the hub-side adapter says — we don't override.
	if fc.Spec.Delivery.Mode == kaprov1alpha1.DeliveryModePush {
		return desiredReady{
			Status:  metav1.ConditionTrue,
			Reason:  kaprov1alpha1.ReasonPushModeNoHeartbeat,
			Message: "push-mode cluster — heartbeat not applicable",
		}
	}

	// NotRegistered applies ONLY to clusters that opted into the CSR bootstrap
	// workflow (spec.bootstrap is set on creation, typically by
	// `kapro spoke bootstrap`). For those, "registered" means the CSR
	// exchange has completed and status.bootstrap.used is true.
	//
	// FleetClusters created without spec.bootstrap — e.g. legacy push-mode
	// objects from `kapro spoke add`, manually-created pull-mode clusters,
	// or auto-imported ones from FleetClusterTemplate — never go through
	// the bootstrap workflow. For those we fall through to Lease-based
	// reachability: a fresh heartbeat Lease is the registration signal.
	//
	// Distinguishing NotRegistered from Unreachable matters for dashboards:
	// NotRegistered is a Day-0 problem (chart not installed yet);
	// Unreachable is a Day-1+ problem (cluster down).
	if fc.Spec.Bootstrap != nil && (fc.Status.Bootstrap == nil || !fc.Status.Bootstrap.Used) {
		return desiredReady{
			Status:  metav1.ConditionUnknown,
			Reason:  kaprov1alpha1.ReasonNotRegistered,
			Message: "FleetCluster has spec.bootstrap set but bootstrap workflow has not yet completed",
		}
	}

	threshold := defaultConsecutiveFailureThreshold
	if fc.Spec.ConsecutiveFailureThreshold != nil && *fc.Spec.ConsecutiveFailureThreshold > 0 {
		threshold = *fc.Spec.ConsecutiveFailureThreshold
	}

	leaseObserved, freshness, err := r.readLeaseFreshness(ctx, fc, now)
	if err != nil {
		// Transient API error. Treat as a soft miss but don't increment —
		// we couldn't make an observation. Re-queue and try again. We do
		// NOT flip Ready on a single API error.
		return desiredReady{
			Status:  metav1.ConditionUnknown,
			Reason:  kaprov1alpha1.ReasonHeartbeatStale,
			Message: fmt.Sprintf("error reading heartbeat Lease: %v", err),
			Misses:  currentMisses(fc),
		}
	}
	if freshness == freshnessFresh {
		return desiredReady{
			Status:          metav1.ConditionTrue,
			Reason:          kaprov1alpha1.ReasonHeartbeatFresh,
			Message:         fmt.Sprintf("Lease renewed %s ago", now.Sub(leaseObserved.Time).Round(time.Second)),
			LeaseObservedAt: leaseObserved,
			Misses:          0,
		}
	}

	newMisses := currentMisses(fc) + 1
	if newMisses >= threshold {
		var msg string
		if leaseObserved == nil {
			msg = fmt.Sprintf("missing heartbeat Lease %s/%s; %d/%d consecutive misses",
				r.heartbeatNamespace(), heartbeatLeaseName(fc.Name), newMisses, threshold)
		} else {
			msg = fmt.Sprintf("Lease %s/%s last renewed %s ago; %d/%d consecutive misses",
				r.heartbeatNamespace(), heartbeatLeaseName(fc.Name),
				now.Sub(leaseObserved.Time).Round(time.Second), newMisses, threshold)
		}
		return desiredReady{
			Status:          metav1.ConditionFalse,
			Reason:          kaprov1alpha1.ReasonUnreachable,
			Message:         msg,
			LeaseObservedAt: leaseObserved,
			Misses:          newMisses,
		}
	}
	var msg string
	if leaseObserved == nil {
		msg = fmt.Sprintf("missing heartbeat Lease %s/%s; %d/%d consecutive misses (below threshold)",
			r.heartbeatNamespace(), heartbeatLeaseName(fc.Name), newMisses, threshold)
	} else {
		msg = fmt.Sprintf("Lease %s/%s last renewed %s ago; %d/%d consecutive misses (below threshold)",
			r.heartbeatNamespace(), heartbeatLeaseName(fc.Name),
			now.Sub(leaseObserved.Time).Round(time.Second), newMisses, threshold)
	}
	return desiredReady{
		Status:          metav1.ConditionUnknown,
		Reason:          kaprov1alpha1.ReasonHeartbeatStale,
		Message:         msg,
		LeaseObservedAt: leaseObserved,
		Misses:          newMisses,
	}
}

type freshnessVerdict int

const (
	freshnessFresh freshnessVerdict = iota
	freshnessStale
	freshnessMissingLease
)

// readLeaseFreshness reads the cluster's coordination Lease and returns the
// extracted observation timestamp (nil if no Lease) plus a freshness verdict.
// An apiserver error (not NotFound) is returned to the caller so it can
// decide whether to count it as a miss.
func (r *ClusterHeartbeatReconciler) readLeaseFreshness(ctx context.Context, fc *kaprov1alpha1.Cluster, now time.Time) (*metav1.Time, freshnessVerdict, error) {
	lease := &coordinationv1.Lease{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: r.heartbeatNamespace(),
		Name:      heartbeatLeaseName(fc.Name),
	}, lease)
	if apierrors.IsNotFound(err) {
		return nil, freshnessMissingLease, nil
	}
	if err != nil {
		return nil, freshnessMissingLease, err
	}
	observed, ok := leaseHeartbeatTime(lease)
	if !ok {
		return nil, freshnessStale, nil
	}
	mt := metav1.NewTime(observed)
	if now.Sub(observed) < heartbeatFreshTimeout {
		return &mt, freshnessFresh, nil
	}
	return &mt, freshnessStale, nil
}

// applyDesired patches the FleetCluster status to match the computed desired
// state, emits transition events, and updates Prometheus gauges/counters.
// Idempotent: a reconcile pass that produces no transitions still updates
// status.heartbeat.observedAt so operators can confirm the reconciler is
// alive.
//
// Concurrency note: FleetCluster status is written by multiple reconcilers
// (this one for conditions[Ready] + heartbeat; kapro_controller for phase +
// versions). They use client.MergeFrom + Status().Patch — the same pattern as
// the rest of the codebase. Concurrent patches against the same Conditions
// slice can race: a write from kapro_controller landing between our Get and
// Patch would be reverted in the slice we send. The system is eventually
// consistent because each reconciler re-runs (this one at
// heartbeatReconcileRequeue cadence, ~1m) and re-asserts its condition. A
// future project-wide migration to server-side apply would eliminate the
// window. Acceptable for preview — the window is bounded by the requeue
// interval, transitions emit metrics + events on every flip, and a momentary
// stale condition does not affect promotion behavior because Phase
// (not Ready) is the gate kapro_controller and the promotion target
// controller read.
func (r *ClusterHeartbeatReconciler) applyDesired(ctx context.Context, fc *kaprov1alpha1.Cluster, desired desiredReady, now time.Time) error {
	before := fc.DeepCopy()

	prev := apimeta.FindStatusCondition(fc.Status.Conditions, kaprov1alpha1.ConditionTypeReady)
	prevStatus := metav1.ConditionUnknown
	prevReason := ""
	if prev != nil {
		prevStatus = prev.Status
		prevReason = prev.Reason
	}

	nowMeta := metav1.NewTime(now)
	cond := metav1.Condition{
		Type:               kaprov1alpha1.ConditionTypeReady,
		Status:             desired.Status,
		Reason:             desired.Reason,
		Message:            desired.Message,
		ObservedGeneration: fc.Generation,
		LastTransitionTime: nowMeta,
	}
	apimeta.SetStatusCondition(&fc.Status.Conditions, cond)

	hb := fc.Status.Heartbeat
	if hb == nil {
		hb = &kaprov1alpha1.ClusterHeartbeatStatus{}
	}
	hb.ObservedAt = &nowMeta
	hb.LeaseObservedAt = desired.LeaseObservedAt
	hb.ConsecutiveMisses = desired.Misses
	hb.Reason = desired.Reason
	if prevStatus != desired.Status || prevReason != desired.Reason {
		hb.LastTransitionAt = &nowMeta
	}
	fc.Status.Heartbeat = hb

	patch := client.MergeFrom(before)
	if err := r.Status().Patch(ctx, fc, patch); err != nil {
		metrics.StatusWrites.WithLabelValues("fleetcluster", "error").Inc()
		return fmt.Errorf("patch FleetCluster %q status: %w", fc.Name, err)
	}
	metrics.StatusWrites.WithLabelValues("fleetcluster", "success").Inc()
	metrics.FleetClusterHeartbeatMisses.WithLabelValues(fc.Name).Set(float64(desired.Misses))

	r.emitTransitionEvents(fc, prevStatus, prevReason, desired)
	return nil
}

func (r *ClusterHeartbeatReconciler) emitTransitionEvents(fc *kaprov1alpha1.Cluster, prevStatus metav1.ConditionStatus, prevReason string, desired desiredReady) {
	if r.Recorder == nil {
		return
	}
	if prevStatus == desired.Status && prevReason == desired.Reason {
		return // no transition
	}
	// Unreachable transitions are the headline operator concern.
	switch {
	case desired.Reason == kaprov1alpha1.ReasonUnreachable && prevReason != kaprov1alpha1.ReasonUnreachable:
		r.Recorder.Event(fc, corev1.EventTypeWarning, eventReasonClusterUnreachable, desired.Message)
		metrics.FleetClusterUnreachableTransitions.WithLabelValues(fc.Name).Inc()
	case prevReason == kaprov1alpha1.ReasonUnreachable && desired.Status == metav1.ConditionTrue:
		r.Recorder.Event(fc, corev1.EventTypeNormal, eventReasonClusterRecovered, desired.Message)
		metrics.FleetClusterRecoveredTransitions.WithLabelValues(fc.Name).Inc()
	case desired.Reason == kaprov1alpha1.ReasonHeartbeatStale && prevReason != kaprov1alpha1.ReasonHeartbeatStale:
		r.Recorder.Event(fc, corev1.EventTypeWarning, eventReasonHeartbeatStale, desired.Message)
	case prevReason == kaprov1alpha1.ReasonHeartbeatStale && desired.Status == metav1.ConditionTrue:
		r.Recorder.Event(fc, corev1.EventTypeNormal, eventReasonHeartbeatRecovered, desired.Message)
	}
}

func (r *ClusterHeartbeatReconciler) heartbeatNamespace() string {
	if r.HeartbeatNamespace != "" {
		return r.HeartbeatNamespace
	}
	return defaultHeartbeatNamespace
}

func (r *ClusterHeartbeatReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func currentMisses(fc *kaprov1alpha1.Cluster) int32 {
	if fc.Status.Heartbeat == nil {
		return 0
	}
	return fc.Status.Heartbeat.ConsecutiveMisses
}

// SetupWithManager registers the reconciler with the manager. It watches
// FleetCluster (primary) and Lease objects in the heartbeat namespace
// (secondary). The Lease watch maps each event to the owning FleetCluster
// reconcile request by stripping the heartbeatLeasePrefix from the Lease
// name.
func (r *ClusterHeartbeatReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("fleetcluster-heartbeat").
		For(&kaprov1alpha1.Cluster{}, builder.WithPredicates(fleetClusterSpecOrBootstrapPredicate{})).
		Watches(
			&coordinationv1.Lease{},
			handler.EnqueueRequestsFromMapFunc(leaseToFleetCluster(r.heartbeatNamespace())),
		).
		Complete(r)
}

// leaseToFleetCluster maps a Lease event to the owning FleetCluster's
// reconcile request. Filters out Leases not in the heartbeat namespace and
// Leases whose name doesn't carry the heartbeat prefix — both signal a Lease
// that has nothing to do with us.
func leaseToFleetCluster(heartbeatNS string) handler.MapFunc {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		if obj.GetNamespace() != heartbeatNS {
			return nil
		}
		name := obj.GetName()
		if len(name) <= len(heartbeatLeasePrefix) || name[:len(heartbeatLeasePrefix)] != heartbeatLeasePrefix {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Name: name[len(heartbeatLeasePrefix):]},
		}}
	}
}

// fleetClusterSpecOrBootstrapPredicate triggers reconciles on spec changes
// (suspend, threshold, delivery mode) and on status.bootstrap transitions
// (used flipping true) — both of which can change the desiredReady output
// without a Lease event firing. Status-only updates beyond bootstrap are
// filtered out to avoid feedback loops with our own status patches.
type fleetClusterSpecOrBootstrapPredicate struct{}

func (fleetClusterSpecOrBootstrapPredicate) Create(_ event.CreateEvent) bool { return true }
func (fleetClusterSpecOrBootstrapPredicate) Delete(_ event.DeleteEvent) bool { return true }
func (fleetClusterSpecOrBootstrapPredicate) Generic(_ event.GenericEvent) bool {
	return true
}
func (fleetClusterSpecOrBootstrapPredicate) Update(e event.UpdateEvent) bool {
	oldFC, ok := e.ObjectOld.(*kaprov1alpha1.Cluster)
	if !ok {
		return true
	}
	newFC, ok := e.ObjectNew.(*kaprov1alpha1.Cluster)
	if !ok {
		return true
	}
	if oldFC.Generation != newFC.Generation {
		return true
	}
	oldUsed := oldFC.Status.Bootstrap != nil && oldFC.Status.Bootstrap.Used
	newUsed := newFC.Status.Bootstrap != nil && newFC.Status.Bootstrap.Used
	return oldUsed != newUsed
}
