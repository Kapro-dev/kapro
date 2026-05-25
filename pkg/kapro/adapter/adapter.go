// Package adapter defines the public Go SDK contract for Kapro delivery
// adapters.
//
// The package intentionally avoids depending on Kapro's internal actuator or
// spoke-provider contracts. Those legacy packages remain runtime
// implementation details; this package is the stable SDK-facing surface.
package adapter

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// Adapter applies or observes one substrate family.
//
// Implementations must be safe for concurrent use. Apply and Rollback must be
// idempotent because controllers can retry after restarts or transient errors.
type Adapter interface {
	// SubstrateKind returns the substrate kind handled by this adapter.
	SubstrateKind() kaprov1alpha1.SubstrateKind
	// ExecutionScope returns where the adapter can run.
	ExecutionScope() kaprov1alpha1.ExecutionScope
	// Capabilities returns the operations this adapter supports.
	Capabilities() Capabilities
	// Apply asks the substrate to move one target toward the requested version.
	Apply(ctx context.Context, req Request) (Result, error)
	// Observe reports convergence without taking ownership of PromotionRun state.
	Observe(ctx context.Context, req Request) (Result, error)
	// Rollback asks the substrate to move the target back to PreviousVersion.
	Rollback(ctx context.Context, req Request) (Result, error)
	// Discover reports substrate-native objects that can be observed or adopted.
	Discover(ctx context.Context, req DiscoveryRequest) (DiscoveryResult, error)
}

// Capabilities describes which adapter operations are supported. Callers should
// branch on these bits instead of invoking an unsupported method and treating
// the expected unsupported result as an error path.
type Capabilities struct {
	ContractVersion string
	SubstrateKind   kaprov1alpha1.SubstrateKind
	ExecutionScope  kaprov1alpha1.ExecutionScope

	SupportsApply       bool
	SupportsObserve     bool
	SupportsRollback    bool
	SupportsDiscover    bool
	SupportsDryRun      bool
	SupportsSubstrateIO bool
}

// Normalize returns a copy with stable defaults applied.
func (c Capabilities) Normalize() Capabilities {
	if c.ContractVersion == "" {
		c.ContractVersion = "v1alpha1"
	}
	if c.ExecutionScope == "" {
		c.ExecutionScope = kaprov1alpha1.ExecutionScopeBoth
	}
	return c
}

// Request is the common input for apply, observe, and rollback operations.
type Request struct {
	// PromotionRun identifies the promotion attempt that produced this request.
	PromotionRun types.NamespacedName
	Plan         string
	Stage        string
	Target       string

	// Mode is the selected delivery mode for this target.
	Mode kaprov1alpha1.SubstrateMode
	// Cluster is the target FleetCluster object. Implementations must not
	// mutate it directly unless they own the Kubernetes patch they are issuing.
	Cluster *kaprov1alpha1.Cluster
	// Substrate is the selected Substrate profile, when the caller has one loaded.
	Substrate *kaprov1alpha1.Substrate

	// AppKey identifies one application stream inside a cluster. Empty means
	// "default".
	AppKey string
	// Version is the desired version for single-artifact delivery.
	Version string
	// PreviousVersion is the version Rollback should restore.
	PreviousVersion string
	// DesiredVersions carries multi-artifact delivery intent. Keys are app keys.
	DesiredVersions map[string]string
	// Parameters are substrate-specific merged settings. Cluster delivery
	// parameters normally override Substrate profile parameters before reaching an
	// adapter.
	Parameters map[string]string
}

// Result is the normalized output from an adapter operation.
type Result struct {
	SubstrateKind  kaprov1alpha1.SubstrateKind
	ExecutionScope kaprov1alpha1.ExecutionScope
	Phase          kaprov1alpha1.DeliveryPhase

	Converged bool
	Applied   bool
	Changed   int

	Format          string
	ObservedDigest  string
	AppliedObjects  int32
	LastAttemptedAt time.Time
	LastAppliedAt   time.Time

	SubstrateObjects []kaprov1alpha1.SubstrateObjectStatus
	Reason           string
	Message          string
}

// DiscoveryRequest is the public discovery input for existing substrate
// adoption. It models Substrate.spec.discovery without requiring a controller.
type DiscoveryRequest struct {
	Substrate      *kaprov1alpha1.Substrate
	SubstrateKind  kaprov1alpha1.SubstrateKind
	ExecutionScope kaprov1alpha1.ExecutionScope
	Namespace      string
	Selector       *metav1.LabelSelector
	MaxObjects     int32
	Parameters     map[string]string
}

// DiscoveryResult is the normalized discovery output used by reference
// adapters and future controller wiring.
type DiscoveryResult struct {
	SubstrateKind  kaprov1alpha1.SubstrateKind
	ExecutionScope kaprov1alpha1.ExecutionScope

	Ready                         bool
	Reason                        string
	Message                       string
	DiscoveredClusters            int32
	DiscoveredApplications        int32
	DiscoveredApplicationSets     int32
	SelectedObjects               []kaprov1alpha1.DiscoveredSubstrateObject
	SkippedObjects                []kaprov1alpha1.DiscoveredSubstrateObject
	UnsupportedPatterns           []kaprov1alpha1.DiscoveredSubstrateObject
	DiscoveryErrors               []string
	SubstrateObjectStatusExamples []kaprov1alpha1.SubstrateObjectStatus
}

// DiscoveryModel is a static description of a built-in substrate's discovery
// shape. It is useful for adapters that only model discovery today while the
// controller still owns actual list/watch execution.
type DiscoveryModel struct {
	SubstrateKind      kaprov1alpha1.SubstrateKind
	ExecutionScope     kaprov1alpha1.ExecutionScope
	DefaultNamespace   string
	Supported          bool
	SelectedObjects    []kaprov1alpha1.DiscoveredSubstrateObject
	SkippedObjects     []kaprov1alpha1.DiscoveredSubstrateObject
	UnsupportedObjects []kaprov1alpha1.DiscoveredSubstrateObject
	Message            string
}

