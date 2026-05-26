package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SubstrateDiscoveryPolicySpec configures continuous discovery for an existing
// substrate profile.
type SubstrateDiscoveryPolicySpec struct {
	// Substrate names the Substrate profile this policy keeps in sync.
	// +kubebuilder:validation:MinLength=1
	SubstrateRef string `json:"substrate"`
	// ExpectedKind optionally pins the referenced Substrate to a specific
	// SubstrateClass name, for example argo or flux. When set and the
	// referenced Substrate resolves to a different kind, the policy fails
	// closed.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,62}$`
	// +kubebuilder:validation:MaxLength=63
	ExpectedKind string `json:"expectedKind,omitempty"`
	// Selector further narrows Substrate.spec.discovery.selector for this
	// continuous adoption policy. It is ANDed with the Substrate selector before
	// reaching the adapter.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// DryRun validates the policy and referenced Substrate without invoking
	// adapter discovery.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
	// SyncInterval controls how often discovery runs. Defaults to 5m.
	// +kubebuilder:default="5m"
	// +optional
	SyncInterval string `json:"syncInterval,omitempty"`
}

// SubstrateDiscoveryPolicyStatus records the latest continuous discovery attempt.
type SubstrateDiscoveryPolicyStatus struct {
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	LastSyncTime       *metav1.Time `json:"lastSyncTime,omitempty"`
	Ready              bool         `json:"ready,omitempty"`
	Reason             string       `json:"reason,omitempty"`
	Message            string       `json:"message,omitempty"`
	// DiscoveredObjects reports the latest aggregate discovery count for quick
	// status inspection. Built-in Argo CD and Flux policies without their own
	// selector mirror Substrate.status counts; policies with their own selector
	// and other adapters report the adapter discovery result.
	DiscoveredObjects int32              `json:"discoveredObjects,omitempty"`
	Conditions        []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=sdp,categories=kapro-all
// +kubebuilder:printcolumn:name="Substrate",type=string,JSONPath=`.spec.substrate`
// +kubebuilder:printcolumn:name="Expected",type=string,JSONPath=`.spec.expectedKind`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Objects",type=integer,JSONPath=`.status.discoveredObjects`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type SubstrateDiscoveryPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SubstrateDiscoveryPolicySpec   `json:"spec,omitempty"`
	Status            SubstrateDiscoveryPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SubstrateDiscoveryPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubstrateDiscoveryPolicy `json:"items"`
}
