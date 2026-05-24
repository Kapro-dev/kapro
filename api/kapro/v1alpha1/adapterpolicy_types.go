package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AdapterPolicySpec configures continuous existing-substrate adapter discovery.
type AdapterPolicySpec struct {
	// Adapter names the adapter expected for SubstrateRef, for example argo
	// or flux. The controller resolves the substrate driver through the public
	// adapter registry and fails closed when this value does not match the
	// referenced Substrate's adapter.
	// +kubebuilder:validation:MinLength=1
	Adapter string `json:"adapter"`
	// SubstrateRef names the Substrate profile this policy keeps in sync.
	// +kubebuilder:validation:MinLength=1
	SubstrateRef string `json:"substrateRef"`
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

// AdapterPolicyStatus records the latest continuous adapter discovery attempt.
type AdapterPolicyStatus struct {
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
// +kubebuilder:resource:scope=Cluster,shortName=adp,categories=kapro-all
// +kubebuilder:printcolumn:name="Adapter",type=string,JSONPath=`.spec.adapter`
// +kubebuilder:printcolumn:name="Substrate",type=string,JSONPath=`.spec.substrateRef`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Objects",type=integer,JSONPath=`.status.discoveredObjects`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type AdapterPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AdapterPolicySpec   `json:"spec,omitempty"`
	Status            AdapterPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type AdapterPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AdapterPolicy `json:"items"`
}
