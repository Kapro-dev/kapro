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

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ApplyRequest carries everything an actuator needs to apply a version.
type ApplyRequest struct {
	// Cluster is the target member cluster.
	Cluster *kaprov1alpha1.MemberCluster
	// Version is the version string to apply (OCI tag or repo@sha256:digest).
	Version string
	// PreviousVersion is the currently running version — for rollback tracking.
	PreviousVersion string
	// AppKey is the key used in MemberCluster.status.currentVersions.
	// Actuators must propagate this so the cluster-controller writes convergence
	// state under the correct key. Defaults to "default" when empty.
	AppKey string
}

// DeltaApplyRequest carries a map of appKey → version for multi-artifact delta delivery.
type DeltaApplyRequest struct {
	// Cluster is the target member cluster.
	Cluster *kaprov1alpha1.MemberCluster
	// DesiredVersions maps appKey → version for all artifacts in this release.
	DesiredVersions map[string]string
}

// SwitchRequest carries everything needed to perform a namespace slot switch.
// Used by the promotion control plane when multi-version is enabled on the KaproApp.
type SwitchRequest struct {
	// Cluster is the target member cluster.
	Cluster *kaprov1alpha1.MemberCluster
	// FromSlot is the currently active slot (e.g. "blue").
	FromSlot string
	// ToSlot is the verified standby slot to switch to (e.g. "green").
	ToSlot string
	// App is the KaproApp spec containing multi-version config.
	App *kaprov1alpha1.KaproAppSpec
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
	// key used in MemberCluster.status.currentVersions. Pass "default" for single-app
	// clusters. This parameter makes the caller's intent explicit and symmetric
	// with Apply(ApplyRequest{AppKey: ...}), removing the implicit coupling that existed
	// when IsConverged had to re-read spec.desiredAppKey from the cluster itself.
	IsConverged(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, version, appKey string) (bool, error)

	// Rollback instructs the delivery system to revert to the given previous version.
	// appKey identifies which application stream within the cluster should be
	// rolled back; implementations must not implicitly reuse a possibly-mutated
	// desiredAppKey from current cluster state.
	Rollback(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, previousVersion, appKey string) error

	// ApplyDelta compares desiredVersions against MemberCluster.status.currentVersions
	// and only applies artifacts that changed. Returns the number of artifacts that
	// required delivery (delta count). Idempotent.
	ApplyDelta(ctx context.Context, req DeltaApplyRequest) (int, error)

	// IsAllConverged returns true when ALL artifacts in desiredVersions match
	// the cluster's currentVersions and Flux has converged.
	IsAllConverged(ctx context.Context, cluster *kaprov1alpha1.MemberCluster, desiredVersions map[string]string) (bool, error)
}

// Switcher is an optional interface for actuators that support multi-version
// namespace slot switching. The promotion control plane checks if the resolved
// actuator implements Switcher when multi-version is enabled on the KaproApp.
//
// The switch sequence is:
//  1. Scale down stateful consumers (Kafka, MQ) in the active slot
//  2. Wait for consumer groups to drain
//  3. Scale up consumers in the standby slot
//  4. Flip traffic routing (Traefik IngressRoute, Gateway HTTPRoute, etc.)
//  5. Verify traffic flows to the new slot
//
// If any step fails, the actuator must roll back completed steps.
type Switcher interface {
	// Switch performs the atomic namespace slot switch.
	// Called by the promotion control plane after all checkpoints pass in the standby slot.
	Switch(ctx context.Context, req SwitchRequest) error

	// ActiveSlot returns the currently active slot name for the given cluster.
	// Returns "" if multi-version is not active on this cluster.
	ActiveSlot(ctx context.Context, cluster *kaprov1alpha1.MemberCluster) (string, error)
}
