package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PromotionArtifact identifies the version being promoted.
type PromotionArtifact struct {
	// Image is the image or logical component name, for example checkout-api.
	// +optional
	Image string `json:"image,omitempty"`
	// Tag is the human version or source tag, for example v1.2.3.
	// +optional
	Tag string `json:"tag,omitempty"`
	// Digest is the immutable artifact digest when known.
	// +optional
	Digest string `json:"digest,omitempty"`
	// Version is the exact value Kapro should write when a promotion uses one
	// version for all selected PromotionUnits.
	// +optional
	Version string `json:"version,omitempty"`
	// Repository is the optional OCI or Git repository containing the artifact.
	// +optional
	Repository string `json:"repository,omitempty"`
}

// PromotionSpec is the desired intent to promote one artifact or set of unit
// versions through one or more PromotionPlans.
type PromotionSpec struct {
	// Artifact describes the primary artifact being promoted.
	// +optional
	Artifact *PromotionArtifact `json:"artifact,omitempty"`
	// Version is the default revision to deliver across selected units.
	// +optional
	Version string `json:"version,omitempty"`
	// Versions maps PromotionUnit name to the backend-native revision to deliver.
	// +optional
	Versions map[string]string `json:"versions,omitempty"`
	// PromotionPlans is the DAG of PromotionPlan nodes this intent should run.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	PromotionPlans []PromotionPlanRef `json:"promotionPlans"`
	// SourceRef references the PromotionSource whose PromotionUnits define the
	// allowed write targets.
	// +optional
	SourceRef string `json:"sourceRef,omitempty"`
	// Policies references PromotionPolicy objects evaluated before or during the
	// promotion.
	// +optional
	Policies []corev1.LocalObjectReference `json:"policies,omitempty"`
	// Suspended pauses all advancement when true.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
	// Scope restricts this Promotion to a subset of FleetClusters.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is the maximum duration for the PromotionRun created from this
	// intent.
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// PromotionPhase is the coarse state of a Promotion intent.
// +kubebuilder:validation:Enum=Pending;Running;Promoted;Failed;RolledBack
type PromotionPhase string

const (
	PromotionPhasePending    PromotionPhase = "Pending"
	PromotionPhaseRunning    PromotionPhase = "Running"
	PromotionPhasePromoted   PromotionPhase = "Promoted"
	PromotionPhaseFailed     PromotionPhase = "Failed"
	PromotionPhaseRolledBack PromotionPhase = "RolledBack"
)

// PromotionStatus defines the observed state of a Promotion intent.
type PromotionStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              PromotionPhase     `json:"phase,omitempty"`
	ActiveRun          string             `json:"activeRun,omitempty"`
	LastRun            string             `json:"lastRun,omitempty"`
	ResolvedVersion    string             `json:"resolvedVersion,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=promo,categories=kapro-all
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`,priority=0
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,priority=0
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.status.activeRun`,priority=0
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,priority=0

// Promotion is the user-facing intent to move a version through the fleet.
// PromotionRun objects record execution attempts.
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

// PromotionPolicySpec declares reusable policy checks for a Promotion.
type PromotionPolicySpec struct {
	// Mode controls whether this policy is required or advisory.
	// +kubebuilder:validation:Enum=enforce;audit
	// +kubebuilder:default=enforce
	Mode string `json:"mode,omitempty"`
	// Selector limits where this policy applies.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// CEL contains simple expression checks evaluated by Kapro.
	// +optional
	CEL []CELPolicyRule `json:"cel,omitempty"`
	// Verification configures artifact signature/provenance checks.
	// +optional
	Verification *VerificationGateSpec `json:"verification,omitempty"`
	// FreezeWindows blocks promotions during named time windows.
	// +optional
	FreezeWindows []AgentTimeWindow `json:"freezeWindows,omitempty"`
	// OnFailure controls behavior when this policy fails.
	// +kubebuilder:validation:Enum=halt;rollback;continue
	// +kubebuilder:default=halt
	// +optional
	OnFailure string `json:"onFailure,omitempty"`
}

// CELPolicyRule is one CEL policy expression.
type CELPolicyRule struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Message    string `json:"message,omitempty"`
}

// PromotionPolicyStatus records policy readiness.
type PromotionPolicyStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ppol,categories=kapro-all
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionPolicy is a reusable guardrail evaluated before or during promotion.
type PromotionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionPolicySpec   `json:"spec,omitempty"`
	Status            PromotionPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionPolicy `json:"items"`
}
