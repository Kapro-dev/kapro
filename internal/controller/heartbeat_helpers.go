package controller

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/heartbeat"
)

// isClusterHeartbeatFresh checks whether a cluster's heartbeat is fresh.
// It first checks the authoritative Lease object (created by the heartbeat
// renewer on the spoke). If the Lease doesn't exist (e.g. during migration
// from the old status-based heartbeat), it falls back to
// MemberCluster.status.lastHeartbeat for backward compatibility.
func isClusterHeartbeatFresh(ctx context.Context, c client.Client, clusterName string, threshold time.Duration) bool {
	fresh, err := heartbeat.IsLeaseHeartbeatFresh(ctx, c, clusterName, threshold)
	if err != nil {
		log.FromContext(ctx).V(1).Info("lease heartbeat check failed, falling back to status",
			"cluster", clusterName, "error", err)
	}
	if fresh {
		return true
	}

	// Fallback: check legacy MemberCluster.status.lastHeartbeat.
	var mc kaprov1alpha1.MemberCluster
	if err := c.Get(ctx, client.ObjectKey{Name: clusterName}, &mc); err != nil {
		return false
	}
	return mc.Status.IsHeartbeatFresh(threshold)
}
