// Package actuator defines KAI — the Kapro Actuator Interface.
//
// KAI is the contract between the Kapro promotion control plane and any delivery system.
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

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// ApplyRequest carries everything an actuator needs to apply a version.
type ApplyRequest struct {
	// Cluster is the target fleet cluster.
	Cluster *kaprov1alpha2.Cluster
	// Version is the version string to apply (OCI tag or repo@sha256:digest).
	Version string
	// PreviousVersion is the currently running version — for rollback tracking.
	PreviousVersion string
	// AppKey is the key used in FleetCluster.status.currentVersions.
	// Actuators must propagate this so the cluster-controller writes convergence
	// state under the correct key. Defaults to "default" when empty.
	AppKey string
}

// DeltaApplyRequest carries a map of appKey → version for multi-artifact delta delivery.
type DeltaApplyRequest struct {
	// Cluster is the target fleet cluster.
	Cluster *kaprov1alpha2.Cluster
	// DesiredVersions maps appKey → version for all artifacts in this promotionrun.
	DesiredVersions map[string]string
}

// Actuator is KAI: the Kapro Actuator Interface.
//
// Any delivery system that can apply a version to a cluster implements this interface.
// Implementations must be safe for concurrent use from multiple goroutines.
type Actuator interface {
	// Apply instructs the delivery system to roll out the given version.
	// It MUST be idempotent: calling Apply twice with the same version must not
	// trigger a double rollout, corrupt state, or return an error solely because
	// the request was replayed after a reconciliation retry.
	Apply(ctx context.Context, req ApplyRequest) error

	// IsConverged returns true when the delivery system confirms the target
	// version is fully rolled out and all workloads are healthy.
	//
	// appKey identifies which application within the cluster to check — it is the
	// key used in FleetCluster.status.currentVersions. Pass "default" for single-app
	// clusters. This parameter makes the caller's intent explicit and symmetric
	// with Apply(ApplyRequest{AppKey: ...}), removing the implicit coupling that existed
	// when IsConverged had to re-read spec.desiredAppKey from the cluster itself.
	IsConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, version, appKey string) (bool, error)

	// Rollback instructs the delivery system to revert to the given previous version.
	// appKey identifies which application stream within the cluster should be
	// rolled back; implementations must not implicitly reuse a possibly-mutated
	// desiredAppKey from current cluster state.
	Rollback(ctx context.Context, cluster *kaprov1alpha2.Cluster, previousVersion, appKey string) error

	// ApplyDelta compares desiredVersions against FleetCluster.status.currentVersions
	// and only applies artifacts that changed. Returns the number of artifacts that
	// required delivery (delta count). Idempotent.
	ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error)

	// IsAllConverged returns true when ALL artifacts in desiredVersions match
	// the cluster's currentVersions and Flux has converged.
	IsAllConverged(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) (bool, error)
}

// BackendObjectReporter is an optional actuator extension that reports the
// backend-native objects expected to converge for a target rollout. Controllers
// use it as status evidence; the Actuator interface remains the write contract.
type BackendObjectReporter interface {
	BackendObjects(ctx context.Context, cluster *kaprov1alpha2.Cluster, desiredVersions map[string]string) ([]kaprov1alpha2.BackendObjectStatus, error)
}
