package v1alpha2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AdapterPolicySpec configures continuous brownfield adapter discovery.
type AdapterPolicySpec struct {
	// Adapter names the adapter expected for BackendRef, for example argo-cd
	// or flux. The controller resolves the backend driver through the public
	// adapter registry and fails closed when this value does not match the
	// referenced Backend's adapter.
	// +kubebuilder:validation:MinLength=1
	Adapter string `json:"adapter"`
	// BackendRef names the Backend profile this policy keeps in sync.
	// +kubebuilder:validation:MinLength=1
	BackendRef string `json:"backendRef"`
	// SyncInterval controls how often discovery runs. Defaults to 5m.
	// +kubebuilder:default="5m"
	// +optional
	SyncInterval string `json:"syncInterval,omitempty"`
}

// AdapterPolicyStatus records the latest continuous adapter discovery attempt.
type AdapterPolicyStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	LastSyncTime       *metav1.Time       `json:"lastSyncTime,omitempty"`
	Ready              bool               `json:"ready,omitempty"`
	Reason             string             `json:"reason,omitempty"`
	Message            string             `json:"message,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=adp,categories=kapro-all
// +kubebuilder:printcolumn:name="Adapter",type=string,JSONPath=`.spec.adapter`
// +kubebuilder:printcolumn:name="Backend",type=string,JSONPath=`.spec.backendRef`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
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
