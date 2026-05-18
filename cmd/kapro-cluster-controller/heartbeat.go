package main

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// heartbeatLoop refreshes a coordination.k8s.io/v1 Lease on the HUB cluster
// named "kapro-heartbeat-<ClusterName>" in HubNamespace at Interval. The hub
// reconciler reads this Lease (see internal/controller/heartbeat.go) and
// flips FleetCluster Ready off once consecutive failures exceed the
// per-cluster ConsecutiveFailureThreshold (PR-8 wiring).
//
// The per-cluster RBAC issued during bootstrap (PR-2) allows the spoke to
// write ONLY its own Lease, enforced via resourceNames=[<lease name>]. A
// compromised spoke cannot affect another cluster's heartbeat.
type heartbeatLoop struct {
	Hub            *HubClient
	ClusterName    string
	HubNamespace   string
	Interval       time.Duration
	HolderIdentity string
}

// Run blocks until ctx is cancelled. Logs failures but keeps trying — a
// transient hub outage shouldn't take the spoke down.
func (h *heartbeatLoop) Run(ctx context.Context) {
	logger := log.Log.WithName("heartbeat").WithValues("cluster", h.ClusterName)
	if h.Interval <= 0 {
		h.Interval = 30 * time.Second
	}
	// Tick immediately so the first heartbeat lands without a 30s wait.
	if err := h.tick(ctx); err != nil {
		logger.Error(err, "first heartbeat failed (will retry)")
	}
	ticker := time.NewTicker(h.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.tick(ctx); err != nil {
				logger.Error(err, "heartbeat tick failed")
			}
		}
	}
}

// tick upserts the Lease with a fresh RenewTime. Idempotent.
func (h *heartbeatLoop) tick(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	hub, err := h.Hub.Client()
	if err != nil {
		return err
	}

	name := heartbeatLeaseName(h.ClusterName)
	lease := &coordinationv1.Lease{}
	err = hub.Get(tctx, client.ObjectKey{Namespace: h.HubNamespace, Name: name}, lease)
	now := metav1.NewMicroTime(time.Now())
	if apierrors.IsNotFound(err) {
		// Create a fresh Lease. The per-cluster RBAC allows create on the
		// specific Lease name. The HolderIdentity is informational only —
		// there's no contention for a per-cluster Lease.
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: h.HubNamespace,
				Labels: map[string]string{
					"kapro.io/fleetcluster":        h.ClusterName,
					"app.kubernetes.io/managed-by": "kapro-cluster-controller",
				},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       ptrString(h.HolderIdentity),
				LeaseDurationSeconds: ptrInt32(h.leaseDurationSeconds()),
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		if err := hub.Create(tctx, lease); err != nil {
			return fmt.Errorf("create heartbeat Lease: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get heartbeat Lease: %w", err)
	}
	patch := client.MergeFrom(lease.DeepCopy())
	lease.Spec.RenewTime = &now
	lease.Spec.HolderIdentity = ptrString(h.HolderIdentity)
	if lease.Spec.LeaseDurationSeconds == nil {
		lease.Spec.LeaseDurationSeconds = ptrInt32(h.leaseDurationSeconds())
	}
	if err := hub.Patch(tctx, lease, patch); err != nil {
		return fmt.Errorf("patch heartbeat Lease: %w", err)
	}
	return nil
}

// heartbeatLeaseName mirrors internal/controller/heartbeat.go's
// heartbeatLeaseName so the spoke writes to the same Lease name the hub
// reader expects. Drift here = silent registration that hub never sees.
//
// Hub-side definition: heartbeatLeasePrefix + clusterName ("kapro-heartbeat-").
func heartbeatLeaseName(clusterName string) string {
	return "kapro-heartbeat-" + clusterName
}

func ptrString(s string) *string { return &s }
func ptrInt32(v int32) *int32    { return &v }

// leaseDurationSeconds returns 2× the heartbeat interval in whole seconds,
// rounding UP so sub-second intervals (e.g. 10.9s) do not produce a Lease
// shorter than 2× the configured cadence. A minimum of 1 second is
// enforced so a zero/negative Interval still yields a valid Lease.
func (h *heartbeatLoop) leaseDurationSeconds() int32 {
	d := 2 * h.Interval
	if d <= 0 {
		return 1
	}
	secs := int64((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return int32(secs)
}
