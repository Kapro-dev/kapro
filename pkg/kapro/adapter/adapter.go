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

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// Adapter applies or observes one backend family.
//
// Implementations must be safe for concurrent use. Apply and Rollback must be
// idempotent because controllers can retry after restarts or transient errors.
type Adapter interface {
	// Driver returns the Backend.spec.driver value handled by this adapter.
	Driver() kaprov1alpha2.BackendDriver
	// Runtime returns where the adapter can run.
	Runtime() kaprov1alpha2.BackendRuntime
	// Apply asks the backend to move one target toward the requested version.
	Apply(ctx context.Context, req Request) (Result, error)
	// Observe reports convergence without taking ownership of PromotionRun state.
	Observe(ctx context.Context, req Request) (Result, error)
	// Rollback asks the backend to move the target back to PreviousVersion.
	Rollback(ctx context.Context, req Request) (Result, error)
	// Discover reports backend-native objects that can be observed or adopted.
	Discover(ctx context.Context, req DiscoveryRequest) (DiscoveryResult, error)
}

// Request is the common input for apply, observe, and rollback operations.
type Request struct {
	// PromotionRun identifies the promotion attempt that produced this request.
	PromotionRun types.NamespacedName
	Plan         string
	Stage        string
	Target       string

	// Mode is the selected delivery mode for this target.
	Mode kaprov1alpha2.DeliveryMode
	// Cluster is the target FleetCluster object. Implementations must not
	// mutate it directly unless they own the Kubernetes patch they are issuing.
	Cluster *kaprov1alpha2.Cluster
	// Backend is the selected Backend profile, when the caller has one loaded.
	Backend *kaprov1alpha2.Backend

	// AppKey identifies one application stream inside a cluster. Empty means
	// "default".
	AppKey string
	// Version is the desired version for single-artifact delivery.
	Version string
	// PreviousVersion is the version Rollback should restore.
	PreviousVersion string
	// DesiredVersions carries multi-artifact delivery intent. Keys are app keys.
	DesiredVersions map[string]string
	// Parameters are backend-specific merged settings. Cluster delivery
	// parameters normally override Backend profile parameters before reaching an
	// adapter.
	Parameters map[string]string
}

// Result is the normalized output from an adapter operation.
type Result struct {
	Driver  kaprov1alpha2.BackendDriver
	Runtime kaprov1alpha2.BackendRuntime
	Phase   kaprov1alpha2.DeliveryPhase

	Converged bool
	Applied   bool
	Changed   int

	Format          string
	ObservedDigest  string
	AppliedObjects  int32
	LastAttemptedAt time.Time
	LastAppliedAt   time.Time

	BackendObjects []kaprov1alpha2.BackendObjectStatus
	Reason         string
	Message        string
}

// DiscoveryRequest is the public discovery input for brownfield backend
// adoption. It models Backend.spec.discovery without requiring a controller.
type DiscoveryRequest struct {
	Backend    *kaprov1alpha2.Backend
	Driver     kaprov1alpha2.BackendDriver
	Runtime    kaprov1alpha2.BackendRuntime
	Namespace  string
	Selector   *metav1.LabelSelector
	MaxObjects int32
	Parameters map[string]string
}

// DiscoveryResult is the normalized discovery output used by reference
// adapters and future controller wiring.
type DiscoveryResult struct {
	Driver  kaprov1alpha2.BackendDriver
	Runtime kaprov1alpha2.BackendRuntime

	Ready                       bool
	Reason                      string
	Message                     string
	DiscoveredClusters          int32
	DiscoveredApplications      int32
	DiscoveredApplicationSets   int32
	SelectedObjects             []kaprov1alpha2.DiscoveredBackendObject
	SkippedObjects              []kaprov1alpha2.DiscoveredBackendObject
	UnsupportedPatterns         []kaprov1alpha2.DiscoveredBackendObject
	DiscoveryErrors             []string
	BackendObjectStatusExamples []kaprov1alpha2.BackendObjectStatus
}

// DiscoveryModel is a static description of a built-in backend's discovery
// shape. It is useful for adapters that only model discovery today while the
// controller still owns actual list/watch execution.
type DiscoveryModel struct {
	Driver             kaprov1alpha2.BackendDriver
	Runtime            kaprov1alpha2.BackendRuntime
	DefaultNamespace   string
	Supported          bool
	SelectedObjects    []kaprov1alpha2.DiscoveredBackendObject
	SkippedObjects     []kaprov1alpha2.DiscoveredBackendObject
	UnsupportedObjects []kaprov1alpha2.DiscoveredBackendObject
	Message            string
}

