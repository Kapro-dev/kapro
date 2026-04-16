package flux

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApplyRequest carries the environment and target version for an apply operation.
type ApplyRequest struct {
	Environment *kaprov1alpha1.Environment
	Version     string
	// PreviousVersion is recorded so Rollback can revert to it.
	PreviousVersion string
}

// FluxActuator implements promotion via the CRD-native outbound pattern:
//
//  1. Apply() writes ClusterRegistration.spec.desiredVersion on the control plane.
//  2. kapro-cluster-controller on the workload cluster polls spec.desiredVersion
//     and patches the local OCIRepository tag — triggering Flux reconciliation.
//  3. IsConverged() reads ClusterRegistration.status.phase + currentVersion
//     to determine whether Flux has converged.
//
// No kubeconfig or inbound connection to workload clusters is needed.
type FluxActuator struct {
	// Client is the control-plane Kubernetes client.
	Client client.Client
}

// Apply sets ClusterRegistration.spec.desiredVersion, signalling the
// cluster-controller to update the local OCIRepository tag.
func (a *FluxActuator) Apply(ctx context.Context, req ApplyRequest) error {
	log := log.FromContext(ctx).WithValues(
		"environment", req.Environment.Name,
		"version", req.Version,
	)

	reg, err := a.getRegistration(ctx, req.Environment.Name)
	if err != nil {
		return fmt.Errorf("FluxActuator.Apply: %w", err)
	}

	if reg.Spec.DesiredVersion == req.Version {
		log.Info("desiredVersion already set, skipping patch")
		return nil
	}

	patch := client.MergeFrom(reg.DeepCopy())
	reg.Spec.DesiredVersion = req.Version
	if err := a.Client.Patch(ctx, reg, patch); err != nil {
		return fmt.Errorf("FluxActuator.Apply: patch ClusterRegistration %s: %w", reg.Name, err)
	}

	log.Info("patched ClusterRegistration.spec.desiredVersion",
		"registration", reg.Name,
		"ociRepo", req.Environment.Spec.Actuator.Flux.OCIRepository,
	)
	return nil
}

// IsConverged returns true when the workload cluster's cluster-controller
// has reconciled the desired version and Flux has converged.
func (a *FluxActuator) IsConverged(ctx context.Context, env *kaprov1alpha1.Environment, version string) (bool, error) {
	reg, err := a.getRegistration(ctx, env.Name)
	if err != nil {
		return false, fmt.Errorf("FluxActuator.IsConverged: %w", err)
	}

	// Heartbeat must be fresh — stale means the cluster-controller is down.
	if !isHeartbeatFresh(reg.Status.LastHeartbeat) {
		return false, fmt.Errorf("cluster %s heartbeat is stale (last seen: %s)", env.Name, reg.Status.LastHeartbeat)
	}

	converged := reg.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		reg.Status.CurrentVersions["ocs"] == version

	log.FromContext(ctx).Info("convergence check",
		"environment", env.Name,
		"phase", reg.Status.Phase,
		"currentVersion", reg.Status.CurrentVersions["ocs"],
		"wantVersion", version,
		"converged", converged,
	)

	return converged, nil
}

// Rollback sets the desired version back to the given (previous) version.
func (a *FluxActuator) Rollback(ctx context.Context, env *kaprov1alpha1.Environment, previousVersion string) error {
	log.FromContext(ctx).Info("rolling back",
		"environment", env.Name,
		"previousVersion", previousVersion,
	)
	return a.Apply(ctx, ApplyRequest{
		Environment: env,
		Version:     previousVersion,
	})
}

// getRegistration returns the ClusterRegistration for the given environment name.
func (a *FluxActuator) getRegistration(ctx context.Context, envName string) (*kaprov1alpha1.ClusterRegistration, error) {
	var regList kaprov1alpha1.ClusterRegistrationList
	if err := a.Client.List(ctx, &regList, client.MatchingLabels{
		"kapro.io/environment": envName,
	}); err != nil {
		return nil, fmt.Errorf("list ClusterRegistrations: %w", err)
	}

	for i := range regList.Items {
		if regList.Items[i].Spec.EnvironmentRef == envName {
			return &regList.Items[i], nil
		}
	}

	return nil, fmt.Errorf("no ClusterRegistration found for environment %q", envName)
}

func isHeartbeatFresh(lastHeartbeat string) bool {
	if lastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, lastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < 2*time.Minute
}

