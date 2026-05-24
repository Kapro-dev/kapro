package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubernetesApplyConfigSpec configures direct Kubernetes API delivery.
type KubernetesApplyConfigSpec struct {
	// KubeconfigSecretRef references a kubeconfig used for hub-push delivery to
	// an external cluster. When omitted, the substrate uses the target Cluster
	// identity already known to Kapro.
	// +optional
	KubeconfigSecretRef *corev1.SecretReference `json:"kubeconfigSecretRef,omitempty"`
	// ServerSideApply enables Kubernetes server-side apply. It is a pointer so
	// clients can deliberately set false while the CRD default remains true.
	// +kubebuilder:default=true
	// +optional
	ServerSideApply *bool `json:"serverSideApply,omitempty"`
	// FieldManager is the field manager used for server-side apply.
	// +kubebuilder:default="kapro"
	// +optional
	FieldManager string `json:"fieldManager,omitempty"`
	// Namespace is the default namespace for namespace-scoped manifests that do
	// not specify metadata.namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Prune allows the substrate to delete previously applied objects that are
	// no longer present in the desired manifest set. Defaults to false.
	// +optional
	Prune bool `json:"prune,omitempty"`
	// TimeoutSeconds bounds one substrate call.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// KubernetesApplyConfigStatus reports validation status for one direct-apply config.
type KubernetesApplyConfigStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	LastObservedTime   *metav1.Time       `json:"lastObservedTime,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=kapplycfg,categories=kapro-substrates
// +kubebuilder:printcolumn:name="SSA",type=boolean,JSONPath=`.spec.serverSideApply`
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KubernetesApplyConfig is the typed config object for the built-in
// kubernetes-apply substrate class.
type KubernetesApplyConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KubernetesApplyConfigSpec   `json:"spec,omitempty"`
	Status            KubernetesApplyConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type KubernetesApplyConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubernetesApplyConfig `json:"items"`
}