// Discover returns the static model as a DiscoveryResult.
func (m DiscoveryModel) Discover(_ context.Context, req DiscoveryRequest) (DiscoveryResult, error) {
	driver := m.Driver
	if driver == "" {
		driver = req.Driver
	}
	runtime := m.Runtime
	if runtime == "" {
		runtime = req.Runtime
	}
	if runtime == "" {
		runtime = kaprov1alpha2.BackendRuntimeBoth
	}
	if !m.Supported {
		return DiscoveryResult{
			Driver:  driver,
			Runtime: runtime,
			Ready:   false,
			Reason:  "DiscoveryUnsupported",
			Message: fmt.Sprintf("discovery is not implemented for %s backends", driver),
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
		Driver:                      driver,
		Runtime:                     runtime,
		Ready:                       true,
		Reason:                      "DiscoveryModeled",
		Message:                     message,
		DiscoveredApplications:      int32(len(m.SelectedObjects) + len(m.SkippedObjects) + len(m.UnsupportedObjects)),
		SelectedObjects:             cloneDiscoveredObjects(m.SelectedObjects),
		SkippedObjects:              cloneDiscoveredObjects(m.SkippedObjects),
		UnsupportedPatterns:         cloneDiscoveredObjects(m.UnsupportedObjects),
		BackendObjectStatusExamples: backendObjectExamples(m.SelectedObjects),
	}, nil
}

// ReferenceAdapter is a discovery-first adapter implementation for built-in
// backend families. Apply, Observe, and Rollback report a failed result with a
// stable reason because the operator still uses the existing runtime actuators
// for side effects.
type ReferenceAdapter struct {
	driver    kaprov1alpha2.BackendDriver
	runtime   kaprov1alpha2.BackendRuntime
	discovery DiscoveryModel
}

// NewReferenceAdapter returns an Adapter backed by a static discovery model.
func NewReferenceAdapter(driver kaprov1alpha2.BackendDriver, runtime kaprov1alpha2.BackendRuntime, discovery DiscoveryModel) *ReferenceAdapter {
	if runtime == "" {
		runtime = kaprov1alpha2.BackendRuntimeBoth
	}
	discovery.Driver = driver
	if discovery.Runtime == "" {
		discovery.Runtime = runtime
	}
	return &ReferenceAdapter{driver: driver, runtime: runtime, discovery: discovery}
}

func (a *ReferenceAdapter) Driver() kaprov1alpha2.BackendDriver { return a.driver }
func (a *ReferenceAdapter) Runtime() kaprov1alpha2.BackendRuntime {
	return a.runtime
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

func (a *ReferenceAdapter) baseResult(phase kaprov1alpha2.DeliveryPhase) Result {
	return Result{Driver: a.driver, Runtime: a.runtime, Phase: phase}
}

func (a *ReferenceAdapter) unsupported(operation string) Result {
	result := a.baseResult(kaprov1alpha2.DeliveryPhaseFailed)
	result.Reason = "OperationUnsupported"
	result.Message = fmt.Sprintf("%s is not implemented by the %s reference adapter; use the operator runtime for side effects", operation, a.driver)
	return result
}

func cloneDiscoveredObjects(in []kaprov1alpha2.DiscoveredBackendObject) []kaprov1alpha2.DiscoveredBackendObject {
	if in == nil {
		return nil
	}
	out := make([]kaprov1alpha2.DiscoveredBackendObject, len(in))
	copy(out, in)
	return out
}

func backendObjectExamples(in []kaprov1alpha2.DiscoveredBackendObject) []kaprov1alpha2.BackendObjectStatus {
	out := make([]kaprov1alpha2.BackendObjectStatus, 0, len(in))
	for _, obj := range in {
		out = append(out, kaprov1alpha2.BackendObjectStatus{
			APIVersion: obj.APIVersion,
			Kind:       obj.Kind,
			Namespace:  obj.Namespace,
			Name:       obj.Name,
			Unit:       obj.Unit,
			Phase:      string(kaprov1alpha2.DeliveryPhasePending),
			Message:    obj.Reason,
		})
	}
	return out
}
