package crd

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	pkgprovider "kapro.io/kapro/pkg/provider"
)

const heartbeatStaleThreshold = 2 * time.Minute

// compile-time check: CRDProvider implements KCI RegistrationReader.
var _ pkgprovider.RegistrationReader = &CRDProvider{}

// CRDProvider reads ClusterRegistration CRDs from the control plane to determine
// cluster connectivity and health. No direct network connection to workload clusters.
// The kapro-cluster-controller on each workload cluster writes these CRDs outbound.
type CRDProvider struct {
	// Client is the control-plane Kubernetes client.
	Client client.Client
}

// GetRegistration returns the ClusterRegistration for the given Environment.
// The label kapro.io/environment must be set on the ClusterRegistration,
// and spec.environmentRef must match.
func (p *CRDProvider) GetRegistration(ctx context.Context, env *kaprov1alpha1.Environment) (*kaprov1alpha1.ClusterRegistration, error) {
	var regList kaprov1alpha1.ClusterRegistrationList
	if err := p.Client.List(ctx, &regList, client.MatchingLabels{
		"kapro.io/environment": env.Name,
	}); err != nil {
		return nil, fmt.Errorf("CRDProvider.GetRegistration: list: %w", err)
	}

	for i := range regList.Items {
		if regList.Items[i].Spec.EnvironmentRef == env.Name {
			return &regList.Items[i], nil
		}
	}

	return nil, fmt.Errorf("CRDProvider.GetRegistration: no ClusterRegistration for environment %q — is kapro-cluster-controller running on that cluster?", env.Name)
}

// IsReachable returns true when the workload cluster's cluster-controller has
// sent a heartbeat within the staleness threshold (2 minutes).
// No network connection needed — the check is purely based on CRD status.
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
		return false, fmt.Errorf("CRDProvider.IsReachable: parse LastHeartbeat: %w", err)
	}

	return time.Since(lastSeen) < heartbeatStaleThreshold, nil
}

// IsHealthy returns true when the cluster is reachable AND Flux is ready.
func (p *CRDProvider) IsHealthy(ctx context.Context, env *kaprov1alpha1.Environment) (bool, error) {
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
	if time.Since(lastSeen) >= heartbeatStaleThreshold {
		return false, nil
	}

	return reg.Status.Health.AllWorkloadsReady, nil
}

// CurrentVersion returns the version currently reported by the workload cluster.
func (p *CRDProvider) CurrentVersion(ctx context.Context, env *kaprov1alpha1.Environment) (string, error) {
	reg, err := p.GetRegistration(ctx, env)
	if err != nil {
		return "", err
	}
	return reg.Status.CurrentVersions["ocs"], nil
}
