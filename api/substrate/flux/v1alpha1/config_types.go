package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FluxSubstrateConfigSpec configures one Flux control plane instance.
type FluxSubstrateConfigSpec struct {
	// Namespace is the namespace containing Flux source and workload objects.
	// Defaults to flux-system when omitted.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// KubeconfigSecretRef references a kubeconfig used for hub-push delivery to
	// an external Flux control plane. When omitted, the substrate uses in-cluster
	// RBAC or the target Cluster identity already known to Kapro.
	// +optional
	KubeconfigSecretRef *corev1.SecretReference `json:"kubeconfigSecretRef,omitempty"`
	// DefaultServiceAccountName is inherited by Flux binding objects when they
	// do not specify a service account.
	// +optional
	DefaultServiceAccountName string `json:"defaultServiceAccountName,omitempty"`
	// TimeoutSeconds bounds one substrate call.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// FluxSubstrateConfigStatus reports validation status for one Flux config.
type FluxSubstrateConfigStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	LastObservedTime   *metav1.Time       `json:"lastObservedTime,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fluxcfg,categories=kapro-substrates
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FluxSubstrateConfig is the typed config object for the built-in Flux
// substrate class.
type FluxSubstrateConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FluxSubstrateConfigSpec   `json:"spec,omitempty"`
	Status            FluxSubstrateConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FluxSubstrateConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FluxSubstrateConfig `json:"items"`
}