// Discover returns the static model as a DiscoveryResult.
func (m DiscoveryModel) Discover(_ context.Context, req DiscoveryRequest) (DiscoveryResult, error) {
	driver := m.SubstrateKind
	if driver == "" {
		driver = req.SubstrateKind
	}
	runtime := m.ExecutionScope
	if runtime == "" {
		runtime = req.ExecutionScope
	}
	if runtime == "" {
		runtime = kaprov1alpha1.ExecutionScopeBoth
	}
	if !m.Supported {
		return DiscoveryResult{
			SubstrateKind:  driver,
			ExecutionScope: runtime,
			Ready:          false,
			Reason:         "DiscoveryUnsupported",
			Message:        fmt.Sprintf("discovery is not implemented for %s substrates", driver),
		}, nil
	}
	namespace := req.Namespace
	if namespace == "" {
		namespace = m.DefaultNamespace
	}
	message := m.Message
	if message == "" {
		message = fmt.Sprintf("modeled %s discovery shape in namespace %q", driver, namespace)
	}
	return DiscoveryResult{
		SubstrateKind:                 driver,
		ExecutionScope:                runtime,
		Ready:                         true,
		Reason:                        "DiscoveryModeled",
		Message:                       message,
		DiscoveredApplications:        int32(len(m.SelectedObjects) + len(m.SkippedObjects) + len(m.UnsupportedObjects)),
		SelectedObjects:               cloneDiscoveredObjects(m.SelectedObjects),
		SkippedObjects:                cloneDiscoveredObjects(m.SkippedObjects),
		UnsupportedPatterns:           cloneDiscoveredObjects(m.UnsupportedObjects),
		SubstrateObjectStatusExamples: substrateObjectExamples(m.SelectedObjects),
	}, nil
}

// ReferenceAdapter is a discovery-first adapter implementation for built-in
// substrate families. Apply, Observe, and Rollback report a failed result with a
// stable reason because the operator still uses the existing runtime actuators
// for side effects.
type ReferenceAdapter struct {
	driver    kaprov1alpha1.SubstrateKind
	runtime   kaprov1alpha1.ExecutionScope
	discovery DiscoveryModel
}

// NewReferenceAdapter returns an Adapter backed by a static discovery model.
func NewReferenceAdapter(driver kaprov1alpha1.SubstrateKind, runtime kaprov1alpha1.ExecutionScope, discovery DiscoveryModel) *ReferenceAdapter {
	if runtime == "" {
		runtime = kaprov1alpha1.ExecutionScopeBoth
	}
	discovery.SubstrateKind = driver
	if discovery.ExecutionScope == "" {
		discovery.ExecutionScope = runtime
	}
	return &ReferenceAdapter{driver: driver, runtime: runtime, discovery: discovery}
}

func (a *ReferenceAdapter) SubstrateKind() kaprov1alpha1.SubstrateKind { return a.driver }
func (a *ReferenceAdapter) ExecutionScope() kaprov1alpha1.ExecutionScope {
	return a.runtime
}

func (a *ReferenceAdapter) Capabilities() Capabilities {
	return Capabilities{
		SubstrateKind:    a.driver,
		ExecutionScope:   a.runtime,
		SupportsDiscover: a.discovery.Supported,
	}.Normalize()
}

func (a *ReferenceAdapter) Apply(_ context.Context, _ Request) (Result, error) {
	return a.unsupported("Apply"), nil
}

func (a *ReferenceAdapter) Observe(_ context.Context, _ Request) (Result, error) {
	return a.unsupported("Observe"), nil
}

func (a *ReferenceAdapter) Rollback(_ context.Context, _ Request) (Result, error) {
	return a.unsupported("Rollback"), nil
}

func (a *ReferenceAdapter) Discover(ctx context.Context, req DiscoveryRequest) (DiscoveryResult, error) {
	return a.discovery.Discover(ctx, req)
}

func (a *ReferenceAdapter) baseResult(phase kaprov1alpha1.DeliveryPhase) Result {
	return Result{SubstrateKind: a.driver, ExecutionScope: a.runtime, Phase: phase}
}

func (a *ReferenceAdapter) unsupported(operation string) Result {
	result := a.baseResult(kaprov1alpha1.DeliveryPhaseFailed)
	result.Reason = "OperationUnsupported"
	result.Message = fmt.Sprintf("%s is not implemented by the %s reference adapter; use the operator runtime for side effects", operation, a.driver)
	return result
}

func cloneDiscoveredObjects(in []kaprov1alpha1.DiscoveredSubstrateObject) []kaprov1alpha1.DiscoveredSubstrateObject {
	if in == nil {
		return nil
	}
	out := make([]kaprov1alpha1.DiscoveredSubstrateObject, len(in))
	copy(out, in)
	return out
}

func substrateObjectExamples(in []kaprov1alpha1.DiscoveredSubstrateObject) []kaprov1alpha1.SubstrateObjectStatus {
	out := make([]kaprov1alpha1.SubstrateObjectStatus, 0, len(in))
	for _, obj := range in {
		out = append(out, kaprov1alpha1.SubstrateObjectStatus{
			APIVersion: obj.APIVersion,
			Kind:       obj.Kind,
			Namespace:  obj.Namespace,
			Name:       obj.Name,
			Unit:       obj.Unit,
			Phase:      string(kaprov1alpha1.DeliveryPhasePending),
			Message:    obj.Reason,
		})
	}
	return out
}
