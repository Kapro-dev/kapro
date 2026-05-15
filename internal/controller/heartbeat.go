package controller

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	defaultHeartbeatNamespace = "kapro-system"
	heartbeatLeasePrefix      = "kapro-heartbeat-"
	heartbeatFreshTimeout     = 2 * time.Minute
	heartbeatStaleFailAfter   = 5 * time.Minute
)

type heartbeatStatus struct {
	Fresh    bool
	Source   string
	Observed time.Time
	Message  string
}

func heartbeatLeaseName(clusterName string) string {
	return heartbeatLeasePrefix + clusterName
}

func (r *ReleaseTargetReconciler) heartbeatNamespace() string {
	if r.HeartbeatNamespace != "" {
		return r.HeartbeatNamespace
	}
	return defaultHeartbeatNamespace
}

func (r *ReleaseTargetReconciler) requireFreshHeartbeat(
	ctx context.Context,
	release *kaprov1alpha1.Release,
	target *kaprov1alpha1.TargetStatus,
	mc *kaprov1alpha1.MemberCluster,
) (ctrl.Result, bool, error) {
	if mc.Spec.Actuator.Mode != "pull" {
		target.HeartbeatStaleSince = ""
		return ctrl.Result{}, true, nil
	}

	status, err := r.memberClusterHeartbeat(ctx, mc)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if status.Fresh {
		target.HeartbeatStaleSince = ""
		target.HeartbeatStaleCount = 0
		return ctrl.Result{}, true, nil
	}

	now := time.Now().UTC()
	target.HeartbeatStaleCount++
	if target.HeartbeatStaleSince == "" {
		target.HeartbeatStaleSince = now.Format(time.RFC3339)
		target.Message = status.Message
		if r.Recorder != nil {
			r.Recorder.Eventf(release, corev1.EventTypeWarning, "HeartbeatStale",
				"[%s/%s] waiting for fresh cluster heartbeat: %s", target.Stage, target.Target, status.Message)
		}
		return ctrl.Result{RequeueAfter: requeueNormal}, false, nil
	}

	staleSince, parseErr := time.Parse(time.RFC3339, target.HeartbeatStaleSince)
	if parseErr != nil {
		target.HeartbeatStaleSince = now.Format(time.RFC3339)
		target.Message = status.Message
		return ctrl.Result{RequeueAfter: requeueNormal}, false, nil
	}
	if target.HeartbeatStaleCount >= missingMCFailThreshold && now.Sub(staleSince) >= heartbeatStaleFailAfter {
		r.failTarget(ctx, release, target,
			fmt.Sprintf("cluster %s heartbeat stale for %s: %s", mc.Name, heartbeatStaleFailAfter, status.Message))
		return ctrl.Result{}, false, nil
	}

	target.Message = status.Message
	return ctrl.Result{RequeueAfter: requeueNormal}, false, nil
}

func (r *ReleaseTargetReconciler) memberClusterHeartbeat(ctx context.Context, mc *kaprov1alpha1.MemberCluster) (heartbeatStatus, error) {
	lease := &coordinationv1.Lease{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: r.heartbeatNamespace(),
		Name:      heartbeatLeaseName(mc.Name),
	}, lease)
	if err == nil {
		if observed, ok := leaseHeartbeatTime(lease); ok {
			if time.Since(observed) < heartbeatFreshTimeout {
				return heartbeatStatus{Fresh: true, Source: "lease", Observed: observed}, nil
			}
			return heartbeatStatus{
				Source:   "lease",
				Observed: observed,
				Message:  fmt.Sprintf("Lease %s/%s last renewed %s ago", lease.Namespace, lease.Name, time.Since(observed).Round(time.Second)),
			}, nil
		}
		status, statusErr := statusHeartbeat(mc)
		if statusErr != nil {
			return status, statusErr
		}
		if !status.Fresh {
			status.Message = fmt.Sprintf("Lease %s/%s has no renewTime or acquireTime; %s", lease.Namespace, lease.Name, status.Message)
		}
		return status, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return heartbeatStatus{}, fmt.Errorf("read heartbeat Lease for %s: %w", mc.Name, err)
	}

	status, statusErr := statusHeartbeat(mc)
	if statusErr != nil {
		return status, statusErr
	}
	if !status.Fresh {
		status.Message = fmt.Sprintf("missing heartbeat Lease %s/%s; %s", r.heartbeatNamespace(), heartbeatLeaseName(mc.Name), status.Message)
	}
	return status, nil
}

func statusHeartbeat(mc *kaprov1alpha1.MemberCluster) (heartbeatStatus, error) {
	if mc.Status.LastHeartbeat != "" {
		observed, parseErr := time.Parse(time.RFC3339, mc.Status.LastHeartbeat)
		if parseErr != nil {
			return heartbeatStatus{
				Source:  "status",
				Message: fmt.Sprintf("MemberCluster.status.lastHeartbeat is invalid: %v", parseErr),
			}, nil
		}
		if time.Since(observed) < heartbeatFreshTimeout {
			return heartbeatStatus{Fresh: true, Source: "status", Observed: observed}, nil
		}
		return heartbeatStatus{
			Source:   "status",
			Observed: observed,
			Message:  fmt.Sprintf("MemberCluster.status.lastHeartbeat last updated %s ago", time.Since(observed).Round(time.Second)),
		}, nil
	}

	return heartbeatStatus{
		Message: "status.lastHeartbeat is empty",
	}, nil
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
