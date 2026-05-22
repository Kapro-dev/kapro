// Delivery staging policy types shared by Fleet, Cluster, Backend adapters,
// and preview classification APIs.
package v1alpha2

// DeliveryStagingType names the staging algorithm a delivery adapter should
// use before it mutates live workload objects.
// +kubebuilder:validation:Enum=TwoPhase
type DeliveryStagingType string

const (
	// DeliveryStagingTwoPhase means the adapter first validates every object
	// with server-side dry-run apply, then commits the batch only when the
	// full dry-run pass succeeds.
	DeliveryStagingTwoPhase DeliveryStagingType = "TwoPhase"
)

// DeliveryStagingFailurePolicy controls what happens when staging fails.
// +kubebuilder:validation:Enum=Abort
type DeliveryStagingFailurePolicy string

const (
	// DeliveryStagingFailureAbort preserves the current OCI pull behavior:
	// a staging failure aborts the entire commit phase.
	DeliveryStagingFailureAbort DeliveryStagingFailurePolicy = "Abort"
)

// DeliveryStagingSpec declares the expected pre-commit safety semantics for a
// delivery adapter. It is intentionally conservative for v0.2.3: the only
// supported strategy is the existing two-phase dry-run-then-commit flow, and
// omitted fields preserve the backend's current default behavior.
type DeliveryStagingSpec struct {
	// Type selects the staging strategy. TwoPhase is the only supported value.
	// +kubebuilder:default=TwoPhase
	// +optional
	Type DeliveryStagingType `json:"type,omitempty"`

	// FailurePolicy selects how dry-run failures affect commit. Abort is the
	// only supported policy and means no object is committed when any staged
	// object fails validation.
	// +kubebuilder:default=Abort
	// +optional
	FailurePolicy DeliveryStagingFailurePolicy `json:"failurePolicy,omitempty"`
}
