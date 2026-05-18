// PromotionTrigger CRD: autonomous source observation that creates
// PromotionRun objects from verified artifact changes.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- PromotionTrigger ---------------------------------------------------------

// PromotionTriggerSpec defines an autonomous source that can create PromotionRun
// objects from verified artifact changes. The controller currently provides
// preview behavior for this API, and the API is intentionally safe by default.
//
// +kubebuilder:validation:XValidation:rule="self.source.type != 'oci' || has(self.source.oci)",message="source.oci is required when source.type=oci"
// +kubebuilder:validation:XValidation:rule="!has(self.maxActive) || self.maxActive >= 1",message="maxActive must be at least 1"
type PromotionTriggerSpec struct {
	// Suspended pauses source observation and promotionrun creation.
	// +kubebuilder:default=true
	Suspended bool `json:"suspended,omitempty"`
	// Source configures where artifact changes are observed.
	Source PromotionTriggerSource `json:"source"`
	// PromotionRunTemplate defines the PromotionRun created for a verified artifact.
	PromotionRunTemplate PromotionTriggerTemplate `json:"promotionrunTemplate"`
	// Cooldown is the minimum duration between promotionruns created by this trigger.
	// +kubebuilder:default="30m"
	Cooldown string `json:"cooldown,omitempty"`
	// MaxActive limits concurrently active PromotionRuns created by this trigger.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MaxActive int32 `json:"maxActive,omitempty"`
	// DryRun records what would be created without creating a PromotionRun.
	// +kubebuilder:default=false
	DryRun bool `json:"dryRun,omitempty"`
	// Parameters are source-specific key-value pairs for future extension.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// PromotionTriggerSource selects the artifact source observed by a PromotionTrigger.
type PromotionTriggerSource struct {
	// Type selects the source backend.
	// +kubebuilder:validation:Enum=oci
	Type string `json:"type"`
	// OCI configures OCI registry tag observation.
	// +optional
	OCI *OCIPromotionTriggerSource `json:"oci,omitempty"`
}

// OCIPromotionTriggerSource configures OCI registry observation.
type OCIPromotionTriggerSource struct {
	// Repository is the OCI repository to observe.
	Repository string `json:"repository"`
	// TagPattern is a regular expression. Only matching tags can create promotionruns.
	// +kubebuilder:validation:MinLength=1
	TagPattern string `json:"tagPattern"`
	// RequireSignature requires a configured verifier to pass before creating a
	// PromotionRun. Defaults to false so triggers do not fail closed unless a
	// signature policy is intentionally enabled.
	// +kubebuilder:default=false
	RequireSignature bool `json:"requireSignature,omitempty"`
	// PollInterval controls how often the source is checked.
	// +kubebuilder:default="5m"
	PollInterval string `json:"pollInterval,omitempty"`
	// SecretRef references registry credentials.
	// Cluster-scoped triggers must include the Secret namespace.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// PromotionTriggerTemplate defines the PromotionRun created from a verified artifact.
type PromotionTriggerTemplate struct {
	// NameTemplate controls the created PromotionRun name. Empty means the controller
	// derives a deterministic name from trigger name and artifact digest.
	// +optional
	NameTemplate string `json:"nameTemplate,omitempty"`
	// PromotionPlans is copied into PromotionRun.spec.promotionplans.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	PromotionPlans []PromotionPlanRef `json:"promotionplans"`
	// Suspended controls PromotionRun.spec.suspended on created PromotionRuns.
	// Defaults to true so detection does not equal deployment.
	// +kubebuilder:default=true
	Suspended bool `json:"suspended,omitempty"`
	// Scope restricts created PromotionRuns to a subset of clusters.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is copied into PromotionRun.spec.timeout.
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Labels are added to created PromotionRuns.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are added to created PromotionRuns.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PromotionTriggerStatus records observed source state and created promotionruns.
type PromotionTriggerStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// LastCheckedAt is the last time the source was checked.
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	// LastTriggeredAt is the last time a PromotionRun was created.
	LastTriggeredAt string `json:"lastTriggeredAt,omitempty"`
	// LastArtifact is the most recent artifact observed by the trigger.
	LastArtifact *PromotionTriggerArtifact `json:"lastArtifact,omitempty"`
	// ActivePromotionRuns lists non-terminal PromotionRuns created by this trigger.
	ActivePromotionRuns []string `json:"activePromotionRuns,omitempty"`
	// ActivePromotionRunCount is the number of non-terminal PromotionRuns created by this trigger.
	ActivePromotionRunCount int32 `json:"activePromotionRunCount,omitempty"`
	// Conditions summarize readiness, suspension, verification, and promotionrun creation.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PromotionTriggerArtifact identifies an observed immutable artifact.
type PromotionTriggerArtifact struct {
	// Tag is the source tag that matched the trigger pattern.
	Tag string `json:"tag,omitempty"`
	// Digest is the immutable artifact digest.
	Digest string `json:"digest,omitempty"`
	// Version is the value copied into PromotionRun.spec.version.
	Version string `json:"version,omitempty"`
	// ObservedAt is the RFC3339 time this artifact was observed.
	ObservedAt string `json:"observedAt,omitempty"`
	// SignatureVerified records whether signature policy passed.
	SignatureVerified bool `json:"signatureVerified,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=reltrig,categories=kapro-all
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.type`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="DryRun",type=boolean,JSONPath=`.spec.dryRun`
// +kubebuilder:printcolumn:name="LastVersion",type=string,JSONPath=`.status.lastArtifact.version`,priority=0
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activePromotionRunCount`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PromotionTrigger observes verified artifact changes and creates PromotionRun objects.
// It is safe by default: triggers are suspended by default, created PromotionRuns are
// suspended by default, and OCI signature verification defaults to required.
type PromotionTrigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionTriggerSpec   `json:"spec,omitempty"`
	Status            PromotionTriggerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionTriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionTrigger `json:"items"`
}
