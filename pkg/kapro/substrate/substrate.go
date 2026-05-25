// Package substrate defines KSI, the Kapro Substrate Interface.
//
// KSI is the public Go contract for delivery substrates. Kapro core owns
// promotion ordering, gate evaluation, retries, and status. A substrate owns
// substrate-specific validation, mutation, and convergence observation.
package substrate

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

const ContractVersionV1Alpha1 = "v1alpha1"

// RequestEnvelope contains common identity and typed substrate data passed to
// KSI calls.
type RequestEnvelope struct {
	// Class is the resolved SubstrateClass selected by Substrate.spec.classRef.
	Class *kaprov1alpha1.SubstrateClass
	// Substrate is the configured substrate instance that selected the class.
	Substrate *kaprov1alpha1.Substrate
	// Config is the typed substrate-owned config object from
	// Substrate.spec.configRef.
	Config runtime.Object
	// Binding is reserved for typed app/workload bindings. It is nil in the
	// Phase-1 class/config path while substrate.parameters remains the app-level
	// compatibility surface.
	Binding runtime.Object
	// Cluster is the target cluster Kapro is promoting to.
	Cluster *kaprov1alpha1.Cluster
	// Parameters are merged opaque compatibility parameters. Substrate-specific
	// typed config should prefer Config; Parameters exists for migration and
	// small demo substrates.
	Parameters map[string]string
	// Credentials contains resolved credential material when the substrate
	// controller asks Kapro core to resolve a SecretRef. Many in-process
	// implementations resolve credentials themselves and leave this nil.
	Credentials *corev1.Secret
}

// ValidateRequest asks a substrate to validate class/config/substrate wiring.
type ValidateRequest struct {
	RequestEnvelope
	DryRun bool
}

// ValidateResult reports whether substrate wiring is valid.
type ValidateResult struct {
	Valid   bool
	Reason  string
	Message string
}

// ApplyRequest asks a substrate to move one target toward desired versions.
type ApplyRequest struct {
	RequestEnvelope
	DesiredVersions map[string]string
	DryRun          bool
}

// ApplyResult reports the mutation attempt outcome.
type ApplyResult struct {
	Accepted         bool
	Applied          int
	Reason           string
	Message          string
	SubstrateObjects []kaprov1alpha1.SubstrateObjectStatus
}

// ObserveRequest asks a substrate to report current convergence for desired versions.
type ObserveRequest struct {
	RequestEnvelope
	DesiredVersions map[string]string
}

// ObserveResult reports convergence without mutating substrate state.
type ObserveResult struct {
	Converged        bool
	Phase            kaprov1alpha1.DeliveryPhase
	Reason           string
	Message          string
	SubstrateObjects []kaprov1alpha1.SubstrateObjectStatus
}

// Capabilities describes the operations and execution modes a substrate supports.
type Capabilities struct {
	ContractVersion         string
	SupportedExecutionModes []kaprov1alpha1.ExecutionMode
	Capabilities            kaprov1alpha1.SubstrateCapabilities
}

// Substrate is KSI: the Kapro Substrate Interface.
type Substrate interface {
	Validate(ctx context.Context, req *ValidateRequest) (*ValidateResult, error)
	Apply(ctx context.Context, req *ApplyRequest) (*ApplyResult, error)
	Observe(ctx context.Context, req *ObserveRequest) (*ObserveResult, error)
	Capabilities(ctx context.Context) (*Capabilities, error)
}

// Rollbacker is the optional KSI rollback extension.
type Rollbacker interface {
	Rollback(ctx context.Context, req *RollbackRequest) (*RollbackResult, error)
}

// RollbackRequest asks a substrate to return a target to previous versions.
type RollbackRequest struct {
	RequestEnvelope
	PreviousVersions map[string]string
}

// RollbackResult reports rollback outcome.
type RollbackResult struct {
	Accepted bool
	Reason   string
	Message  string
}

// TwoPhaser is the optional KSI staged delivery extension.
type TwoPhaser interface {
	Prepare(ctx context.Context, req *PrepareRequest) (*PrepareResult, error)
	Commit(ctx context.Context, req *CommitRequest) (*CommitResult, error)
	Discard(ctx context.Context, req *DiscardRequest) (*DiscardResult, error)
}

// PrepareRequest asks a substrate to validate or stage desired versions.
type PrepareRequest struct {
	RequestEnvelope
	DesiredVersions map[string]string
	DryRun          bool
}

// PrepareResult reports staged work.
type PrepareResult struct {
	Handle  string
	Reason  string
	Message string
}

// CommitRequest asks a substrate to make a prepared handle live.
type CommitRequest struct {
	RequestEnvelope
	Handle string
}

// CommitResult reports staged commit outcome.
type CommitResult struct {
	Applied int
	Phase   kaprov1alpha1.DeliveryPhase
	Reason  string
	Message string
}

// DiscardRequest asks a substrate to discard a prepared handle.
type DiscardRequest struct {
	RequestEnvelope
	Handle string
}

// DiscardResult reports discard outcome.
type DiscardResult struct {
	Discarded bool
	Reason    string
	Message   string
}

// Discoverer is the optional KSI existing-substrate discovery extension.
type Discoverer interface {
	Discover(ctx context.Context, req *DiscoverRequest) (*DiscoverResult, error)
}

// DiscoverRequest asks a substrate to discover substrate-native objects.
type DiscoverRequest struct {
	RequestEnvelope
}

// DiscoverResult reports bounded discovery evidence.
type DiscoverResult struct {
	SelectedObjects        []kaprov1alpha1.DiscoveredSubstrateObject
	SkippedObjects         []kaprov1alpha1.DiscoveredSubstrateObject
	UnsupportedPatterns    []kaprov1alpha1.DiscoveredSubstrateObject
	DiscoveredClusters     int32
	DiscoveredApplications int32
	Errors                 []string
}
