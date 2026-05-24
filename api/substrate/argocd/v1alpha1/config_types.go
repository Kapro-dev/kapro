package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ArgoCDSubstrateConfigSpec configures one Argo CD control plane instance.
type ArgoCDSubstrateConfigSpec struct {
	// Endpoint is the Argo CD API server URL.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
	// Namespace is the namespace that contains Argo CD Applications and
	// cluster Secrets. Defaults to argocd when omitted.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// CredentialsRef references credentials used to call Argo CD or patch Argo
	// CD Kubernetes objects. Substrate controllers may ignore this when they use
	// in-cluster RBAC only.
	// +optional
	CredentialsRef *corev1.SecretReference `json:"credentialsRef,omitempty"`
	// DefaultProject is inherited by Argo CD application bindings when they do
	// not specify a project.
	// +optional
	DefaultProject string `json:"defaultProject,omitempty"`
	// TimeoutSeconds bounds one substrate call.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// ArgoCDSubstrateConfigStatus reports validation status for one Argo CD config.
type ArgoCDSubstrateConfigStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	LastObservedTime   *metav1.Time       `json:"lastObservedTime,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=argocfg,categories=kapro-substrates
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpoint`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ArgoCDSubstrateConfig is the typed config object for the built-in Argo CD
// substrate class.
type ArgoCDSubstrateConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ArgoCDSubstrateConfigSpec   `json:"spec,omitempty"`
	Status            ArgoCDSubstrateConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ArgoCDSubstrateConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ArgoCDSubstrateConfig `json:"items"`
}
