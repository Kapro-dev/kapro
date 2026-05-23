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
// delivery adapter. It is intentionally conservative while Kapro is pre-stable:
// the only supported strategy is the existing two-phase dry-run-then-commit
// flow, and omitted fields preserve the backend's current default behavior.
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

// DeliveryStagingStatus records what happened during the latest staged apply
// attempt for one delivered app. The two-phase path is validation-atomic: every
// rendered object must pass server-side dry-run before Kapro commits any live
// object. Kubernetes still does not provide a multi-object transaction during
// commit, so commit-phase infrastructure failures may leave partial commits.
type DeliveryStagingStatus struct {
	// Type records the staging algorithm used for this attempt.
	// +optional
	Type DeliveryStagingType `json:"type,omitempty"`
	// FailurePolicy records how staging failures affected commit.
	// +optional
	FailurePolicy DeliveryStagingFailurePolicy `json:"failurePolicy,omitempty"`
	// StagedObjects is the count of objects that passed server-side dry-run
	// validation.
	// +optional
	StagedObjects int32 `json:"stagedObjects,omitempty"`
	// StagingFailedObjects is the count of objects that failed server-side
	// dry-run validation. When this is non-zero, no commit phase ran.
	// +optional
	StagingFailedObjects int32 `json:"stagingFailedObjects,omitempty"`
	// CommittedObjects is the count of objects successfully committed through
	// server-side apply.
	// +optional
	CommittedObjects int32 `json:"committedObjects,omitempty"`
	// CommitFailedObjects is the count of objects that failed during the commit
	// phase after staging succeeded.
	// +optional
	CommitFailedObjects int32 `json:"commitFailedObjects,omitempty"`
	// FailurePhase records where the latest failed attempt stopped. It is empty
	// on success.
	// +optional
	FailurePhase DeliveryPhase `json:"failurePhase,omitempty"`
}
