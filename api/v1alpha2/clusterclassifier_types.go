// ClusterClassifier CRD: preview classification policy for deriving stable
// fleet labels and delivery hints from Cluster metadata/capabilities.
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterClassifierSpec defines a conservative, preview classification policy.
// No core controller consumes this by default in v0.2.3; platform automation can
// use it to standardize labels used by Plan stage selectors.
type ClusterClassifierSpec struct {
	// Suspend pauses any controller or automation that reconciles this
	// classifier. It is false by default.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Selector narrows the Cluster set this classifier may evaluate. Empty means
	// all Clusters are eligible.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Rules are evaluated by name by future classifier automation. Rules should
	// be ordered from most-specific to least-specific by authors, but v0.2.3 does
	// not define controller precedence.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Rules []ClusterClassifierRule `json:"rules"`
}

// ClusterClassifierRule maps a match expression to classification outputs.
// +kubebuilder:validation:XValidation:rule="has(self.labels) || has(self.delivery)",message="at least one of labels or delivery must be set"
type ClusterClassifierRule struct {
	// Name is a stable rule identifier surfaced in status and audit trails.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Match selects Clusters for this rule.
	Match ClusterClassifierMatch `json:"match"`

	// Labels are the Cluster metadata labels a classifier controller may apply.
	// These labels are intended for Plan stage selectors, for example
	// kapro.io/tier=canary or kapro.io/staging=two-phase.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Delivery carries optional delivery hints a classifier controller may apply
	// to matched clusters. Omitted fields leave existing Cluster delivery config
	// untouched.
	// +optional
	Delivery *ClusterClassifierDeliveryHints `json:"delivery,omitempty"`
}

// ClusterClassifierMatch selects clusters by metadata labels and/or reported
// capability fields.
// +kubebuilder:validation:XValidation:rule="has(self.labels) || has(self.capabilities)",message="at least one of labels or capabilities must be set"
type ClusterClassifierMatch struct {
	// Labels matches Cluster.metadata.labels.
	// +optional
	Labels *metav1.LabelSelector `json:"labels,omitempty"`

	// Capabilities matches selected Cluster.spec/status fields. All non-empty
	// fields are ANDed.
	// +optional
	Capabilities *ClusterCapabilitySelector `json:"capabilities,omitempty"`
}

// ClusterCapabilitySelector is a small, explicit allowlist of fields that are
// stable enough for classification and stage selection.
type ClusterCapabilitySelector struct {
	// Cloud matches status.capabilities.cloud.
	// +optional
	Cloud string `json:"cloud,omitempty"`

	// Region matches status.capabilities.region.
	// +optional
	Region string `json:"region,omitempty"`

	// Zone matches status.capabilities.zone.
	// +optional
	Zone string `json:"zone,omitempty"`

	// Provider matches spec.provider.kind or status.provider.
	// +optional
	Provider string `json:"provider,omitempty"`

	// DeliveryMode matches spec.delivery.mode.
	// +kubebuilder:validation:Enum=push;pull
	// +optional
	DeliveryMode string `json:"deliveryMode,omitempty"`

	// BackendRef matches spec.delivery.backendRef.
	// +optional
	BackendRef string `json:"backendRef,omitempty"`

	// TopologyTier matches spec.topology.tier.
	// +optional
	TopologyTier string `json:"topologyTier,omitempty"`
}

// ClusterClassifierDeliveryHints are optional outputs for matched clusters.
type ClusterClassifierDeliveryHints struct {
	// Staging declares the intended staging behavior for matched clusters. The
	// only supported v0.2.3 value is TwoPhase/Abort.
	// +optional
	Staging *DeliveryStagingSpec `json:"staging,omitempty"`
}

// ClusterClassifierStatus reports classifier evaluation state when a
// controller or external automation owns this CRD.
type ClusterClassifierStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// MatchedClusters is the last observed number of Clusters matched by at
	// least one rule.
	// +optional
	MatchedClusters int32 `json:"matchedClusters,omitempty"`

	// LastEvaluationTime is when the classifier was last evaluated.
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`

	// Conditions summarize readiness and validation state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cclf,categories=kapro-all
// +kubebuilder:printcolumn:name="Matched",type=integer,JSONPath=`.status.matchedClusters`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterClassifier is a preview policy for deriving cluster classification
// labels and delivery staging hints. It is inert unless a classifier controller
// or external platform automation is explicitly installed.
type ClusterClassifier struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterClassifierSpec   `json:"spec,omitempty"`
	Status            ClusterClassifierStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterClassifierList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterClassifier `json:"items"`
}
