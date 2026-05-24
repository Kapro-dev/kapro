package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebhookSubstrateConfigSpec configures one external HTTP delivery endpoint.
type WebhookSubstrateConfigSpec struct {
	// Endpoint is the HTTP endpoint invoked by this substrate.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
	// Method is the HTTP method used for delivery. Defaults to POST.
	// +kubebuilder:validation:Enum=POST;PUT;PATCH
	// +kubebuilder:default="POST"
	// +optional
	Method string `json:"method,omitempty"`
	// CredentialsRef references an optional bearer token, basic auth material,
	// or other implementation-defined auth secret.
	// +optional
	CredentialsRef *corev1.SecretReference `json:"credentialsRef,omitempty"`
	// HeadersRef references static HTTP headers stored in a Secret.
	// +optional
	HeadersRef *corev1.SecretReference `json:"headersRef,omitempty"`
	// BodyTemplate is an optional implementation-defined request body template.
	// When omitted, the substrate sends the standard KSI request envelope.
	// +optional
	BodyTemplate string `json:"bodyTemplate,omitempty"`
	// TimeoutSeconds bounds one substrate call.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// WebhookSubstrateConfigStatus reports validation status for one webhook config.
type WebhookSubstrateConfigStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	LastObservedTime   *metav1.Time       `json:"lastObservedTime,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=whcfg,categories=kapro-substrates
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpoint`
// +kubebuilder:printcolumn:name="Method",type=string,JSONPath=`.spec.method`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WebhookSubstrateConfig is the typed config object for the built-in webhook
// substrate class.
type WebhookSubstrateConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WebhookSubstrateConfigSpec   `json:"spec,omitempty"`
	Status            WebhookSubstrateConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type WebhookSubstrateConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebhookSubstrateConfig `json:"items"`
}
