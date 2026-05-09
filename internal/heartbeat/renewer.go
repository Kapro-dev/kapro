// Package heartbeat implements lease-based heartbeat for Kapro cluster controllers.
// Inspired by Kargo's pkg/heartbeat/renewer.go — uses coordination.k8s.io/v1 Lease
// objects on the hub cluster instead of writing MemberCluster.status.lastHeartbeat.
//
// At 1000+ clusters this reduces hub API writes from 16.7/sec (one status patch
// per cluster per minute) to 16.7 Lease renewals per second — same rate but
// Lease objects are tiny and don't trigger informer notifications on the hub
// controllers that watch MemberCluster status.
package heartbeat

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LeaseNamePrefix is prepended to the cluster name to form the Lease object name.
	LeaseNamePrefix = "kapro-heartbeat-"

	// DefaultNamespace is the namespace where heartbeat Leases are created.
	DefaultNamespace = "kapro-system"

	// LabelHeartbeat marks a Lease as a Kapro heartbeat.
	LabelHeartbeat = "kapro.io/heartbeat"

	// LabelCluster identifies which cluster owns the heartbeat Lease.
	LabelCluster = "kapro.io/cluster"
)

// Renewer creates, renews, and deletes a coordination.k8s.io/v1 Lease on the
// hub cluster as a lightweight heartbeat signal. It runs as a goroutine
// alongside the cluster-controller's main reconcile loop.
type Renewer struct {
	hubClient   client.Client
	clusterName string
	namespace   string
	leaseName   string

	// LeaseDuration is how long the lease is valid. Default: 60s.
	leaseDuration time.Duration
	// RenewInterval is how often the lease is renewed. Default: 20s (1/3 of duration).
	renewInterval time.Duration
}

// NewRenewer creates a Renewer that will manage a heartbeat Lease on the hub.
func NewRenewer(hubClient client.Client, clusterName string, leaseDuration time.Duration) *Renewer {
	if leaseDuration <= 0 {
		leaseDuration = 60 * time.Second
	}
	return &Renewer{
		hubClient:     hubClient,
		clusterName:   clusterName,
		namespace:     DefaultNamespace,
		leaseName:     LeaseNamePrefix + clusterName,
		leaseDuration: leaseDuration,
		renewInterval: leaseDuration / 3,
	}
}

// Run starts the heartbeat renewal loop. It blocks until ctx is cancelled,
// then deletes the Lease for a clean exit. Thread-safe — designed to run as
// a goroutine.
func (r *Renewer) Run(ctx context.Context) {
	log := ctrl.Log.WithName("heartbeat").WithValues(
		"cluster", r.clusterName,
		"lease", r.leaseName,
		"duration", r.leaseDuration,
		"interval", r.renewInterval,
	)
	log.Info("starting heartbeat renewer")

	// Initial renewal.
	if err := r.renew(ctx); err != nil {
		log.Error(err, "initial heartbeat lease creation failed; will retry")
	}

	ticker := time.NewTicker(r.renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := r.renew(ctx); err != nil {
				log.Error(err, "failed to renew heartbeat lease")
			}
		case <-ctx.Done():
			// Use a fresh context for the shutdown delete.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := r.delete(shutdownCtx); err != nil {
				log.Error(err, "failed to delete heartbeat lease on shutdown")
			}
			cancel()
			log.Info("heartbeat renewer stopped")
			return
		}
	}
}

func (r *Renewer) renew(ctx context.Context) error {
	now := metav1.NewMicroTime(time.Now())
	durationSeconds := int32(r.leaseDuration.Seconds())

	cur := &coordinationv1.Lease{}
	err := r.hubClient.Get(ctx, types.NamespacedName{
		Name:      r.leaseName,
		Namespace: r.namespace,
	}, cur)

	if apierrors.IsNotFound(err) {
		return r.hubClient.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      r.leaseName,
				Namespace: r.namespace,
				Labels: map[string]string{
					LabelHeartbeat: "true",
					LabelCluster:   r.clusterName,
				},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       ptr.To(r.clusterName),
				LeaseDurationSeconds: ptr.To(durationSeconds),
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		})
	}
	if err != nil {
		return fmt.Errorf("get existing heartbeat lease: %w", err)
	}

	// Update the existing Lease.
	if cur.Labels == nil {
		cur.Labels = map[string]string{}
	}
	cur.Labels[LabelHeartbeat] = "true"
	cur.Labels[LabelCluster] = r.clusterName
	cur.Spec.HolderIdentity = ptr.To(r.clusterName)
	cur.Spec.LeaseDurationSeconds = ptr.To(durationSeconds)
	if cur.Spec.AcquireTime == nil {
		cur.Spec.AcquireTime = &now
	}
	cur.Spec.RenewTime = &now
	return r.hubClient.Update(ctx, cur)
}

func (r *Renewer) delete(ctx context.Context) error {
	err := r.hubClient.Delete(ctx, &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.leaseName,
			Namespace: r.namespace,
		},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// IsLeaseHeartbeatFresh checks whether the heartbeat Lease for the named cluster
// has been renewed within the given threshold. This is the hub-side helper that
// replaces MemberCluster.status.IsHeartbeatFresh().
func IsLeaseHeartbeatFresh(ctx context.Context, c client.Client, clusterName string, threshold time.Duration) (bool, error) {
	leaseName := LeaseNamePrefix + clusterName
	var lease coordinationv1.Lease
	if err := c.Get(ctx, types.NamespacedName{
		Name:      leaseName,
		Namespace: DefaultNamespace,
	}, &lease); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get heartbeat lease for %s: %w", clusterName, err)
	}

	if lease.Spec.RenewTime == nil {
		return false, nil
	}
	return time.Since(lease.Spec.RenewTime.Time) < threshold, nil
}
