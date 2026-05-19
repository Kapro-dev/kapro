// Promotion CRD: durable user-facing intent to roll a version through a Kapro
// fleet. The PromotionController creates PromotionRun objects as execution
// attempts; users do not normally write PromotionRun directly.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PromotionSpec is the durable intent to deliver a version through a Kapro
// fleet. It refers to a parent Kapro (which owns source, plan, clusters,
// delivery) and adds the rollout target (version, scope, optional plan
// override).
type PromotionSpec struct {
	// KaproRef is the name of the Kapro fleet this intent targets.
	// +kubebuilder:validation:MinLength=1
	KaproRef string `json:"kaproRef"`
	// Version is the default revision to deliver across all units.
	// +optional
	Version string `json:"version,omitempty"`
	// Versions maps PromotionUnit name to a per-unit revision.
	// Either Version or at least one Versions entry must be set.
	// +optional
	Versions map[string]string `json:"versions,omitempty"`
	// PromotionPlans optionally overrides the inline Kapro.spec.promotionplan
	// for this intent. When unset, the controller derives a single plan ref
	// from the parent Kapro's inline plan.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	PromotionPlans []PromotionPlanRef `json:"promotionPlans,omitempty"`
	// Scope restricts this Promotion to a subset of the parent fleet.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is the maximum duration for each PromotionRun attempt.
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Suspended pauses creation of new PromotionRun attempts when true.
	// In-flight attempts are also suspended via PromotionRun.spec.suspended.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
}

// PromotionPhase is the coarse state of a Promotion intent.
// +kubebuilder:validation:Enum=Pending;Running;Promoted;Failed;Suspended
type PromotionPhase string

const (
	PromotionPhasePending   PromotionPhase = "Pending"
	PromotionPhaseRunning   PromotionPhase = "Running"
	PromotionPhasePromoted  PromotionPhase = "Promoted"
	PromotionPhaseFailed    PromotionPhase = "Failed"
	PromotionPhaseSuspended PromotionPhase = "Suspended"
)

// PromotionAttemptRef is one historical or active attempt under a Promotion.
type PromotionAttemptRef struct {
	// Name is the PromotionRun name for this attempt.
	Name string `json:"name"`
	// SpecHash is the deterministic hash of the Promotion spec that produced
	// this attempt. Used to detect spec drift and trigger a new attempt.
	SpecHash string `json:"specHash"`
	// Version is the resolved version applied for this attempt (the value
	// echoed into PromotionRun.spec.version at stamp time).
	// +optional
	Version string `json:"version,omitempty"`
	// Phase is the last-observed PromotionRun.status.phase for this attempt.
	// +optional
	Phase PromotionRunPhase `json:"phase,omitempty"`
	// StartedAt is when the attempt was created.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// CompletedAt is when the attempt reached a terminal phase
	// (Complete, Failed, or Superseded).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// SupersededReason is set when this attempt was marked Superseded
	// instead of finishing naturally.
	// +optional
	SupersededReason string `json:"supersededReason,omitempty"`
}

// PromotionStatus is the observed state of a Promotion intent.
type PromotionStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase aggregates the active attempt's PromotionRun phase into a coarse
	// Promotion phase.
	Phase PromotionPhase `json:"phase,omitempty"`
	// ActiveAttemptRef points at the current (non-terminal) PromotionRun.
	// nil when no attempt is in flight (all attempts terminal).
	// +optional
	ActiveAttemptRef *PromotionAttemptRef `json:"activeAttemptRef,omitempty"`
	// Attempts records up to the last 20 attempts (newest first). Older
	// attempts remain discoverable via PromotionRun objects with the
	// kapro.io/promotion label.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	Attempts []PromotionAttemptRef `json:"attempts,omitempty"`
	// ResolvedVersion echoes the most recent attempt's version for quick
	// at-a-glance status.
	// +optional
	ResolvedVersion string             `json:"resolvedVersion,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// MaxPromotionAttempts is the cap on Promotion.status.attempts[]. Older
// attempts are pruned but the underlying PromotionRun objects remain.
const MaxPromotionAttempts = 20

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=promo,categories=kapro-all
// +kubebuilder:printcolumn:name="Kapro",type=string,JSONPath=`.spec.kaproRef`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.status.activeAttemptRef.name`
// +kubebuilder:printcolumn:name="Attempts",type=integer,JSONPath=`.status.attempts.length()`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Promotion is the durable user-facing intent to roll a version through a
// Kapro fleet. The controller materializes intent into one or more
// PromotionRun attempts; PromotionRun and PromotionTarget are observe-only
// runtime objects.
type Promotion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionSpec   `json:"spec,omitempty"`
	Status            PromotionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Promotion `json:"items"`
}
