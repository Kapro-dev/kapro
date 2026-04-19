// Package actuator defines KAI — the Kapro Actuator Interface.
//
// KAI is the contract between the Kapro promotion engine and any delivery system.
// Analogous to Kubernetes CRI: Kapro doesn't care if you use Flux, ArgoCD, Helm,
// or Pulumi — it calls the same three methods.
//
// Built-in implementations live in internal/actuator/:
//   - flux/ — patches OCIRepository tag + triggers Flux reconciliation
//
// External implementations (ArgoCD, Helm, KServe) can implement this interface
// and register via actuator.Registry at startup.
package actuator

import (
	"context"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApplyRequest carries everything an actuator needs to apply a version.
type ApplyRequest struct {
	// Environment is the target environment.
	Environment *kaprov1alpha1.Environment
	// Version is the version string to apply (OCI tag or repo@sha256:digest).
	Version string
	// PreviousVersion is the currently running version — for rollback tracking.
	PreviousVersion string
	// AppKey is the key used in ManagedCluster.status.currentVersions.
	// Actuators must propagate this so the cluster-controller writes convergence
	// state under the correct key. Defaults to "default" when empty.
	AppKey string
}

// Actuator is KAI: the Kapro Actuator Interface.
//
// Any delivery system that can apply a version to a cluster implements this interface.
// Implementations must be safe for concurrent use from multiple goroutines.
type Actuator interface {
	// Apply instructs the delivery system to roll out the given version.
	// Idempotent — calling Apply twice with the same version is safe.
	Apply(ctx context.Context, req ApplyRequest) error

	// IsConverged returns true when the delivery system confirms the target
	// version is fully rolled out and all workloads are healthy.
	//
	// appKey identifies which application within the cluster to check — it is the
	// key used in ManagedCluster.status.currentVersions. Pass "default" for single-app
	// environments. This parameter makes the caller's intent explicit and symmetric
	// with Apply(ApplyRequest{AppKey: ...}), removing the implicit coupling that existed
	// when IsConverged had to re-read spec.desiredAppKey from the ManagedCluster itself.
	//
	// v0.2 change: appKey was added to this signature. Implementations that previously
	// resolved appKey internally (e.g. reading ManagedCluster.spec.desiredAppKey) should
	// now use the caller-supplied appKey directly.
	IsConverged(ctx context.Context, env *kaprov1alpha1.Environment, version, appKey string) (bool, error)

	// Rollback instructs the delivery system to revert to the given previous version.
	// Called when GatePolicy.onFailure == rollback.
	Rollback(ctx context.Context, env *kaprov1alpha1.Environment, previousVersion string) error
}
