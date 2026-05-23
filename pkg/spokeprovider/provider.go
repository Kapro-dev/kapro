// Package spokeprovider defines KSP — the Kapro Spoke Provider interface.
//
// KSP is the contract between the spoke-side delivery loop (running inside
// kapro-cluster-controller) and any concrete delivery implementation. It is
// the spoke-side analogue of pkg/actuator (KAI), which faces the hub side.
//
// One Provider services one BackendDriver. The first-party providers are:
//   - "oci"      — internal/spokeprovider/outbound (PR-5): the outbound-agent.
//     Pulls OCI artifacts directly and applies them via the
//     two-phase apply engine from internal/delivery.
//   - "flux"     — patches an existing OCIRepository tag and waits for Flux to
//     reconcile. Not yet implemented; planned for a follow-up PR.
//   - "argo"     — analogous to flux but for ArgoCD Application objects.
//   - "external" — gRPC-dispatched out-of-tree plugin via PluginRegistration.
//
// Providers are registered into a *Registry at spoke binary startup and
// resolved per-reconcile from BackendProfile.spec.driver. The spoke binary
// never imports a concrete provider type directly past the wire-up site, so
// adding a new driver does not perturb the loop or the status writer.
package spokeprovider

import (
	"context"
	"time"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const ContractVersionV1Alpha1 = "v1alpha1"

const (
	CapabilityReconcile = "reconcile"
	CapabilityObserve   = "observe"
	CapabilityApply     = "apply"
	CapabilityDryRun    = "dry-run"
)

// ReconcileRequest carries the inputs the loop hands to a Provider once per
// (cluster, app, version) tick. Cluster and BackendProfile are non-nil;
// callers guarantee this before calling Reconcile.
type ReconcileRequest struct {
	Cluster        *kaprov1alpha2.Cluster
	AppKey         string
	DesiredVersion string
	BackendProfile *kaprov1alpha2.Backend
	// Parameters is the merged parameter map: BackendProfile.Spec.Parameters
	// overlaid with FleetCluster.Spec.Delivery.Parameters (cluster wins).
	Parameters map[string]string
}

// ReconcileResult is what the loop writes to FleetCluster.status.delivery[app]
// after one Provider.Reconcile call. All fields are populated even on the
// failure paths so the caller can write a single coherent status update.
type ReconcileResult struct {
	Phase           kaprov1alpha2.DeliveryPhase
	Format          string
	ObservedDigest  string
	Staging         *kaprov1alpha2.DeliveryStagingStatus
	AppliedObjects  int32
	LastAttemptedAt time.Time
	LastAppliedAt   time.Time
	Err             error
}

// Capabilities describes the KSP contract and operations a provider supports.
type Capabilities struct {
	ContractVersion   string
	Driver            kaprov1alpha2.BackendDriver
	SupportsReconcile bool
	SupportsObserve   bool
	SupportsApply     bool
	SupportsDryRun    bool
}

// Normalize returns a copy with stable defaults applied.
func (c Capabilities) Normalize() Capabilities {
	if c.ContractVersion == "" {
		c.ContractVersion = ContractVersionV1Alpha1
	}
	return c
}

// Provider services one BackendDriver.
//
// Implementations MUST be safe for concurrent use. Reconcile MUST NOT panic
// on any input: malformed Parameters, unreachable registry, or zero-length
// DesiredVersion all map to a populated ReconcileResult{Phase: Failed, Err:…}.
type Provider interface {
	// Driver returns the BackendDriver value this provider services.
	Driver() kaprov1alpha2.BackendDriver
	// Capabilities returns the KSP contract metadata for this provider.
	Capabilities() Capabilities
	// Reconcile reconciles ONE (cluster, app, version) tuple on the local
	// spoke cluster and returns a ReconcileResult.
	Reconcile(ctx context.Context, req ReconcileRequest) ReconcileResult
}
