package crd

import (
	"context"
	"fmt"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const heartbeatStaleThreshold = 2 * time.Minute

// CRDProvider implements the Provider interface using ClusterRegistration CRDs.
// No direct connection to workload clusters — the kapro-cluster-controller
// writes status, and this provider reads it from the control plane.
type CRDProvider struct{}

// GetRegistration returns the ClusterRegistration for the given Environment.
// The ClusterRegistration is written by kapro-cluster-controller on the workload cluster.
func (p *CRDProvider) GetRegistration(ctx context.Context, env *kaprov1alpha1.Environment) (*kaprov1alpha1.ClusterRegistration, error) {
	// TODO: list ClusterRegistrations where spec.environmentRef == env.Name
	return nil, fmt.Errorf("not implemented")
}

// IsReachable checks if the workload cluster's controller has sent a heartbeat
// within the staleness threshold. No network connection needed — reads CRD status.
func (p *CRDProvider) IsReachable(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error) {
	reg, err := p.GetRegistration(ctx, env)
	if err != nil {
		return false, err
	}

	if reg.Status.LastHeartbeat == "" {
		return false, nil
	}

	lastSeen, err := time.Parse(time.RFC3339, reg.Status.LastHeartbeat)
	if err != nil {
		return false, err
	}

	return time.Since(lastSeen) < heartbeatStaleThreshold, nil
}
