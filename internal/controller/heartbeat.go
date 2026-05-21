package controller

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	defaultHeartbeatNamespace = "kapro-system"
	heartbeatLeasePrefix      = "kapro-heartbeat-"
	// heartbeatFreshTimeout is the window beyond which a Lease renewal is
	// considered a miss. The FleetClusterHeartbeatReconciler reads the Lease
	// and counts misses; this constant defines what counts as a miss but
	// NOT how many misses are tolerated (that is per-cluster via
	// spec.consecutiveFailureThreshold).
	heartbeatFreshTimeout = 2 * time.Minute
)

func heartbeatLeaseName(clusterName string) string {
	return heartbeatLeasePrefix + clusterName
}

// requireFreshHeartbeat blocks a target from progressing when the cluster's
// heartbeat is not fresh. Reachability is decided by the
// FleetClusterHeartbeatReconciler via Spec.ConsecutiveFailureThreshold — this
// function only reads that decision (conditions[Ready] + status.heartbeat)
// and surfaces it on the target.
//
// Behavior matrix (DeliveryMode == pull):
//
//	Ready=True                       → proceed.
//	Ready=False reason=Unreachable   → defer; emit ClusterUnreachable event.
//	Ready=Unknown reason=Stale       → defer; emit HeartbeatStale event the
//	                                   first time we see it.
//	Ready=Unknown reason=Suspended   → defer; the cluster is paused.
//	Ready=Unknown reason=NotRegistered → defer; cluster has never bootstrapped.
//	Ready condition missing          → defer; reconciler hasn't run yet.
//
// The function never fails the target on heartbeat staleness. That decision
// belongs to the cluster-level threshold (which controls Phase=Unreachable)
// or to an explicit operator action (suspend / reject / delete). Auto-failing
// after a fixed wall-clock window was the v0.4 behavior and proved brittle
// during normal network blips. Operators can still cancel a stuck target via
// the standard reject flow.
//
// status.heartbeatStaleSince and status.heartbeatStaleCount stay updated for
// dashboards and runbooks but are no longer load-bearing.
func (r *PromotionTargetReconciler) requireFreshHeartbeat(
	ctx context.Context,
	promotionrun *kaprov1alpha2.PromotionRun,
	target *kaprov1alpha2.TargetStatus,
	mc *kaprov1alpha2.Cluster,
) (ctrl.Result, bool, error) {
	_ = ctx // ctx retained for future use (status patches, list calls)
	if mc.Spec.Delivery.Mode != kaprov1alpha2.DeliveryModePull {
		target.HeartbeatStaleSince = ""
		target.HeartbeatStaleCount = 0
		return ctrl.Result{}, true, nil
	}

	now := time.Now().UTC()
	ready := apimeta.FindStatusCondition(mc.Status.Conditions, kaprov1alpha2.ConditionTypeReady)
	switch {
	case ready != nil && ready.Status == metav1.ConditionTrue:
		// Heartbeat fresh per the reconciler. Clear per-target staleness state.
		target.HeartbeatStaleSince = ""
		target.HeartbeatStaleCount = 0
		return ctrl.Result{}, true, nil

	case ready != nil && ready.Status == metav1.ConditionFalse && ready.Reason == kaprov1alpha2.ReasonUnreachable:
		target.HeartbeatStaleCount++
		if target.HeartbeatStaleSince == "" {
			target.HeartbeatStaleSince = now.Format(time.RFC3339)
			if r.Recorder != nil {
				r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, "ClusterUnreachable",
					"[%s/%s] cluster %s is Unreachable per heartbeat threshold; deferring promotion: %s",
					target.Stage, target.Target, mc.Name, ready.Message)
			}
		}
		target.Message = fmt.Sprintf("cluster %s Unreachable: %s", mc.Name, ready.Message)
		return ctrl.Result{RequeueAfter: requeueNormal}, false, nil

	default:
		// Unknown / Stale / Suspended / NotRegistered / missing condition.
		// All mean "wait, don't proceed, don't fail."
		target.HeartbeatStaleCount++
		reason := "HeartbeatNotReady"
		message := "FleetCluster Ready condition not yet observed"
		if ready != nil {
			reason = ready.Reason
			message = ready.Message
		}
		if target.HeartbeatStaleSince == "" {
			target.HeartbeatStaleSince = now.Format(time.RFC3339)
			if r.Recorder != nil {
				r.Recorder.Eventf(promotionrun, corev1.EventTypeWarning, reason,
					"[%s/%s] cluster %s heartbeat not fresh: %s",
					target.Stage, target.Target, mc.Name, message)
			}
		}
		target.Message = fmt.Sprintf("cluster %s heartbeat not fresh (%s): %s", mc.Name, reason, message)
		return ctrl.Result{RequeueAfter: requeueNormal}, false, nil
	}
}

func leaseHeartbeatTime(lease *coordinationv1.Lease) (time.Time, bool) {
	if lease.Spec.RenewTime != nil {
		return lease.Spec.RenewTime.Time, true
	}
	if lease.Spec.AcquireTime != nil {
		return lease.Spec.AcquireTime.Time, true
	}
	if !lease.CreationTimestamp.IsZero() {
		return lease.CreationTimestamp.Time, true
	}
	return time.Time{}, false
}
