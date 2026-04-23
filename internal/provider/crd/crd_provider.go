package crd

import (
	"context"
	"fmt"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkgprovider "kapro.io/kapro/pkg/provider"
)

const heartbeatStaleThreshold = 2 * time.Minute

// compile-time check: CRDProvider implements KCI RegistrationReader.
var _ pkgprovider.RegistrationReader = &CRDProvider{}

// CRDProvider reads MemberCluster CRDs from the control plane to determine
// cluster health and reachability. No direct network connection to workload clusters.
// The kapro-cluster-controller on each workload cluster writes these CRDs outbound.
type CRDProvider struct{}

// IsReachable returns true when the workload cluster's cluster-controller has
// sent a heartbeat within the staleness threshold (2 minutes).
// No network connection needed — the check is purely based on CRD status.
func (p *CRDProvider) IsReachable(_ context.Context, mc *kaprov1alpha1.MemberCluster) (bool, error) {
	if mc == nil {
		return false, fmt.Errorf("CRDProvider.IsReachable: cluster is nil")
	}
	if mc.Status.LastHeartbeat == "" {
		return false, nil
	}
	lastSeen, err := time.Parse(time.RFC3339, mc.Status.LastHeartbeat)
	if err != nil {
		return false, fmt.Errorf("CRDProvider.IsReachable: parse LastHeartbeat: %w", err)
	}
	return time.Since(lastSeen) < heartbeatStaleThreshold, nil
}

// IsHealthy returns true when the cluster is reachable AND all workloads report ready.
func (p *CRDProvider) IsHealthy(ctx context.Context, mc *kaprov1alpha1.MemberCluster) (bool, error) {
	reachable, err := p.IsReachable(ctx, mc)
	if err != nil || !reachable {
		return false, err
	}
	return mc.Status.Health.AllWorkloadsReady, nil
}

// CurrentVersion returns the version currently reported by the workload cluster.
// appKey selects which application version to read from status.currentVersions.
// Pass "default" for single-app clusters.
func (p *CRDProvider) CurrentVersion(_ context.Context, mc *kaprov1alpha1.MemberCluster, appKey string) (string, error) {
	if mc == nil {
		return "", fmt.Errorf("CRDProvider.CurrentVersion: cluster is nil")
	}
	if appKey == "" {
		appKey = "default"
	}
	return mc.Status.CurrentVersions[appKey], nil
}
