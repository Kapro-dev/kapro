package flux

import (
	"context"
	"fmt"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// FluxActuator implements the Actuator interface for Flux.
// It promotes a version by mutating the OCI tag on the target OCIRepository,
// then waits for Flux to reconcile (convergence checked via ClusterRegistration.status).
type FluxActuator struct{}

type ApplyRequest struct {
	Environment *kaprov1alpha1.Environment
	Version     string
}

// Apply mutates the OCIRepository tag to trigger Flux reconciliation.
// The actual delivery is handled by Flux — Kapro only sets the target version.
func (a *FluxActuator) Apply(ctx context.Context, req ApplyRequest) error {
	flux := req.Environment.Spec.Actuator.Flux
	if flux == nil {
		return fmt.Errorf("environment %s has no flux actuator configured", req.Environment.Name)
	}

	// TODO: connect to workload cluster (via ClusterRegistration SA token)
	// TODO: patch OCIRepository.spec.ref.tag = req.Version
	// TODO: annotate with reconcile timestamp to force immediate reconcile
	return nil
}

// IsConverged checks ClusterRegistration.status.phase == Converged
// and currentVersion == expected version.
// Does NOT connect to the workload cluster — reads CRD on control plane.
func (a *FluxActuator) IsConverged(ctx context.Context, env *kaprov1alpha1.Environment, version string) (bool, error) {
	// TODO: read ClusterRegistration for this environment
	// TODO: check phase == Converged && currentVersion == version
	return false, nil
}

// Rollback sets the OCI tag back to the previous stable version.
func (a *FluxActuator) Rollback(ctx context.Context, env *kaprov1alpha1.Environment, version string) error {
	// TODO: apply previous version tag
	return nil
}
