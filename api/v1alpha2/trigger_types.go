// Trigger CRD: autonomous source observation that creates or
// updates a Promotion from verified artifact changes. The PromotionController
// then materializes each Promotion update into a PromotionRun attempt; the
// trigger itself never writes PromotionRun directly.
package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Trigger ---------------------------------------------------------

// TriggerSpec defines an autonomous source that creates or updates
// a Promotion from verified artifact changes. The controller currently
// provides preview behavior for this API, and the API is intentionally safe
// by default.
//
// +kubebuilder:validation:XValidation:rule="self.source.type != 'oci' || has(self.source.oci)",message="source.oci is required when source.type=oci"
// +kubebuilder:validation:XValidation:rule="!has(self.maxActive) || self.maxActive >= 1",message="maxActive must be at least 1"
type TriggerSpec struct {
	// Suspended pauses source observation and Promotion creation/update.
	// +kubebuilder:default=true
	Suspended bool `json:"suspended,omitempty"`
	// Source configures where artifact changes are observed.
	Source PromotionTriggerSource `json:"source"`
	// PromotionTemplate defines the Promotion the trigger creates or updates.
	// Renamed from promotionrunTemplate when the trigger moved from emitting
	// PromotionRun directly to emitting Promotion intent.
	PromotionTemplate PromotionTriggerTemplate `json:"promotionTemplate"`
	// Cooldown is the minimum duration between Promotion updates created by
	// this trigger.
	// +kubebuilder:default="30m"
	Cooldown string `json:"cooldown,omitempty"`
	// MaxActive limits concurrently non-terminal PromotionRuns observed under
	// the trigger's managed Promotion. A high count usually means a prior
	// rollout is still in flight when a new artifact arrives.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MaxActive int32 `json:"maxActive,omitempty"`
	// DryRun records what would be created without writing the Promotion.
	// +kubebuilder:default=false
	DryRun bool `json:"dryRun,omitempty"`
	// Parameters are source-specific key-value pairs for future extension.
	// Fleet core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// PromotionTriggerSource selects the artifact source observed by a Trigger.
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
	// TagPattern is a regular expression. Only matching tags can create or update
	// Promotions.
	// +kubebuilder:validation:MinLength=1
	TagPattern string `json:"tagPattern"`
	// RequireSignature requires a configured verifier to pass before creating or
	// updating Promotion intent. Defaults to false so triggers do not fail closed
	// unless a signature policy is intentionally enabled.
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

// PromotionTriggerTemplate defines the Promotion created or updated from a
// verified artifact. Mirrors PromotionSpec with the rollout-input fields the
// trigger is allowed to set.
type PromotionTriggerTemplate struct {
	// KaproRef is the name of the parent Fleet fleet the managed Promotion
	// targets. Required; the PromotionController uses it to resolve the
	// inline plan and clusters.
	// +kubebuilder:validation:MinLength=1
	KaproRef string `json:"fleetRef"`
	// NameTemplate controls the managed Promotion name. Empty means the
	// controller derives a deterministic name from the trigger name.
	// +optional
	NameTemplate string `json:"nameTemplate,omitempty"`
	// Plans optionally overrides Fleet.spec.plan on the managed Promotion.
	// Flat list of Plan CRD names — v1alpha2 dropped the PlanRef object form
	// (no fake-name disambiguator, no cross-plan dependsOn). To run the same
	// Plan against a different cluster subset, create a second Trigger with a
	// narrower scope rather than listing the Plan twice.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Plans []string `json:"plans,omitempty"`
	// Suspended controls Promotion.spec.suspended on creation. Defaults to
	// true so detection does not equal deployment.
	// +kubebuilder:default=true
	Suspended bool `json:"suspended,omitempty"`
	// Scope restricts the managed Promotion to a subset of clusters.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is copied into Promotion.spec.timeout.
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Labels are added to the managed Promotion.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are added to the managed Promotion.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// TriggerStatus records observed source state and the managed
// Promotion's progress.
type TriggerStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// LastCheckedAt is the last time the source was checked.
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	// LastTriggeredAt is the last time the managed Promotion was created or
	// updated.
	LastTriggeredAt string `json:"lastTriggeredAt,omitempty"`
	// LastArtifact is the most recent artifact observed by the trigger.
	LastArtifact *PromotionTriggerArtifact `json:"lastArtifact,omitempty"`
	// ManagedPromotion is the name of the Promotion this trigger
	// creates and updates.
	ManagedPromotion string `json:"managedPromotion,omitempty"`
	// ActivePromotionRunCount is the number of non-terminal PromotionRuns
	// observed under the managed Promotion. Equals 1 during a healthy
	// in-flight rollout; >1 usually means a prior attempt has not been
	// superseded yet.
	ActivePromotionRunCount int32 `json:"activePromotionRunCount,omitempty"`
	// RecentArtifacts is a bounded history of recently observed artifacts
	// (newest first, capped at 20). Records tag movement even when dedup
	// suppresses a Promotion update.
	// +kubebuilder:validation:MaxItems=20
	// +optional
	RecentArtifacts []PromotionTriggerArtifact `json:"recentArtifacts,omitempty"`
	// Conditions summarize readiness, suspension, verification, and the
	// managed Promotion's creation/update state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// MaxRecentArtifacts caps TriggerStatus.RecentArtifacts to keep
// status size bounded.
const MaxRecentArtifacts = 20

// PromotionTriggerArtifact identifies an observed immutable artifact.
type PromotionTriggerArtifact struct {
	// Tag is the source tag that matched the trigger pattern.
	Tag string `json:"tag,omitempty"`
	// Digest is the immutable artifact digest.
	Digest string `json:"digest,omitempty"`
	// Version is the value copied into Promotion.spec.version.
	Version string `json:"version,omitempty"`
	// ObservedAt is the RFC3339 time this artifact was observed.
	ObservedAt string `json:"observedAt,omitempty"`
	// SignatureVerified records whether signature policy passed.
	SignatureVerified bool `json:"signatureVerified,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=reltrig,categories=kapro-all
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.type`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="DryRun",type=boolean,JSONPath=`.spec.dryRun`
// +kubebuilder:printcolumn:name="LastVersion",type=string,JSONPath=`.status.lastArtifact.version`,priority=0
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activePromotionRunCount`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Trigger observes verified artifact changes and creates or updates
// Promotion intent. It is safe by default for promotion-side concerns: triggers
// are suspended by default and created Promotions default to suspended. OCI
// signature verification is opt-in per source (`spec.source.oci.requireSignature`,
// default false) so a trigger does not fail closed unless a signature policy is
// intentionally enabled.
type Trigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TriggerSpec   `json:"spec,omitempty"`
	Status            TriggerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Trigger `json:"items"`
}
